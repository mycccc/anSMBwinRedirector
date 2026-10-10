//go:build windows

package gui

import (
	"strings"
	"testing"

	"ansmbwinhelper/internal/diag"
	"ansmbwinhelper/internal/snapshot"
)

// 调试窗口的**只读诊断文本**（decisions/011 §2.7 / §9.3 D-1 / 013 §6.2）：
// 标签本地化、值原样、且⛔不含任何秘密。
func TestFormatInspectShowsDiagnosticsWithRawValues(t *testing.T) {
	info := diag.WinDivertInfo{
		Available:            true,
		WinDivertDir:         `C:\app`,
		RuntimeFilesOK:       false,
		ServiceExists:        true,
		DriverState:          "Running",
		ServiceImagePath:     `\??\C:\app\WinDivert64.sys`,
		ImagePathIsOurs:      true,
		DriverRuntimeVersion: "2.2.2",
		ConsumersQueried:     true,
		Files: []diag.RuntimeFile{
			{Name: "WinDivert.dll", Path: `C:\app\WinDivert.dll`,
				Actual: "aaaa1111", Expected: "aaaa1111", Version: "2.2.2.0", OK: true},
			{Name: "WinDivert64.sys", Path: `C:\app\WinDivert64.sys`,
				Actual: "deadbeef", Expected: "8da08533", Version: "2.2.2.0", OK: false},
		},
		Consumers: []diag.Consumer{{PID: 4321, Layer: "Network", IsOurs: true}},
	}
	out := formatInspect(info)

	// 标签（本地化）+ 值（原样）都要在
	for _, want := range []string{
		"WinDivert.dll", "WinDivert64.sys",
		`C:\app\WinDivert.dll`, `C:\app\WinDivert64.sys`,
		"deadbeef", "8da08533", // 实际 / 期望哈希原样输出
		"2.2.2", "2.2.2.0", "4321", "Network", `\??\C:\app\WinDivert64.sys`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("调试文本缺少 %q\n---\n%s", want, out)
		}
	}
	// ⛔ 不得出现任何"可安全卸载"式的错误结论（007 §4.7.1 的不可证明性）。
	if strings.Contains(out, "safe to uninstall") || strings.Contains(out, "可以安全卸载") {
		t.Error("⛔ 不得给出\"可以安全卸载\"的结论")
	}
}

// ⛔ D-1 修复的**回归守卫**：只读诊断**没有**查询运行时驱动版本 / 可观察使用者时，
// 必须明确显示"未查询"，**不得**显示成"（无）"或拿别的版本冒充。
func TestFormatInspectNeverFakesUnqueriedValues(t *testing.T) {
	info := diag.WinDivertInfo{Available: true, WinDivertDir: `C:\app`, RuntimeFilesOK: true}
	out := formatInspect(info)

	if !strings.Contains(out, "未查询") {
		t.Errorf("未查询时正文必须出现\"未查询\"，得到:\n%s", out)
	}
	// ⛔ 不得把"没查"写成"（无）"（那会被读成"查过且没有"）。
	if strings.Contains(out, "（无）") {
		t.Errorf("⛔ 未查询不得显示为\"（无）\":\n%s", out)
	}
	// ⛔ 不得用期望版本冒充运行时版本。
	if strings.Contains(out, "2.2.2") {
		t.Errorf("⛔ 未查询时不得出现任何版本号:\n%s", out)
	}
}

// 「无法检查」与「检查结果」必须区分清楚（不得只说"不可用"而没有任何原因）。
func TestFormatInspectUnavailableCarriesReason(t *testing.T) {
	out := formatInspect(diag.WinDivertInfo{Available: false, Err: "缺少 WinDivert.dll"})
	if !strings.Contains(out, "缺少 WinDivert.dll") {
		t.Fatalf("不可用时必须带原因，得到:\n%s", out)
	}
}

// ⛔ 调试文本不得包含密码 / 掩码长度等秘密（decisions/012 §3.3 / 013 §6.2）。
//
// 说明：`diag.WinDivertInfo` 结构上**没有**密码字段，因此这是一条"回归守卫"——
// 未来若有人往诊断结构里加了敏感字段，本测试会立刻失败。
func TestFormatInspectCarriesNoSecrets(t *testing.T) {
	const canary = "p@ssw0rd-CANARY-1234"
	info := diag.WinDivertInfo{
		Available: true, WinDivertDir: `C:\app`,
		Files: []diag.RuntimeFile{{Name: "WinDivert.dll", Path: `C:\app\WinDivert.dll`, Actual: "x", Expected: "y"}},
	}
	out := formatInspect(info)
	if strings.Contains(out, canary) {
		t.Fatal("⛔ 调试文本泄露了密码")
	}
}

// --- 状态栏操作名：**展示层**本地化，内部标识不变（Gate 2026-10-09 裁定）-------

func TestOperationTextIsLocalizedButIdentifierStays(t *testing.T) {
	cases := map[snapshot.Operation]string{
		snapshot.OpApply:   "应用中",
		snapshot.OpStart:   "启动中",
		snapshot.OpStop:    "停止中",
		snapshot.OpExit:    "退出中",
		snapshot.OpConnect: "连接中",
		snapshot.OpDebug:   "调试中",
	}
	for op, want := range cases {
		if got := operationText(op); got != want {
			t.Errorf("operationText(%s) = %q，期望 %q", op, got, want)
		}
		// ⚠️ 内部标识必须保持不变（供日志 / 错误关联 / 复制详情使用）。
		if string(op) == want {
			t.Errorf("内部标识 %q 被改成了展示文案 —— 违反分层", op)
		}
	}
	// 未知操作 ⇒ 退回内部标识（不 panic、不显示 key）。
	if got := operationText(snapshot.Operation("SomethingNew")); got != "SomethingNew" {
		t.Errorf("未知操作应退回内部标识，得到 %q", got)
	}
}
