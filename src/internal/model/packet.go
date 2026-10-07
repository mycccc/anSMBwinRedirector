package model

import (
	"encoding/binary"
	"net/netip"
)

// Direction 表示报文方向。
type Direction int

const (
	// Outbound：Windows → Android，改写**目的端口** 445 → AndroidPort。
	Outbound Direction = iota
	// Inbound：Android → Windows，还原**源端口** AndroidPort → 445。
	Inbound
)

const (
	ipv4MinHeaderLen = 20
	tcpMinHeaderLen  = 20
	protoTCP         = 6
)

// TCPv4 是从报文中解析出的最小信息集（只读，不含载荷）。
type TCPv4 struct {
	SrcIP   netip.Addr
	DstIP   netip.Addr
	SrcPort uint16
	DstPort uint16
	FIN     bool
	SYN     bool
	RST     bool
}

// tcpOffset 返回 TCP 头相对报文起始的偏移与 TCP 段长度。
func tcpOffset(b []byte) (ihl int, tcpLen int, ok bool) {
	if len(b) < ipv4MinHeaderLen {
		return 0, 0, false
	}
	if b[0]>>4 != 4 { // 非 IPv4
		return 0, 0, false
	}
	ihl = int(b[0]&0x0f) * 4
	if ihl < ipv4MinHeaderLen || len(b) < ihl+tcpMinHeaderLen {
		return 0, 0, false
	}
	// 非首片（fragment offset != 0）没有完整 TCP 头，不处理。
	if binary.BigEndian.Uint16(b[6:8])&0x1fff != 0 {
		return 0, 0, false
	}
	if b[9] != protoTCP { // 非 TCP
		return 0, 0, false
	}
	totalLen := int(binary.BigEndian.Uint16(b[2:4]))
	if totalLen < ihl+tcpMinHeaderLen || totalLen > len(b) {
		// 头声明的长度不可能覆盖一个完整 TCP 头，或超出实际缓冲区（截断报文）：
		// 只改写我们能完整核算的报文，其余一律原样放行。
		return 0, 0, false
	}
	return ihl, totalLen - ihl, true
}

// ParseTCPv4 解析 IPv4+TCP 报文的最小信息集。
// 不支持的结构（非 IPv4 / 非 TCP / 非首片 / 头不完整）返回 ok=false，调用方应原样放行。
func ParseTCPv4(b []byte) (p TCPv4, ok bool) {
	ihl, _, ok := tcpOffset(b)
	if !ok {
		return TCPv4{}, false
	}
	var src4, dst4 [4]byte
	copy(src4[:], b[12:16])
	copy(dst4[:], b[16:20])
	tcp := b[ihl:]
	return TCPv4{
		SrcIP:   netip.AddrFrom4(src4),
		DstIP:   netip.AddrFrom4(dst4),
		SrcPort: binary.BigEndian.Uint16(tcp[0:2]),
		DstPort: binary.BigEndian.Uint16(tcp[2:4]),
		FIN:     tcp[13]&0x01 != 0,
		SYN:     tcp[13]&0x02 != 0,
		RST:     tcp[13]&0x04 != 0,
	}, true
}

// RewritePort 按方向把端口改写为 newPort。
//
//   - Outbound：改**目的端口**（445 → AndroidPort）
//   - Inbound：改**源端口**（AndroidPort → 445）
//
// 只改 2 字节；**不改 IP、不改另一侧端口、不动载荷**（decisions/005 I9）；
// TCP 校验和按 RFC 1624 增量修正（IP 头校验和不受影响）。
//
// 返回改写前的解析结果；ok=false 表示报文结构不支持，调用方应原样放行。
func RewritePort(b []byte, dir Direction, newPort uint16) (before TCPv4, ok bool) {
	before, ok = ParseTCPv4(b)
	if !ok {
		return TCPv4{}, false
	}
	ihl, _, _ := tcpOffset(b)
	tcp := b[ihl:]

	var off int
	var oldPort uint16
	switch dir {
	case Outbound:
		off, oldPort = 2, before.DstPort
	case Inbound:
		off, oldPort = 0, before.SrcPort
	}
	if oldPort == newPort {
		return before, true // 幂等：已经是目标端口
	}

	// 增量修正 TCP 校验和（含伪首部中不变的 src/dst IP 与协议号）。
	csumOff := 16
	oldCsum := binary.BigEndian.Uint16(tcp[csumOff : csumOff+2])
	newCsum := UpdateChecksum16(oldCsum, oldPort, newPort)
	binary.BigEndian.PutUint16(tcp[csumOff:csumOff+2], newCsum)

	binary.BigEndian.PutUint16(tcp[off:off+2], newPort)
	return before, true
}

// UpdateChecksum16 按 RFC 1624 用增量方式更新 16 位校验和。
//
//	HC' = ~(~HC + ~m + m')
func UpdateChecksum16(old, oldVal, newVal uint16) uint16 {
	sum := uint32(^old) + uint32(^oldVal) + uint32(newVal)
	for sum > 0xFFFF {
		sum = (sum & 0xFFFF) + (sum >> 16)
	}
	c := ^uint16(sum)
	if c == 0 {
		// 全 0 在校验和字段中表示"未计算"，按惯例发送为全 1。
		return 0xFFFF
	}
	return c
}

// TCPChecksum 整包重算 TCP 校验和（含伪首部）。
// 仅供单测/诊断使用；产品路径使用增量更新 + WinDivert 的 CalcChecksums。
func TCPChecksum(b []byte) (uint16, bool) {
	ihl, tcpLen, ok := tcpOffset(b)
	if !ok {
		return 0, false
	}
	var sum uint32
	add := func(p []byte) {
		for i := 0; i+1 < len(p); i += 2 {
			sum += uint32(binary.BigEndian.Uint16(p[i : i+2]))
		}
		if len(p)%2 == 1 {
			sum += uint32(p[len(p)-1]) << 8
		}
	}
	add(b[12:20]) // 伪首部：src + dst
	sum += uint32(protoTCP)
	sum += uint32(tcpLen)

	tcp := make([]byte, tcpLen)
	copy(tcp, b[ihl:ihl+tcpLen])
	binary.BigEndian.PutUint16(tcp[16:18], 0)
	add(tcp)

	for sum > 0xFFFF {
		sum = (sum & 0xFFFF) + (sum >> 16)
	}
	c := ^uint16(sum)
	if c == 0 {
		return 0xFFFF, true
	}
	return c, true
}
