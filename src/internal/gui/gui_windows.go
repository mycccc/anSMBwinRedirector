//go:build windows

// Package gui 是 v1 的 **GUI / Tray 层**（decisions/003 信息架构 + decisions/008 交互契约）。
//
// ⛔ 硬边界（decisions/008 §2.2 / **G1–G3**）：
//
//	不 import divert、不持有 WinDivert handle、不调用任何 WinDivert API；
//	不执行系统命令（sc / windivertctl / netsh）；
//	不直接读写 config.json（一切持久化只经 Controller.Apply / QuarantineConfig）；
//	不自行推断运行态 / 可达性（只读 Snapshot）。
//
// 一句话：**GUI 不能"做"网络，只能"看"状态和"提"命令。**
package gui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lxn/walk"
	. "github.com/lxn/walk/declarative"

	"ansmbwinhelper/internal/app"
	"ansmbwinhelper/internal/controller"
	"ansmbwinhelper/internal/diag"
	"ansmbwinhelper/internal/model"
	"ansmbwinhelper/internal/snapshot"
)

// gui 是 GUI 层的全部状态。
//
// ⚠️ 它只持有两类东西（decisions/008 **G12**）：
//  1. `snapshot.Snapshot` —— Runtime/Config 的**唯一**真相来源（G6）；
//  2. `draft`             —— GUI 本地的编辑模型，**不属于** Runtime 状态。
type gui struct {
	app *app.App
	ctl *controller.Controller

	mw  *walk.MainWindow
	ni  *walk.NotifyIcon
	ico map[iconKind]*walk.Icon

	// 托盘菜单动作（decisions/008 §16：托盘 = 全局控制，与主窗口同一份快照）
	actState, actStart, actStop, actApply *walk.Action

	// 操作区
	btnStart, btnStop, btnApply                     *walk.PushButton
	btnAdd, btnEdit, btnDel                         *walk.PushButton
	btnTest, btnRetry, btnInspect, btnCopy, btnExit *walk.PushButton

	// 「配置不可用」专用面板（decisions/008 §14）
	pnlUnavailable *walk.Composite
	btnCopyPath    *walk.PushButton
	btnQuarantine  *walk.PushButton

	// 状态区
	lblMode   *walk.Label
	lblBanner *walk.Label
	lblConfig *walk.Label
	tv        *walk.TableView
	mdl       *targetModel
	teDetails *walk.TextEdit

	draft *draft
	snap  snapshot.Snapshot

	// exiting 表示"正在退出"：此后 Closing 不再被取消（decisions/008 §16）。
	exiting bool
}

// Run 构建 GUI、进入消息循环，并在退出时完成 runtime 停机；返回进程退出码。
//
// ⚠️ 互斥体的释放**不在这里** —— 它是退出序列的最后一步，由入口在 `Run` 返回后调用。
func Run(a *app.App) int {
	g := &gui{
		app:   a,
		ctl:   a.Ctl,
		draft: newDraft(),
		mdl:   &targetModel{},
		ico:   map[iconKind]*walk.Icon{},
	}
	// ⚠️ 必须在创建任何窗口**之前**（原因见 commoncontrols_windows.go）。
	initCommonControls()

	if err := g.build(); err != nil {
		// 构建失败时窗口还不存在 ⇒ 用 nil owner 的 MessageBox。
		walk.MsgBox(nil, "anSMBwinRedirector 启动失败", err.Error(), walk.MsgBoxIconError)
		return 1
	}

	g.draft.reset(g.ctl.Snapshot().Persisted)

	// 跨线程更新必须回到 UI 线程（decisions/008 **G4**）。
	g.ctl.Subscribe(func(s snapshot.Snapshot) {
		g.mw.Synchronize(func() { g.render(s) })
	})
	g.render(g.ctl.Snapshot())

	// 启动时显示主窗口（托盘是常驻形态；关闭窗口才隐藏到托盘，decisions/008 §16）。
	g.mw.SetVisible(true)

	g.mw.Run() // 消息循环：直到显式"退出"（关窗只是隐藏到托盘）

	// 退出序列（decisions/007 §4.6）：只停 runtime。
	// ⛔ 不执行 sc stop / sc delete、不卸载驱动、不删除 runtime 文件（L5/L6）。
	g.ctl.Shutdown(3 * time.Second)
	return 0
}

// build 创建主窗口与托盘。
func (g *gui) build() error {
	for _, k := range []iconKind{iconStopped, iconRunning, iconRunningFailed, iconUnavailable} {
		ic, err := makeTrayIcon(k)
		if err != nil {
			return err
		}
		g.ico[k] = ic
	}

	decl := MainWindow{
		AssignTo: &g.mw,
		Title:    "anSMBwinRedirector",
		MinSize:  Size{Width: 880, Height: 560},
		Size:     Size{Width: 1000, Height: 660},
		Layout:   VBox{},
		Children: []Widget{
			// ① 状态条：全局模式 + 聚合（decisions/008 §9）
			Label{AssignTo: &g.lblMode, Text: "…"},
			// 全局错误横幅（decisions/008 §13：全局类**归并**展示）
			Label{AssignTo: &g.lblBanner, Text: " "},

			// ② 操作区（decisions/008 §7）
			Composite{
				Layout: HBox{},
				Children: []Widget{
					PushButton{AssignTo: &g.btnStart, Text: "启动", OnClicked: func() { g.onStart() }},
					PushButton{AssignTo: &g.btnStop, Text: "停止", OnClicked: func() { g.onStop() }},
					PushButton{AssignTo: &g.btnApply, Text: "应用更改", OnClicked: func() { g.onApply() }},
					VSeparator{},
					PushButton{AssignTo: &g.btnAdd, Text: "添加", OnClicked: func() { g.onAdd() }},
					PushButton{AssignTo: &g.btnEdit, Text: "编辑", OnClicked: func() { g.onEdit() }},
					PushButton{AssignTo: &g.btnDel, Text: "删除", OnClicked: func() { g.onDel() }},
					VSeparator{},
					PushButton{AssignTo: &g.btnTest, Text: "连接测试", OnClicked: func() { g.onTest() }},
					PushButton{AssignTo: &g.btnRetry, Text: "重试启动", OnClicked: func() { g.onRetry() }},
					HSpacer{},
					PushButton{AssignTo: &g.btnInspect, Text: "检查 WinDivert（只读）", OnClicked: func() { g.onInspect() }},
				},
			},

			// 「配置不可用」面板（decisions/008 §14：阻塞式 + 显式修复动作，**不退出程序**）
			Composite{
				AssignTo: &g.pnlUnavailable,
				Layout:   HBox{},
				Visible:  false,
				Children: []Widget{
					Label{Text: "⛔ 配置不可用 —— 修复前不会启动重定向，也不会覆盖该文件。"},
					HSpacer{},
					PushButton{AssignTo: &g.btnCopyPath, Text: "复制配置路径", OnClicked: func() { g.onCopyPath() }},
					PushButton{AssignTo: &g.btnQuarantine, Text: "用空配置重新开始", OnClicked: func() { g.onQuarantine() }},
				},
			},

			// ③ 目标表格（decisions/008 §5；`运行态` 与 `可达性` 是两列，二者正交）
			TableView{
				AssignTo:              &g.tv,
				AlternatingRowBG:      true,
				ColumnsOrderable:      false,
				MultiSelection:        false,
				Model:                 g.mdl,
				Columns:               columns(),
				OnCurrentIndexChanged: func() { g.onSelectionChanged() },
			},

			// ⑤ 详情条（decisions/008 §13：错误可见性优先，避免"弹窗地狱"）
			Label{Text: "详情（选中行 / 全局）："},
			TextEdit{AssignTo: &g.teDetails, ReadOnly: true, VScroll: true, MinSize: Size{Height: 130}},

			// ⑥ 底部：配置与构建信息
			Composite{
				Layout: HBox{},
				Children: []Widget{
					Label{AssignTo: &g.lblConfig, Text: ""},
					HSpacer{},
					PushButton{AssignTo: &g.btnCopy, Text: "复制详情", OnClicked: func() { g.onCopy() }},
					PushButton{AssignTo: &g.btnExit, Text: "退出", OnClicked: func() { g.onExit() }},
				},
			},
		},
	}
	if err := decl.Create(); err != nil {
		return err
	}
	g.tv.SetCellStyler(g.mdl)

	// 关闭主窗口 = **隐藏到托盘**（decisions/008 §16）；真正退出只走显式"退出"。
	g.mw.Closing().Attach(func(canceled *bool, reason walk.CloseReason) {
		if g.exiting {
			return
		}
		*canceled = true
		g.mw.Hide()
	})

	return g.buildTray()
}

func columns() []TableViewColumn {
	out := make([]TableViewColumn, 0, len(tableColumns))
	for _, c := range tableColumns {
		out = append(out, TableViewColumn{Title: c.Title, Width: c.Width})
	}
	return out
}

// buildTray 建立托盘图标与菜单（decisions/008 §16）。
//
// ⛔ 托盘只做**全局控制**：不显示单个 Android IP，也不承担主窗口的管理职责
// （v1 **不做**"重试全部失败目标"）。
// ⛔ 菜单里**不含**任何 WinDivert / 驱动 / 系统命令入口。
func (g *gui) buildTray() error {
	ni, err := walk.NewNotifyIcon(g.mw)
	if err != nil {
		return err
	}
	g.ni = ni
	if err := ni.SetIcon(g.ico[iconStopped]); err != nil {
		return err
	}
	ni.MouseUp().Attach(func(x, y int, button walk.MouseButton) {
		if button == walk.LeftButton {
			g.showWindow()
		}
	})
	ni.MessageClicked().Attach(func() { g.showWindow() })

	// 状态行：不可点击（decisions/008 §16）。
	g.actState = walk.NewAction()
	_ = g.actState.SetEnabled(false)

	g.actStart = walk.NewAction()
	_ = g.actStart.SetText("启动")
	g.actStart.Triggered().Attach(func() { g.onStart() })

	g.actStop = walk.NewAction()
	_ = g.actStop.SetText("停止")
	g.actStop.Triggered().Attach(func() { g.onStop() })

	g.actApply = walk.NewAction()
	_ = g.actApply.SetText("应用更改")
	g.actApply.Triggered().Attach(func() { g.onApply() })

	open := walk.NewAction()
	_ = open.SetText("打开主窗口(&O)")
	open.Triggered().Attach(func() { g.showWindow() })

	exit := walk.NewAction()
	_ = exit.SetText("退出(&X)")
	exit.Triggered().Attach(func() { g.onExit() })

	for _, a := range []*walk.Action{
		g.actState, walk.NewSeparatorAction(),
		g.actStart, g.actStop, g.actApply, walk.NewSeparatorAction(),
		open, walk.NewSeparatorAction(),
		exit,
	} {
		if err := g.ni.ContextMenu().Actions().Add(a); err != nil {
			return err
		}
	}
	return ni.SetVisible(true)
}

func (g *gui) showWindow() {
	g.mw.Synchronize(func() {
		g.mw.Show()
		_ = g.mw.SetFocus()
	})
}

// --- 渲染（唯一数据来源 = Snapshot + Draft）--------------------------------

func (g *gui) render(s snapshot.Snapshot) {
	g.snap = s

	// 基线同步（⚠️ 这里的判据很容易写错，实机验证时踩过）：
	//
	// Apply 成功后，快照的 Persisted 已经变成"刚刚保存的内容"，但**草稿自己保存的旧基线**
	// 仍是上一版 —— 所以 `hasUnsaved()` 这时**仍然返回 true**。
	// 若用 `if !hasUnsaved() { reset }` 当条件，基线永远刷不新，
	// 界面上的"⚠ 有未保存更改"会**一直挂着**（实机复现过）。
	//
	// 正确判据：**只要草稿内容与磁盘内容一致，就把磁盘当作新基线**。
	// 另保留"草稿本身没编辑过但磁盘变了"（配置重载 / quarantine）的分支。
	if sameTargets(g.draft.list(), s.Persisted) || !g.draft.hasUnsaved() {
		g.draft.reset(s.Persisted)
	}

	g.mdl.setRows(g.buildRows(s))

	_ = g.lblMode.SetText(g.modeText(s))
	banner, _ := g.bannerText(s)
	_ = g.lblBanner.SetText(banner)
	_ = g.lblConfig.SetText(g.configText(s))
	g.renderDetails()
	g.updateTray(s)
	g.updateEnabled(s)
}

// sameTargets 报告两个 Target 列表是否**逐项相同**（含 ID —— ID 是身份，decisions/006 §6.1）。
func sameTargets(a, b []model.Target) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (g *gui) modeText(s snapshot.Snapshot) string {
	out := "● " + s.StatusLine()
	if s.Global.Operation != snapshot.OpNone {
		out += "   ▸ 正在执行: " + string(s.Global.Operation)
	}
	if g.draft.hasUnsaved() {
		out += "   ⚠ 有未保存更改"
	}
	return out
}

// bannerText 归并**全局范围**的错误（decisions/009 §3：Target 级**不得**聚合到全局）。
func (g *gui) bannerText(s snapshot.Snapshot) (string, bool) {
	var parts []string
	for _, e := range s.Errors {
		if e.Scope != diag.ScopeGlobal {
			continue
		}
		p := fmt.Sprintf("⛔ [%s] %s", e.Code, e.Summary)
		if e.Detail != "" {
			p += " — " + e.Detail
		}
		if len(e.AffectedTargetIDs) > 0 {
			p += fmt.Sprintf("（影响 %d 个目标）", len(e.AffectedTargetIDs))
		}
		parts = append(parts, p)
	}
	if len(parts) == 0 {
		return " ", false
	}
	return strings.Join(parts, "    "), true
}

func (g *gui) configText(s snapshot.Snapshot) string {
	return fmt.Sprintf("配置: %s  ·  状态=%s  ·  schema=%d  ·  目标=%d  ·  备份=%v",
		s.Config.Path, s.Config.State, s.Config.SchemaVersion, s.Config.TargetCount, s.Config.BackupExists)
}

// renderDetails 渲染详情条：配置不可用时展示违规项；否则展示选中行 + 最近事件。
func (g *gui) renderDetails() {
	var b strings.Builder

	if g.snap.Global.Mode == snapshot.ModeConfigUnavailable {
		b.WriteString("⛔ 配置不可用（在修复前不会启动重定向，也不会覆盖该文件）\r\n\r\n")
		b.WriteString("路径: " + g.snap.Config.Path + "\r\n\r\n")
		if len(g.snap.Config.Violations) > 0 {
			b.WriteString("问题:\r\n")
			for _, v := range g.snap.Config.Violations {
				b.WriteString("  · " + v + "\r\n")
			}
		} else {
			b.WriteString("问题: （详见下方最近事件）\r\n")
		}
		// ⚠️ 处置文案必须与**实际提供的动作**一致：schema_version 过新时
		// 不提供动作②（decisions/006 §6.10），此时不能再写"或 ② ..."。
		if g.ctl.ConfigCode() == diag.CodeConfigSchemaUnsupported {
			b.WriteString("\r\n处置: ① 复制路径后人工修复该文件。\r\n" +
				"      （该文件的 schema_version 高于本程序支持的版本，因此**不提供**\"用空配置重新开始\"；" +
				"请升级程序，或手工处理该文件。）\r\n")
		} else {
			b.WriteString("\r\n处置: ① 复制路径后人工修复该文件；或 ② 用空配置重新开始（原文件会被改名为 " +
				"config.json.corrupt-<时间戳>，内容保留、不删除）。\r\n")
		}
	}

	if r, ok := g.selectedRow(); ok {
		b.WriteString(fmt.Sprintf("目标 %s\r\n", r.id))
		b.WriteString(fmt.Sprintf("  Android IP : %s\r\n", r.ip))
		b.WriteString(fmt.Sprintf("  SMB 端口   : %d\r\n", r.port))
		b.WriteString(fmt.Sprintf("  运行态     : %s\r\n", r.runtimeDisplay()))
		b.WriteString(fmt.Sprintf("  可达性     : %s\r\n", r.reachDisplay()))
		if n := r.noteDisplay(); n != "" {
			b.WriteString(fmt.Sprintf("  说明       : %s\r\n", n))
		}
		if r.rtErr != nil {
			b.WriteString(fmt.Sprintf("  ⛔ 失败原因 : [%s] %s\r\n", r.rtErr.Code, r.rtErr.Summary))
			if r.rtErr.Detail != "" {
				b.WriteString("     " + r.rtErr.Detail + "\r\n")
			}
			b.WriteString("     建议     : 检查权限 / 驱动状态后，使用「重试启动」。\r\n")
		}
		b.WriteString("\r\n")
	}

	// Target 范围的错误：逐目标列出（**不**聚合到全局）。
	var targetErrs []diag.ErrorInfo
	for _, e := range g.snap.Errors {
		if e.Scope == diag.ScopeTarget {
			targetErrs = append(targetErrs, e)
		}
	}
	if len(targetErrs) > 0 {
		b.WriteString("目标级错误：\r\n")
		for _, e := range targetErrs {
			b.WriteString(fmt.Sprintf("  [%s] %s（目标 %s）\r\n", e.Code, e.Summary, shortID(e.TargetID)))
		}
		b.WriteString("\r\n")
	}

	if len(g.snap.Recent) > 0 {
		b.WriteString("最近事件：\r\n")
		evs := g.snap.Recent
		if len(evs) > 6 {
			evs = evs[len(evs)-6:]
		}
		for _, e := range evs {
			b.WriteString(fmt.Sprintf("  %s %-5s %s\r\n", e.Time.Format("15:04:05"), e.Level, e.Message))
		}
	}
	_ = g.teDetails.SetText(b.String())
}

func shortID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}

func (g *gui) updateTray(s snapshot.Snapshot) {
	if g.ni == nil {
		return
	}
	busy := s.Global.Operation != snapshot.OpNone
	unavailable := s.Global.Mode == snapshot.ModeConfigUnavailable
	running := s.Global.Mode == snapshot.ModeRunning

	_ = g.ni.SetIcon(g.ico[g.trayIconKind(s)])
	_ = g.ni.SetToolTip("anSMBwinRedirector · " + s.StatusLine())

	// 菜单与主窗口使用**同一份快照**（decisions/008 §16：不持有独立状态）。
	if g.actState != nil {
		_ = g.actState.SetText("● " + s.StatusLine())
	}
	if g.actStart != nil {
		_ = g.actStart.SetEnabled(!busy && !unavailable && !running)
	}
	if g.actStop != nil {
		_ = g.actStop.SetEnabled(!busy && running)
	}
	if g.actApply != nil {
		has := g.draft.hasUnsaved()
		_ = g.actApply.SetText(fmt.Sprintf("应用更改（%d 项未保存）", g.draft.pendingCount()))
		_ = g.actApply.SetVisible(has)
		_ = g.actApply.SetEnabled(has && !busy && !unavailable)
	}
}

// trayIconKind 由**同一份快照**推导图标状态（decisions/008 §16）。
func (g *gui) trayIconKind(s snapshot.Snapshot) iconKind {
	switch s.Global.Mode {
	case snapshot.ModeConfigUnavailable:
		return iconUnavailable
	case snapshot.ModeRunning:
		if _, _, failed := s.Aggregate(); failed > 0 {
			return iconRunningFailed
		}
		return iconRunning
	default:
		return iconStopped
	}
}

// updateEnabled 依据**快照 + 草稿 + 串行化**推导控件可用性（decisions/008 **G5**）。
func (g *gui) updateEnabled(s snapshot.Snapshot) {
	busy := s.Global.Operation != snapshot.OpNone
	unavailable := s.Global.Mode == snapshot.ModeConfigUnavailable
	running := s.Global.Mode == snapshot.ModeRunning

	set := func(btn *walk.PushButton, on bool) {
		if btn != nil {
			btn.SetEnabled(on)
		}
	}
	// ⛔ 目标数为 0 时**仍然允许启动**（decisions/006 §6.4 / 008 §9：`运行中 · 0 个目标`）。
	set(g.btnStart, !busy && !unavailable && !running)
	set(g.btnStop, !busy && running)
	set(g.btnApply, !busy && !unavailable && g.draft.hasUnsaved())

	set(g.btnAdd, !busy && !unavailable)
	set(g.btnEdit, !busy && !unavailable && g.hasSelection())
	set(g.btnDel, !busy && !unavailable && g.hasSelection())

	set(g.btnTest, !busy && g.hasSelection())
	// 「重试启动」用**磁盘配置**重试，与草稿无关（故不因未保存更改而禁用）。
	set(g.btnRetry, !busy && !unavailable && running && g.selectedFailed())
	set(g.btnInspect, !busy)
	set(g.btnCopy, true)
	set(g.btnExit, !busy)

	// 「配置不可用」面板（decisions/008 §14）。
	if g.pnlUnavailable != nil {
		g.pnlUnavailable.SetVisible(unavailable)
	}
	// schema_version 过新时**不提供**"以空配置重开"（decisions/006 §6.10）。
	schemaTooNew := g.ctl.ConfigCode() == diag.CodeConfigSchemaUnsupported
	set(g.btnCopyPath, unavailable)
	set(g.btnQuarantine, unavailable && !schemaTooNew && !busy)

	// 删除按钮在"待删除"行上变成"撤销删除"（decisions/008 §8）。
	label := "删除"
	if r, ok := g.selectedRow(); ok && r.pendingDelete {
		label = "撤销删除"
	}
	if g.btnDel != nil {
		_ = g.btnDel.SetText(label)
	}
}

// --- 表格数据 ---------------------------------------------------------------

// buildRows 合并 Draft 与 Snapshot（decisions/008 §3.3 的渲染来源表）。
func (g *gui) buildRows(s snapshot.Snapshot) []guiRow {
	rows := make([]guiRow, 0, len(g.draft.targets)+len(g.draft.deleted))

	for _, t := range g.draft.targets {
		r := guiRow{
			id:        t.ID,
			ip:        t.AndroidIP.String(),
			port:      t.AndroidPort,
			persisted: g.draft.isPersisted(t.ID),
			unsaved:   g.draft.isUnsavedTarget(t.ID),
			note:      noteFor(t),
		}
		if rt, ok := s.Runtime[t.ID]; ok {
			r.runtime, r.rtErr = rt.State, rt.Err
		} else {
			r.runtime = snapshot.RTStopped
		}
		if re, ok := s.Reach[t.ID]; ok {
			r.reach, r.lastChecked = re.State, re.LastChecked
		} else {
			r.reach = snapshot.ReachUnknown
		}
		rows = append(rows, r)
	}

	// 待删除：仍存在于磁盘配置，草稿已移除（decisions/008 §8）。
	for _, t := range g.draft.pendingDeleteTargets() {
		r := guiRow{
			id:            t.ID,
			ip:            t.AndroidIP.String(),
			port:          t.AndroidPort,
			pendingDelete: true,
			persisted:     true,
			runtime:       snapshot.RTStopped,
			reach:         snapshot.ReachUnknown,
		}
		if rt, ok := s.Runtime[t.ID]; ok {
			r.runtime, r.rtErr = rt.State, rt.Err
		}
		if re, ok := s.Reach[t.ID]; ok {
			r.reach, r.lastChecked = re.State, re.LastChecked
		}
		rows = append(rows, r)
	}
	return rows
}

// noteFor 生成**非阻塞**提示（decisions/009 §2.3 的 Note：不是错误）。
func noteFor(t model.Target) string {
	if t.AndroidPort == model.WindowsMatchPort {
		return diag.NoteText(diag.NoteAndroidPortEqualsWindowsPort)
	}
	return ""
}

func (g *gui) hasSelection() bool {
	_, ok := g.selectedRow()
	return ok
}

func (g *gui) selectedRow() (guiRow, bool) {
	if g.tv == nil {
		return guiRow{}, false
	}
	return g.mdl.row(g.tv.CurrentIndex())
}

func (g *gui) selectedFailed() bool {
	r, ok := g.selectedRow()
	return ok && r.runtime == snapshot.RTFailed
}

func (g *gui) onSelectionChanged() {
	g.renderDetails()
	g.updateEnabled(g.snap)
}

// --- 命令（只经 Controller；GUI 不自己做任何网络 / 文件操作）-----------------

func (g *gui) runAsync(name string, fn func() error) {
	go func() {
		err := fn()
		if err == nil {
			return
		}
		g.mw.Synchronize(func() {
			// 用户**显式发起**的动作失败 ⇒ 立即明确告知；同样的错误也会出现在横幅/详情里。
			walk.MsgBox(g.mw, name+"失败", errText(err), walk.MsgBoxIconError)
		})
	}()
}

func errText(err error) string {
	if errors.Is(err, controller.ErrConfigUnavailable) {
		return "配置不可用：请先修复配置文件（见详情条）。"
	}
	if errors.Is(err, controller.ErrBusy) {
		return "已有操作正在进行，请稍候。"
	}
	var ve *controller.ViolationsError
	if errors.As(err, &ve) {
		return "配置校验失败：\r\n  · " + strings.Join(ve.Violations, "\r\n  · ")
	}
	return fmt.Sprintf("[%s] %v", diag.CodeOr(err, diag.CodeInternalError), err)
}

func (g *gui) onStart() { g.runAsync("启动", func() error { return g.ctl.Start() }) }

func (g *gui) onStop() { g.runAsync("停止", func() error { return g.ctl.Stop() }) }

// onApply 是**唯一**的配置提交入口（decisions/008 **G11**）：
// GUI 提交 Draft，由 Controller 内部按 006 §6.12 的顺序完成
// 校验 → 写盘 → 差量 → Stop(确认关闭) → Start。
//
// ⛔ 不得拆成独立的 Save + Apply 两个可并发命令。
func (g *gui) onApply() {
	draftCopy := g.draft.list()
	g.runAsync("应用更改", func() error { return g.ctl.Apply(draftCopy) })
}

func (g *gui) onAdd() {
	res, ok, err := showEditDialog(g.mw, "添加目标", "", model.DefaultAndroidPort, "", g.draft.list())
	if err != nil {
		walk.MsgBox(g.mw, "打开对话框失败", err.Error(), walk.MsgBoxIconError)
		return
	}
	if !ok || res == nil {
		return
	}
	if err := g.draft.add(res.ip, res.port); err != nil {
		walk.MsgBox(g.mw, "添加失败", err.Error(), walk.MsgBoxIconError)
		return
	}
	g.render(g.ctl.Snapshot()) // 只重渲染草稿；**不写盘**
}

func (g *gui) onEdit() {
	r, ok := g.selectedRow()
	if !ok {
		return
	}
	if r.pendingDelete {
		g.draft.undoRemove(r.id)
		g.render(g.ctl.Snapshot())
		return
	}
	res, ok, err := showEditDialog(g.mw, "编辑目标", r.ip, r.port, r.id, g.draft.list())
	if err != nil {
		walk.MsgBox(g.mw, "打开对话框失败", err.Error(), walk.MsgBoxIconError)
		return
	}
	if !ok || res == nil {
		return
	}
	if !g.draft.update(r.id, res.ip, res.port) {
		walk.MsgBox(g.mw, "编辑失败", "未找到该目标", walk.MsgBoxIconError)
		return
	}
	g.render(g.ctl.Snapshot())
}

func (g *gui) onDel() {
	r, ok := g.selectedRow()
	if !ok {
		return
	}
	if r.pendingDelete { // 撤销删除
		g.draft.undoRemove(r.id)
		g.render(g.ctl.Snapshot())
		return
	}
	msg := fmt.Sprintf("确定删除目标 %s:%d 吗？\r\n\r\n"+
		"删除只是**编辑草稿**；点击「应用更改」后才真正生效（decisions/008 §8）。", r.ip, r.port)
	if confirm(g.mw, "删除目标", msg, walk.MsgBoxYesNo) != walk.DlgCmdYes {
		return
	}
	g.draft.remove(r.id)
	g.render(g.ctl.Snapshot())
}

// onTest 连接测试（decisions/007 §4.11 / 008 §12）：
//
//	运行中 → 探测 AndroidIP:445（走完整链路）
//	未运行 → 探测 AndroidIP:AndroidPort（只验 Android 侧可达）
//
// ⚠️ 失败是**非错误**（`NOTE_TARGET_UNREACHABLE`），所以这里不弹错误框；
// 结果由 Controller 写入快照的 Reach 并重新渲染。
func (g *gui) onTest() {
	r, ok := g.selectedRow()
	if !ok {
		return
	}
	id := r.id
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = g.ctl.TestConnection(ctx, id) // 结果/失败都经快照呈现
	}()
}

// onRetry 单目标重试启动（decisions/007 §4.9）。
//
// 用**磁盘配置**再次 Apply：manager 只对"当前没有 runtime / 已失败"的目标动手，
// 其余目标不受影响 —— 因此它天然是"按 Target 隔离"的重试。
func (g *gui) onRetry() {
	persisted := append([]model.Target(nil), g.snap.Persisted...)
	g.runAsync("重试启动", func() error { return g.ctl.Apply(persisted) })
}

// onInspect 只读检查（decisions/007 §4.7.3 / 008 §15）：**只有检查，没有卸载**。
func (g *gui) onInspect() {
	go func() {
		info := g.ctl.Inspect()
		g.mw.Synchronize(func() {
			if err := showInspectDialog(g.mw, info); err != nil {
				walk.MsgBox(g.mw, "打开对话框失败", err.Error(), walk.MsgBoxIconError)
			}
		})
	}()
}

func (g *gui) onCopy() {
	if err := walk.Clipboard().SetText(g.ctl.CopyDetails()); err != nil {
		walk.MsgBox(g.mw, "复制失败", err.Error(), walk.MsgBoxIconError)
	}
}

func (g *gui) onCopyPath() {
	if err := walk.Clipboard().SetText(g.snap.Config.Path); err != nil {
		walk.MsgBox(g.mw, "复制失败", err.Error(), walk.MsgBoxIconError)
	}
}

// onQuarantine 执行 decisions/006 §6.10 的动作 ②（改名 + 以空配置重开）。
// ⛔ 只改名：不删除、不覆盖任何文件内容。
func (g *gui) onQuarantine() {
	msg := "将把当前配置文件改名为 config.json.corrupt-<时间戳>，\r\n" +
		"然后用**空配置**重新开始。\r\n\r\n" +
		"原文件内容会完整保留在新名字下（不删除、不覆盖）。\r\n\r\n确定继续吗？"
	if confirm(g.mw, "用空配置重新开始", msg, walk.MsgBoxYesNo) != walk.DlgCmdYes {
		return
	}
	g.runAsync("以空配置重新开始", func() error {
		_, err := g.ctl.QuarantineConfig()
		return err
	})
}

// onExit 退出（decisions/008 §8 / §16）。
func (g *gui) onExit() {
	if g.draft.hasUnsaved() {
		switch confirm(g.mw, "有未保存更改",
			"是否保存并应用更改后退出？\r\n\r\n是 = 保存并应用\r\n否 = 放弃更改\r\n取消 = 不退出",
			walk.MsgBoxYesNoCancel) {
		case walk.DlgCmdYes:
			// "保存并应用"仍是**单一** Apply（G11），成功后才退出。
			draftCopy := g.draft.list()
			go func() {
				err := g.ctl.Apply(draftCopy)
				g.mw.Synchronize(func() {
					if err != nil {
						walk.MsgBox(g.mw, "应用更改失败", errText(err), walk.MsgBoxIconError)
						return
					}
					g.doExit()
				})
			}()
			return
		case walk.DlgCmdNo:
			// 放弃更改
		default:
			return // 取消
		}
	}
	g.doExit()
}

func (g *gui) doExit() {
	if g.exiting {
		return
	}
	g.exiting = true
	// ⛔ 不执行 sc stop / sc delete、不卸载驱动、不删除 runtime 文件（decisions/007 L5/L6）。
	if g.ni != nil {
		_ = g.ni.SetVisible(false)
		_ = g.ni.Dispose()
	}
	walk.App().Exit(0)
}
