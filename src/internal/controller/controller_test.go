package controller

import (
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"ansmbwinhelper/internal/config"
	"ansmbwinhelper/internal/diag"
	"ansmbwinhelper/internal/model"
	"ansmbwinhelper/internal/snapshot"
)

// --- 测试替身 --------------------------------------------------------------

type fakeManager struct {
	mu      sync.Mutex
	status  map[string]snapshot.RuntimeEntry
	failOn  map[string]error // targetID -> 失败原因
	applied [][]model.Target // 每次 Apply 的输入
	stops   int
}

func newFakeManager() *fakeManager {
	return &fakeManager{status: map[string]snapshot.RuntimeEntry{}, failOn: map[string]error{}}
}

func (f *fakeManager) Apply(desired []model.Target) map[string]error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.applied = append(f.applied, append([]model.Target(nil), desired...))

	// 被移除的目标 ⇒ Stopped。
	keep := map[string]bool{}
	for _, t := range desired {
		keep[t.ID] = true
	}
	for id := range f.status {
		if !keep[id] {
			delete(f.status, id)
		}
	}
	errs := map[string]error{}
	for _, t := range desired {
		if err, bad := f.failOn[t.ID]; bad {
			f.status[t.ID] = snapshot.RuntimeEntry{State: snapshot.RTFailed}
			errs[t.ID] = err
			continue
		}
		f.status[t.ID] = snapshot.RuntimeEntry{State: snapshot.RTRunning}
	}
	return errs
}

func (f *fakeManager) StopAll() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stops++
	for id := range f.status {
		f.status[id] = snapshot.RuntimeEntry{State: snapshot.RTStopped}
	}
}

func (f *fakeManager) Status() map[string]snapshot.RuntimeEntry {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[string]snapshot.RuntimeEntry, len(f.status))
	for k, v := range f.status {
		out[k] = v
	}
	return out
}

func (f *fakeManager) applyCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.applied)
}

type fakeInspector struct{ info diag.WinDivertInfo }

func (f fakeInspector) Inspect() diag.WinDivertInfo { return f.info }

func newTestController(t *testing.T) (*Controller, *fakeManager, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	mgr := newFakeManager()
	c := New(Options{
		Store:     config.NewStore(path),
		Manager:   mgr,
		Inspector: fakeInspector{},
		Version:   diag.VersionInfo{Version: "0.1.0-test"},
		OSInfo:    "test",
	})
	return c, mgr, path
}

func tgt(id, ip string, port uint16) model.Target {
	return model.Target{ID: id, AndroidIP: netip.MustParseAddr(ip), AndroidPort: port}
}

// --- G11：Apply 是唯一的提交入口 -------------------------------------------

func TestApplyIsSingleEntryThatPersistsAndReconciles(t *testing.T) {
	c, mgr, path := newTestController(t)
	c.LoadConfig()

	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	draft := []model.Target{tgt("a", "192.168.1.100", 4450)}
	if err := c.Apply(draft); err != nil {
		t.Fatalf("Apply 失败: %v", err)
	}
	// 一次调用同时完成：持久化 + runtime 收敛。
	if _, err := os.Stat(path); err != nil {
		t.Fatal("Apply 必须完成持久化（不是又一个独立的 Save 命令）")
	}
	if got := mgr.applyCount(); got != 2 { // Start 一次 + Apply 一次
		t.Fatalf("Apply 必须触发 runtime 收敛，apply 次数 = %d", got)
	}
	if snap := c.Snapshot(); len(snap.Persisted) != 1 || snap.Persisted[0].ID != "a" {
		t.Fatalf("快照未反映新配置: %+v", snap.Persisted)
	}
}

func TestApplyValidationFailureChangesNothing(t *testing.T) {
	c, mgr, path := newTestController(t)
	c.LoadConfig()
	_ = c.Start()
	before := mgr.applyCount()

	// 非法（组播）+ 重复 IP：两种违规都要被发现，且不写盘、不动 runtime。
	bad := []model.Target{
		tgt("a", "224.0.0.1", 4450),
		tgt("b", "10.0.0.1", 4450),
		tgt("c", "10.0.0.1", 4460),
	}
	err := c.Apply(bad)
	if err == nil {
		t.Fatal("非法配置必须被拒绝")
	}
	var ve *ViolationsError
	if !asViolations(err, &ve) {
		t.Fatalf("应返回 ViolationsError，得到 %T", err)
	}
	if len(ve.Violations) < 2 {
		t.Errorf("应列出全部违规项，得到 %v", ve.Violations)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Error("校验失败时不得写盘")
	}
	if mgr.applyCount() != before {
		t.Error("校验失败时不得触碰 runtime")
	}
}

func TestApplyWhileStoppedDoesNotTouchRuntime(t *testing.T) {
	// decisions/006 §6.13：Apply 不改变全局运行模式。
	c, mgr, _ := newTestController(t)
	c.LoadConfig()
	if err := c.Apply([]model.Target{tgt("a", "10.0.0.1", 4450)}); err != nil {
		t.Fatal(err)
	}
	if mgr.applyCount() != 0 {
		t.Fatal("停止态下 Apply 不应启动 runtime")
	}
	if snap := c.Snapshot(); snap.Global.Mode != snapshot.ModeStopped {
		t.Errorf("Apply 不应改变全局模式，得到 %v", snap.Global.Mode)
	}
	if len(c.Snapshot().Persisted) != 1 {
		t.Error("停止态下 Apply 仍应完成持久化")
	}
}

func TestApplyRejectedWhenConfigUnavailable(t *testing.T) {
	c, mgr, path := newTestController(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"schema_version":1,"targets":[{"id":"a","android_ip":"1.1.1.1"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)

	c.LoadConfig()
	if c.Snapshot().Global.Mode != snapshot.ModeConfigUnavailable {
		t.Fatal("坏配置必须进入 ConfigUnavailable")
	}
	if err := c.Apply([]model.Target{tgt("b", "10.0.0.1", 4450)}); err == nil {
		t.Fatal("配置不可用时必须禁止保存")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("配置不可用时绝不得覆盖原文件")
	}
	if mgr.applyCount() != 0 {
		t.Error("配置不可用时不得触碰 runtime")
	}
}

func TestSerializationRejectsConcurrentOperations(t *testing.T) {
	// decisions/007 L8：串行化，不排队。
	c, _, _ := newTestController(t)
	c.LoadConfig()

	if err := c.begin(snapshot.OpApply); err != nil {
		t.Fatal(err)
	}
	if err := c.Start(); err == nil {
		t.Fatal("已有操作进行时，新的 Start 必须被拒绝")
	}
	if err := c.Apply(nil); err == nil {
		t.Fatal("已有操作进行时，新的 Apply 必须被拒绝")
	}
	c.end()
	if err := c.Start(); err != nil {
		t.Fatalf("操作结束后应可再次提交: %v", err)
	}
}

// --- Start / Stop ---------------------------------------------------------

func TestStartWithEmptyTargetsIsAllowed(t *testing.T) {
	// decisions/006 §6.4：targets=[] 合法 ⇒ `运行中 · 0 个目标`。
	c, mgr, _ := newTestController(t)
	c.LoadConfig()
	if err := c.Start(); err != nil {
		t.Fatalf("空配置必须可以启动: %v", err)
	}
	snap := c.Snapshot()
	if snap.Global.Mode != snapshot.ModeRunning {
		t.Fatalf("模式 = %v，期望 Running", snap.Global.Mode)
	}
	if got := snap.StatusLine(); got != "运行中 · 0 个目标" {
		t.Errorf("状态条 = %q", got)
	}
	if mgr.applyCount() != 1 {
		t.Error("空配置的 Start 也应执行一次收敛（收敛到空集合）")
	}
}

func TestStartBlockedWhenConfigUnavailable(t *testing.T) {
	c, _, path := newTestController(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{ not json`), 0o644); err != nil {
		t.Fatal(err)
	}
	c.LoadConfig()
	if err := c.Start(); err == nil {
		t.Fatal("配置不可用时必须禁止 Start（decisions/006 §6.10）")
	}
}

func TestStopDoesNotTouchConfigOrDriver(t *testing.T) {
	c, mgr, path := newTestController(t)
	c.LoadConfig()
	_ = c.Start()
	_ = c.Apply([]model.Target{tgt("a", "10.0.0.1", 4450)})
	beforeContent, _ := os.ReadFile(path)

	if err := c.Stop(); err != nil {
		t.Fatal(err)
	}
	if mgr.stops != 1 {
		t.Errorf("StopAll 调用次数 = %d", mgr.stops)
	}
	afterContent, _ := os.ReadFile(path)
	if string(beforeContent) != string(afterContent) {
		t.Error("Stop 不得修改配置")
	}
	if snap := c.Snapshot(); snap.Global.Mode != snapshot.ModeStopped {
		t.Errorf("模式 = %v，期望 Stopped", snap.Global.Mode)
	}
}

// --- 失败隔离与聚合 --------------------------------------------------------

func TestSingleTargetFailureDoesNotAffectOthers(t *testing.T) {
	// decisions/007 L7 + decisions/009 §3。
	c, mgr, _ := newTestController(t)
	c.LoadConfig()
	// 用 **Target 级**错误码，验证"行内失败原因"路径；
	// 全局类错误（权限/驱动）的归并行为由 TestGlobalClassErrorIsAggregatedAcrossTargets 覆盖。
	mgr.failOn["b"] = diag.Errorf(diag.CodeTargetStartFailed, "该目标的运行时建立失败")

	_ = c.Start()
	if err := c.Apply([]model.Target{
		tgt("a", "10.0.0.1", 4450),
		tgt("b", "10.0.0.2", 4450),
		tgt("c", "10.0.0.3", 4450),
	}); err != nil {
		t.Fatal(err)
	}

	snap := c.Snapshot()
	total, running, failed := snap.Aggregate()
	if total != 3 || running != 2 || failed != 1 {
		t.Fatalf("聚合 = %d/%d/%d，期望 3/2/1", total, running, failed)
	}
	if snap.Global.Mode != snapshot.ModeRunning {
		t.Error("单个目标失败不得改变全局模式（L7）")
	}
	// 失败只落在 b 上。
	for _, r := range snap.Rows() {
		switch r.ID {
		case "a", "c":
			if r.Runtime != snapshot.RTRunning {
				t.Errorf("%s 应为 Running，得到 %v", r.ID, r.Runtime)
			}
		case "b":
			if r.Runtime != snapshot.RTFailed || r.RuntimeErr == nil {
				t.Errorf("b 应为 Failed 且带错误，得到 %v", r.Runtime)
			}
		}
	}
}

func TestGlobalClassErrorIsAggregatedAcrossTargets(t *testing.T) {
	// 权限 / 驱动类错误在逐 Target 尝试时会重复出现 ⇒ 必须归并为一条（decisions/009 §3）。
	c, mgr, _ := newTestController(t)
	c.LoadConfig()
	mgr.failOn["a"] = diag.Errorf(diag.CodeAdminRequired, "需要管理员权限")
	mgr.failOn["b"] = diag.Errorf(diag.CodeAdminRequired, "需要管理员权限")

	_ = c.Start()
	_ = c.Apply([]model.Target{tgt("a", "10.0.0.1", 4450), tgt("b", "10.0.0.2", 4450)})

	var global []diag.ErrorInfo
	for _, e := range c.Snapshot().Errors {
		if e.Scope == diag.ScopeGlobal {
			global = append(global, e)
		}
	}
	if len(global) != 1 {
		t.Fatalf("全局类错误应归并为 1 条，得到 %d 条: %+v", len(global), global)
	}
	if len(global[0].AffectedTargetIDs) != 2 {
		t.Errorf("受影响目标数 = %d，期望 2", len(global[0].AffectedTargetIDs))
	}
}

func TestErrorsAreClearedWhenConditionDisappears(t *testing.T) {
	// decisions/009 §4：错误是"状态"，条件消失后必须被清除。
	c, mgr, _ := newTestController(t)
	c.LoadConfig()
	mgr.failOn["a"] = diag.Errorf(diag.CodeTargetStartFailed, "端口占用")

	_ = c.Start()
	_ = c.Apply([]model.Target{tgt("a", "10.0.0.1", 4450)})
	if got := len(c.Snapshot().Errors); got == 0 {
		t.Fatal("启动失败应产生错误状态")
	}

	delete(mgr.failOn, "a")
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	if got := len(c.Snapshot().Errors); got != 0 {
		t.Fatalf("失败条件消失后错误必须被清除，仍有 %d 条: %+v", got, c.Snapshot().Errors)
	}
}

// --- 连接测试的探测目标（decisions/007 §4.11）------------------------------

func TestProbePortSelection(t *testing.T) {
	if got := probePort(true, 4450); got != model.WindowsMatchPort {
		t.Errorf("运行中应探测 :445，得到 %d", got)
	}
	if got := probePort(false, 4450); got != 4450 {
		t.Errorf("未运行应探测 AndroidPort，得到 %d", got)
	}
}

// --- 只读检查（L6：不得有卸载能力）-----------------------------------------

func TestInspectIsReadOnly(t *testing.T) {
	c, _, _ := newTestController(t)
	info := c.Inspect()
	if info.Available {
		t.Error("测试替身应报告不可用（只验证调用链与快照更新）")
	}
	// 结构性约束：controller 包中不得出现任何卸载/系统命令调用。
	assertNoUninstallCalls(t)
}

// --- Shutdown -------------------------------------------------------------

func TestShutdownStopsEverythingWithoutUninstall(t *testing.T) {
	c, mgr, _ := newTestController(t)
	c.LoadConfig()
	_ = c.Start()
	_ = c.Apply([]model.Target{tgt("a", "10.0.0.1", 4450)})

	c.Shutdown(0)
	if mgr.stops != 1 {
		t.Errorf("Shutdown 必须停止全部 runtime，stops = %d", mgr.stops)
	}
	snap := c.Snapshot()
	if snap.Global.Mode != snapshot.ModeStopped {
		t.Errorf("模式 = %v，期望 Stopped", snap.Global.Mode)
	}
	for _, e := range snap.Recent {
		if strings.Contains(e.Message, "sc stop") {
			t.Error("Shutdown 不得执行驱动卸载")
		}
	}
}

// --- 小工具 ---------------------------------------------------------------

func asViolations(err error, out **ViolationsError) bool {
	if v, ok := err.(*ViolationsError); ok {
		*out = v
		return true
	}
	return false
}

// assertNoUninstallCalls 结构性检查：controller 包的生产代码中**不得**出现
// 任何驱动卸载 / 系统命令调用（decisions/007 **L6**：v1 没有卸载能力）。
func assertNoUninstallCalls(t *testing.T) {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	forbidden := []string{"sc stop", "sc delete", "windivertctl", "UninstallDriver", "RemoveDriver", "sc.exe"}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		// 只看代码行：注释里出现"不得使用 sc stop"这类说明是允许的。
		for _, line := range strings.Split(string(data), "\n") {
			code := strings.TrimSpace(line)
			if strings.HasPrefix(code, "//") {
				continue
			}
			for _, f := range forbidden {
				if strings.Contains(code, f) {
					t.Errorf("%s 中出现被禁止的卸载/系统命令痕迹: %q", name, f)
				}
			}
		}
	}
}
