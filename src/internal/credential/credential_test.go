package credential

import (
	"errors"
	"testing"
)

// decisions/012 C5：**只做基本非空校验**，⛔ 不做长度/字符集/强度策略。
func TestValidateBasicNonEmptyOnly(t *testing.T) {
	cases := map[string]struct {
		host, user, pass string
		want             error
	}{
		"正常":    {"192.168.1.100", "ansmb", "p", nil},
		"空主机":   {"", "ansmb", "p", ErrHostRequired},
		"空白主机":  {"   ", "ansmb", "p", ErrHostRequired},
		"空用户名":  {"192.168.1.100", "", "p", ErrUsernameRequired},
		"空密码":   {"192.168.1.100", "ansmb", "", ErrPasswordRequired},
		"单字符密码": {"192.168.1.100", "u", "x", nil},
		"含空格密码": {"192.168.1.100", "u", "a b", nil}, // ⛔ 不做字符集限制
		"很长的密码": {"192.168.1.100", "u", "0123456789", nil},
	}
	for name, c := range cases {
		if got := Validate(c.host, c.user, c.pass); !errors.Is(got, c.want) {
			t.Errorf("%s: Validate=%v, 期望 %v", name, got, c.want)
		}
	}
}

// ⛔ 返回的 error 不得包含密码（decisions/012 §3.3）。
func TestValidateErrorsNeverContainPassword(t *testing.T) {
	const secret = "top-secret"
	for _, c := range []struct{ host, user, pass string }{
		{"", "u", secret},
		{"h", "", secret},
		{"h", "u", ""},
	} {
		err := Validate(c.host, c.user, c.pass)
		if err == nil {
			continue
		}
		if contains(err.Error(), secret) {
			t.Errorf("错误文本泄露了密码: %v", err)
		}
	}
}

func contains(s, sub string) bool {
	if sub == "" {
		return false
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
