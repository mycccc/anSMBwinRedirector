package runtime

import (
	"fmt"
	"net/netip"
	"strings"
	"sync"
	"testing"

	"ansmbwinhelper/internal/model"
	"ansmbwinhelper/internal/snapshot"
)

// --- 测试替身：记录**全局事件顺序**，用于验证"先停全部再起全部" ----------------

type eventLog struct {
	mu  sync.Mutex
	evs []string
}

func (l *eventLog) add(s string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.evs = append(l.evs, s)
}

func (l *eventLog) reset() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.evs = nil
}

func (l *eventLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.evs...)
}

type fakeRuntime struct {
	id      string
	log     *eventLog
	startE  error
	stopped bool
	err     error
}

func (f *fakeRuntime) Start() error {
	f.log.add("start:" + f.id)
	if f.startE != nil {
		return f.startE
	}
	return nil
}

func (f *fakeRuntime) Stop() {
	if !f.stopped {
		f.stopped = true
		f.log.add("stop:" + f.id)
	}
}

func (f *fakeRuntime) Err() error         { return f.err }
func (f *fakeRuntime) Counters() Counters { return Counters{} }

type fakeFactory struct {
	log     *eventLog
	startE  map[string]error // targetID -> Start 错误
	newE    map[string]error // targetID -> New 错误
	created map[string]*fakeRuntime
}

func newFakeFactory(log *eventLog) *fakeFactory {
	return &fakeFactory{log: log, startE: map[string]error{}, newE: map[string]error{}, created: map[string]*fakeRuntime{}}
}

func (f *fakeFactory) New(t model.Target) (Runtime, error) {
	if err, bad := f.newE[t.ID]; bad {
		return nil, err
	}
	r := &fakeRuntime{id: t.ID, log: f.log, startE: f.startE[t.ID]}
	f.created[t.ID] = r
	return r, nil
}

func mkTarget(id, ip string, port uint16) model.Target {
	return model.Target{ID: id, AndroidIP: netip.MustParseAddr(ip), AndroidPort: port}
}

// --- 用例 -----------------------------------------------------------------

func TestApplyStartsNewTargets(t *testing.T) {
	log := &eventLog{}
	m := NewManager(newFakeFactory(log))

	errs := m.Apply([]model.Target{mkTarget("a", "10.0.0.1", 4450)})
	if len(errs) != 0 {
		t.Fatalf("不应有错误: %v", errs)
	}
	if got := log.snapshot(); len(got) != 1 || got[0] != "start:a" {
		t.Fatalf("事件 = %v", got)
	}
	if st := m.Status(); st["a"].State != "Running" {
		t.Fatalf("状态 = %v", st)
	}
}

func TestApplyStopsRemovedTargets(t *testing.T) {
	log := &eventLog{}
	m := NewManager(newFakeFactory(log))
	m.Apply([]model.Target{mkTarget("a", "10.0.0.1", 4450), mkTarget("b", "10.0.0.2", 4450)})
	log.reset()

	m.Apply([]model.Target{mkTarget("a", "10.0.0.1", 4450)})
	got := strings.Join(log.snapshot(), " ")
	if !strings.Contains(got, "stop:b") {
		t.Fatalf("被删除的目标应停止：%v", got)
	}
	if strings.Contains(got, "stop:a") || strings.Contains(got, "start:a") {
		t.Fatalf("未变化的目标**不应**被重建：%v", got)
	}
}

func TestApplyRestartsModifiedTarget(t *testing.T) {
	log := &eventLog{}
	m := NewManager(newFakeFactory(log))
	m.Apply([]model.Target{mkTarget("a", "10.0.0.1", 4450)})
	log.reset()

	m.Apply([]model.Target{mkTarget("a", "10.0.0.1", 4460)})
	got := log.snapshot()
	if len(got) != 2 || got[0] != "stop:a" || got[1] != "start:a" {
		t.Fatalf("端口变更应重建该 Target（先停后起）：%v", got)
	}
}

func TestApplyLeavesUnchangedRuntimeUntouched(t *testing.T) {
	log := &eventLog{}
	m := NewManager(newFakeFactory(log))
	desired := []model.Target{mkTarget("a", "10.0.0.1", 4450), mkTarget("b", "10.0.0.2", 4450)}
	m.Apply(desired)
	log.reset()

	m.Apply(desired) // 相同配置再 Apply
	if got := log.snapshot(); len(got) != 0 {
		t.Fatalf("相同配置不应触发任何重建：%v", got)
	}
}

// ★ 核心不变式：IP 换手必须"先停全部 → 再起全部"（decisions/006 §6.12）。
func TestIPHandoverStopsAllBeforeStartingAny(t *testing.T) {
	log := &eventLog{}
	m := NewManager(newFakeFactory(log))
	m.Apply([]model.Target{
		mkTarget("a", "192.168.1.100", 4450),
		mkTarget("b", "192.168.1.101", 4450),
	})
	log.reset() // 只看第二轮

	// A 与 B 交换 IP。
	m.Apply([]model.Target{
		mkTarget("a", "192.168.1.101", 4450),
		mkTarget("b", "192.168.1.100", 4450),
	})

	evs := log.snapshot()
	if len(evs) != 4 {
		t.Fatalf("应为 2 停 + 2 起，得到 %v", evs)
	}
	for i := 0; i < 2; i++ {
		if !strings.HasPrefix(evs[i], "stop:") {
			t.Fatalf("前两个事件必须是 stop（否则中间态会出现两个 Target 抢同一 IP）：%v", evs)
		}
	}
	for i := 2; i < 4; i++ {
		if !strings.HasPrefix(evs[i], "start:") {
			t.Fatalf("后两个事件必须是 start：%v", evs)
		}
	}
	// 中间态检查：换手双方都必须已经被停止（否则会出现两个 Target 抢同一 IP）。
	stopped := map[string]bool{}
	for _, e := range evs {
		if strings.HasPrefix(e, "stop:") {
			stopped[strings.TrimPrefix(e, "stop:")] = true
		}
	}
	if len(stopped) != 2 {
		t.Fatalf("换手双方都必须被停止：%v", stopped)
	}
}

func TestStartFailureDoesNotAffectOthers(t *testing.T) {
	log := &eventLog{}
	f := newFakeFactory(log)
	f.startE["b"] = fmt.Errorf("模拟启动失败")
	m := NewManager(f)

	errs := m.Apply([]model.Target{
		mkTarget("a", "10.0.0.1", 4450),
		mkTarget("b", "10.0.0.2", 4450),
		mkTarget("c", "10.0.0.3", 4450),
	})
	if len(errs) != 1 || errs["b"] == nil {
		t.Fatalf("只有 b 应报错：%v", errs)
	}
	st := m.Status()
	if st["a"].State != "Running" || st["c"].State != "Running" {
		t.Fatalf("其他目标应保持运行：%v", st)
	}
	// ⚠️ 修正过的断言（2026-10-07，实机验证时发现的缺陷）：
	// 启动失败的目标**没有 runtime**，但必须**显式呈现为 Failed**，
	// 而不是"既不在运行集合里、也不算失败"——否则快照的聚合会出现
	// `N 个目标（0 运行中 · 0 失败）` 这种自相矛盾的状态行
	// （decisions/006 §6.13 / 007 §4.9 / 008 §9）。
	if st["b"].State != snapshot.RTFailed {
		t.Fatalf("启动失败的目标必须呈现为 Failed：%v", st)
	}
	// 失败的目标必须已被回滚（调用了 Stop）。
	if !f.created["b"].stopped {
		t.Fatal("Start 失败后必须回滚（调用 Stop）")
	}
}

func TestFactoryFailureIsReportedAndIsolated(t *testing.T) {
	log := &eventLog{}
	f := newFakeFactory(log)
	f.newE["b"] = fmt.Errorf("模拟工厂失败")
	m := NewManager(f)

	errs := m.Apply([]model.Target{
		mkTarget("a", "10.0.0.1", 4450),
		mkTarget("b", "10.0.0.2", 4450),
	})
	if errs["b"] == nil {
		t.Fatalf("工厂失败必须上报：%v", errs)
	}
	if m.Status()["a"].State != "Running" {
		t.Fatal("a 不应受影响")
	}
}

func TestStopAllStopsEverything(t *testing.T) {
	log := &eventLog{}
	m := NewManager(newFakeFactory(log))
	m.Apply([]model.Target{mkTarget("a", "10.0.0.1", 4450), mkTarget("b", "10.0.0.2", 4450)})

	m.StopAll()
	if len(m.Status()) != 0 {
		t.Fatalf("StopAll 后不应有运行中的 runtime：%v", m.Status())
	}
	evs := log.snapshot()
	stops := 0
	for _, e := range evs {
		if strings.HasPrefix(e, "stop:") {
			stops++
		}
	}
	if stops != 2 {
		t.Fatalf("应停止 2 个，得到 %v", evs)
	}
	// 幂等：再次调用不应重复 Stop。
	m.StopAll()
	if got := log.snapshot(); len(got) != 4 {
		t.Fatalf("StopAll 应幂等：%v", got)
	}
}
