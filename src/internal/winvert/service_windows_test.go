//go:build windows

package winvert

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc/mgr"
)

// 真机集成测试：默认跳过，需 ANSMB_WIN_LIVE=1（会创建/删除 WinDivert 服务，需管理员）。
//
// ⚠️ 运行前必须把本测试二进制放在**含 WinDivert.dll / WinDivert64.sys 的目录**里
// （WinDivertDir() 取的是 os.Executable() 所在目录）。
//
// 它验证的是 decisions/011 的**核心链路**：
//
//	造一个指向**旧路径**的"遗留服务"
//	  → BeforeOpen() 必须把它**移除**（T-e：⛔ 不得复用）
//	  → Prepare() + OpenSession() + AfterOpen() 必须确认 ImagePath 指向**本程序目录**（T-c）
//	  → AfterStop() 必须移除服务并让 .sys 解锁
func TestWindowsLiveServiceLifecycle(t *testing.T) {
	if os.Getenv("ANSMB_WIN_LIVE") != "1" {
		t.Skip("需要 ANSMB_WIN_LIVE=1（会创建/删除 WinDivert 服务，需管理员）")
	}

	dir, err := WinDivertDir()
	if err != nil {
		t.Fatalf("WinDivertDir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, DLLName)); err != nil {
		t.Fatalf("前置条件失败：测试二进制必须与 %s 同目录（%v）", DLLName, err)
	}

	// 0) 前置：确保系统里没有 WinDivert 服务。
	if err := AfterStop(); err != nil {
		t.Fatalf("初始清理失败: %v", err)
	}

	// 1) 造一个"遗留服务"：指向**临时目录**里的 .sys（模拟旧版本/旧路径）。
	staleDir := t.TempDir()
	staleSys := filepath.Join(staleDir, SysName)
	if err := copyFile(filepath.Join(dir, SysName), staleSys); err != nil {
		t.Fatalf("准备遗留 .sys 失败: %v", err)
	}
	if err := createDriverService(ServiceName, staleSys); err != nil {
		t.Fatalf("创建遗留服务失败: %v", err)
	}
	defer deleteDriverService(ServiceName)

	info, err := queryService()
	if err != nil || !info.Exists {
		t.Fatalf("前置条件失败：遗留服务未创建（err=%v info=%+v）", err, info)
	}
	t.Logf("已创建遗留服务：ImagePath=%s", info.ImagePath)
	if imagePathMatches(info.ImagePath, dir) {
		t.Fatal("前置条件失败：遗留服务的 ImagePath 不应等于本程序目录")
	}

	// 2) ⛔ T-e：接管必须移除遗留服务（不得"发现已有服务就直接复用"）。
	if err := BeforeOpen(); err != nil {
		t.Fatalf("BeforeOpen（接管）失败: %v", err)
	}
	if info, _ = queryService(); info.Exists {
		t.Fatalf("接管后服务仍存在（T-e 失败：可能复用了旧服务）: %+v", info)
	}
	t.Log("✅ 接管成功：指向旧路径的遗留服务已被移除")

	// 3) Prepare → Open → AfterOpen（T-c：ImagePath 必须指向本程序目录）。
	if err := Prepare(); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	sess, err := OpenSession("false", DefaultBufSize)
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	if err := AfterOpen(); err != nil {
		t.Fatalf("AfterOpen（ImagePath 验证）失败: %v", err)
	}
	if info, _ = queryService(); !imagePathMatches(info.ImagePath, dir) {
		t.Fatalf("T-c 失败：ImagePath=%s 未指向本程序目录 %s", info.ImagePath, dir)
	}
	t.Logf("✅ 安装后 ImagePath 指向本程序目录：%s", info.ImagePath)

	// 4) 关闭 handle → AfterStop（停 + 移除 + 等 .sys 解锁）。
	if err := sess.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := AfterStop(); err != nil {
		t.Fatalf("AfterStop: %v", err)
	}
	if info, _ = queryService(); info.Exists {
		t.Fatal("AfterStop 后服务仍存在")
	}
	if !fileOpenable(filepath.Join(dir, SysName)) {
		t.Fatal("AfterStop 后 .sys 仍被占用（驱动可能未真正卸载）")
	}
	t.Log("✅ AfterStop：服务已移除、.sys 已解锁")
}

// 真机集成测试：**Repair / 重新接管**（decisions/011 §2.7）与运行时文件校验。
//
// 同样需要 `ANSMB_WIN_LIVE=1`，且测试二进制必须与 WinDivert.dll / WinDivert64.sys 同目录。
func TestWindowsLiveRepair(t *testing.T) {
	if os.Getenv("ANSMB_WIN_LIVE") != "1" {
		t.Skip("需要 ANSMB_WIN_LIVE=1（会创建/删除 WinDivert 服务，需管理员）")
	}

	dir, err := WinDivertDir()
	if err != nil {
		t.Fatalf("WinDivertDir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, DLLName)); err != nil {
		t.Fatalf("前置条件失败：测试二进制必须与 %s 同目录（%v）", DLLName, err)
	}
	// 前置：系统里没有 WinDivert 服务。
	if err := AfterStop(); err != nil {
		t.Fatalf("初始清理失败: %v", err)
	}

	// ① 运行时文件逐项校验（decisions/011 §2.7：路径 + 实际/期望 SHA-256）。
	files := RuntimeFileInfos()
	if len(files) != 2 {
		t.Fatalf("应报告 2 个运行时文件，得到 %d（%+v）", len(files), files)
	}
	for _, f := range files {
		if !f.OK || f.Actual == "" || f.Expected == "" {
			t.Errorf("文件校验应通过（OK/Actual/Expected 都要有）: %+v", f)
		}
	}
	t.Logf("✅ 运行时文件校验通过：%s=%s… %s=%s…",
		files[0].Name, files[0].Actual[:12], files[1].Name, files[1].Actual[:12])

	// ② 造一个指向**旧路径**的遗留服务。
	staleDir := t.TempDir()
	staleSys := filepath.Join(staleDir, SysName)
	if err := copyFile(filepath.Join(dir, SysName), staleSys); err != nil {
		t.Fatalf("准备遗留 .sys 失败: %v", err)
	}
	if err := createDriverService(ServiceName, staleSys); err != nil {
		t.Fatalf("创建遗留服务失败: %v", err)
	}
	defer deleteDriverService(ServiceName)

	// ③ Repair 必须移除它（复用接管流程；⛔ 不是裸卸载）。
	InvalidatePrepare() // 模拟"用户刚替换过文件"
	if err := (Repairer{}).Repair(); err != nil {
		t.Fatalf("Repair 失败: %v", err)
	}
	if info, _ := queryService(); info.Exists {
		t.Fatalf("Repair 后遗留服务仍存在: %+v", info)
	}
	t.Log("✅ Repair：遗留服务已移除、运行时文件校验通过（驱动仍由下次 Start 加载）")
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func createDriverService(name, sysPath string) error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := m.CreateService(name, `\??\`+sysPath, mgr.Config{
		DisplayName: name,
		StartType:   mgr.StartManual,
		ServiceType: windows.SERVICE_KERNEL_DRIVER,
	})
	if err != nil {
		return err
	}
	defer s.Close()
	return nil
}

func deleteDriverService(name string) {
	m, err := mgr.Connect()
	if err != nil {
		return
	}
	defer m.Disconnect()
	if s, err := m.OpenService(name); err == nil {
		_ = s.Delete()
		_ = s.Close()
	}
}
