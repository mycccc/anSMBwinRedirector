package model

import "fmt"

// BuildForwardFilter 生成某 Target 的 OUT filter（decisions/005 §6.3）。
//
//	outbound and ip and ip.DstAddr == <IP> and tcp.DstPort == 445
//
// 与 P0 逐字一致；端口固定 445（I7）。
func BuildForwardFilter(t Target) string {
	return fmt.Sprintf("outbound and ip and ip.DstAddr == %s and tcp.DstPort == %d",
		t.AndroidIP, WindowsMatchPort)
}

// BuildReverseFilter 生成某 Target 的 IN filter（decisions/005 §6.3）。
//
//	inbound and ip and ip.SrcAddr == <IP> and tcp.SrcPort == <AndroidPort>
//
// 端口取自 Target，**不得写死**（I8）。
// IN filter 的唯一归属同样由 Android IP 唯一保证（I1）——
// 即使两个 Target 的 AndroidPort 相同，二者也不相交。
func BuildReverseFilter(t Target) string {
	return fmt.Sprintf("inbound and ip and ip.SrcAddr == %s and tcp.SrcPort == %d",
		t.AndroidIP, t.AndroidPort)
}

// Filters 返回该 Target 的一对 filter。
func Filters(t Target) (forward, reverse string) {
	return BuildForwardFilter(t), BuildReverseFilter(t)
}
