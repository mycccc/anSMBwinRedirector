// Package runtime 拥有每个 Target 的 WinDivert handle 与改写循环，并保证
// decisions/005 的 I1–I4 与 decisions/007 的 L3/L4/L11。
//
// 关键不变式：
//
//	I1/I2  Android IP 唯一、每个 Target 至多一个 runtime
//	I3/I4  任一 AndroidIP 至多一个 active OUT handle、至多一个 active IN handle
//	L4     "已停止" = 两个 handle 的 Close 已返回 **且** 两个循环 goroutine 已退出
//	L11    IN handle 打开失败必须回滚已打开的 OUT handle
//
// ⛔ 本包**不**直接用 WinDivert：handle 的创建/关闭全部经 winvert（唯一适配层）。
package runtime

import (
	"fmt"
	"sync"

	"ansmbwinhelper/internal/model"
	"ansmbwinhelper/internal/snapshot"
	"ansmbwinhelper/internal/winvert"
)

// Counters 是单个 runtime 的分层归因计数（诊断用）。
type Counters struct {
	OutRewritten  uint64
	InRewritten   uint64
	PassThrough   uint64
	NoMatch       uint64
	NotApplicable uint64
	RecvErr       uint64
	SendErr       uint64
}

// Runtime 是一个 Target 的运行时。
type Runtime interface {
	// Start 建立 handles 并启动循环；失败必须自行回滚（L11）。
	Start() error
	// Stop 停止并**等待**"确认关闭"（L4），可重复调用。
	Stop()
	// Err 返回导致该 runtime 退出的内部错误（正常停止时为 nil）。
	Err() error
	// Counters 返回计数。
	Counters() Counters
}

// Factory 创建 Runtime。由平台层提供（Windows 用 WinDivert；其它平台返回错误）。
type Factory interface {
	New(t model.Target) (Runtime, error)
}

// NewPlatformFactory 返回当前平台的工厂。
func NewPlatformFactory() Factory { return platformFactory{} }

type entry struct {
	target model.Target
	rt     Runtime
}

// Manager 拥有全部 Target runtime。
//
// ⛔ 调用方（controller）负责**串行化**（decisions/007 L8）；Manager 自身只做状态一致。
type Manager struct {
	f  Factory
	mu sync.Mutex
	// cur 是当前**已建立**的 runtime（键为 Target 稳定 ID）。
	cur map[string]*entry
	// failed 记录"最近一次 Apply 中启动失败"的 Target。
	//
	// ⚠️ 必须单独记录：失败的目标**没有** runtime，因此不在 cur 中；
	// 但它们是**显式的失败状态**，不能表现为"既没运行也没失败"
	// （decisions/006 §6.13、007 §4.9、008 §9 的聚合计数依赖它）。
	// 下一次 Apply 会**重试**这些目标（009 §10 的"重试启动"）。
	failed map[string]error
}

// NewManager 创建 Manager。
func NewManager(f Factory) *Manager {
	return &Manager{f: f, cur: map[string]*entry{}, failed: map[string]error{}}
}

// Apply 让运行中的 runtime 集合收敛到 desired。
//
// 差量规则（decisions/006 §6.12）：
//
//	toStop  = 被删除的 ∪ 被修改的 ∪ 【IP 被其他 Target 接手的】
//	toStart = 新增的 ∪ 被修改的
//
// 执行顺序是硬约束：**先停全部 toStop → 确认全部关闭 → 再起全部 toStart**。
// 否则 IP 换手（A 100→101、B 101→100）会产生"两个 Target 抢同一 IP"的中间态，
// 违反 I1/I3/I4。
//
// 每个 Target 的失败**互不影响**（decisions/007 L7）。
func (m *Manager) Apply(desired []model.Target) map[string]error {
	m.mu.Lock()
	defer m.mu.Unlock()

	desiredByID := make(map[string]model.Target, len(desired))
	for _, t := range desired {
		desiredByID[t.ID] = t
	}

	// --- 计算差量 ---
	toStop := map[string]bool{}
	curByIP := map[string]string{} // AndroidIP -> targetID
	for id, e := range m.cur {
		curByIP[e.target.AndroidIP.String()] = id
	}
	for id, e := range m.cur {
		d, keep := desiredByID[id]
		if !keep {
			toStop[id] = true // 删除
			continue
		}
		if d.AndroidIP != e.target.AndroidIP || d.AndroidPort != e.target.AndroidPort {
			toStop[id] = true // 修改（IP 或端口）
		}
	}
	// IP 换手：desired 中的目标要用的 IP，当前被**另一个** ID 占用。
	for id, d := range desiredByID {
		holder, occupied := curByIP[d.AndroidIP.String()]
		if !occupied || holder == id {
			continue
		}
		if _, stillDesired := desiredByID[holder]; stillDesired {
			// 占用者仍在 desired 里但换了 IP —— 上面已标记；这里只是兜底。
			continue
		}
		toStop[holder] = true
	}

	// --- 先停全部（并发执行、逐一等待确认关闭）---
	stopIDs := make([]string, 0, len(toStop))
	for id := range toStop {
		stopIDs = append(stopIDs, id)
	}
	for _, id := range stopIDs {
		if e, ok := m.cur[id]; ok {
			e.rt.Stop()
			delete(m.cur, id)
		}
	}

	// 不再被 desired 包含的目标：连同其失败记录一起清掉。
	for id := range m.failed {
		if _, keep := desiredByID[id]; !keep {
			delete(m.failed, id)
		}
	}

	// --- 再起全部 ---
	errs := map[string]error{}

	// decisions/011 §2.2：**任何 WinDivertOpen 之前必须接管**。
	// 只要有目标要新建 runtime，就先确保系统里没有遗留的 WinDivert 服务。
	// ⛔ 接管失败 ⇒ 这些目标全部显式失败，**不得** fallback 到旧服务（T-b）。
	pending := make([]model.Target, 0, len(desired))
	for _, t := range desired {
		if _, unchanged := m.cur[t.ID]; !unchanged {
			pending = append(pending, t)
		}
	}
	var takeoverErr error
	if len(pending) > 0 {
		takeoverErr = winvert.BeforeOpen()
	}

	for _, t := range desired {
		if _, unchanged := m.cur[t.ID]; unchanged {
			continue // 未变化的 runtime 保持不动（不主动重建）
		}
		if takeoverErr != nil {
			errs[t.ID] = takeoverErr
			m.failed[t.ID] = takeoverErr
			continue
		}
		rt, err := m.f.New(t)
		if err != nil {
			errs[t.ID] = err
			m.failed[t.ID] = err
			continue
		}
		if err := rt.Start(); err != nil {
			rt.Stop()
			errs[t.ID] = err
			m.failed[t.ID] = err
			continue
		}
		delete(m.failed, t.ID)
		m.cur[t.ID] = &entry{target: t, rt: rt}
	}
	return errs
}

// StopAll 停止全部 runtime、等待"确认关闭"，**然后**停止并移除 WinDivert 服务。
//
// 顺序是硬约束（decisions/011 §2.4）：
//
//	停 TargetRuntime →（每个都"确认关闭"，007 L4）→ AfterStop（停服务 + 等消失 + 等 .sys 解锁）
//
// ⛔ 不得在 handle 尚未全部关闭时停驱动。
// 返回值是"服务生命周期"这一步的失败原因（可为 nil）；runtime 停止本身不返回错误。
func (m *Manager) StopAll() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, e := range m.cur {
		e.rt.Stop()
		delete(m.cur, id)
	}
	// 停机后不再有"失败"这一状态（全部为 Stopped）。
	m.failed = map[string]error{}
	return winvert.AfterStop()
}

// Status 返回当前全部 runtime 的状态。
func (m *Manager) Status() map[string]snapshot.RuntimeEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]snapshot.RuntimeEntry, len(m.cur)+len(m.failed))
	for id, e := range m.cur {
		st := snapshot.RuntimeEntry{State: snapshot.RTRunning}
		if err := e.rt.Err(); err != nil {
			st.State = snapshot.RTFailed
		}
		out[id] = st
	}
	// 启动失败的目标：没有 runtime，但**必须**显式呈现为 Failed。
	for id := range m.failed {
		if _, running := out[id]; running {
			continue
		}
		out[id] = snapshot.RuntimeEntry{State: snapshot.RTFailed}
	}
	return out
}

// Counters 返回某个 runtime 的计数（诊断用）。
func (m *Manager) Counters(id string) (Counters, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.cur[id]
	if !ok {
		return Counters{}, false
	}
	return e.rt.Counters(), true
}

// Summary 返回一行汇总，便于实验日志。
func (m *Manager) Summary() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := fmt.Sprintf("runtimes=%d", len(m.cur))
	for id, e := range m.cur {
		c := e.rt.Counters()
		s += fmt.Sprintf(" [%s out=%d in=%d pass=%d]", short(id), c.OutRewritten, c.InRewritten, c.PassThrough)
	}
	return s
}

func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}
