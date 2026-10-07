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
	ip   netip.Addr
	port uint16
}

// showEditDialog 打开"添加 / 编辑目标"对话框。
//
// 校验与 `decisions/005 §6.1` **同源**（调用 `model.ValidateTarget`），
// 并额外检查 Android IP 在集合内唯一（I1，排除自身）。
//
// ⚠️ `AndroidPort == 445` **允许**，只给**非阻塞**提示，**不得**作为校验错误（decisions/005 §6.1、008 §6）。
//
// 返回 (结果, 用户是否确定, 错误)。
func showEditDialog(owner walk.Form, title, initIP string, initPort uint16, selfID string, all []model.Target) (*editResult, bool, error) {
	var dlg *walk.Dialog
	var leIP *walk.LineEdit
	var nePort *walk.NumberEdit
	var lblHint *walk.Label

	var res *editResult

	dlgDecl := Dialog{
		AssignTo: &dlg,
		Title:    title,
		MinSize:  Size{Width: 460, Height: 250},
		Layout:   VBox{},
		Children: []Widget{
			Label{Text: "Android IP（IPv4 单播；允许回环地址，仅用于本机 / VM 测试）"},
			LineEdit{AssignTo: &leIP, Text: initIP},
			Label{Text: "Android SMB 端口（1–65535；默认 4450，不得写死）"},
			NumberEdit{
				AssignTo: &nePort,
				MinValue: 1,
				MaxValue: 65535,
				Value:    float64(initPort),
			},
			Label{AssignTo: &lblHint, Text: " "},
			Composite{
				Layout: HBox{},
				Children: []Widget{
					HSpacer{},
					PushButton{
						Text: "确定",
						OnClicked: func() {
							r, err := validateEdit(leIP.Text(), uint16(nePort.Value()), selfID, all)
							if err != nil {
								_ = lblHint.SetText("⛔ " + err.Error())
								return
							}
							res = r
							dlg.Accept()
						},
					},
					PushButton{
						Text:      "取消",
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
			_ = lblHint.SetText("提示: " + diag.NoteText(diag.NoteAndroidPortEqualsWindowsPort))
			return
		}
		_ = lblHint.SetText(" ")
	}

	if err := dlgDecl.Create(owner); err != nil {
		return nil, false, err
	}
	nePort.ValueChanged().Attach(updateHint)
	updateHint()

	if dlg.Run() != walk.DlgCmdOK {
		return nil, false, nil
	}
	return res, true, nil
}

// validateEdit 复用 model 的校验口径（同源，不另写一套）。
func validateEdit(ipText string, port uint16, selfID string, all []model.Target) (*editResult, error) {
	ipText = strings.TrimSpace(ipText)
	if ipText == "" {
		return nil, errors.New("请填写 Android IP")
	}
	ip, err := netip.ParseAddr(ipText)
	if err != nil {
		return nil, fmt.Errorf("不是合法的 IP 地址: %q", ipText)
	}
	ip = ip.Unmap()

	// 复用同一份校验实现（ID 用占位符 —— 这里只验 IP/端口，ID 由调用方管理）。
	probeID := selfID
	if probeID == "" {
		probeID = "probe"
	}
	if err := model.ValidateTarget(model.Target{ID: probeID, AndroidIP: ip, AndroidPort: port}); err != nil {
		return nil, err
	}

	// I1：Android IP 在集合内唯一（排除自身）。
	for _, t := range all {
		if t.ID == selfID {
			continue
		}
		if t.AndroidIP == ip {
			return nil, fmt.Errorf("%w: %s（已被另一个目标使用）", model.ErrDuplicateIP, ip)
		}
	}
	return &editResult{ip: ip, port: port}, nil
}

// showInspectDialog 展示**只读**的 WinDivert 使用情况（decisions/008 §15）。
//
// ⛔ 对话框里**没有**任何执行按钮：v1 不提供卸载能力（decisions/007 L6），
// 且**永远不得**出现"可以安全卸载"这一结论（007 §4.7.1 的不可证明性）。
func showInspectDialog(owner walk.Form, info diag.WinDivertInfo) error {
	var dlg *walk.Dialog
	var te *walk.TextEdit

	body := formatInspect(info)
	decl := Dialog{
		AssignTo: &dlg,
		Title:    "检查 WinDivert 使用情况（只读）",
		MinSize:  Size{Width: 620, Height: 420},
		Layout:   VBox{},
		Children: []Widget{
			Label{Text: "以下内容为**只读**检查结果。本程序不提供卸载驱动的能力。"},
			TextEdit{AssignTo: &te, ReadOnly: true, VScroll: true, Text: body},
			Composite{
				Layout: HBox{},
				Children: []Widget{
					HSpacer{},
					PushButton{Text: "关闭", OnClicked: func() { dlg.Accept() }},
				},
			},
		},
	}
	if err := decl.Create(owner); err != nil {
		return err
	}
	dlg.Run()
	return nil
}

func formatInspect(info diag.WinDivertInfo) string {
	var b strings.Builder
	if !info.Available {
		b.WriteString("不可用: " + info.Err + "\r\n")
		return b.String()
	}
	line := func(k, v string) { b.WriteString(k + ": " + v + "\r\n") }
	line("服务存在", strconv.FormatBool(info.ServiceExists))
	line("服务 ImagePath", valueOrDash(info.ServiceImagePath))
	line("ImagePath 指向本程序 runtime", strconv.FormatBool(info.ImagePathIsOurs))
	line("驱动状态", valueOrDash(info.DriverState))
	line("驱动版本", valueOrDash(info.DriverVersion))
	line("WinDivert 文件目录（= exe 目录）", valueOrDash(info.WinDivertDir))
	line("WinDivert 文件完整", strconv.FormatBool(info.RuntimeFilesOK))
	b.WriteString("\r\n可观察使用者（NETWORK / NETWORK_FORWARD / FLOW / SOCKET）：\r\n")
	if len(info.Consumers) == 0 {
		b.WriteString("  （无）\r\n")
	} else {
		for _, c := range info.Consumers {
			b.WriteString(fmt.Sprintf("  PID=%d  层=%s  本程序=%v\r\n", c.PID, c.Layer, c.IsOurs))
		}
	}
	b.WriteString("\r\n⚠ " + diag.NoteText(diag.NoteReflectConsumersUnprovable) + "\r\n")
	b.WriteString("因此本程序**不提供卸载能力**（无法证明系统中不存在其他使用者）。\r\n")
	return b.String()
}

func valueOrDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "（未知）"
	}
	return s
}

// confirm 是统一的确认框（decisions/008 G7：破坏性动作必须显式确认）。
func confirm(owner walk.Form, title, text string, style walk.MsgBoxStyle) int {
	return walk.MsgBox(owner, title, text, style|walk.MsgBoxIconQuestion|walk.MsgBoxDefButton2)
}
