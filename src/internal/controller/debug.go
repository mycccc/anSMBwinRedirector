package controller

import (
	"ansmbwinhelper/internal/diag"
	"ansmbwinhelper/internal/snapshot"
)

// --- 调试窗口的动作（decisions/011 §2.7）------------------------------------
//
// ⚠️ 两者都**经本层**执行（decisions/008 **G2** 的改写版）：
// GUI ⛔ 不得自己调用 `sc` / SCM，⛔ 不得绕过 `internal/winvert`。

// Repairer 执行 WinDivert 的**修复 / 重新接管**；由 `winvert.Repairer` 实现。
//
// ⛔ controller **不直接 import winvert** —— 保持"唯一 WinDivert 适配层"的边界。
type Repairer interface {
	Repair() error
}

// SystemOps 是 Windows 系统服务操作（decisions/011 §2.7 的「重启 Workstation」）。
//
// ⛔ 由平台层实现（固定命令、无 shell 注入面）；GUI 不得直接执行系统命令。
type SystemOps interface {
	// RestartWorkstation 重启 Windows Workstation（LanmanWorkstation）服务。
	RestartWorkstation() error
}

// RepairWinDivert 执行 WinDivert 的**修复 / 重新接管**（decisions/011 §2.7）。
//
// 语义（⛔ 必须准确，不得含糊）：
//
//	它走的正是 Start 前置的**同一套接管流程**（stop → delete → 轮询直到 1060）
//	+ 重新校验当前目录的运行时文件（含 SHA-256）。
//	驱动真正的"安装"仍由后续 Start 的 `WinDivertOpen` 完成。
//
// ⛔ 要求当前**不是 Running**：有活动 handle 时无法安全移除服务。
// ⛔ **不是裸卸载**；⛔ 不提供 Force。
func (c *Controller) RepairWinDivert() error {
	if c.opts.Repairer == nil {
		return diag.Errorf(diag.CodePlatformUnsupported, "%s", "当前平台不支持 WinDivert 修复")
	}
	if err := c.begin(snapshot.OpDebug); err != nil {
		return err
	}
	defer c.end()

	if c.isRunning() {
		detail := "重定向正在运行，请先停止再修复"
		c.recordError(diag.New(diag.CodeRepairRequiresStop, diag.SrcDebug, detail, ""))
		return diag.Errorf(diag.CodeRepairRequiresStop, "%s", detail)
	}

	if err := c.opts.Repairer.Repair(); err != nil {
		// ⚠️ 保证错误**一定携带 Code**（诊断条目与用户可见摘要都依赖它）：
		// 平台实现通常已带码；这里对"无码错误"补一层，避免退化成 INTERNAL_ERROR。
		if _, ok := diag.CodeOf(err); !ok {
			err = diag.Wrap(diag.CodeWinDivertTakeoverFailed, err, "%s", err.Error())
		}
		c.recordError(diag.New(diag.CodeWinDivertTakeoverFailed, diag.SrcDebug, err.Error(), ""))
		return err
	}

	// 修复后刷新诊断（让调试窗口立刻看到新的服务/文件状态）。
	info := c.opts.Inspector.Inspect()
	c.mu.Lock()
	c.wd = info
	c.ring.Info(diag.SrcDebug, "已执行 WinDivert 修复 / 重新接管")
	c.publishLocked()
	c.mu.Unlock()
	return nil
}

// RestartWorkstation 重启 Windows Workstation 服务（decisions/011 §2.7）。
//
// ⚠️ 用途必须说清（实机已证明）：它是**清理 Windows SMB 客户端会话状态**，
// ⛔ 不是"修复网络"，也**不保证**解决连接问题。可能中断当前 SMB 连接。
func (c *Controller) RestartWorkstation() error {
	if c.opts.SystemOps == nil {
		return diag.Errorf(diag.CodePlatformUnsupported, "%s", "当前平台不支持重启 Workstation 服务")
	}
	if err := c.begin(snapshot.OpDebug); err != nil {
		return err
	}
	defer c.end()

	if err := c.opts.SystemOps.RestartWorkstation(); err != nil {
		c.recordError(diag.New(diag.CodeWorkstationRestartFailed, diag.SrcDebug, err.Error(), ""))
		return diag.Errorf(diag.CodeWorkstationRestartFailed, "重启 Workstation 服务失败：%s", err.Error())
	}
	c.mu.Lock()
	c.ring.Info(diag.SrcDebug, "已重启 Windows Workstation 服务（清理 SMB 客户端会话）")
	c.publishLocked()
	c.mu.Unlock()
	return nil
}

// isRunning 报告全局模式是否为 Running（修复的前置条件）。
func (c *Controller) isRunning() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.mode == snapshot.ModeRunning
}
