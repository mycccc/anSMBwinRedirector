package model

import "sync"

// FlowKey 是 **Target 作用域内** 的最小关联键（decisions/005 §6.2）。
//
//		key = (WindowsSrcIP, WindowsSrcPort)
//
//	  - OUT 插入：取改写**前**报文的 ip.SrcAddr + tcp.SrcPort
//	  - IN  查表：取入站报文的     ip.DstAddr + tcp.DstPort
//
// 为什么要带 WindowsSrcIP：多宿主主机上，同一个 Windows 源端口可以对**同一 Target**
// 存在两条连接（本地 IP 不同 ⇒ 4 元组不同），只用端口会串流。
//
// 程序范围内的实际作用域是 (Target, WindowsSrcIP, WindowsSrcPort)；
// 表本身属于 TargetRuntime，因此**不需要**把 Target 放进 key。
type FlowKey struct {
	WinIP   [4]byte
	WinPort uint16
}

// NewFlowKey 从 Windows 侧端点构造 key。
func NewFlowKey(winIP [4]byte, winPort uint16) FlowKey {
	return FlowKey{WinIP: winIP, WinPort: winPort}
}

type flowEntry struct {
	finOut bool
	finIn  bool
	seq    uint64 // 最后活跃序号，仅用于容量兜底时选出最旧项
}

// DefaultFlowTableCapacity 是单个 Target 的表项上限（容量兜底）。
const DefaultFlowTableCapacity = 4096

// FlowTable 是 **Target-scoped reverse-rewrite existence table**
// （"该反向包是否由本程序建立的 port-rewrite flow 所产生"）。
//
// ⛔ 它**不是** NAT table、**不是** TCP 状态机、**不是**连接跟踪
// （decisions/005 I6）：不含端口范围 / IP 改写 / UDP / 任意 FROM→TO。
// 它服务的产品语义只有一种：AndroidIP:445 → AndroidIP:AndroidPort。
//
// 回收策略（沿用 P0.1 的取舍）：
//   - RST ⇒ 立即回收
//   - 双向 FIN ⇒ 回收
//   - **刻意不按时间过期**：SMB 会话可长时间空闲仍有效，按时间过期会打断真实会话
//   - 另有容量上限兜底（超出时淘汰最旧项）
//
// 并发安全：每个 TargetRuntime 有 OUT / IN 两条循环 goroutine，都会访问本表。
type FlowTable struct {
	mu   sync.Mutex
	m    map[FlowKey]*flowEntry
	cap  int
	next uint64
}

// NewFlowTable 创建表；capacity <= 0 时使用 DefaultFlowTableCapacity。
func NewFlowTable(capacity int) *FlowTable {
	if capacity <= 0 {
		capacity = DefaultFlowTableCapacity
	}
	return &FlowTable{m: make(map[FlowKey]*flowEntry), cap: capacity}
}

// Record 记录一条由本程序建立的映射（OUT 改写成功后调用）。
// 已存在则刷新活跃序号（不重复插入）。
func (t *FlowTable) Record(k FlowKey) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.next++
	if e, ok := t.m[k]; ok {
		e.seq = t.next
		return
	}
	if len(t.m) >= t.cap {
		t.evictOldestLocked()
	}
	t.m[k] = &flowEntry{seq: t.next}
}

// Lookup 判断该反向包是否属于本程序建立的映射。
// **未命中 ⇒ 调用方必须原样放行（PASS-THROUGH）**，这正是
// "直连 AndroidIP:<AndroidPort> 不被误伤" 的机制（decisions/005 §6.2）。
func (t *FlowTable) Lookup(k FlowKey) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	_, ok := t.m[k]
	return ok
}

// Observe 依据报文标志回收表项。
//
//	dir = Outbound 记录 OUT 方向 FIN；dir = Inbound 记录 IN 方向 FIN。
//	RST ⇒ 立即回收；双向 FIN ⇒ 回收。
func (t *FlowTable) Observe(k FlowKey, dir Direction, rst, fin bool) {
	if !rst && !fin {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	e, ok := t.m[k]
	if !ok {
		return
	}
	if rst {
		delete(t.m, k)
		return
	}
	if dir == Outbound {
		e.finOut = true
	} else {
		e.finIn = true
	}
	if e.finOut && e.finIn {
		delete(t.m, k)
	}
}

// Len 返回当前表项数（诊断用）。
func (t *FlowTable) Len() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.m)
}

// Clear 清空整表。
// 使用场景：Target 被删除 / 配置变更后该 Target 被重建（decisions/006 §6.12）。
func (t *FlowTable) Clear() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.m = make(map[FlowKey]*flowEntry)
}

func (t *FlowTable) evictOldestLocked() {
	var (
		oldest FlowKey
		found  bool
		minSeq uint64
	)
	for k, e := range t.m {
		if !found || e.seq < minSeq {
			oldest, minSeq, found = k, e.seq, true
		}
	}
	if found {
		delete(t.m, oldest)
	}
}
