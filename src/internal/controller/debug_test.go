package controller

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"ansmbwinhelper/internal/diag"
	"ansmbwinhelper/internal/model"
	"ansmbwinhelper/internal/snapshot"
)

// --- 测试替身：调试窗口的动作（decisions/011 §2.7）--------------------------

type fakeRepairer struct {
	calls int
	err   error
}

func (f *fakeRepairer) Repair() error { f.calls++; return f.err }

type fakeSystemOps struct {
	calls int
	err   error
}

func (f *fakeSystemOps) RestartWorkstation() error { f.calls++; return f.err }

// newDebugController 在连接测试替身之上再注入修复 / 系统操作替身。
func newDebugController(t *testing.T) (*Controller, *fakeRepairer, *fakeSystemOps) {
	t.Helper()
	c, _, _, _ := newConnectController(t)
	rp := &fakeRepairer{}
	so := &fakeSystemOps{}
	c.opts.Repairer = rp
	c.opts.SystemOps = so
	return c, rp, so
}

// --- RepairWinDivert --------------------------------------------------------

func TestRepairRefusedWhileRunning(t *testing.T) {
	c, rp, _ := newDebugController(t)
	startRunning(t, c, tgtCred("a", "192.168.1.100", 4450, "u", "p"))

	err := c.RepairWinDivert()
	code, ok := diag.CodeOf(err)
	if !ok || code != diag.CodeRepairRequiresStop {
		t.Fatalf("运行时修复必须被拒绝（REPAIR_REQUIRES_STOP），得到 %v/%v", err, code)
	}
	if rp.calls != 0 {
		t.Error("⛔ 被拒绝时不得调用修复实现（有活动 handle）")
	}
	if !hasCode(c, diag.CodeRepairRequiresStop) {
		t.Error("拒绝原因必须进诊断")
	}
	// ⛔ 该错误**不阻塞**（它只是"这个动作现在不能做"）。
	for _, e := range c.Snapshot().Errors {
		if e.Code == diag.CodeRepairRequiresStop && e.Blocking() {
			t.Error("REPAIR_REQUIRES_STOP 不得是阻塞类错误")
		}
	}
}

func TestRepairAllowedWhenStopped(t *testing.T) {
	c, rp, _ := newDebugController(t)
	c.LoadConfig() // Stopped

	if err := c.RepairWinDivert(); err != nil {
		t.Fatalf("Stopped 时应允许修复: %v", err)
	}
	if rp.calls != 1 {
		t.Fatalf("应调用修复实现 1 次，得到 %d", rp.calls)
	}
}

func TestRepairFailureIsRecorded(t *testing.T) {
	c, rp, _ := newDebugController(t)
	c.LoadConfig()
	rp.err = errors.New("服务无法删除")

	err := c.RepairWinDivert()
	code, ok := diag.CodeOf(err)
	if !ok || code != diag.CodeWinDivertTakeoverFailed {
		t.Fatalf("应返回 WINDIVERT_TAKEOVER_FAILED，得到 %v/%v", err, code)
	}
	if !hasCode(c, diag.CodeWinDivertTakeoverFailed) {
		t.Error("失败必须进诊断")
	}
}

func TestRepairUnavailableWithoutImplementation(t *testing.T) {
	c, _, _, _ := newConnectController(t)
	c.LoadConfig()
	c.opts.Repairer = nil
	err := c.RepairWinDivert()
	if code, ok := diag.CodeOf(err); !ok || code != diag.CodePlatformUnsupported {
		t.Fatalf("无实现时应报 PLATFORM_UNSUPPORTED，得到 %v/%v", err, code)
	}
}

// --- RestartWorkstation -----------------------------------------------------

func TestRestartWorkstationSuccessAndFailure(t *testing.T) {
	c, _, so := newDebugController(t)
	c.LoadConfig()

	if err := c.RestartWorkstation(); err != nil {
		t.Fatalf("应成功: %v", err)
	}
	if so.calls != 1 {
		t.Fatalf("应调用 1 次，得到 %d", so.calls)
	}

	so.err = errors.New("sc 失败")
	err := c.RestartWorkstation()
	if code, ok := diag.CodeOf(err); !ok || code != diag.CodeWorkstationRestartFailed {
		t.Fatalf("应返回 WORKSTATION_RESTART_FAILED，得到 %v/%v", err, code)
	}
	if !hasCode(c, diag.CodeWorkstationRestartFailed) {
		t.Error("失败必须进诊断")
	}
}

// --- 串行化：调试动作不得与 Start/Stop/Apply/Connect 并发 -------------------

func TestDebugOpsAreSerialized(t *testing.T) {
	c, rp, _ := newDebugController(t)
	c.LoadConfig()

	// 人为占住串行化槽位（模拟另一操作进行中）。
	if err := c.begin(snapshot.OpDebug); err != nil {
		t.Fatalf("无法占位: %v", err)
	}
	if err := c.RepairWinDivert(); !errors.Is(err, ErrBusy) {
		t.Errorf("应返回 ErrBusy，得到 %v", err)
	}
	if rp.calls != 0 {
		t.Error("忙时不得调用修复实现")
	}
	c.end()
}

// --- ⛔ 密码泄露审计：贯穿全部用户可见 / 诊断输出路径 ------------------------

func TestNoPasswordLeakAcrossAllPaths(t *testing.T) {
	const secret = "p@ssw0rd-LEAK-CANARY-9182"
	const user = "canary-user"

	c, _, cred, op := newConnectController(t)
	rp := &fakeRepairer{}
	so := &fakeSystemOps{}
	c.opts.Repairer = rp
	c.opts.SystemOps = so

	targets := []model.Target{tgtCred("a", "192.168.1.100", 4450, user, secret)}
	c.LoadConfig()
	if err := c.Apply(targets); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if err := c.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// ① 成功路径
	if err := c.Connect("a", false); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	// ② 最坏情况：凭据实现的错误文本里**夹带**密码
	cred.storeErr = errors.New("cmdkey 失败 /pass:" + secret + " 被拒绝")
	_ = c.Connect("a", false)
	// ③ 打开失败也夹带密码
	cred.storeErr = nil
	op.err = errors.New("explorer 失败 " + secret)
	_ = c.Connect("a", false)
	// ④ 诊断 / 修复 / 重启 / 停止
	_ = c.Inspect()
	_ = c.RepairWinDivert() // Running ⇒ 拒绝，但也要看它的输出
	_ = c.RestartWorkstation()

	// 审计必须覆盖 **Stop 之前** 与 **Stop 之后** 两条路径
	// （注意：Stop 会 ClearByScope(ScopeTarget)，会清掉连接类错误）。
	maskedSeen := false
	for stage := 0; stage < 2; stage++ {
		if stage == 1 {
			_ = c.Stop()
		}
		for _, bad := range collectSecretOutputs(c) {
			if strings.Contains(bad.text, secret) {
				t.Errorf("⛔ [%s] %s 泄露了密码", stageTag(stage), bad.name)
			}
		}
		for _, e := range c.Snapshot().Errors {
			if strings.Contains(e.Detail, "***") {
				maskedSeen = true
			}
		}
	}

	// 目标自身（model.Target 的三种打印方式）也必须安全。
	for _, bad := range []secretOutput{
		{"model.Target.String()", targets[0].String()},
		{"model.Target %v", fmt.Sprintf("%v", targets[0])},
		{"model.Target %+v", fmt.Sprintf("%+v", targets[0])},
	} {
		if strings.Contains(bad.text, secret) {
			t.Errorf("⛔ %s 泄露了密码", bad.name)
		}
	}

	// ✅ 审计**有效性对照**：若 `%v` 不走 String()，上面那些"不泄露"就是假绿。
	if got := fmt.Sprintf("%v", targets[0]); got != targets[0].String() {
		t.Fatalf("审计前提失效：model.Target 的 %%v 未调用 String()（得到 %q）—— 结论不可靠", got)
	}

	// 反向守卫：确认脱敏**确实跑过**（避免"根本没触发"造成的假绿）。
	if !maskedSeen {
		t.Error("未观察到掩码痕迹 —— 脱敏路径可能根本没被执行")
	}
}

func stageTag(stage int) string {
	if stage == 0 {
		return "Stop 前"
	}
	return "Stop 后"
}

type secretOutput struct{ name, text string }

// collectSecretOutputs 收集**一切可能被用户看到或粘贴出去**的输出。
func collectSecretOutputs(c *Controller) []secretOutput {
	snap := c.Snapshot()
	out := []secretOutput{
		{"CopyDetails()", c.CopyDetails()},
		{"Details() %v", fmt.Sprintf("%v", c.Details())},
		{"snapshot %v", fmt.Sprintf("%v", snap)},
		{"snapshot %+v", fmt.Sprintf("%+v", snap)},
		{"persisted slice %v", fmt.Sprintf("%v", snap.Persisted)},
		{"persisted slice %+v", fmt.Sprintf("%+v", snap.Persisted)},
		{"reach %v", fmt.Sprintf("%v", snap.Reach)},
		{"winvert info %v", fmt.Sprintf("%v", snap.WinDivert)},
		{"config info %v", fmt.Sprintf("%v", snap.Config)},
		{"global %v", fmt.Sprintf("%v", snap.Global)},
		{"errors slice %+v", fmt.Sprintf("%+v", snap.Errors)},
		{"recent slice %+v", fmt.Sprintf("%+v", snap.Recent)},
	}
	for i, e := range snap.Errors {
		out = append(out, secretOutput{fmt.Sprintf("ErrorInfo[%d]", i),
			e.String() + " | " + e.Detail + " | " + e.Summary})
	}
	for i, ev := range snap.Recent {
		out = append(out, secretOutput{fmt.Sprintf("ring[%d]", i), ev.Message + " | " + string(ev.Source)})
	}
	return out
}
