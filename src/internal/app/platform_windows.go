//go:build windows

package app

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"golang.org/x/sys/windows"

	"ansmbwinhelper/internal/controller"
	"ansmbwinhelper/internal/diag"
	"ansmbwinhelper/internal/i18n"
	ansmbrt "ansmbwinhelper/internal/runtime"
	"ansmbwinhelper/internal/winvert"
)

// mutexName 是单实例互斥体的名字（decisions/007 §4.1）。
//
//	⛔ **不含版本号** —— 不同版本之间也必须互斥；
//	`Global\` 命名空间 ⇒ 跨会话互斥（网络拦截是**整机**行为）。
const mutexName = `Global\anSMBwinRedirector.single-instance`

// SingleInstance 持有单实例所有权。
type SingleInstance struct {
	handle windows.Handle
	// MutexAbandoned 表示**本次等待**观察到互斥体处于 abandoned 状态。
	//
	// ⚠️ 它**不代表**上一个实例异常退出（decisions/009 §13.1 的实机修正）：
	// 命名对象随最后一个句柄关闭被销毁，真实单实例场景下根本观察不到 abandoned。
	MutexAbandoned bool
}

// checkPlatform 检查平台：v1 仅支持 **Windows x64**（decisions/009 §18）。
//
// ⚠️ 必须看**本机（native）**架构，而不是进程架构：
// amd64 程序可以在 ARM64 上以仿真方式运行，但我们的 amd64 `WinDivert64.sys` 无法加载。
//
// ⚠️ 诚实标注：本检测依赖 `PROCESSOR_ARCHITEW6432` / `PROCESSOR_ARCHITECTURE`
// 两个文档化环境变量，**未在真实 ARM64 机器上验证过**（本项目没有该环境）。
func checkPlatform() error {
	native := os.Getenv("PROCESSOR_ARCHITEW6432")
	if native == "" {
		native = os.Getenv("PROCESSOR_ARCHITECTURE")
	}
	native = strings.ToUpper(strings.TrimSpace(native))
	if native == "" {
		// 拿不到就不阻断（宁可继续，也不要因为环境变量缺失而拒绝启动）。
		return nil
	}
	if native != "AMD64" {
		return diag.Errorf(diag.CodePlatformUnsupported,
			i18n.T("app.platform_arch"), native)
	}
	return nil
}

// checkAdmin 检查管理员权限（decisions/007 §4.8 P1）。
func checkAdmin() error {
	if !windows.GetCurrentProcessToken().IsElevated() {
		return diag.Errorf(diag.CodeAdminRequired, "%s", i18n.T("app.admin_required"))
	}
	return nil
}

// acquireSingleInstance 获取单实例所有权（decisions/007 §4.1）。
//
// 用命名互斥体，**不**扫描进程名/PID、**不**用锁文件、**不**自造 IPC。
//
//	WaitForSingleObject 结果的含义（decisions/009 §13.1）：
//	  WAIT_OBJECT_0  取得所有权
//	  WAIT_ABANDONED 观察到 abandoned 状态 ⇒ 接管并记一条提示（**不**判定上次是否异常退出）
//	  WAIT_TIMEOUT   另一个**存活**实例持有 ⇒ 拒绝启动
func acquireSingleInstance() (*SingleInstance, error) {
	name, err := windows.UTF16PtrFromString(mutexName)
	if err != nil {
		return nil, diag.Wrap(diag.CodeSingleInstanceConflict, err, "%s", i18n.T("app.mutex_name_failed"))
	}
	h, err := windows.CreateMutex(nil, false, name)
	// ⚠️ x/sys/windows 的 CreateMutex 在**对象已存在**时会返回 ERROR_ALREADY_EXISTS，
	// **即使句柄是有效的**（zsyscall_windows.go：`if handle == 0 || e1 == ERROR_ALREADY_EXISTS`）。
	// 因此这里**不能**把该错误当致命错误：它只表示"对象已存在"，
	// 究竟是被别人持有、还是上一个实例终止后留下的，必须由 WaitForSingleObject 判定。
	if err != nil && !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		// 真正的创建失败（例如无权创建 Global\ 对象）⇒ 明确报错，
		// ⛔ 不降级为 Local\（那会静默削弱"整机唯一"）。
		return nil, diag.Wrap(diag.CodeSingleInstanceConflict, err, "%s", i18n.T("app.mutex_create_failed"))
	}
	if h == 0 {
		return nil, diag.Errorf(diag.CodeSingleInstanceConflict, "%s", i18n.T("app.mutex_create_failed"))
	}

	res, err := windows.WaitForSingleObject(h, 0)
	if err != nil {
		_ = windows.CloseHandle(h)
		return nil, diag.Wrap(diag.CodeSingleInstanceConflict, err, "%s", i18n.T("app.mutex_wait_failed"))
	}
	switch res {
	case windows.WAIT_OBJECT_0:
		return &SingleInstance{handle: h}, nil
	case windows.WAIT_ABANDONED:
		return &SingleInstance{handle: h, MutexAbandoned: true}, nil
	default: // WAIT_TIMEOUT
		_ = windows.CloseHandle(h)
		return nil, diag.Errorf(diag.CodeSingleInstanceConflict, "%s", i18n.T("app.already_running"))
	}
}

// Release 释放单实例所有权。
//
// ⚠️ 只在**正常退出序列的最后一步**调用：全部 runtime 已"确认关闭"、最终诊断已输出之后，
// `main()` 自然返回之前。之后**不得**再执行任何语句（decisions/007 L2 的生命周期细化）。
func (s *SingleInstance) Release() {
	if s == nil || s.handle == 0 {
		return
	}
	_ = windows.ReleaseMutex(s.handle)
	_ = windows.CloseHandle(s.handle)
	s.handle = 0
}

// OSInfo 返回用于诊断展示的运行环境。
func OSInfo() string {
	return fmt.Sprintf("Windows %s %s", getenvOr("OS", "?"), NativeArch())
}

// NativeArch 返回本机（native）架构。
func NativeArch() string {
	if v := os.Getenv("PROCESSOR_ARCHITEW6432"); v != "" {
		return fmt.Sprintf(i18n.T("app.arch_native_fmt"), v, os.Getenv("PROCESSOR_ARCHITECTURE"))
	}
	return os.Getenv("PROCESSOR_ARCHITECTURE")
}

func getenvOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// newManager 构造平台 runtime manager（唯一可创建 WinDivert handle 的路径经 winvert）。
func newManager() controller.RuntimeManager {
	return ansmbrt.NewManager(ansmbrt.NewPlatformFactory())
}

// newInspector 构造**只读**的 WinDivert 使用情况检查器（decisions/007 §4.7.3）。
func newInspector() controller.Inspector { return winvert.Inspector{} }
