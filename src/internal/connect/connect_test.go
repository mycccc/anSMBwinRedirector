package connect

import "testing"

// ValidateHost 既是输入校验，也是**注入防护**（host 会被拼进命令行）。
func TestValidateHostAcceptsOnlyIPv4Literals(t *testing.T) {
	ok := []string{"192.168.1.100", "10.0.0.1", "0.0.0.0", "255.255.255.255"}
	for _, h := range ok {
		if err := ValidateHost(h); err != nil {
			t.Errorf("%q 应被接受，得到 %v", h, err)
		}
	}
	bad := []string{
		"", "192.168.1", "192.168.1.100.5", "192.168.1.",
		"192.168.1.100 ", " 192.168.1.100",
		`192.168.1.100"`, `192.168.1.100\`, "192.168.1.1&calc", "192.168.1.1;rm",
		"host-name", "192.168.1.1a", "1.2.3.4/24",
	}
	for _, h := range bad {
		if err := ValidateHost(h); err == nil {
			t.Errorf("%q 必须被拒绝（防注入）", h)
		}
	}
}
