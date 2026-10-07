package winvert

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// `assets/` 是本仓库保存的 WinDivert 2.2.2-A x64 发布文件 —— 它们是**发布输入**
// （decisions/010 §B：外置发布，不再内嵌）。
//
// 这些断言保证 assets_common.go 里的期望哈希与 assets/ 中的文件一致，
// 也就是说：运行期 `Prepare()` 用来校验"可执行文件旁边那两个文件"的期望值，
// 确实是我们验证过的原件，而不是某个被替换/损坏的副本。
func TestExpectedHashesMatchRepoAssets(t *testing.T) {
	for _, name := range []string{DLLName, SysName} {
		data, err := os.ReadFile(filepath.Join("assets", name))
		if err != nil {
			t.Fatalf("读取 assets/%s 失败: %v", name, err)
		}
		if !HashMatches(name, data) {
			t.Errorf("assets/%s 与固化在二进制里的期望哈希不一致：%s", name, hashMismatchDetail(name, data))
		}
	}
}

// 校验必须是"逐字节严格比对"，且对未知文件**拒绝**（不能默认放行）。
func TestHashMatchesRejectsTamperedAndUnknown(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("assets", DLLName))
	if err != nil {
		t.Fatal(err)
	}
	if !HashMatches(DLLName, data) {
		t.Fatal("原件必须通过校验")
	}
	bad := append([]byte(nil), data...)
	bad[0] ^= 0xFF
	if HashMatches(DLLName, bad) {
		t.Error("被改动一个字节的文件必须校验失败")
	}
	if HashMatches("unknown.bin", data) {
		t.Error("未知文件名必须校验失败（不能默认放行）")
	}
}

// 32 位 DLL 与 64 位 SYS 混放是最现实的故障之一：两者哈希不同 ⇒ 必被拒绝。
func TestFilesAreDistinct(t *testing.T) {
	if ExpectedDLLSHA256 == ExpectedSysSHA256 {
		t.Fatal("DLL 与 SYS 的期望哈希不应相同")
	}
}

// decisions/009 §16：WinDivert 许可文本必须**随包**。
// 它不与 exe 内嵌，而是作为独立文件随发布包分发（decisions/010 §B.1），
// 但必须与二进制资源放在一起，避免打包时被漏掉
// （detour 的问题正是引用了实际不存在的许可文件）。
func TestLicenseFileTravelsWithAssets(t *testing.T) {
	if _, err := os.Stat(filepath.Join("assets", "WinDivert-LICENSE.txt")); err != nil {
		t.Fatalf("许可文本必须与本项目的 WinDivert 二进制资源放在一起: %v", err)
	}
}

// 「Prepare 的失败不得被缓存」是**实机验证发现的缺陷**的回归测试：
// 用户补齐/修复同目录的 WinDivert 文件后，「重试启动」必须能真正再校验一次。
func TestPrepareStateCachesSuccessButNeverFailure(t *testing.T) {
	var st prepareState
	calls := 0
	fail := func() error { calls++; return errors.New("boom") }

	if err := st.do(fail); err == nil {
		t.Fatal("首次失败应返回错误")
	}
	if calls != 1 {
		t.Fatalf("calls=%d，期望 1", calls)
	}
	// 关键断言：第二次必须**真正重试**（失败未被缓存）。
	if err := st.do(fail); err == nil {
		t.Fatal("第二次仍应返回错误")
	}
	if calls != 2 {
		t.Fatalf("失败被缓存了：calls=%d，期望 2", calls)
	}

	ok := func() error { calls++; return nil }
	if err := st.do(ok); err != nil {
		t.Fatalf("修好后应成功：%v", err)
	}
	if err := st.do(ok); err != nil {
		t.Fatalf("成功后仍应返回 nil：%v", err)
	}
	if calls != 3 {
		t.Fatalf("成功后不应再执行：calls=%d，期望 3", calls)
	}
}
