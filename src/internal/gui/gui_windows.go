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
	btnStart, btnStop, btnApply         *walk.PushButton
	btnAdd, btnEdit, btnDel, btnConnect *walk.PushButton
	btnDebug, btnCopy, btnExit          *walk.PushButton

	// 表格右键菜单（decisions/012 §3.4：Connect / Edit / Delete，Connect 为第一项）
	actCtxConnect, actCtxEdit, actCtxDelete *walk.Action
	// dialogOpen 防止双击被处理两次（ItemActivated 的重入保护）。
	dialogOpen bool

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
		walk.MsgBox(nil, T("app.start_failed_title"), err.Error(), walk.MsgBoxIconError)
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
		Title:    T("app.title"),
		MinSize:  Size{Width: 880, Height: 560},
		Size:     Size{Width: 1020, Height: 660},
		Layout:   VBox{},
		Children: []Widget{
			// ① 状态条：全局模式 + 聚合（decisions/008 §9）
			Label{AssignTo: &g.lblMode, Text: "…"},
			// 全局错误横幅（decisions/008 §13：全局类**归并**展示）
			Label{AssignTo: &g.lblBanner, Text: " "},

			// ② 操作区（decisions/008 §7；2026-10-08 修订：删「连接测试」「重试启动」，
			//    顺序 = 启停 → 增删改 → 应用更改 → 连接）
			Composite{
				Layout: HBox{},
				Children: []Widget{
					PushButton{AssignTo: &g.btnStart, Text: T("main.start"), OnClicked: func() { g.onStart() }},
					PushButton{AssignTo: &g.btnStop, Text: T("main.stop"), OnClicked: func() { g.onStop() }},
					VSeparator{},
					PushButton{AssignTo: &g.btnAdd, Text: T("main.add"), OnClicked: func() { g.onAdd() }},
					PushButton{AssignTo: &g.btnEdit, Text: T("main.edit"), OnClicked: func() { g.onEdit() }},
					PushButton{AssignTo: &g.btnDel, Text: T("main.delete"), OnClicked: func() { g.onDel() }},
					// 「应用更改」移到增/改/删之后（与"改列表 → 确认 → 应用"的心智一致）。
					PushButton{AssignTo: &g.btnApply, Text: T("main.apply"), OnClicked: func() { g.onApply() }},
					PushButton{AssignTo: &g.btnConnect, Text: T("main.connect"), OnClicked: func() { g.onConnect() }},
					HSpacer{},
					PushButton{AssignTo: &g.btnDebug, Text: T("main.debug"), OnClicked: func() { g.onDebug() }},
				},
			},

			// 「配置不可用」面板（decisions/008 §14：阻塞式 + 显式修复动作，**不退出程序**）
			Composite{
				AssignTo: &g.pnlUnavailable,
				Layout:   HBox{},
				Visible:  false,
				Children: []Widget{
					Label{Text: T("banner.config_unavailable")},
					HSpacer{},
					PushButton{AssignTo: &g.btnCopyPath, Text: T("btn.copy_config_path"), OnClicked: func() { g.onCopyPath() }},
					PushButton{AssignTo: &g.btnQuarantine, Text: T("main.restart_empty"), OnClicked: func() { g.onQuarantine() }},
				},
			},

			// ③ 目标表格（decisions/008 §5；`运行态` 与 `可达性` 是两列，二者正交）
			//
			// ⚠️ 交互（decisions/012 §3.4 / 008 §5）：
			//   单击 = 选中（无副作用）；**双击 = 编辑**；**右键 = 上下文菜单**（Connect/Edit/Delete）。
			//   右键**先**把命中行设为当前行（见 onTableMouseUp），因此菜单动作作用于**命中的那一行**。
			TableView{
				AssignTo:              &g.tv,
				AlternatingRowBG:      true,
				ColumnsOrderable:      false,
				MultiSelection:        false,
				Model:                 g.mdl,
				Columns:               columns(),
				OnCurrentIndexChanged: func() { g.onSelectionChanged() },
				OnItemActivated:       func() { g.onEdit() },
				OnMouseUp:             func(x, y int, button walk.MouseButton) { g.onTableMouseUp(x, y, button) },
				ContextMenuItems: []MenuItem{
					Action{AssignTo: &g.actCtxConnect, Text: T("main.connect"), OnTriggered: func() { g.onConnect() }},
					Action{AssignTo: &g.actCtxEdit, Text: T("main.edit"), OnTriggered: func() { g.onEdit() }},
					Action{AssignTo: &g.actCtxDelete, Text: T("main.delete"), OnTriggered: func() { g.onDel() }},
				},
			},

			// ⑤ 详情条（decisions/008 §13：错误可见性优先，避免"弹窗地狱"）
			Label{Text: T("detail.header")},
			TextEdit{AssignTo: &g.teDetails, ReadOnly: true, VScroll: true, MinSize: Size{Height: 130}},

			// ⑥ 底部：配置与构建信息
			Composite{
				Layout: HBox{},
				Children: []Widget{
					Label{AssignTo: &g.lblConfig, Text: ""},
					HSpacer{},
					PushButton{AssignTo: &g.btnCopy, Text: T("btn.copy_details"), OnClicked: func() { g.onCopy() }},
					PushButton{AssignTo: &g.btnExit, Text: T("btn.exit"), OnClicked: func() { g.onExit() }},
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
	_ = g.actStart.SetText(T("main.start"))
	g.actStart.Triggered().Attach(func() { g.onStart() })

	g.actStop = walk.NewAction()
	_ = g.actStop.SetText(T("main.stop"))
	g.actStop.Triggered().Attach(func() { g.onStop() })

	g.actApply = walk.NewAction()
	_ = g.actApply.SetText(T("main.apply"))
	g.actApply.Triggered().Attach(func() { g.onApply() })

	open := walk.NewAction()
	_ = open.SetText(T("tray.open_main"))
	open.Triggered().Attach(func() { g.showWindow() })

	exit := walk.NewAction()
	_ = exit.SetText(T("tray.exit"))
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
		out += T("status.op_prefix") + operationText(s.Global.Operation)
	}
	if g.draft.hasUnsaved() {
		out += T("status.unsaved_suffix")
	}
	return out
}

// operationText 把**内部** `Operation` 标识映射为用户可见文案（decisions/013 §6.2）。
//
// ⚠️ 分层（Gate 2026-10-09 裁定）：
//   - 内部标识（`Apply` / `Debug` / …）**保持不变**：程序逻辑、日志、错误关联、
//     以及「复制详情」的「当前操作」行仍用稳定标识（②诊断数据层）；
//   - **只有状态栏**经本函数换成 `应用中` / `调试中` / …（①用户可见文案层）。
//
// 缺 key ⇒ 退回内部标识（可见但可诊断，`T()` 会记 Issue）。
func operationText(op snapshot.Operation) string {
	key := "op." + strings.ToLower(string(op))
	if t := T(key); t != key {
		return t
	}
	return string(op)
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
			p += fmt.Sprintf(T("status.affects_targets"), len(e.AffectedTargetIDs))
		}
		parts = append(parts, p)
	}
	if len(parts) == 0 {
		return " ", false
	}
	return strings.Join(parts, "    "), true
}

func (g *gui) configText(s snapshot.Snapshot) string {
	return fmt.Sprintf(T("detail.config_line"),
		s.Config.Path, s.Config.State, s.Config.SchemaVersion, s.Config.TargetCount, s.Config.BackupExists)
}

// renderDetails 渲染详情条：配置不可用时展示违规项；否则展示选中行 + 最近事件。
func (g *gui) renderDetails() {
	var b strings.Builder

	if g.snap.Global.Mode == snapshot.ModeConfigUnavailable {
		b.WriteString(T("detail.config_unavailable_header"))
		b.WriteString(T("detail.path_label") + g.snap.Config.Path + "\r\n\r\n")
		if len(g.snap.Config.Violations) > 0 {
			b.WriteString(T("detail.problem_label"))
			for _, v := range g.snap.Config.Violations {
				b.WriteString("  · " + v + "\r\n")
			}
		} else {
			b.WriteString(T("detail.problem_see_events"))
		}
		// ⚠️ 处置文案必须与**实际提供的动作**一致：schema_version 过新时
		// 不提供动作②（decisions/006 §6.10），此时不能再写"或 ② ..."。
		if g.ctl.ConfigCode() == diag.CodeConfigSchemaUnsupported {
			b.WriteString(T("detail.fix_action1") +
				T("detail.schema_too_new_1") +
				T("detail.schema_too_new_2"))
		} else {
			b.WriteString(T("detail.fix_action2_1") +
				T("detail.fix_action2_2"))
		}
	}

	if r, ok := g.selectedRow(); ok {
		b.WriteString(fmt.Sprintf(T("detail.target_header"), r.id))
		b.WriteString(fmt.Sprintf("  Android IP : %s\r\n", r.ip))
		b.WriteString(fmt.Sprintf(T("detail.target_port"), r.port))
		b.WriteString(fmt.Sprintf(T("detail.target_runtime"), r.runtimeDisplay()))
		b.WriteString(fmt.Sprintf(T("detail.target_reach"), r.reachDisplay()))
		if n := r.noteDisplay(); n != "" {
			b.WriteString(fmt.Sprintf(T("detail.target_note"), n))
		}
		if r.rtErr != nil {
			b.WriteString(fmt.Sprintf(T("detail.target_failure"), r.rtErr.Code, r.rtErr.Summary))
			if r.rtErr.Detail != "" {
				b.WriteString("     " + r.rtErr.Detail + "\r\n")
			}
			b.WriteString(T("detail.target_advice"))
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
		b.WriteString(T("detail.target_errors_header"))
		for _, e := range targetErrs {
			b.WriteString(fmt.Sprintf(T("detail.target_error_line"), e.Code, e.Summary, shortID(e.TargetID)))
		}
		b.WriteString("\r\n")
	}

	if len(g.snap.Recent) > 0 {
		b.WriteString(T("detail.recent_events_header"))
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
		_ = g.actApply.SetText(fmt.Sprintf(T("status.apply_with_pending"), g.draft.pendingCount()))
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

	// 「连接」：要求该行**已持久化**且其 runtime 正在运行（decisions/012 C1：⛔ 不自动 Start）。
	//  ⛔ "待删除"行不可连接。
	connectable := func(r guiRow) bool {
		return !busy && !unavailable && !r.pendingDelete &&
			r.persisted && r.runtime == snapshot.RTRunning
	}
	if r, ok := g.selectedRow(); ok {
		set(g.btnConnect, connectable(r))
	} else {
		set(g.btnConnect, false)
	}
	set(g.btnDebug, !busy)
	set(g.btnCopy, true)
	set(g.btnExit, !busy)

	// 右键菜单的动作与按钮同步可用性（菜单本身在右键时已把命中行设为当前行）。
	if r, ok := g.selectedRow(); ok {
		g.setActionEnabled(g.actCtxConnect, connectable(r))
		g.setActionEnabled(g.actCtxEdit, !busy && !unavailable)
		g.setActionEnabled(g.actCtxDelete, !busy && !unavailable)
	} else {
		g.setActionEnabled(g.actCtxConnect, false)
		g.setActionEnabled(g.actCtxEdit, false)
		g.setActionEnabled(g.actCtxDelete, false)
	}

	// 「配置不可用」面板（decisions/008 §14）。
	if g.pnlUnavailable != nil {
		g.pnlUnavailable.SetVisible(unavailable)
	}
	// schema_version 过新时**不提供**"以空配置重开"（decisions/006 §6.10）。
	schemaTooNew := g.ctl.ConfigCode() == diag.CodeConfigSchemaUnsupported
	set(g.btnCopyPath, unavailable)
	set(g.btnQuarantine, unavailable && !schemaTooNew && !busy)

	// 删除按钮在"待删除"行上变成"撤销删除"（decisions/008 §8）。
	label := T("main.delete")
	if r, ok := g.selectedRow(); ok && r.pendingDelete {
		label = T("btn.undo_delete")
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
			username:  t.Username,
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
			walk.MsgBox(g.mw, name+T("status.failed"), errText(err), walk.MsgBoxIconError)
		})
	}()
}

func errText(err error) string {
	if errors.Is(err, controller.ErrConfigUnavailable) {
		return T("err.config_unavailable_hint")
	}
	if errors.Is(err, controller.ErrBusy) {
		return T("err.busy")
	}
	var ve *controller.ViolationsError
	if errors.As(err, &ve) {
		return T("err.violations_header") + strings.Join(ve.Violations, "\r\n  · ")
	}
	return fmt.Sprintf("[%s] %v", diag.CodeOr(err, diag.CodeInternalError), err)
}

func (g *gui) onStart() { g.runAsync(T("main.start"), func() error { return g.ctl.Start() }) }

func (g *gui) onStop() { g.runAsync(T("main.stop"), func() error { return g.ctl.Stop() }) }

// onApply 是**唯一**的配置提交入口（decisions/008 **G11**）：
// GUI 提交 Draft，由 Controller 内部按 006 §6.12 的顺序完成
// 校验 → 写盘 → 差量 → Stop(确认关闭) → Start。
//
// ⛔ 不得拆成独立的 Save + Apply 两个可并发命令。
func (g *gui) onApply() {
	draftCopy := g.draft.list()
	g.runAsync(T("main.apply"), func() error { return g.ctl.Apply(draftCopy) })
}

// beginDialog 提供对话框的**重入保护**：双击被派发两次、或菜单与按钮同时触发时，
// ⛔ 不得打开两个编辑框（2026-10-09 Gate 明确要求）。
func (g *gui) beginDialog() bool {
	if g.dialogOpen {
		return false
	}
	g.dialogOpen = true
	return true
}

func (g *gui) endDialog() { g.dialogOpen = false }

// onTableMouseUp 处理表格右键：**先把命中行设为当前行**。
//
// 这样上下文菜单（以及一切"作用于选中行"的动作）作用于**用户真正点到的那一行**，
// ⛔ 不会误操作之前选中的另一行；点在空白处 ⇒ 清空当前行 ⇒ 所有动作变成 no-op。
func (g *gui) onTableMouseUp(x, y int, button walk.MouseButton) {
	if button != walk.RightButton || g.tv == nil {
		return
	}
	_ = g.tv.SetCurrentIndex(g.tv.IndexAt(x, y))
}

// setActionEnabled 只在动作已创建时设置可用性
// （声明式上下文菜单的 Action 在 `Create()` 期间才产生）。
func (g *gui) setActionEnabled(a *walk.Action, on bool) {
	if a != nil {
		_ = a.SetEnabled(on)
	}
}

func (g *gui) onAdd() {
	if !g.beginDialog() {
		return
	}
	defer g.endDialog()

	// 新增：无凭据、默认端口（decisions/005 I8）。
	res, ok, err := showEditDialog(g.mw, T("dialog.add_target"),
		model.Target{AndroidPort: model.DefaultAndroidPort}, g.draft.list())
	if err != nil {
		walk.MsgBox(g.mw, T("err.dialog_open_failed"), err.Error(), walk.MsgBoxIconError)
		return
	}
	if !ok || res == nil {
		return
	}
	if err := g.draft.add(res.ip, res.port, res.username, res.password); err != nil {
		walk.MsgBox(g.mw, T("err.add_failed"), err.Error(), walk.MsgBoxIconError)
		return
	}
	g.render(g.ctl.Snapshot()) // 只重渲染草稿；**不写盘**
}

// onEdit 编辑选中行（**双击**与「编辑」按钮、右键菜单的 Edit 同一入口）。
//
// ⚠️ 凭据语义（decisions/012 §3.4）：对话框拿到**草稿里的原始 Target**
// （含原密码），因此"只改 IP/端口"时密码**原样保留**。
func (g *gui) onEdit() {
	if !g.beginDialog() {
		return
	}
	defer g.endDialog()

	r, ok := g.selectedRow()
	if !ok {
		return
	}
	if r.pendingDelete { // 待删除行：编辑 = 撤销删除
		g.draft.undoRemove(r.id)
		g.render(g.ctl.Snapshot())
		return
	}
	orig, ok := g.draft.byID(r.id)
	if !ok {
		walk.MsgBox(g.mw, T("err.edit_failed"), T("err.target_not_found"), walk.MsgBoxIconError)
		return
	}
	res, ok2, err := showEditDialog(g.mw, T("dialog.edit_target"), orig, g.draft.list())
	if err != nil {
		walk.MsgBox(g.mw, T("err.dialog_open_failed"), err.Error(), walk.MsgBoxIconError)
		return
	}
	if !ok2 || res == nil {
		return
	}
	if !g.draft.update(r.id, res.ip, res.port, res.username, res.password) {
		walk.MsgBox(g.mw, T("err.edit_failed"), T("err.target_not_found"), walk.MsgBoxIconError)
		return
	}
	g.render(g.ctl.Snapshot())
}

func (g *gui) onDel() {
	if !g.beginDialog() {
		return
	}
	defer g.endDialog()

	r, ok := g.selectedRow()
	if !ok {
		return
	}
	if r.pendingDelete { // 撤销删除
		g.draft.undoRemove(r.id)
		g.render(g.ctl.Snapshot())
		return
	}
	msg := fmt.Sprintf(T("dlg.delete_confirm_1")+
		T("dlg.delete_confirm_2"), r.ip, r.port)
	if confirm(g.mw, T("dlg.delete_title"), msg, walk.MsgBoxYesNo) != walk.DlgCmdYes {
		return
	}
	g.draft.remove(r.id)
	g.render(g.ctl.Snapshot())
}

// onDebug 打开「调试」窗口（decisions/011 §2.7 / 008 §15）：
// **只读诊断 + 修复（走接管流程）+ 重启 Workstation + 复制详情**。
//
// ⛔ 窗口内**没有**"强制卸载"；⛔ GUI 不直接执行任何系统命令 —— 动作经 Controller。
func (g *gui) onDebug() {
	go func() {
		info := g.ctl.Inspect()
		g.mw.Synchronize(func() { g.showDebugAndHandle(info) })
	}()
}

func (g *gui) showDebugAndHandle(info diag.WinDivertInfo) {
	if !g.beginDialog() {
		return
	}
	act, err := showDebugDialog(g.mw, info, g.debugRepairAllowed())
	g.endDialog()
	if err != nil {
		walk.MsgBox(g.mw, T("err.dialog_open_failed"), err.Error(), walk.MsgBoxIconError)
		return
	}

	switch act {
	case debugActionCopy:
		if cerr := walk.Clipboard().SetText(g.ctl.CopyDetails()); cerr != nil {
			walk.MsgBox(g.mw, T("err.copy_failed"), cerr.Error(), walk.MsgBoxIconError)
		}
	case debugActionRepair:
		// 长操作（stop→delete→轮询，可达数秒）⇒ 异步，绝不阻塞 UI 线程。
		g.runDebugOp(T("debug.repair"), g.ctl.RepairWinDivert)
	case debugActionRestartWorkstation:
		g.runDebugOp(T("debug.restart_workstation"), g.ctl.RestartWorkstation)
	}
}

// runDebugOp 异步执行一个调试动作，完成后**重新打开调试窗口**以显示新状态。
func (g *gui) runDebugOp(name string, fn func() error) {
	go func() {
		err := fn()
		g.mw.Synchronize(func() {
			if err != nil {
				walk.MsgBox(g.mw, name+T("status.failed"), errText(err), walk.MsgBoxIconError)
			}
			g.onDebug() // 刷新（重新 Inspect）
		})
	}()
}

// debugRepairAllowed 报告"当前是否允许修复 WinDivert"。
//
// ⛔ 必须**没有活动 handle**（decisions/011 §2.7）：运行时不可修复，先"停止"。
func (g *gui) debugRepairAllowed() bool {
	return g.snap.Global.Mode != snapshot.ModeRunning && g.snap.Global.Operation == snapshot.OpNone
}

func (g *gui) onCopy() {
	if err := walk.Clipboard().SetText(g.ctl.CopyDetails()); err != nil {
		walk.MsgBox(g.mw, T("err.copy_failed"), err.Error(), walk.MsgBoxIconError)
	}
}

func (g *gui) onCopyPath() {
	if err := walk.Clipboard().SetText(g.snap.Config.Path); err != nil {
		walk.MsgBox(g.mw, T("err.copy_failed"), err.Error(), walk.MsgBoxIconError)
	}
}

// onQuarantine 执行 decisions/006 §6.10 的动作 ②（改名 + 以空配置重开）。
// ⛔ 只改名：不删除、不覆盖任何文件内容。
func (g *gui) onQuarantine() {
	msg := T("dlg.reset_confirm_1") +
		T("dlg.reset_confirm_2") +
		T("dlg.reset_confirm_3")
	if confirm(g.mw, T("main.restart_empty"), msg, walk.MsgBoxYesNo) != walk.DlgCmdYes {
		return
	}
	g.runAsync(T("dlg.reset_title"), func() error {
		_, err := g.ctl.QuarantineConfig()
		return err
	})
}

// onExit 退出（decisions/008 §8 / §16）。
func (g *gui) onExit() {
	if g.draft.hasUnsaved() {
		switch confirm(g.mw, T("dlg.exit_unsaved_title"),
			T("dlg.exit_unsaved_text"),
			walk.MsgBoxYesNoCancel) {
		case walk.DlgCmdYes:
			// "保存并应用"仍是**单一** Apply（G11），成功后才退出。
			draftCopy := g.draft.list()
			go func() {
				err := g.ctl.Apply(draftCopy)
				g.mw.Synchronize(func() {
					if err != nil {
						walk.MsgBox(g.mw, T("err.apply_failed"), errText(err), walk.MsgBoxIconError)
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
	// ⛔ 退出路径**不**执行显式清理（不 `sc stop` / 不 `sc delete` / 不删任何文件）：
	//    进程终止 ⇒ WinDivert handle 全部关闭 ⇒ 驱动卸载 ⇒ 驱动**自删服务注册项**
	//    ⇒ 实测 `sc query WinDivert` = 1060（2026-10-10，`local_dev/logs/gui-checklist/g17.out`）。
	//
	// ⚠️ 该「结果正确」**不等价于** Stop 的显式清理：Stop 会 stop → delete → 轮询 1060 → 等 `.sys` 解锁，
	//    并**检测/报告**失败；Exit 没有这套动作、也没有错误上报点。见 `decisions/011` §2.4。
	// ⚠️ 不要把这里读成"因 `decisions/007` L5 而保留"：L5 已被 `decisions/011` §2.4 **反转**（Stop 必须显式删服务）。
	if g.ni != nil {
		_ = g.ni.SetVisible(false)
		_ = g.ni.Dispose()
	}
	walk.App().Exit(0)
}
