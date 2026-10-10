//go:build windows

package winvert

import (
	"errors"
	"syscall"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// SCM 错误码（用常量而不是字符串/本地化输出）。
const (
	errnoServiceDoesNotExist    syscall.Errno = 1060
	errnoServiceMarkedForDelete syscall.Errno = 1072
)

// queryService 读取 WinDivert 服务的当前状态（**只读**）。
func queryService() (ServiceInfo, error) {
	m, err := mgr.Connect()
	if err != nil {
		return ServiceInfo{}, err
	}
	defer m.Disconnect()

	s, err := m.OpenService(ServiceName)
	if err != nil {
		if errors.Is(err, errnoServiceDoesNotExist) {
			return ServiceInfo{}, nil // 未安装：正常状态
		}
		return ServiceInfo{}, err
	}
	defer s.Close()

	st, err := s.Query()
	if err != nil {
		return ServiceInfo{}, err
	}
	info := ServiceInfo{Exists: true, State: stateString(st.State)}
	if cfg, cerr := s.Config(); cerr == nil {
		info.ImagePath = cfg.BinaryPathName
	}
	return info, nil
}

// stopService 请求停止 WinDivert 服务。
//
// ⚠️ 实测（手测报告附录 C）：`sc stop` 会让服务**连同注册项一起消失**（1060），
// 因此本函数只负责"发出停止请求"；"服务是否真的消失"由 waitServiceGone 轮询确认。
//
// ⚠️ stop 之后 SCM 可能短暂处于"已标记为删除"（1072）：这属于**内部竞态**，
// 这里视为"停止请求已生效"，继续交给轮询（decisions/011 §2.3）。
func stopService() error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()

	s, err := m.OpenService(ServiceName)
	if err != nil {
		if errors.Is(err, errnoServiceDoesNotExist) {
			return nil
		}
		return err
	}
	defer s.Close()

	if st, qerr := s.Query(); qerr == nil && st.State == svc.Stopped {
		return nil // 已经是停止态
	}
	if _, err := s.Control(svc.Stop); err != nil {
		if errors.Is(err, errnoServiceMarkedForDelete) {
			return nil
		}
		return err
	}
	return nil
}

// deleteService 删除 WinDivert 服务的**注册项**。
//
// ⚠️ 为什么必须显式删除（2026-10-08 Windows 实机测试发现）：
// `sc stop` 只在服务**真的被启动过**时才会让注册项随驱动卸载自动消失
// （WinDivert 的驱动在 unload 时会自删注册项）。而一个**遗留但从未启动**的注册项
// （例如旧版本/别的工具留下的）在 `stop` 之后**仍然存在** ⇒ 只靠轮询等不到 1060。
//
// 因此接管流程是：stop（尽力）→ **delete**（显式清掉注册项）→ 轮询直到 1060。
//
// 1060（不存在）与 1072（已标记为删除）都视为"已达成目的"。
func deleteService() error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()

	s, err := m.OpenService(ServiceName)
	if err != nil {
		if errors.Is(err, errnoServiceDoesNotExist) || errors.Is(err, errnoServiceMarkedForDelete) {
			return nil
		}
		return err
	}
	defer s.Close()

	if err := s.Delete(); err != nil {
		if errors.Is(err, errnoServiceDoesNotExist) || errors.Is(err, errnoServiceMarkedForDelete) {
			return nil
		}
		return err
	}
	return nil
}

// fileOpenable 报告文件能否以"独占（不共享）"方式打开 —— 用于判断 `.sys` 是否已解锁。
//
// ⚠️ 必须用 sharemode=0：Go 的 os.OpenFile 默认允许共享，**检测不出**驱动占用。
func fileOpenable(path string) bool {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return false
	}
	h, err := windows.CreateFile(p,
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		0, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return false
	}
	_ = windows.CloseHandle(h)
	return true
}

// stateString 把 svc.State 转成稳定的人类可读串（⛔ 不本地化）。
func stateString(s svc.State) string {
	switch s {
	case svc.Stopped:
		return "STOPPED"
	case svc.StartPending:
		return "START_PENDING"
	case svc.StopPending:
		return "STOP_PENDING"
	case svc.Running:
		return "RUNNING"
	case svc.ContinuePending:
		return "CONTINUE_PENDING"
	case svc.PausePending:
		return "PAUSE_PENDING"
	case svc.Paused:
		return "PAUSED"
	default:
		return "UNKNOWN"
	}
}
