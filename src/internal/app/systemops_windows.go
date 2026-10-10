//go:build windows

package app

import (
	"context"
	"fmt"
	"os/exec"
	"syscall"
	"time"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"

	"ansmbwinhelper/internal/controller"
	"ansmbwinhelper/internal/winvert"
)

// --- 调试窗口的系统操作（decisions/011 §2.7 / 008 G2 的改写版）--------------
//
// ⛔ GUI 不得直接执行系统命令；这两个实现由 `controller` 层调用。

const (
	// workstationService 是 Windows 的 Workstation 服务名（内部标识，不本地化）。
	workstationService = "LanmanWorkstation"
	// 单条 `sc` 命令的超时。
	workstationCmdTimeout = 30 * time.Second
	// 等待服务进入目标状态的上限（停止 / 启动各一份预算）。
	workstationWaitTimeout = 30 * time.Second
	// 状态轮询间隔（与 winvert 的做法一致：轮询而不是 sleep-then-assume）。
	workstationPollInterval = 200 * time.Millisecond
)

// newRepairer 返回 Windows 上的 WinDivert 修复实现（decisions/011 §2.7）。
func newRepairer() controller.Repairer { return winvert.Repairer{} }

// newSystemOps 返回 Windows 上的系统服务操作实现。
func newSystemOps() controller.SystemOps { return systemOps{} }

type systemOps struct{}

// RestartWorkstation 重启 Windows Workstation（LanmanWorkstation）服务。
//
// ⚠️ 用途（⛔ 必须如实理解）：它是**清理 Windows SMB 客户端会话状态**，
// 不是"修复网络"，也**不保证**解决连接问题（实机已证明）。
//
// 实现要点（⛔ 不轻信命令退出码，以**状态轮询**为准 —— 与 011 的接管流程同一原则）：
//
//	① `sc stop`  → 轮询直到 **Stopped**（超时 ⇒ 明确报错：可能被依存服务占用）
//	② `sc start` → 轮询直到 **Running**（超时 ⇒ 明确报错）
//
// ⛔ 命令是固化的（无 shell、无字符串拼接）⇒ 无注入面。
func (systemOps) RestartWorkstation() error {
	// ⚠️ `sc stop` 的退出码**不作为判定依据**（"服务本已停止"等也会返回非零）；
	//    真正的判定是下面的状态轮询。
	_ = runSC("stop", workstationService)
	if err := waitWorkstationState(svc.Stopped); err != nil {
		return fmt.Errorf("无法停止 %s（可能被依存服务占用）：%w", workstationService, err)
	}
	_ = runSC("start", workstationService)
	if err := waitWorkstationState(svc.Running); err != nil {
		return fmt.Errorf("无法启动 %s：%w", workstationService, err)
	}
	return nil
}

// runSC 以**隐藏窗口**执行一条 `sc` 命令。
func runSC(args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), workstationCmdTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sc", args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return cmd.Run()
}

// waitWorkstationState 轮询直到服务进入期望状态（上限 workstationWaitTimeout）。
func waitWorkstationState(want svc.State) error {
	deadline := time.Now().Add(workstationWaitTimeout)
	last := "unknown"
	for {
		if st, err := queryWorkstationState(); err == nil {
			last = stateText(st)
			if st == want {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("超时（最后状态=%s，期望=%s）", last, stateText(want))
		}
		time.Sleep(workstationPollInterval)
	}
}

func queryWorkstationState() (svc.State, error) {
	m, err := mgr.Connect()
	if err != nil {
		return 0, err
	}
	defer m.Disconnect()
	s, err := m.OpenService(workstationService)
	if err != nil {
		return 0, err
	}
	defer s.Close()
	st, err := s.Query()
	if err != nil {
		return 0, err
	}
	return st.State, nil
}

func stateText(s svc.State) string {
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
