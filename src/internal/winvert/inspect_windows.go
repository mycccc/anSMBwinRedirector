//go:build windows

package winvert

import (
	"strings"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"

	"ansmbwinhelper/internal/diag"
)

// Inspector 实现 controller.Inspector：**只读**的 WinDivert 使用情况检查。
//
// ⛔⛔ **硬边界（decisions/011 §9.3 的 D-1 修复，2026-10-09）**：
// 本文件**不得**出现任何 `divert.Open(...)`（或其它可能隐式安装/加载驱动的 API）。
//
//	实测（测试机，可复现）：删除服务后仅调用一次 Inspect，`sc query WinDivert`
//	就从 **1060（未安装）** 变成 **RUNNING** —— 即"只读检查"会把驱动装上并加载，
//	违反 007 §4.8 / T1-B。因此只读路径改为**纯 SCM + 文件系统 + 版本资源**：
//
//	  ✅ SCM 查询（服务存在性 / 状态 / ImagePath / 归属）
//	  ✅ 文件系统查询（DLL/SYS 存在性）
//	  ✅ SHA-256 比对（与编译期期望常量）
//	  ✅ 文件版本资源（静态元数据，见 version_windows.go）
//	  ⛔ divert.Open / REFLECT 句柄（会隐式安装+加载驱动）
//	  ⛔ 运行时驱动版本（只能靠 REFLECT 得到 ⇒ v1 只读路径**不查**，显示"未查询"）
//	  ⛔ 可观察使用者枚举（同上）
//
// 驱动的**安装与加载只允许**发生在明确的 `Start` / 已批准的生命周期操作里。
type Inspector struct{}

// Inspect 采集 WinDivert 诊断信息（decisions/009 §10）。
//
// ⛔ 全程**不打开驱动句柄**（见上）。
func (Inspector) Inspect() diag.WinDivertInfo {
	info := diag.WinDivertInfo{Available: true}
	info.WinDivertDir = DescribeWinDivertFiles()

	// 只读检查同样需要装载 WinDivert **用户态 DLL**（Prepare 只 LoadLibrary，不加载驱动）。
	// ⚠️ 若 DLL 缺失/哈希不符，仍要把"服务/文件"的事实报告出来，而不是只报"不可用"。
	if err := Prepare(); err != nil {
		info.Err = err.Error()
	}
	ok, verr := VerifyWinDivertFiles()
	info.RuntimeFilesOK = ok
	if !ok && verr != nil && info.Err == "" {
		info.Err = verr.Error()
	}
	// 逐项校验结果（decisions/011 §2.7：路径 + 实际/期望 SHA-256 + 文件版本）。
	info.Files = RuntimeFileInfos()

	// SCM 查询（不加载驱动）。
	collectService(&info)

	// ⛔ 不查询运行时驱动版本 / 不枚举可观察使用者（都需要 REFLECT 句柄）。
	info.ConsumersQueried = false
	return info
}

// collectService 查询 WinDivert 服务是否存在、状态、ImagePath 是否属于本程序。
func collectService(info *diag.WinDivertInfo) {
	m, err := mgr.Connect()
	if err != nil {
		return
	}
	defer m.Disconnect()

	s, err := m.OpenService("WinDivert")
	if err != nil {
		return // 服务不存在（驱动未安装）
	}
	defer s.Close()
	info.ServiceExists = true

	if cfg, err := s.Config(); err == nil {
		info.ServiceImagePath = cfg.BinaryPathName
		info.ImagePathIsOurs = imagePathIsOurs(cfg.BinaryPathName)
	}
	if st, err := s.Query(); err == nil {
		info.DriverState = stateName(st.State)
	}
}

// imagePathIsOurs 判定服务 ImagePath 是否指向**本程序自己的 WinDivert 文件目录**
// （= 可执行文件所在目录；decisions/007 §4.7.3 的检查①，位置口径见 decisions/010 §D）。
func imagePathIsOurs(p string) bool {
	dir, err := WinDivertDir()
	if err != nil || p == "" {
		return false
	}
	return strings.Contains(strings.ToLower(p), strings.ToLower(dir))
}

func stateName(s svc.State) string {
	switch s {
	case svc.Stopped:
		return "Stopped"
	case svc.StartPending:
		return "StartPending"
	case svc.StopPending:
		return "StopPending"
	case svc.Running:
		return "Running"
	default:
		return "Unknown"
	}
}
