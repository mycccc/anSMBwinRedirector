package controller

import (
	"errors"
	"net/netip"
	"path/filepath"
	"strings"
	"testing"

	"ansmbwinhelper/internal/config"
	"ansmbwinhelper/internal/credential"
	"ansmbwinhelper/internal/diag"
	"ansmbwinhelper/internal/model"
	"ansmbwinhelper/internal/snapshot"
)

// --- 测试替身（decisions/012）----------------------------------------------

type storedCred struct{ host, user, pass string }

type fakeCred struct {
	state      credential.State
	inspectErr error
	storeErr   error
	stored     []storedCred
}

func (f *fakeCred) Inspect(string) (credential.State, error) { return f.state, f.inspectErr }

func (f *fakeCred) Store(host, user, pass string) error {
	f.stored = append(f.stored, storedCred{host, user, pass})
	return f.storeErr
}

type fakeOpener struct {
	opened []string
	err    error
}

func (f *fakeOpener) Open(host string) error {
	f.opened = append(f.opened, host)
	return f.err
}

func newConnectController(t *testing.T) (*Controller, *fakeManager, *fakeCred, *fakeOpener) {
	t.Helper()
	dir := t.TempDir()
	mgr := newFakeManager()
	cred := &fakeCred{}
	op := &fakeOpener{}
	c := New(Options{
		Store:      config.NewStore(filepath.Join(dir, "config.json")),
		Manager:    mgr,
		Inspector:  fakeInspector{},
		Version:    diag.VersionInfo{Version: "1.0-test"},
		OSInfo:     "test",
		Credential: cred,
		Opener:     op,
	})
	return c, mgr, cred, op
}

func tgtCred(id, ip string, port uint16, user, pass string) model.Target {
	return model.Target{
		ID: id, AndroidIP: netip.MustParseAddr(ip), AndroidPort: port,
		Username: user, Password: pass,
	}
}

// startRunning 让 controller 进入 Running 且该目标 runtime 为 Running。
func startRunning(t *testing.T, c *Controller, targets ...model.Target) {
	t.Helper()
	c.LoadConfig()
	if err := c.Apply(targets); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if err := c.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
}

// --- 前置条件：必须正在运行（decisions/012 C1）------------------------------

func TestConnectRequiresRunning(t *testing.T) {
	c, _, cred, op := newConnectController(t)
	c.LoadConfig()
	// 只保存、不 Start ⇒ Stopped
	if err := c.Apply([]model.Target{tgtCred("a", "192.168.1.100", 4450, "u", "p")}); err != nil {
		t.Fatal(err)
	}
	err := c.Connect("a", true)
	if err == nil {
		t.Fatal("未运行时 Connect 必须失败")
	}
	code, ok := diag.CodeOf(err)
	if !ok || code != diag.CodeConnectNotRunning {
		t.Fatalf("应返回 CONNECT_NOT_RUNNING，得到 %v (%v)", err, code)
	}
	if len(cred.stored) != 0 || len(op.opened) != 0 {
		t.Error("前置条件失败时不得写凭据、不得打开共享")
	}
	// 错误必须进快照，且**不阻塞**（decisions/007 L7）。
	var found *diag.ErrorInfo
	for i, e := range c.Snapshot().Errors {
		if e.Code == diag.CodeConnectNotRunning {
			found = &c.Snapshot().Errors[i]
			break
		}
	}
	if found == nil {
		t.Fatal("连接类错误必须出现在快照里")
	}
	if found.Blocking() || found.Scope != diag.ScopeTarget {
		t.Errorf("连接类错误应为单目标、非阻塞，得到 blocking=%v scope=%v", found.Blocking(), found.Scope)
	}
}

func TestConnectUnknownTarget(t *testing.T) {
	c, _, _, _ := newConnectController(t)
	c.LoadConfig()
	_ = c.Start()
	if err := c.Connect("nope", true); !errors.Is(err, ErrConnectNotFound) {
		t.Fatalf("未知 ID 应返回 ErrConnectNotFound，得到 %v", err)
	}
}

// --- 无凭据：跳过凭据步骤（decisions/012 §3.4）-----------------------------

func TestConnectWithoutCredentialsSkipsCredentialStep(t *testing.T) {
	c, _, cred, op := newConnectController(t)
	startRunning(t, c, tgt("a", "192.168.1.100", 4450))

	if err := c.Connect("a", false); err != nil {
		t.Fatalf("无凭据时应直接打开共享: %v", err)
	}
	if len(cred.stored) != 0 {
		t.Errorf("无凭据不得写凭据，实际写了 %d 次", len(cred.stored))
	}
	if len(op.opened) != 1 || op.opened[0] != "192.168.1.100" {
		t.Errorf("应打开 \\\\192.168.1.100\\，得到 %v", op.opened)
	}
}

// --- 有凭据：先下发、再打开 ------------------------------------------------

func TestConnectWritesCredentialThenOpensShare(t *testing.T) {
	c, _, cred, op := newConnectController(t)
	startRunning(t, c, tgtCred("a", "192.168.1.100", 4450, "ansmb", "s3cret"))

	if err := c.Connect("a", false); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if len(cred.stored) != 1 {
		t.Fatalf("应写 1 次凭据，得到 %d", len(cred.stored))
	}
	if got := cred.stored[0]; got.host != "192.168.1.100" || got.user != "ansmb" || got.pass != "s3cret" {
		t.Errorf("凭据内容错误: %+v", got)
	}
	if len(op.opened) != 1 || op.opened[0] != "192.168.1.100" {
		t.Errorf("应打开该主机，得到 %v", op.opened)
	}
}

// --- 已有凭据 ⇒ 需要确认（decisions/012 C2）--------------------------------

func TestConnectExistingCredentialNeedsConfirm(t *testing.T) {
	c, _, cred, op := newConnectController(t)
	startRunning(t, c, tgtCred("a", "192.168.1.100", 4450, "ansmb", "s3cret"))
	cred.state = credential.State{Exists: true, Username: "someone-else"}

	err := c.Connect("a", false)
	if !errors.Is(err, ErrConnectNeedsCredentialConfirm) {
		t.Fatalf("应要求确认，得到 %v", err)
	}
	if len(cred.stored) != 0 || len(op.opened) != 0 {
		t.Error("未确认时不得写凭据、不得打开共享")
	}
	// ⚠️ "需要确认"不是运行错误 ⇒ 不得写入 ErrorInfo。
	for _, e := range c.Snapshot().Errors {
		if e.Code == diag.CodeCredentialWriteFailed || e.Code == diag.CodeShareOpenFailed {
			t.Errorf("“需要确认”不应产生错误条目: %v", e.Code)
		}
	}

	// 确认后继续
	if err := c.Connect("a", true); err != nil {
		t.Fatalf("确认后应成功: %v", err)
	}
	if len(cred.stored) != 1 || len(op.opened) != 1 {
		t.Errorf("确认后应写凭据并打开，得到 stored=%d opened=%d", len(cred.stored), len(op.opened))
	}
}

func TestConnectInspectFailureIsFailSafe(t *testing.T) {
	c, _, cred, op := newConnectController(t)
	startRunning(t, c, tgtCred("a", "192.168.1.100", 4450, "ansmb", "s3cret"))
	cred.inspectErr = errors.New("查询失败")

	// 查不出来 ⇒ 按"可能覆盖"处理，仍要求确认（宁可多问一次）
	if err := c.Connect("a", false); !errors.Is(err, ErrConnectNeedsCredentialConfirm) {
		t.Fatalf("查询失败时应要求确认，得到 %v", err)
	}
	if len(cred.stored) != 0 || len(op.opened) != 0 {
		t.Error("不应有任何副作用")
	}
}

// --- 前置检查 PlanConnect ---------------------------------------------------

func TestPlanConnectReportsExistingCredentialWithoutPassword(t *testing.T) {
	c, _, cred, _ := newConnectController(t)
	startRunning(t, c, tgtCred("a", "192.168.1.100", 4450, "ansmb", "s3cret"))
	cred.state = credential.State{Exists: true, Username: "olduser"}

	p, err := c.PlanConnect("a")
	if err != nil {
		t.Fatalf("PlanConnect: %v", err)
	}
	if !p.HasCredentials || !p.ExistingCredential || p.ExistingUsername != "olduser" {
		t.Errorf("计划内容错误: %+v", p)
	}
	if !p.NeedsConfirm() {
		t.Error("NeedsConfirm 应为 true")
	}
}

// --- 失败路径与错误码 -------------------------------------------------------

func TestConnectStoreFailureRecordsCodedError(t *testing.T) {
	c, _, cred, op := newConnectController(t)
	startRunning(t, c, tgtCred("a", "192.168.1.100", 4450, "ansmb", "s3cret"))
	cred.storeErr = errors.New("cmdkey 失败")

	err := c.Connect("a", false)
	code, ok := diag.CodeOf(err)
	if !ok || code != diag.CodeCredentialWriteFailed {
		t.Fatalf("应返回 CREDENTIAL_WRITE_FAILED，得到 %v/%v", err, code)
	}
	if len(op.opened) != 0 {
		t.Error("写凭据失败后不得打开共享")
	}
	if !hasCode(c, diag.CodeCredentialWriteFailed) {
		t.Error("失败必须记入快照")
	}
}

func TestConnectOpenFailureRecordsCodedError(t *testing.T) {
	c, _, cred, op := newConnectController(t)
	startRunning(t, c, tgtCred("a", "192.168.1.100", 4450, "ansmb", "s3cret"))
	op.err = errors.New("start 失败")

	err := c.Connect("a", false)
	code, ok := diag.CodeOf(err)
	if !ok || code != diag.CodeShareOpenFailed {
		t.Fatalf("应返回 SHARE_OPEN_FAILED，得到 %v/%v", err, code)
	}
	if len(cred.stored) != 1 {
		t.Errorf("打开失败时凭据已下发（顺序使然），得到 %d", len(cred.stored))
	}
	if !hasCode(c, diag.CodeShareOpenFailed) {
		t.Error("失败必须记入快照")
	}
}

// --- ⛔ 密码绝不进入诊断 / 错误文本（decisions/012 §3.3）--------------------

func TestConnectNeverLeaksPasswordIntoDiagnostics(t *testing.T) {
	const secret = "s3cret-password"
	c, _, cred, _ := newConnectController(t)
	startRunning(t, c, tgtCred("a", "192.168.1.100", 4450, "ansmb", secret))

	// 最坏情况：凭据实现把密码夹带进错误文本。
	cred.storeErr = errors.New("cmdkey 失败：/pass:" + secret + " 被拒绝")

	err := c.Connect("a", false)
	if err == nil {
		t.Fatal("应失败")
	}
	if strings.Contains(err.Error(), secret) {
		t.Error("⛔ 错误文本泄露了密码")
	}
	details := c.CopyDetails()
	if strings.Contains(details, secret) {
		t.Error("⛔ 诊断/复制详情泄露了密码")
	}
	for _, e := range c.Snapshot().Errors {
		if strings.Contains(e.Detail, secret) || strings.Contains(e.String(), secret) {
			t.Error("⛔ ErrorInfo 泄露了密码")
		}
	}
	if !strings.Contains(details, "***") {
		t.Error("应能在详情中看到已被掩码（证明脱敏真的生效）")
	}
}

// --- 清除语义：只清连接类 ---------------------------------------------------

func TestConnectSuccessClearsOnlyConnectErrors(t *testing.T) {
	c, mgr, _, _ := newConnectController(t)
	mgr.failOn["a"] = errors.New("启动失败")
	startRunning(t, c, tgtCred("a", "192.168.1.100", 4450, "ansmb", "s3cret"))
	if !hasCode(c, diag.CodeTargetStartFailed) {
		t.Fatal("前置条件：应先有一个 runtime 失败错误")
	}
	// 让该目标可运行后重试（模拟用户修好环境）
	mgr.failOn = map[string]error{}
	_ = c.Stop()
	_ = c.Start()

	if err := c.Connect("a", false); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	for _, e := range c.Snapshot().Errors {
		if e.Code == diag.CodeCredentialWriteFailed || e.Code == diag.CodeShareOpenFailed {
			t.Errorf("连接类历史错误应被清除: %v", e.Code)
		}
	}
}

func hasCode(c *Controller, code diag.Code) bool {
	for _, e := range c.Snapshot().Errors {
		if e.Code == code {
			return true
		}
	}
	return false
}

// --- Reachability 更新规则（Gate 2026-10-09 裁定 = 方案 A）-------------------
//
// 语义：`Reachable` = 「**最近一次 Connect 操作成功**」，不是独立网络探测。

func TestConnectSuccessSetsReachabilityReachable(t *testing.T) {
	c, _, _, _ := newConnectController(t)
	startRunning(t, c, tgtCred("a", "192.168.1.100", 4450, "ansmb", "s3cret"))

	if err := c.Connect("a", false); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	e, ok := c.Snapshot().Reach["a"]
	if !ok {
		t.Fatal("成功后必须写入 Reachability")
	}
	if e.State != snapshot.ReachReachable {
		t.Errorf("应写 Reachable，得到 %v", e.State)
	}
	if e.LastChecked.IsZero() {
		t.Error("必须同时更新 LastChecked")
	}
}

func TestConnectFailuresNeverWriteReachability(t *testing.T) {
	// ① 凭据写入失败
	c1, _, cred, _ := newConnectController(t)
	startRunning(t, c1, tgtCred("a", "192.168.1.100", 4450, "ansmb", "s3cret"))
	cred.storeErr = errors.New("cmdkey 失败")
	if err := c1.Connect("a", false); err == nil {
		t.Fatal("应失败")
	}
	assertReachAbsent(t, c1, "a", "凭据写入失败")

	// ② 打开共享失败
	c2, _, _, op := newConnectController(t)
	startRunning(t, c2, tgtCred("a", "192.168.1.100", 4450, "ansmb", "s3cret"))
	op.err = errors.New("start 失败")
	if err := c2.Connect("a", false); err == nil {
		t.Fatal("应失败")
	}
	assertReachAbsent(t, c2, "a", "打开共享失败")

	// ③ 用户取消覆盖凭据（未确认）
	c3, _, cred3, _ := newConnectController(t)
	startRunning(t, c3, tgtCred("a", "192.168.1.100", 4450, "ansmb", "s3cret"))
	cred3.state = credential.State{Exists: true, Username: "someone-else"}
	if err := c3.Connect("a", false); !errors.Is(err, ErrConnectNeedsCredentialConfirm) {
		t.Fatalf("应要求确认，得到 %v", err)
	}
	assertReachAbsent(t, c3, "a", "未确认（等同取消）")
}

// assertReachAbsent 断言既**没有**写成 Reachable，也**没有**写成 Unreachable
// （⛔ 失败不得被判为"主机不可达"——认证/凭据/Explorer 失败 ≠ 不可达）。
func assertReachAbsent(t *testing.T, c *Controller, id, why string) {
	t.Helper()
	e, ok := c.Snapshot().Reach[id]
	if !ok {
		return // 从未写过 ⇒ 符合预期（保持"尚未连接"）
	}
	if e.State == snapshot.ReachReachable {
		t.Errorf("⛔ %s 时不得写 Reachable", why)
	}
	if e.State == snapshot.ReachUnreachable {
		t.Errorf("⛔ %s 时不得写 Unreachable（失败 ≠ 主机不可达）", why)
	}
}
