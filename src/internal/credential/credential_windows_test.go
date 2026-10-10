//go:build windows

package credential

import (
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
)

// 这是一个**真机集成测试**：默认跳过，只有在 Windows 上显式设置
// ANSMB_WIN_LIVE=1 时才运行（避免在常规 `go test` 里改动本机凭据库）。
//
// 它使用 TEST-NET-3（203.0.113.0/24，保留给文档用）里的地址与一次性凭据，
// **不会触碰任何真实主机**，结束时删除自己写入的凭据。
const (
	liveHost     = "203.0.113.9"
	liveUser     = "ansmb-live-test"
	livePassword = "live-secret-0123"
)

func TestWindowsLiveCredentialRoundTrip(t *testing.T) {
	if os.Getenv("ANSMB_WIN_LIVE") != "1" {
		t.Skip("需要 ANSMB_WIN_LIVE=1（会写入本机凭据库，随后自行清理）")
	}
	a := New()
	cmdkeyDelete(liveHost)
	defer cmdkeyDelete(liveHost)

	// ① 初始：不存在
	st, err := a.Inspect(liveHost)
	if err != nil {
		t.Fatalf("Inspect(初始) 失败: %v", err)
	}
	if st.Exists {
		t.Fatalf("前置条件失败：%s 上已存在凭据（username=%q），请先清理", liveHost, st.Username)
	}

	// ② 写入
	if err := a.Store(liveHost, liveUser, livePassword); err != nil {
		t.Fatalf("Store 失败: %v", err)
	}
	if strings.Contains(errString(err), livePassword) {
		t.Error("⛔ Store 的错误文本泄露了密码")
	}

	// ③ 复查：应存在且用户名正确
	st, err = a.Inspect(liveHost)
	if err != nil {
		t.Fatalf("Inspect(写入后) 失败: %v", err)
	}
	if !st.Exists {
		t.Fatal("写入后 Inspect 应报告存在")
	}
	if st.Username != liveUser {
		t.Errorf("用户名应可读回，得到 %q", st.Username)
	}

	// ④ 基本校验依然生效
	if err := a.Store(liveHost, "", livePassword); err == nil {
		t.Error("空用户名必须被拒绝")
	}

	// ⑤ 清理后应回到不存在
	cmdkeyDelete(liveHost)
	st, err = a.Inspect(liveHost)
	if err != nil {
		t.Fatalf("Inspect(清理后) 失败: %v", err)
	}
	if st.Exists {
		t.Error("清理后不应再存在凭据")
	}
	t.Log("凭据层真机往返：Inspect/Store/清理 全部通过")
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func cmdkeyDelete(host string) {
	cmd := exec.Command("cmdkey", "/delete:"+host)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	_ = cmd.Run() // 不存在时返回非 0，忽略
}
