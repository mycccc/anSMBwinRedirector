//go:build windows

package gui

import (
	"fmt"
	"time"

	"github.com/lxn/walk"

	"ansmbwinhelper/internal/diag"
	"ansmbwinhelper/internal/snapshot"
)

// tableColumn 是表格列定义（decisions/003 §4 / 008 §5.1）。
type tableColumn struct {
	Title string
	Width int
}

// tableColumns 的列顺序即渲染顺序。
//
// ⚠️ `运行态` 与 `可达性` 是**两列**（decisions/008 §0 对 003 的细化）：
// 两者正交（007 §4.2 四维模型），合并成一列会让"运行中但 Android 没开"变成看起来像故障。
//
// ⚠️ `用户名` 列来自 **Draft**（decisions/012 §3.1：Target 含凭据字段）。
// ⛔ **没有密码列**，也**不得**用任何可推断密码的占位（长度/掩码字符数）。
var tableColumns = []tableColumn{
	{Title: "Android IP", Width: 150},
	{Title: T("table.smb_port"), Width: 85},
	{Title: T("table.username"), Width: 110},
	{Title: T("table.runtime"), Width: 140},
	{Title: T("table.reachability"), Width: 140},
	{Title: T("table.note"), Width: 240},
}

// guiRow 是表格的一行（**渲染专用**）。
//
// 来源（decisions/008 §3.3）—— 这张表是"渲染来源"的权威落点：
//
//	ip / port / username      ← GUI Draft（草稿）
//	runtime / rtErr         ← Snapshot.Runtime
//	reach / lastChecked     ← Snapshot.Reach
//	pendingDelete / unsaved / persisted / note ← GUI 派生（**不是** Runtime 状态）
//
// ⛔ 这里**不携带密码**（哪怕是掩码）—— 密码不属于渲染数据（decisions/012 §3.3）。
type guiRow struct {
	id string

	// --- 来自 Draft ---
	ip       string
	port     uint16
	username string

	// --- 来自 Snapshot（Runtime，实际生效）---
	runtime snapshot.RuntimeState
	rtErr   *diag.ErrorInfo

	// --- 来自 Snapshot（可达性，与 Runtime 正交）---
	reach       snapshot.Reachability
	lastChecked time.Time

	// --- GUI 派生 ---
	pendingDelete bool // 已在草稿中删除（但 runtime 可能仍在跑）
	unsaved       bool // 草稿与磁盘不一致
	persisted     bool // 存在于磁盘配置中
	note          string
}

// runtimeDisplay 返回运行态的**文字**表示（decisions/008 §5.2 / §17）。
//
// ⚠️ 过渡态（Starting/Stopping）用 ◐，**不是**失败；颜色只作次要线索（008 §5.2 "不得用颜色作为唯一区分"）。
func (r guiRow) runtimeDisplay() string {
	switch r.runtime {
	case snapshot.RTRunning:
		return T("state.running")
	case snapshot.RTStarting:
		return T("state.starting")
	case snapshot.RTStopping:
		return T("state.stopping")
	case snapshot.RTFailed:
		return T("state.failed")
	default:
		return T("state.not_running")
	}
}

// reachDisplay 返回可达性文字。
//
// ⚠️ "不可达"**不是**错误、也不是失败（decisions/008 G10 / §11）。
func (r guiRow) reachDisplay() string {
	d := r.reach.Display()
	if r.reach == snapshot.ReachUnknown || r.lastChecked.IsZero() {
		return d
	}
	return d + "  (" + r.lastChecked.Format("15:04:05") + ")"
}

// noteDisplay 返回"说明"列：非阻塞提示 + GUI 派生标记。
func (r guiRow) noteDisplay() string {
	s := r.note
	switch {
	case r.pendingDelete:
		s = joinNote(s, T("row.pending_delete"))
	case !r.persisted:
		s = joinNote(s, T("row.unsaved_new"))
	case r.unsaved:
		s = joinNote(s, T("row.unsaved_modified"))
	}
	return s
}

func joinNote(a, b string) string {
	if a == "" {
		return b
	}
	return a + " · " + b
}

// targetModel 是 walk 的表格模型。
//
// ⚠️ 它**只**承载渲染数据，不持有任何 runtime/配置真相（decisions/008 G12）。
type targetModel struct {
	walk.TableModelBase
	rows []guiRow
}

func (m *targetModel) RowCount() int { return len(m.rows) }

func (m *targetModel) Value(row, col int) interface{} {
	if row < 0 || row >= len(m.rows) {
		return ""
	}
	r := m.rows[row]
	switch col {
	case 0:
		return r.ip
	case 1:
		return fmt.Sprintf("%d", r.port)
	case 2:
		return r.username
	case 3:
		return r.runtimeDisplay()
	case 4:
		return r.reachDisplay()
	default:
		return r.noteDisplay()
	}
}

// StyleCell 只提供**次要**颜色线索（decisions/008 §5.2）。
func (m *targetModel) StyleCell(style *walk.CellStyle) {
	row := style.Row()
	if row < 0 || row >= len(m.rows) {
		return
	}
	r := m.rows[row]
	if r.pendingDelete {
		style.TextColor = walk.RGB(128, 128, 128)
		return
	}
	switch style.Col() {
	case 3: // 运行态
		switch r.runtime {
		case snapshot.RTRunning:
			style.TextColor = walk.RGB(0, 120, 0)
		case snapshot.RTFailed:
			style.TextColor = walk.RGB(176, 0, 0)
		}
	case 4: // 可达性：不可达**不**着色（它不是错误）
	}
}

func (m *targetModel) setRows(rows []guiRow) {
	m.rows = rows
	m.PublishRowsReset()
}

func (m *targetModel) row(i int) (guiRow, bool) {
	if i < 0 || i >= len(m.rows) {
		return guiRow{}, false
	}
	return m.rows[i], true
}

func (m *targetModel) indexOf(id string) int {
	for i := range m.rows {
		if m.rows[i].id == id {
			return i
		}
	}
	return -1
}
