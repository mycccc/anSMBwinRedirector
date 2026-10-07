package diag

import (
	"fmt"
	"sort"
	"sync"
	"time"
)

// ErrorInfo 是一条**当前状态**的错误（decisions/009 §2.1）。
//
// ⚠️ 错误是"状态"而不是"一次性事件"（decisions/009 §4）：
// 进入状态后必须在条件消失时被清除，不得"弹一次就没了"或"永远挂着"。
type ErrorInfo struct {
	Code     Code
	Category Category
	Scope    Scope
	Summary  string
	Detail   string
	Source   Source

	// TargetID 在 Scope == ScopeTarget 时非空。
	TargetID string
	// AffectedTargetIDs 在 Scope == ScopeGlobal 且由目标触发时列出受影响目标。
	AffectedTargetIDs []string

	FirstAt    time.Time
	OccurredAt time.Time
	Count      int
}

// New 构造一条错误（分类信息取自封闭码表，不由调用方自造）。
func New(code Code, src Source, detail string, targetID string) ErrorInfo {
	m := Meta(code)
	now := time.Now()
	return ErrorInfo{
		Code:       code,
		Category:   m.Category,
		Scope:      m.Scope,
		Summary:    m.Summary,
		Detail:     detail,
		Source:     src,
		TargetID:   targetID,
		FirstAt:    now,
		OccurredAt: now,
		Count:      1,
	}
}

// Blocking 表示该错误会阻止 Start / Apply。
func (e ErrorInfo) Blocking() bool { return Meta(e.Code).Blocking }

func (e ErrorInfo) String() string {
	if e.TargetID != "" {
		return fmt.Sprintf("%s: %s (target=%s)", e.Code, e.Summary, e.TargetID)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Summary)
}

// Aggregator 按 (Code, Scope, TargetID) 聚合错误（decisions/009 §3）。
//
// 聚合发生在 **controller 层**，GUI/入口只渲染结果，不得自行归并（decisions/008 G6/G12）。
type Aggregator struct {
	mu sync.Mutex
	m  map[aggKey]*ErrorInfo
}

type aggKey struct {
	Code     Code
	Scope    Scope
	TargetID string
}

// NewAggregator 创建空聚合器。
func NewAggregator() *Aggregator { return &Aggregator{m: map[aggKey]*ErrorInfo{}} }

// Add 合并一条错误：
//   - 同 (Code, Scope, TargetID) 只保留最新一条，Count 自增
//   - ScopeGlobal 且由目标触发时，把 targetID 并入 AffectedTargetIDs
func (a *Aggregator) Add(e ErrorInfo) {
	a.mu.Lock()
	defer a.mu.Unlock()

	// 全局类错误：目标身份不进 key，改为累积到受影响列表。
	key := aggKey{Code: e.Code, Scope: e.Scope, TargetID: e.TargetID}
	if e.Scope == ScopeGlobal {
		key.TargetID = ""
	}
	if prev, ok := a.m[key]; ok {
		prev.Detail = e.Detail
		prev.Source = e.Source
		prev.OccurredAt = e.OccurredAt
		prev.Count++
		if e.Scope == ScopeGlobal && e.TargetID != "" {
			prev.AffectedTargetIDs = appendUnique(prev.AffectedTargetIDs, e.TargetID)
		}
		return
	}
	cp := e
	if cp.Scope == ScopeGlobal && cp.TargetID != "" {
		cp.AffectedTargetIDs = appendUnique(cp.AffectedTargetIDs, cp.TargetID)
		cp.TargetID = ""
	}
	a.m[key] = &cp
}

// Remove 移除一条错误（条件消失时调用）。
func (a *Aggregator) Remove(code Code, scope Scope, targetID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.m, aggKey{Code: code, Scope: scope, TargetID: targetID})
}

// ClearTarget 清除某目标上的全部 Target 级错误。
func (a *Aggregator) ClearTarget(targetID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for k := range a.m {
		if k.Scope == ScopeTarget && k.TargetID == targetID {
			delete(a.m, k)
		}
	}
}

// ClearByScope 清除某个 Scope 的全部错误。
func (a *Aggregator) ClearByScope(s Scope) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for k := range a.m {
		if k.Scope == s {
			delete(a.m, k)
		}
	}
}

// HasBlocking 报告是否存在阻塞类错误。
func (a *Aggregator) HasBlocking() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, e := range a.m {
		if e.Blocking() {
			return true
		}
	}
	return false
}

// List 返回当前全部错误（稳定顺序：先 Scope，再 Code）。
func (a *Aggregator) List() []ErrorInfo {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]ErrorInfo, 0, len(a.m))
	for _, e := range a.m {
		out = append(out, *e)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Scope != out[j].Scope {
			return out[i].Scope < out[j].Scope
		}
		return out[i].Code < out[j].Code
	})
	return out
}

func appendUnique(xs []string, v string) []string {
	for _, x := range xs {
		if x == v {
			return xs
		}
	}
	return append(xs, v)
}
