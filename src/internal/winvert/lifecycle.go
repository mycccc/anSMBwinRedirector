package winvert

import (
	"sync"

	"ansmbwinhelper/internal/diag"
)

// WinDivert 服务生命周期的**状态机**（decisions/011 §2.2 / §2.4）。
//
// 调用顺序（由 runtime 层保证）：
//
//	BeforeOpen()  ──►  WinDivertOpen(...)  ──►  AfterOpen()
//	  （接管：停+等消失）        （按需安装）         （验证 ImagePath）
//
//	Stop / Exit：全部 runtime 确认关闭 ──► AfterStop()（停+等消失+等 .sys 解锁）
//
// ⛔ T-e：BeforeOpen **绝不**因为"服务已存在"就跳过 —— 有服务就必须先停掉。
var (
	lifecycleMu sync.Mutex
	// verifiedSinceInstall 表示"本轮安装之后已经验证过 ImagePath"，
	// 使 AfterOpen 只需真正验证一次（每次 BeforeOpen 会把它复位）。
	verifiedSinceInstall bool
)

// BeforeOpen 在**任何 WinDivertOpen 之前**执行接管。
//
// 语义（decisions/011 §2.2）：
//
//	服务不存在          ⇒ 直接返回（让 WinDivertOpen 按需从本目录安装）
//	服务存在            ⇒ 停止 → **轮询直到服务消失** → 返回
//
// ⛔ 不存在"复用已有服务"的分支（T-e）；⛔ 旧服务未消失时不得继续 Open（T-a）；
// 接管失败返回 `WINDIVERT_TAKEOVER_FAILED`，调用方**不得** fallback（T-b）。
func BeforeOpen() error {
	lifecycleMu.Lock()
	defer lifecycleMu.Unlock()
	verifiedSinceInstall = false

	info, err := queryService()
	if err != nil {
		return diag.Wrap(diag.CodeWinDivertTakeoverFailed, err, "查询 %s 服务状态失败", ServiceName)
	}
	if !info.Exists {
		return nil
	}
	if err := removeServiceAndWait(stopService, deleteService, queryService,
		ServiceWaitTimeout, servicePollInterval); err != nil {
		return diag.Wrap(diag.CodeWinDivertTakeoverFailed, err,
			"接管 %s 服务失败（不会复用旧服务）", ServiceName)
	}
	return nil
}

// AfterOpen 在**首个 handle 成功打开之后**执行：验证服务 ImagePath（T-c）。
//
// 幂等：本轮只真正验证一次。
func AfterOpen() error {
	lifecycleMu.Lock()
	defer lifecycleMu.Unlock()
	if verifiedSinceInstall {
		return nil
	}
	dir, err := WinDivertDir()
	if err != nil {
		return err
	}
	info, err := queryService()
	if err != nil {
		return diag.Wrap(diag.CodeWinDivertTakeoverFailed, err, "验证 %s 服务状态失败", ServiceName)
	}
	if !info.Exists {
		return diag.Errorf(diag.CodeWinDivertTakeoverFailed,
			"%s 服务在打开 handle 之后仍不存在（无法确认加载的是本程序的 %s）", ServiceName, SysName)
	}
	if !imagePathMatches(info.ImagePath, dir) {
		return diag.Errorf(diag.CodeWinDivertTakeoverFailed,
			"%s 服务实际加载的不是本程序目录的 %s：ImagePath=%s，期望目录=%s",
			ServiceName, SysName, info.ImagePath, dir)
	}
	verifiedSinceInstall = true
	return nil
}

// AfterStop 在**全部 Target runtime 确认关闭之后**执行：停止并删除服务，并等待 `.sys` 解锁。
//
// 顺序（decisions/011 §2.4）：
//
//	停止服务 → 轮询直到服务消失（1060）→ 确认 .sys 解锁
//
// ⚠️ 调用方**必须**先完成"runtime 已停止"（007 L4），否则会与 handle 关闭竞态。
func AfterStop() error {
	lifecycleMu.Lock()
	defer lifecycleMu.Unlock()
	verifiedSinceInstall = false

	info, err := queryService()
	if err != nil {
		return diag.Wrap(diag.CodeWinDivertTakeoverFailed, err, "查询 %s 服务状态失败", ServiceName)
	}
	if info.Exists {
		// 同 BeforeOpen：stop 不保证注册项消失，必须显式删除
		// （decisions/011 §2.4 的"停止并删除 WinDivert service"）。
		if err := removeServiceAndWait(stopService, deleteService, queryService,
			ServiceWaitTimeout, servicePollInterval); err != nil {
			return diag.Wrap(diag.CodeWinDivertTakeoverFailed, err, "移除 %s 服务失败", ServiceName)
		}
	}
	dir, derr := WinDivertDir()
	if derr != nil {
		return derr
	}
	if err := waitFileUnlocked(dir+`\`+SysName, fileUnlockTimeout, servicePollInterval, fileOpenable); err != nil {
		return diag.Wrap(diag.CodeWinDivertTakeoverFailed, err,
			"%s 仍被占用（驱动可能未真正卸载）", SysName)
	}
	return nil
}

// QueryServiceForDiagnostics 返回服务快照（**只读**，供 Debug 窗口与诊断使用）。
func QueryServiceForDiagnostics() (ServiceInfo, error) { return queryService() }

// --- 修复 / 重新接管（decisions/011 §2.7）----------------------------------

// Repairer 实现 controller.Repairer：WinDivert 的**修复 / 重新接管**。
//
// ⛔ 它**不是**"裸卸载"：走的正是 Start 时同一套接管流程（BeforeOpen 的 stop→delete→等 1060），
// 并且**只清理服务注册 + 重新校验当前目录的运行时文件**。
type Repairer struct{}

// Repair 执行修复（decisions/011 §2.7）。
//
// 诚实说明（⛔ 不得含糊）：
//
//	驱动的"安装"是由后续 Start 的 `WinDivertOpen` 完成的（decisions/011 §2.2）。
//	本函数做的是：
//	  ① 复位 `Prepare()` 的成功缓存（用户可能刚替换了同目录的 WinDivert 文件）；
//	  ② 移除**已存在**的 WinDivert 服务注册（stop → delete → 轮询直到 1060）；
//	  ③ 重新校验当前目录两个运行时文件的 SHA-256。
//
// ⛔ 不加载驱动、⛔ 不修改任何文件、⛔ 不提供 Force。
func (Repairer) Repair() error {
	InvalidatePrepare()

	// ① 接管：与 Start 前置**完全相同**的代码路径（T-e：有服务就必须先停掉）。
	if err := BeforeOpen(); err != nil {
		return err
	}
	// ② 文件校验（并可顺手把 DLL 装载起来，等价于一次 Prepare）。
	if _, err := VerifyWinDivertFiles(); err != nil {
		return err
	}
	if err := Prepare(); err != nil {
		return err
	}
	return nil
}
