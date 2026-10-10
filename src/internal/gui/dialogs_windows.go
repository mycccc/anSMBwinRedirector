//go:build windows

package gui

import (
	"errors"
	"fmt"
	"net/netip"
	"strconv"
	"strings"

	"github.com/lxn/walk"
	. "github.com/lxn/walk/declarative"

	"ansmbwinhelper/internal/diag"
	"ansmbwinhelper/internal/model"
)

// editResult 是"添加 / 编辑目标"对话框的结果。
type editResult struct {
	ip       netip.Addr
	port     uint16
	username string
	password string
}

// showEditDialog 打开"添加 / 编辑目标"对话框（decisions/012 §3.4：4 个字段）。
//
// 校验与 `decisions/005 §6.1` **同源**（调用 `model.ValidateTarget`），
// 并额外检查 Android IP 在集合内唯一（I1，排除自身）。
//
// ⚠️ `AndroidPort == 445` **允许**，只给**非阻塞**提示，**不得**作为校验错误（decisions/005 §6.1、008 §6）。
//
// ⚠️ **密码语义（decisions/012 §3.4）**：密码框**不预填**（⛔ 不得显示长度/掩码字符数），
// 只有用户**实际改动过**密码框才更新密码 —— 否则**原样保留** `init.Password`。
// 用户名清空 ⇒ 视为"移除凭据"（密码一并清空）。
//
// 返回 (结果, 用户是否确定, 错误)。
func showEditDialog(owner walk.Form, title string, init model.Target, all []model.Target) (*editResult, bool, error) {
	var dlg *walk.Dialog
	var leIP *walk.LineEdit
	var nePort *walk.NumberEdit
	var leUser, lePass *walk.LineEdit
	var lblHint *walk.Label

	var res *editResult
	// passwordDirty：密码框被用户改过 ⇒ 以框内容为准；否则保留 init.Password。
	passwordDirty := false

	dlgDecl := Dialog{
		AssignTo: &dlg,
		Title:    title,
		MinSize:  Size{Width: 480, Height: 360},
		Layout:   VBox{},
		Children: []Widget{
			Label{Text: T("dlg.android_ip_hint")},
			LineEdit{AssignTo: &leIP, Text: ipText(init.AndroidIP)},
			Label{Text: T("dlg.android_port_hint")},
			NumberEdit{
				AssignTo: &nePort,
				MinValue: 1,
				MaxValue: 65535,
				Value:    float64(init.AndroidPort),
			},
			Label{Text: T("dlg.username_hint")},
			LineEdit{AssignTo: &leUser, Text: init.Username},
			Label{Text: T("dlg.password_hint")},
			LineEdit{AssignTo: &lePass, PasswordMode: true},
			Label{Text: passwordStateHint(init.Password)},
			Label{AssignTo: &lblHint, Text: " "},
			Composite{
				Layout: HBox{},
				Children: []Widget{
					HSpacer{},
					PushButton{
						Text: T("dialog.ok"),
						OnClicked: func() {
							pass := effectivePassword(init.Password, passwordDirty, lePass.Text())
							r, err := validateEdit(leIP.Text(), uint16(nePort.Value()),
								leUser.Text(), pass, passwordDirty, init.ID, all)
							if err != nil {
								_ = lblHint.SetText("⛔ " + err.Error())
								return
							}
							res = r
							dlg.Accept()
						},
					},
					PushButton{
						Text:      T("dialog.cancel"),
						OnClicked: func() { dlg.Cancel() },
					},
				},
			},
		},
	}

	// 端口提示：445 是"允许但无重写意义"（no-op），只提示、不阻断。
	//
	// ⚠️ 必须在 `Create()` **之后**才能碰 `nePort` / `lblHint`：
	// 声明式控件的实例是在 Create 期间才产生的，Create 之前它们是 nil
	// （在 Create 之前调用会在打开对话框时直接 panic —— 实机验证时踩到过）。
	updateHint := func() {
		if uint16(nePort.Value()) == model.WindowsMatchPort {
			_ = lblHint.SetText(T("dlg.hint_prefix") + diag.NoteText(diag.NoteAndroidPortEqualsWindowsPort))
			return
		}
		_ = lblHint.SetText(" ")
	}

	if err := dlgDecl.Create(owner); err != nil {
		return nil, false, err
	}
	nePort.ValueChanged().Attach(updateHint)
	lePass.TextChanged().Attach(func() { passwordDirty = true })
	updateHint()

	if dlg.Run() != walk.DlgCmdOK {
		return nil, false, nil
	}
	return res, true, nil
}

// effectivePassword 计算"本次编辑后真正生效的密码"（decisions/012 §3.4）。
//
//	edited == false ⇒ **用户没碰密码框** ⇒ 原样保留（避免"只改端口却把密码清空"）
//	edited == true  ⇒ 以输入框内容为准（可能为空 ⇒ 由 validateEdit 判定合法性）
func effectivePassword(orig string, edited bool, raw string) string {
	if !edited {
		return orig
	}
	return raw
}

// ipText 把 IP 渲染为输入框初值（Add 模式下零值 ⇒ 空串，而不是 "invalid IP"）。
func ipText(a netip.Addr) string {
	if !a.IsValid() {
		return ""
	}
	return a.String()
}

// passwordStateHint 显示"密码已配置"**状态**，⛔ 绝不显示密码本身、长度或掩码字符数（decisions/012 C4）。
func passwordStateHint(existingPassword string) string {
	if existingPassword == "" {
		return " "
	}
	return T("dlg.password_saved_hint")
}

// validateEdit 复用 model 的校验口径（同源，不另写一套）。
func validateEdit(ipRaw string, port uint16, username, password string, passwordEdited bool, selfID string, all []model.Target) (*editResult, error) {
	ipStr := strings.TrimSpace(ipRaw)
	if ipStr == "" {
		return nil, errors.New(T("dlg.err.ip_required"))
	}
	ip, err := netip.ParseAddr(ipStr)
	if err != nil {
		return nil, fmt.Errorf(T("dlg.err.ip_invalid"), ipStr)
	}
	ip = ip.Unmap()

	username = strings.TrimSpace(username)
	password = strings.TrimSpace(password)
	if username == "" {
		// 清空用户名 ⇒ 移除凭据；但"填了密码却没用户名"是明确错误，不静默丢弃。
		if passwordEdited && password != "" {
			return nil, errors.New(T("dlg.err.cred_incomplete"))
		}
		password = ""
	} else if password == "" {
		return nil, errors.New(T("dlg.err.cred_incomplete"))
	}

	// 复用同一份校验实现（ID 用占位符 —— 这里只验 IP/端口/凭据，ID 由调用方管理）。
	probeID := selfID
	if probeID == "" {
		probeID = "probe"
	}
	if err := model.ValidateTarget(model.Target{
		ID: probeID, AndroidIP: ip, AndroidPort: port,
		Username: username, Password: password,
	}); err != nil {
		return nil, err
	}

	// I1：Android IP 在集合内唯一（排除自身）。
	for _, t := range all {
		if t.ID == selfID {
			continue
		}
		if t.AndroidIP == ip {
			return nil, fmt.Errorf(T("dlg.err.ip_dup"), model.ErrDuplicateIP, ip)
		}
	}
	return &editResult{ip: ip, port: port, username: username, password: password}, nil
}

// --- 调试窗口（decisions/011 §2.7 / 008 §15）-------------------------------

// debugAction 是调试窗口返回的"用户想要执行的动作"。
//
// ⚠️ 窗口本身**不执行任何动作**：它只回传意图，由 `gui` 经 **Controller** 执行
// （decisions/008 G2 的改写版：GUI 不得直接执行系统命令、不得绕过 controller）。
type debugAction int

const (
	debugActionNone debugAction = iota
	debugActionCopy
	debugActionRepair
	debugActionRestartWorkstation
)

// showDebugDialog 展示**只读诊断**并收集用户选择的下一个动作。
//
// ⛔ 窗口中**没有**"强制卸载 / Force Cleanup"；`Repair` 走的是 011 §2.2 的接管流程。
func showDebugDialog(owner walk.Form, info diag.WinDivertInfo, repairAllowed bool) (debugAction, error) {
	var dlg *walk.Dialog
	var btnRepair *walk.PushButton

	act := debugActionNone

	decl := Dialog{
		AssignTo: &dlg,
		Title:    T("debug.title"),
		MinSize:  Size{Width: 760, Height: 560},
		Layout:   VBox{},
		Children: []Widget{
			Label{Text: T("debug.readonly_note")},
			TextEdit{ReadOnly: true, VScroll: true, HScroll: true, Text: formatInspect(info)},
			Label{Text: T("debug.repair_note")},
			// 「重启 Workstation」的影响提示必须**常驻可见**，不能只在确认框里出现一次。
			Label{Text: T("debug.restart_note")},
			Composite{
				Layout: HBox{},
				Children: []Widget{
					PushButton{
						AssignTo: &btnRepair,
						Text:     T("debug.repair"),
						OnClicked: func() {
							act = debugActionRepair
							dlg.Accept()
						},
					},
					PushButton{
						Text: T("debug.restart_workstation"),
						OnClicked: func() {
							// 破坏性动作必须显式确认（decisions/008 G7）。
							if confirm(dlg, T("debug.restart_confirm_title"),
								T("debug.restart_confirm_text"), walk.MsgBoxYesNo) == walk.DlgCmdYes {
								act = debugActionRestartWorkstation
								dlg.Accept()
							}
						},
					},
					HSpacer{},
					PushButton{Text: T("btn.copy_details"), OnClicked: func() { act = debugActionCopy; dlg.Accept() }},
					PushButton{Text: T("btn.close"), OnClicked: func() { dlg.Cancel() }},
				},
			},
		},
	}
	if err := decl.Create(owner); err != nil {
		return debugActionNone, err
	}
	// ⚠️ `Enabled` 必须在 Create **之后**设置（声明式控件在 Create 期间才产生）。
	if btnRepair != nil && !repairAllowed {
		btnRepair.SetEnabled(false)
	}
	dlg.Run()
	return act, nil
}

// formatInspect 渲染只读诊断文本。
//
// ⚠️ **标签**经 localization；**值**（路径 / 状态 / 版本 / 哈希 / `true`·`false` / PID / 层名）
// 一律**原样输出**（decisions/013 §6.2）。
// ⛔ 绝不包含密码或配置全文。
func formatInspect(info diag.WinDivertInfo) string {
	var b strings.Builder
	if !info.Available {
		b.WriteString(T("debug.unavailable_prefix") + info.Err + "\r\n")
		return b.String()
	}
	line := func(k, v string) { b.WriteString(k + ": " + v + "\r\n") }
	line(T("debug.program_dir"), valueOrDash(info.WinDivertDir))
	line(T("debug.files_ok"), strconv.FormatBool(info.RuntimeFilesOK))
	line(T("debug.service_exists"), strconv.FormatBool(info.ServiceExists))
	line(T("debug.service_state"), valueOrDash(info.DriverState))
	line(T("debug.service_image_path"), valueOrDash(info.ServiceImagePath))
	line(T("debug.image_path_is_ours"), strconv.FormatBool(info.ImagePathIsOurs))
	// ⚠️ **运行时**驱动版本：只读诊断**不查询**（查询会隐式安装驱动，decisions/011 §9.3 D-1）。
	// ⛔ 不得用文件版本或期望版本冒充它。
	line(T("debug.driver_runtime_version"), runtimeVersionText(info.DriverRuntimeVersion))

	// 运行时文件（decisions/011 §2.7：路径 + 实际/期望 SHA-256 + **文件**版本）。
	b.WriteString(T("debug.files_header"))
	if len(info.Files) == 0 {
		b.WriteString(T("debug.consumers_none"))
	} else {
		for _, f := range info.Files {
			b.WriteString(fmt.Sprintf(T("debug.file_line"), f.Name, valueOrDash(f.Path)))
			b.WriteString(fmt.Sprintf(T("debug.file_version_line"), valueOrUnknown(f.Version)))
			switch {
			case f.Err != "":
				b.WriteString(fmt.Sprintf(T("debug.file_err_line"), f.Err))
			default:
				// ⚠️ **始终**打印实际/期望 SHA-256（decisions/011 §2.7）。
				b.WriteString(fmt.Sprintf(T("debug.file_hash_lines"), valueOrUnknown(f.Actual), valueOrUnknown(f.Expected)))
				if !f.OK {
					b.WriteString(T("debug.file_hash_mismatch"))
				}
			}
		}
	}

	b.WriteString(T("debug.consumers_header"))
	if !info.ConsumersQueried {
		// ⛔ 不得把"没查"显示成"（无）"。
		b.WriteString(T("debug.consumers_not_queried"))
	} else if len(info.Consumers) == 0 {
		b.WriteString(T("debug.consumers_none"))
	} else {
		for _, c := range info.Consumers {
			b.WriteString(fmt.Sprintf(T("debug.consumer_line"), c.PID, c.Layer, c.IsOurs))
		}
	}
	b.WriteString("\r\n⚠ " + diag.NoteText(diag.NoteReflectConsumersUnprovable) + "\r\n")
	b.WriteString(T("debug.no_uninstall"))
	return b.String()
}

// runtimeVersionText 渲染运行时驱动版本：空 ⇒ "未查询（只读诊断不打开驱动句柄）"。
func runtimeVersionText(v string) string {
	if strings.TrimSpace(v) == "" {
		return T("debug.not_queried")
	}
	return v
}

// valueOrUnknown 用于"本该有值但读不到"的诊断字段（⛔ 不显示成 "-" 混淆视听）。
func valueOrUnknown(s string) string {
	if strings.TrimSpace(s) == "" {
		return T("common.unknown")
	}
	return s
}

func valueOrDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return T("common.unknown")
	}
	return s
}

// confirm 是统一的确认框（decisions/008 G7：破坏性动作必须显式确认）。
func confirm(owner walk.Form, title, text string, style walk.MsgBoxStyle) int {
	return walk.MsgBox(owner, title, text, style|walk.MsgBoxIconQuestion|walk.MsgBoxDefButton2)
}
