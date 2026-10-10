// Package snapshot 是 runtime → UI 的**唯一状态出口**，严格对应 decisions/008 §3。
//
// ⛔ 本包**不含** GUI Draft（编辑中草稿）。按 decisions/008 §3：
//
//	Runtime / Target 的状态，唯一来源是 Runtime Snapshot；
//	编辑中的 Draft 是 GUI 本地的编辑模型，不属于 Runtime 状态。
//
// 状态模型是**四维正交**的（decisions/007 §4.2）：
//
//	① 全局运行模式  ConfigUnavailable | Stopped | Running
//	② 进行中的操作  none | Apply | Start | Stop | Exit   （串行化对象）
//	③ per-Target 运行态 Stopped | Starting | Running | Stopping | Failed
//	④ per-Target 可达性 Unknown | Reachable | Unreachable （与 ③ 无关）
package snapshot

import "ansmbwinhelper/internal/i18n"

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"ansmbwinhelper/internal/diag"
	"ansmbwinhelper/internal/model"
)

// GlobalMode 是全局运行模式（decisions/007 §4.2）。
type GlobalMode string

const (
	ModeConfigUnavailable GlobalMode = "ConfigUnavailable"
	ModeStopped           GlobalMode = "Stopped"
	ModeRunning           GlobalMode = "Running"
)

// Operation 是进行中的操作（串行化对象，decisions/007 L8）。
type Operation string

const (
	OpNone  Operation = "none"
	OpApply Operation = "Apply"
	OpStart Operation = "Start"
	OpStop  Operation = "Stop"
	OpExit  Operation = "Exit"
	// OpConnect 是"连接某个目标"（decisions/012）：与 Apply/Start/Stop/Exit 共用同一串行化
	// （decisions/007 L8）—— 它要求 Running，因此不能与 Stop/Start 并发。
	OpConnect Operation = "Connect"
	// OpDebug 是「调试」窗口里的动作（修复 / 重新接管 WinDivert、重启 Workstation 服务，
	// decisions/011 §2.7）：同样串行化 —— 修复要求"没有活动 handle"，
	// 因此**不能**与 Start/Stop/Apply/Connect 并发。
	OpDebug Operation = "Debug"
)

// RuntimeState 是单个 Target 的运行态。
type RuntimeState string

const (
	RTStopped  RuntimeState = "Stopped"
	RTStarting RuntimeState = "Starting"
	RTRunning  RuntimeState = "Running"
	RTStopping RuntimeState = "Stopping"
	RTFailed   RuntimeState = "Failed"
)

// Reachability 是单个 Target 的**"最近一次「连接」的结果"**（与 RuntimeState **正交**）。
//
// ⚠️ **更新来源（decisions/008 §11，Gate 2026-10-09 裁定 = 方案 A）**：
//   - `Reachable` **只由 `Controller.Connect` 完整成功时写入**（含 `LastCheckedAt`）；
//   - 连接失败（凭据失败 / 打开共享失败）**不写**任何状态 —— ⛔ 尤其**不写 `Unreachable`**；
//   - 因此它表示「**最近一次 Connect 操作成功**」，⛔ **不是**独立网络探测/实时可达性，
//     也**不保证** Android 主机或 SMB 服务今后持续可用。
type Reachability string

const (
	ReachUnknown     Reachability = "Unknown"
	ReachReachable   Reachability = "Reachable"
	ReachUnreachable Reachability = "Unreachable"
)

// Display 返回界面文案（decisions/008 §11：Unknown 显示为"尚未连接"）。
//
// ⚠️ 用户可见文案 ⇒ 经 i18n（decisions/013 §6.2）；取值本身仍是稳定内部标识。
func (r Reachability) Display() string {
	switch r {
	case ReachReachable:
		return i18n.T("snapshot.reach_reachable")
	case ReachUnreachable:
		return i18n.T("snapshot.reach_unreachable")
	default:
		return i18n.T("snapshot.reach_unknown")
	}
}

// Global 是全局状态。
type Global struct {
	Mode      GlobalMode
	Operation Operation
}

// Config 是配置层状态（decisions/009 §11；⛔ 不含配置全文）。
type Config struct {
	Path          string
	State         string
	SchemaVersion int
	TargetCount   int
	BackupExists  bool
	Violations    []string
}

// RuntimeEntry 是某个 Target 的运行态。
type RuntimeEntry struct {
	State RuntimeState
	Err   *diag.ErrorInfo
}

// ReachEntry 是某个 Target 的可达性。
type ReachEntry struct {
	State       Reachability
	LastChecked time.Time
}

// Snapshot 是 runtime 状态的完整快照。
//
// Persisted / Runtime / Reachability **分开保存**（decisions/008 §3.2），
// 由 Rows() 合并成渲染视图 —— 合并只发生在渲染侧。
type Snapshot struct {
	Global    Global
	Config    Config
	Persisted []model.Target
	Runtime   map[string]RuntimeEntry
	Reach     map[string]ReachEntry
	WinDivert diag.WinDivertInfo
	Errors    []diag.ErrorInfo
	Recent    []diag.Entry
}

// Row 是渲染一行所需的信息（Persisted + Runtime + Reachability 的合并视图）。
type Row struct {
	ID           string
	AndroidIP    string
	AndroidPort  uint16
	Runtime      RuntimeState
	RuntimeErr   *diag.ErrorInfo
	Reachability Reachability
	LastChecked  time.Time
}

// Rows 返回按 Android IP 升序排列的渲染行。
//
// 排序固定（decisions/008 §5.3）：默认按 Android IP 升序，**不按运行态排序**，
// 避免状态变化导致行跳动。
func (s Snapshot) Rows() []Row {
	rows := make([]Row, 0, len(s.Persisted))
	for _, t := range s.Persisted {
		r := Row{
			ID:           t.ID,
			AndroidIP:    t.AndroidIP.String(),
			AndroidPort:  t.AndroidPort,
			Runtime:      RTStopped,
			Reachability: ReachUnknown,
		}
		if e, ok := s.Runtime[t.ID]; ok {
			r.Runtime = e.State
			r.RuntimeErr = e.Err
		}
		if e, ok := s.Reach[t.ID]; ok {
			r.Reachability = e.State
			r.LastChecked = e.LastChecked
		}
		rows = append(rows, r)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].AndroidIP != rows[j].AndroidIP {
			return rows[i].AndroidIP < rows[j].AndroidIP
		}
		return rows[i].AndroidPort < rows[j].AndroidPort
	})
	return rows
}

// Aggregate 返回聚合计数文案（decisions/008 §9）。
//
//	运行中 · N 个目标（M 运行中 · K 失败）
//	运行中 · 0 个目标          ← targets=[] 是**合法**状态
func (s Snapshot) Aggregate() (total, running, failed int) {
	total = len(s.Persisted)
	for _, t := range s.Persisted {
		switch s.Runtime[t.ID].State {
		case RTRunning:
			running++
		case RTFailed:
			failed++
		}
	}
	return
}

// StatusLine 返回状态条文本（decisions/008 §9）。
//
// ⚠️ 用户可见文案 ⇒ 经 i18n（decisions/013 §6.2）。
func (s Snapshot) StatusLine() string {
	switch s.Global.Mode {
	case ModeConfigUnavailable:
		return i18n.T("snapshot.status_config_unavailable")
	case ModeRunning:
		total, running, failed := s.Aggregate()
		if total == 0 {
			return i18n.T("snapshot.status_running_zero")
		}
		return fmt.Sprintf(i18n.T("snapshot.status_running_n"), total, running, failed)
	default:
		return fmt.Sprintf(i18n.T("snapshot.status_stopped_n"), len(s.Persisted))
	}
}

// Clone 深拷贝（快照在发布/读取时都必须与内部状态解耦）。
func (s Snapshot) Clone() Snapshot {
	c := s
	c.Persisted = append([]model.Target(nil), s.Persisted...)
	c.Config.Violations = append([]string(nil), s.Config.Violations...)
	c.Errors = append([]diag.ErrorInfo(nil), s.Errors...)
	c.Recent = append([]diag.Entry(nil), s.Recent...)
	c.Runtime = make(map[string]RuntimeEntry, len(s.Runtime))
	for k, v := range s.Runtime {
		c.Runtime[k] = v
	}
	c.Reach = make(map[string]ReachEntry, len(s.Reach))
	for k, v := range s.Reach {
		c.Reach[k] = v
	}
	c.WinDivert.Consumers = append([]diag.Consumer(nil), s.WinDivert.Consumers...)
	return c
}

// Publisher 持有当前快照并推送给订阅者（未来的 GUI / Tray）。
type Publisher struct {
	mu   sync.Mutex
	cur  Snapshot
	subs []func(Snapshot)
}

// NewPublisher 创建发布器。
func NewPublisher(initial Snapshot) *Publisher {
	return &Publisher{cur: initial.Clone()}
}

// Publish 替换当前快照并通知订阅者。
func (p *Publisher) Publish(s Snapshot) {
	c := s.Clone()
	p.mu.Lock()
	p.cur = c
	subs := make([]func(Snapshot), len(p.subs))
	copy(subs, p.subs)
	p.mu.Unlock()
	for _, fn := range subs {
		fn(c.Clone())
	}
}

// Current 返回当前快照副本。
func (p *Publisher) Current() Snapshot {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cur.Clone()
}

// Subscribe 注册订阅者（订阅时刻**不会**立即回调；调用方可先读 Current）。
func (p *Publisher) Subscribe(fn func(Snapshot)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.subs = append(p.subs, fn)
}
