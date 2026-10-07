// Package model 定义 v1 的 Target / Flow / filter / 报文改写模型。
//
// 严格对应 decisions/005：
//   - Target = ID + AndroidIP + AndroidPort（Windows 侧匹配目标为派生值 AndroidIP:445）
//   - FlowTable 是 **Target 作用域** 的 "reverse-rewrite existence table"（不是 NAT、不是连接跟踪）
//   - filter 每 Target 一对，极简，不含任何超出该 Target 的匹配
//
// 本包是纯逻辑，不依赖 Windows / WinDivert，可在任意平台单测。
package model

import (
	"errors"
	"fmt"
	"net/netip"
)

const (
	// WindowsMatchPort 是 Windows 侧固定的目的端口（decisions/005 I7）。
	// 它是**全局固定项**，不可配置。
	WindowsMatchPort uint16 = 445

	// DefaultAndroidPort 只作为"新建 Target 时的默认值"（decisions/005 I8）。
	// ⛔ 匹配/改写逻辑中不得出现写死的 4450。
	DefaultAndroidPort uint16 = 4450
)

// Target 是一条规则（decisions/005 §6.1 / decisions/003 §3.1）。
//
// ID 是身份：创建时生成一次、不可编辑、持久化、**跨 IP/端口编辑保持不变**。
type Target struct {
	ID          string
	AndroidIP   netip.Addr
	AndroidPort uint16
}

// WindowsMatchIP 是**派生值**：Windows 侧匹配目标 = AndroidIP:445。
// 该值不存储、不可编辑（decisions/003 §3.1）。
func (t Target) WindowsMatchIP() netip.Addr { return t.AndroidIP }

// String 用于日志与诊断输出。
func (t Target) String() string {
	return fmt.Sprintf("%s (%s:%d -> :%d)", t.ID, t.AndroidIP, t.AndroidPort, WindowsMatchPort)
}

var (
	// ErrIDRequired 表示 Target 缺少稳定 ID。
	ErrIDRequired = errors.New("目标缺少稳定 ID")

	// ErrAddrInvalid 表示 AndroidIP 不是合法 IPv4 unicast。
	ErrAddrInvalid = errors.New("Android IP 必须是合法 IPv4 单播地址")

	// ErrAddrNotEndpoint 表示地址属于明显不能作为 Target endpoint 的保留/特殊地址。
	ErrAddrNotEndpoint = errors.New("Android IP 不能是未指定 / 广播 / 组播 / 保留地址")

	// ErrPortOutOfRange 表示 AndroidPort 不在 1..65535。
	ErrPortOutOfRange = errors.New("Android SMB 端口必须在 1..65535")

	// ErrDuplicateIP 表示规则集合内 Android IP 重复（decisions/005 I1）。
	ErrDuplicateIP = errors.New("该 Android IP 已存在目标规则")

	// ErrDuplicateID 表示稳定 ID 重复（decisions/006 §6.6）。
	ErrDuplicateID = errors.New("目标 ID 重复")
)

// reservedHigh 是 240.0.0.0/4（reserved），不可能是真实 Target。
var reservedHigh = netip.MustParseAddr("240.0.0.0")

// broadcast4 是 255.255.255.255。
var broadcast4 = netip.MustParseAddr("255.255.255.255")

// ValidateTarget 校验单个 Target 的字段（decisions/005 §6.1）。
//
// 口径（**不自造长黑名单**）：
//   - 必须是合法 IPv4 unicast
//   - 拒绝：未指定(0.0.0.0)、广播(255.255.255.255)、组播(224.0.0.0/4)、保留(240.0.0.0/4)
//   - **允许 loopback**（仅用于本机/VM 测试，不代表典型 Android LAN Target）
//   - AndroidPort ∈ [1,65535]；**445 允许**（实现层按 no-op 处理，decisions/005 §6.1）
func ValidateTarget(t Target) error {
	if t.ID == "" {
		return ErrIDRequired
	}
	if err := validateAndroidIP(t.AndroidIP); err != nil {
		return err
	}
	if t.AndroidPort == 0 {
		return fmt.Errorf("%w: 0", ErrPortOutOfRange)
	}
	return nil
}

func validateAndroidIP(a netip.Addr) error {
	if !a.IsValid() || !a.Is4() {
		return fmt.Errorf("%w: %q", ErrAddrInvalid, a.String())
	}
	switch {
	case a.IsUnspecified(), a == broadcast4, a.IsMulticast():
		return fmt.Errorf("%w: %s", ErrAddrNotEndpoint, a)
	}
	if reservedHigh.Compare(a) <= 0 && a.Compare(netip.MustParseAddr("255.255.255.255")) < 0 {
		return fmt.Errorf("%w: %s (240.0.0.0/4 保留)", ErrAddrNotEndpoint, a)
	}
	return nil
}

// ValidateTargets 校验整个规则集合。
//
// 强制两条不变式（decisions/005 §6.4 / §6.3）：
//   - I1：Android IP 在 active Target 集合中**唯一**
//     （重复 IP 会让两条 OUT filter 完全相同 ⇒ 同一包被两个 handle 各改写一次）
//   - ID 唯一（decisions/006 §6.6）
func ValidateTargets(ts []Target) error {
	seenIP := make(map[netip.Addr]string, len(ts))
	seenID := make(map[string]struct{}, len(ts))
	for _, t := range ts {
		if err := ValidateTarget(t); err != nil {
			return fmt.Errorf("%s: %w", t.ID, err)
		}
		if prev, dup := seenIP[t.AndroidIP]; dup {
			return fmt.Errorf("%w: %s（已由 %s 使用）", ErrDuplicateIP, t.AndroidIP, prev)
		}
		seenIP[t.AndroidIP] = t.ID
		if _, dup := seenID[t.ID]; dup {
			return fmt.Errorf("%w: %s", ErrDuplicateID, t.ID)
		}
		seenID[t.ID] = struct{}{}
	}
	return nil
}

// FindByIP 返回 Android IP 对应的 Target（decisions/005 I1 保证至多一个）。
func FindByIP(ts []Target, ip netip.Addr) (Target, bool) {
	for _, t := range ts {
		if t.AndroidIP == ip {
			return t, true
		}
	}
	return Target{}, false
}

// CollectViolations 返回**全部**违规项（而不是只报第一条）。
//
// 供配置层使用：decisions/006 §6.10 要求"列出全部违规项"，便于用户一次修完。
func CollectViolations(ts []Target) []string {
	var out []string
	seenIP := make(map[netip.Addr]string, len(ts))
	seenID := make(map[string]struct{}, len(ts))
	for i, t := range ts {
		where := fmt.Sprintf("targets[%d]", i)
		if err := ValidateTarget(t); err != nil {
			out = append(out, fmt.Sprintf("%s: %v", where, err))
			continue
		}
		if prev, dup := seenIP[t.AndroidIP]; dup {
			out = append(out, fmt.Sprintf("%s: %v: %s（已由 %s 使用）", where, ErrDuplicateIP, t.AndroidIP, prev))
			continue
		}
		if _, dup := seenID[t.ID]; dup {
			out = append(out, fmt.Sprintf("%s: %v: %s", where, ErrDuplicateID, t.ID))
			continue
		}
		seenIP[t.AndroidIP] = t.ID
		seenID[t.ID] = struct{}{}
	}
	return out
}
