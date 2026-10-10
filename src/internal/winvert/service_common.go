// Package winvert 的服务生命周期部分（decisions/011）。
//
// 本文件只放**跨平台可测**的核心：轮询等待、ImagePath 归一化比较、以及生命周期状态机。
// 真正的 SCM 调用在 service_windows.go。
//
// 硬约束（decisions/011 §2.2）：
//
//	⛔ T-e：**不存在**"发现已有 WinDivert 服务 ⇒ 直接复用"的捷径。
//	首次 Start 必须**接管**：停掉已有服务 → 等它真正消失（1060）→ 由本程序目录的 .sys 重新安装。
//	⛔ T-a：旧服务未消失时**不得** WinDivertOpen()。
//	⛔ T-b：接管失败必须显式失败，**不得** fallback 到旧服务。
//	⛔ T-c：安装后必须**验证 ImagePath** 指向当前程序目录。
//
// ⚠️ 等待必须是**状态轮询 + 超时**（T2），⛔ 不得实现成 "sleep 一下就当已经好了"。
package winvert

import (
	"fmt"
	"strings"
	"time"
)

// ServiceName 是 WinDivert 的服务名（固定值，不可配置，也不本地化）。
const ServiceName = "WinDivert"

// 轮询参数。
//
// ⚠️ decisions/011 **T2**：具体取值属于**实现参数**，由实现按实测选定（产品层只要求
// "轮询 + 超时才失败"）。这里是 2026-10-08 在本项目测试机上的选定值。
const (
	servicePollInterval = 100 * time.Millisecond
	// ServiceWaitTimeout 是"等待服务消失"的上限（Stop/Exit 与接管共用）。
	ServiceWaitTimeout = 5 * time.Second
	// fileUnlockTimeout 是"等待 .sys 解锁"的上限。
	fileUnlockTimeout = 3 * time.Second
)

// ServiceInfo 是 WinDivert 服务的只读快照。
type ServiceInfo struct {
	// Exists 报告服务是否已注册。
	Exists bool
	// State 是人类可读的当前状态（"RUNNING" / "STOPPED" / …）；Exists=false 时为空。
	State string
	// ImagePath 是服务注册的二进制路径（SCM 原始形式，通常带 `\??\` 前缀）。
	ImagePath string
}

// removeServiceAndWait 是"停掉并移除 WinDivert 服务"的**纯编排**（可单测）。
//
// 顺序（decisions/011 §2.2 / §2.4）：
//
//	stop（尽力）→ **delete（显式清掉注册项）** → 轮询直到 1060
//
// ⚠️ **delete 这一步不能省**（2026-10-08 真机测试发现的缺陷）：
// `sc stop` 只在服务**确实被启动过**时才让注册项随驱动卸载自动消失；
// 一个**遗留但从未启动**的注册项在 stop 之后仍然存在 ⇒ 只靠轮询等不到 1060。
//
// ⛔ 仍然**不是** sleep-then-assume：最后一步是状态轮询 + 超时。
func removeServiceAndWait(
	stop, del func() error,
	query func() (ServiceInfo, error),
	timeout, interval time.Duration,
) error {
	if stop != nil {
		if err := stop(); err != nil {
			return fmt.Errorf("停止 %s 服务失败: %w", ServiceName, err)
		}
	}
	if del != nil {
		if err := del(); err != nil {
			return fmt.Errorf("删除 %s 服务注册项失败: %w", ServiceName, err)
		}
	}
	return waitServiceGone(query, timeout, interval)
}

// waitServiceGone 轮询直到 query() 报告"服务已不存在"。
//
// 契约（decisions/011 §2.3）：
//   - **轮询 + 超时**，⛔ 不是 sleep-then-assume；
//   - `sc stop` 之后 SCM 可能短暂处于"已标记为删除"（1072），此时查询仍可能看到服务 ⇒
//     继续轮询即可，**不当作错误**；
//   - 超时 ⇒ 返回错误（由调用方映射为 WINDIVERT_TAKEOVER_FAILED）。
//
// 抽成这个形状是为了**可在任意平台上单测**（不需要真 SCM）。
func waitServiceGone(query func() (ServiceInfo, error), timeout, interval time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		info, err := query()
		if err != nil {
			return fmt.Errorf("查询 %s 服务状态失败: %w", ServiceName, err)
		}
		if !info.Exists {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("等待 %s 服务消失超时（%.1fs，最后状态=%s）",
				ServiceName, timeout.Seconds(), info.State)
		}
		time.Sleep(interval)
	}
}

// normalizeImagePath 归一化 SCM 的 ImagePath，便于比较。
//
// ⚠️ 故意**不用 filepath**：本函数的语义是"Windows 驱动路径"，
// 必须与宿主平台无关（否则在 macOS/Linux 上跑单测时行为不同）。
// 归一化项：去首尾空白、去 `\??\` 前缀、统一分隔符为正斜杠→反斜杠、统一小写。
func normalizeImagePath(p string) string {
	p = strings.TrimSpace(p)
	p = strings.TrimPrefix(p, `\??\`)
	p = strings.ReplaceAll(p, "/", `\`)
	return strings.ToLower(p)
}

// imagePathMatches 判定服务注册的 ImagePath 是否就是 dir 下的 SysName。
//
// ⚠️ 这是 decisions/011 **T-c** 的判据：安装后必须确认实际加载的是**当前程序目录**的 .sys。
func imagePathMatches(imagePath, dir string) bool {
	if imagePath == "" || dir == "" {
		return false
	}
	want := normalizeImagePath(strings.TrimRight(dir, `\/`) + `\` + SysName)
	return normalizeImagePath(imagePath) == want
}

// waitFileUnlocked 轮询直到文件不再被占用（能按"独占、只读、不共享"打开）。
//
// 用途：Stop/Exit 之后确认 `.sys` **已解锁**（decisions/011 §2.4）。
// 同样是**轮询 + 超时**，⛔ 不是 sleep-then-assume。
func waitFileUnlocked(path string, timeout, interval time.Duration, canOpen func(string) bool) error {
	deadline := time.Now().Add(timeout)
	for {
		if canOpen(path) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("等待 %s 解锁超时（%.1fs）", path, timeout.Seconds())
		}
		time.Sleep(interval)
	}
}
