package model

import (
	"encoding/binary"
	"net/netip"
	"testing"
)

// buildTCPv4 造一个校验和正确的 IPv4+TCP 报文。
func buildTCPv4(t *testing.T, src, dst netip.Addr, srcPort, dstPort uint16, flags byte, payload []byte) []byte {
	t.Helper()

	tcp := make([]byte, 20+len(payload))
	binary.BigEndian.PutUint16(tcp[0:2], srcPort)
	binary.BigEndian.PutUint16(tcp[2:4], dstPort)
	tcp[12] = 0x50 // data offset = 5
	tcp[13] = flags
	copy(tcp[20:], payload)

	ip := make([]byte, 20)
	ip[0] = 0x45
	binary.BigEndian.PutUint16(ip[2:4], uint16(20+len(tcp)))
	ip[8] = 64
	ip[9] = protoTCP
	s4 := src.As4()
	d4 := dst.As4()
	copy(ip[12:16], s4[:])
	copy(ip[16:20], d4[:])
	binary.BigEndian.PutUint16(ip[10:12], ipChecksum(ip))

	pkt := append(ip, tcp...)
	c, ok := TCPChecksum(pkt)
	if !ok {
		t.Fatal("TCPChecksum failed on freshly built packet")
	}
	binary.BigEndian.PutUint16(pkt[20+16:20+18], c)
	return pkt
}

func ipChecksum(ip []byte) uint16 {
	var sum uint32
	for i := 0; i+1 < len(ip); i += 2 {
		sum += uint32(binary.BigEndian.Uint16(ip[i : i+2]))
	}
	for sum > 0xFFFF {
		sum = (sum & 0xFFFF) + (sum >> 16)
	}
	return ^uint16(sum)
}

func mustAddr(s string) netip.Addr { return netip.MustParseAddr(s) }

// --- Target 校验（decisions/005 §6.1）--------------------------------------

func TestValidateTargetAcceptsOrdinaryUnicast(t *testing.T) {
	for _, ip := range []string{"192.168.1.100", "10.0.0.5", "172.16.1.20", "127.0.0.1"} {
		tgt := Target{ID: "id-" + ip, AndroidIP: mustAddr(ip), AndroidPort: 4450}
		if err := ValidateTarget(tgt); err != nil {
			t.Errorf("%s: 期望通过，得到 %v", ip, err)
		}
	}
}

func TestValidateTargetAllowsAndroidPort445(t *testing.T) {
	// decisions/005 §6.1：AndroidPort == 445 **允许**（实现层按 no-op 处理）。
	tgt := Target{ID: "x", AndroidIP: mustAddr("192.168.1.100"), AndroidPort: 445}
	if err := ValidateTarget(tgt); err != nil {
		t.Fatalf("445 必须被允许，得到 %v", err)
	}
}

func TestValidateTargetRejects(t *testing.T) {
	cases := []struct {
		name string
		tgt  Target
	}{
		{"未指定 0.0.0.0", Target{ID: "x", AndroidIP: mustAddr("0.0.0.0"), AndroidPort: 4450}},
		{"广播 255.255.255.255", Target{ID: "x", AndroidIP: mustAddr("255.255.255.255"), AndroidPort: 4450}},
		{"组播 224.0.0.1", Target{ID: "x", AndroidIP: mustAddr("224.0.0.1"), AndroidPort: 4450}},
		{"保留 240.0.0.1", Target{ID: "x", AndroidIP: mustAddr("240.0.0.1"), AndroidPort: 4450}},
		{"端口 0", Target{ID: "x", AndroidIP: mustAddr("192.168.1.100"), AndroidPort: 0}},
		{"缺 ID", Target{ID: "", AndroidIP: mustAddr("192.168.1.100"), AndroidPort: 4450}},
		{"非 IPv4", Target{ID: "x", AndroidIP: mustAddr("fe80::1"), AndroidPort: 4450}},
	}
	for _, c := range cases {
		if err := ValidateTarget(c.tgt); err == nil {
			t.Errorf("%s: 期望被拒绝，却通过了", c.name)
		}
	}
}

func TestValidateTargetsRejectsDuplicateIP(t *testing.T) {
	// decisions/005 I1 + §6.3：重复 IP（即使端口不同）必须拒绝 ——
	// 两条 OUT filter 会完全相同。
	ts := []Target{
		{ID: "a", AndroidIP: mustAddr("192.168.1.100"), AndroidPort: 4450},
		{ID: "b", AndroidIP: mustAddr("192.168.1.100"), AndroidPort: 4460},
	}
	if err := ValidateTargets(ts); err == nil {
		t.Fatal("重复 Android IP（不同端口）必须被拒绝")
	}
}

func TestValidateTargetsRejectsDuplicateID(t *testing.T) {
	ts := []Target{
		{ID: "same", AndroidIP: mustAddr("192.168.1.100"), AndroidPort: 4450},
		{ID: "same", AndroidIP: mustAddr("192.168.1.101"), AndroidPort: 4450},
	}
	if err := ValidateTargets(ts); err == nil {
		t.Fatal("重复 ID 必须被拒绝")
	}
}

func TestValidateTargetsAllowsSamePortDifferentIP(t *testing.T) {
	// decisions/005 §6.3：不同 IP + 相同 AndroidPort **允许**。
	ts := []Target{
		{ID: "a", AndroidIP: mustAddr("192.168.1.100"), AndroidPort: 4450},
		{ID: "b", AndroidIP: mustAddr("192.168.1.101"), AndroidPort: 4450},
	}
	if err := ValidateTargets(ts); err != nil {
		t.Fatalf("不同 IP + 相同端口应当允许，得到 %v", err)
	}
}

// --- filter（decisions/005 §6.3）-------------------------------------------

func TestFilterStrings(t *testing.T) {
	tgt := Target{ID: "a", AndroidIP: mustAddr("192.168.1.100"), AndroidPort: 4450}
	fwd, rev := Filters(tgt)
	if want := "outbound and ip and ip.DstAddr == 192.168.1.100 and tcp.DstPort == 445"; fwd != want {
		t.Errorf("OUT filter\n got %q\nwant %q", fwd, want)
	}
	if want := "inbound and ip and ip.SrcAddr == 192.168.1.100 and tcp.SrcPort == 4450"; rev != want {
		t.Errorf("IN filter\n got %q\nwant %q", rev, want)
	}
}

// --- 报文改写（decisions/005 §6.2 / I9）-------------------------------------

func TestRewriteOutboundOnlyChangesDstPort(t *testing.T) {
	src, dst := mustAddr("192.168.1.191"), mustAddr("192.168.1.100")
	pkt := buildTCPv4(t, src, dst, 49912, 445, 0x02, nil)
	orig := append([]byte(nil), pkt...)

	if _, ok := RewritePort(pkt, Outbound, 4450); !ok {
		t.Fatal("RewritePort 返回 not ok")
	}

	// IP 头逐字节不变
	for i := 0; i < 20; i++ {
		if pkt[i] != orig[i] {
			t.Fatalf("IP 头第 %d 字节被改动", i)
		}
	}
	// 源端口不变
	if got := binary.BigEndian.Uint16(pkt[20:22]); got != 49912 {
		t.Errorf("源端口被改动: %d", got)
	}
	// 目的端口 = 4450
	if got := binary.BigEndian.Uint16(pkt[22:24]); got != 4450 {
		t.Errorf("目的端口 = %d, 期望 4450", got)
	}
	// 差异只有 2 字节的端口 + 2 字节校验和
	if diff := countDiffBytes(orig, pkt); diff != 4 {
		t.Errorf("改动字节数 = %d, 期望 4（端口 2 + 校验和 2）", diff)
	}
	assertChecksumValid(t, pkt)
}

func TestRewriteInboundOnlyChangesSrcPort(t *testing.T) {
	src, dst := mustAddr("192.168.1.100"), mustAddr("192.168.1.191")
	pkt := buildTCPv4(t, src, dst, 4450, 49912, 0x12, []byte("smb-payload"))
	orig := append([]byte(nil), pkt...)

	if _, ok := RewritePort(pkt, Inbound, 445); !ok {
		t.Fatal("RewritePort 返回 not ok")
	}
	if got := binary.BigEndian.Uint16(pkt[20:22]); got != 445 {
		t.Errorf("源端口 = %d, 期望 445", got)
	}
	if got := binary.BigEndian.Uint16(pkt[22:24]); got != 49912 {
		t.Errorf("目的端口被改动: %d", got)
	}
	// 载荷逐字节不变
	if string(pkt[40:]) != "smb-payload" {
		t.Error("载荷被改动")
	}
	if diff := countDiffBytes(orig, pkt); diff != 4 {
		t.Errorf("改动字节数 = %d, 期望 4", diff)
	}
	assertChecksumValid(t, pkt)
}

func TestRewriteIsIdempotent(t *testing.T) {
	pkt := buildTCPv4(t, mustAddr("192.168.1.191"), mustAddr("192.168.1.100"), 50000, 445, 0x02, nil)
	RewritePort(pkt, Outbound, 4450)
	snap := append([]byte(nil), pkt...)
	if _, ok := RewritePort(pkt, Outbound, 4450); !ok {
		t.Fatal("第二次改写返回 not ok")
	}
	if countDiffBytes(snap, pkt) != 0 {
		t.Error("重复改写不应再改动任何字节")
	}
}

func TestRewriteRejectsUnsupportedPackets(t *testing.T) {
	base := buildTCPv4(t, mustAddr("192.168.1.191"), mustAddr("192.168.1.100"), 50000, 445, 0x02, nil)

	cases := map[string][]byte{
		"太短":      {0x45},
		"全 0":     make([]byte, 40),
		"非 IPv4":  func() []byte { p := append([]byte(nil), base...); p[0] = 0x65; return p }(),
		"非 TCP":   func() []byte { p := append([]byte(nil), base...); p[9] = 17; return p }(),
		"非首片分片":   func() []byte { p := append([]byte(nil), base...); p[6], p[7] = 0x00, 0x01; return p }(),
		"头声明超出报文": func() []byte { p := append([]byte(nil), base...); binary.BigEndian.PutUint16(p[2:4], 5000); return p }(),
	}
	for name, pkt := range cases {
		if _, ok := RewritePort(pkt, Outbound, 4450); ok {
			t.Errorf("%s: 期望不处理，却处理了", name)
		}
	}
}

func TestRewriteDoesNotPanicOnGarbage(t *testing.T) {
	for n := 0; n < 80; n++ {
		p := make([]byte, n)
		for i := range p {
			p[i] = byte(i * 7)
		}
		_, _ = RewritePort(p, Outbound, 4450)
		_, _ = ParseTCPv4(p)
		_, _ = TCPChecksum(p)
	}
}

func TestParseTCPv4Flags(t *testing.T) {
	pkt := buildTCPv4(t, mustAddr("192.168.1.191"), mustAddr("192.168.1.100"), 1, 2, 0x07, nil) // FIN|SYN|RST
	p, ok := ParseTCPv4(pkt)
	if !ok {
		t.Fatal("ParseTCPv4 失败")
	}
	if !p.FIN || !p.SYN || !p.RST {
		t.Errorf("标志解析错误: %+v", p)
	}
	if p.SrcPort != 1 || p.DstPort != 2 {
		t.Errorf("端口解析错误: %+v", p)
	}
}

func countDiffBytes(a, b []byte) int {
	n := 0
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			n++
		}
	}
	return n
}

func assertChecksumValid(t *testing.T, pkt []byte) {
	t.Helper()
	want, ok := TCPChecksum(pkt)
	if !ok {
		t.Fatal("TCPChecksum 失败")
	}
	got := binary.BigEndian.Uint16(pkt[20+16 : 20+18])
	if got != want {
		t.Fatalf("增量校验和 %#04x != 整包重算 %#04x", got, want)
	}
}

// --- FlowTable（decisions/005 §6.2 + I5/I6）--------------------------------

func TestFlowTableIsolation(t *testing.T) {
	// 直接写死 S8 的回归语义：未被 OUT 记录的流，IN 侧一律不得命中。
	ft := NewFlowTable(0)
	win := [4]byte{192, 168, 1, 191}
	if ft.Lookup(NewFlowKey(win, 49958)) {
		t.Fatal("未记录的流不应命中（这正是直连 AndroidPort 不被误伤的前提）")
	}
	ft.Record(NewFlowKey(win, 49912))
	if !ft.Lookup(NewFlowKey(win, 49912)) {
		t.Fatal("已记录的流应命中")
	}
	if ft.Lookup(NewFlowKey(win, 49913)) {
		t.Fatal("不同源端口不应命中")
	}
	other := [4]byte{10, 0, 0, 5}
	if ft.Lookup(NewFlowKey(other, 49912)) {
		t.Fatal("相同端口不同本地 IP 不应命中（多宿主隔离）")
	}
}

func TestFlowTableReclaimOnRST(t *testing.T) {
	ft := NewFlowTable(0)
	k := NewFlowKey([4]byte{192, 168, 1, 191}, 50000)
	ft.Record(k)
	ft.Observe(k, Outbound, true, false)
	if ft.Lookup(k) {
		t.Fatal("RST 应立即回收表项")
	}
}

func TestFlowTableReclaimOnBidirectionalFIN(t *testing.T) {
	ft := NewFlowTable(0)
	k := NewFlowKey([4]byte{192, 168, 1, 191}, 50001)
	ft.Record(k)
	ft.Observe(k, Outbound, false, true)
	if !ft.Lookup(k) {
		t.Fatal("单向 FIN 不应立刻回收")
	}
	ft.Observe(k, Inbound, false, true)
	if ft.Lookup(k) {
		t.Fatal("双向 FIN 应回收")
	}
}

func TestFlowTableCapacityBounded(t *testing.T) {
	ft := NewFlowTable(8)
	for i := 0; i < 100; i++ {
		ft.Record(NewFlowKey([4]byte{10, 0, 0, 1}, uint16(40000+i)))
	}
	if ft.Len() > 8 {
		t.Fatalf("表项数 %d 超过上限 8（容量兜底失效）", ft.Len())
	}
}

func TestFlowTableConcurrentAccess(t *testing.T) {
	ft := NewFlowTable(64)
	done := make(chan struct{})
	for g := 0; g < 4; g++ {
		go func(g int) {
			defer func() { done <- struct{}{} }()
			for i := 0; i < 500; i++ {
				k := NewFlowKey([4]byte{10, 0, byte(g), 1}, uint16(40000+i%8))
				ft.Record(k)
				ft.Lookup(k)
				ft.Observe(k, Direction(i%2), i%97 == 0, false)
			}
		}(g)
	}
	for i := 0; i < 4; i++ {
		<-done
	}
}
