//go:build windows

package gui

import (
	"net/netip"
	"os"
	"testing"

	"ansmbwinhelper/internal/i18n"
	"ansmbwinhelper/internal/model"
)

// TestMain 固定 zh-CN（与其它包一致）：断言可直接对照中文文案。
func TestMain(m *testing.M) {
	if data, err := os.ReadFile("../i18n/assets/language.json"); err == nil {
		if cat, err := i18n.ParseCatalog(data); err == nil {
			i18n.SetDefault(i18n.New(cat, i18n.LanguageChineseSimplified))
		}
	}
	os.Exit(m.Run())
}

func addr(t *testing.T, s string) netip.Addr {
	t.Helper()
	a, err := netip.ParseAddr(s)
	if err != nil {
		t.Fatalf("bad addr %q: %v", s, err)
	}
	return a
}

// --- 密码语义（decisions/012 §3.4 / 用户 ⑤-2B 审计点 4）--------------------

func TestEffectivePasswordKeepsOriginalWhenUntouched(t *testing.T) {
	if got := effectivePassword("orig", false, ""); got != "orig" {
		t.Fatalf("未改动密码框 ⇒ 必须保留原密码，得到 %q", got)
	}
	// "只改端口"场景：密码框空且未改动 ⇒ 仍保留
	if got := effectivePassword("orig", false, "typed-but-ignored"); got != "orig" {
		t.Fatalf("未改动时不得采用框内内容，得到 %q", got)
	}
}

func TestEffectivePasswordUsesTypedValue(t *testing.T) {
	if got := effectivePassword("orig", true, "new"); got != "new" {
		t.Fatalf("改动后应以框内内容为准，得到 %q", got)
	}
	if got := effectivePassword("orig", true, ""); got != "" {
		t.Fatalf("改动后清空 ⇒ 空，得到 %q", got)
	}
}

func TestPasswordStateHintNeverLeaksLength(t *testing.T) {
	if got := passwordStateHint(""); got != " " {
		t.Fatalf("无密码 ⇒ 占位空格，得到 %q", got)
	}
	// ⛔ 只表明"已配置"，不得出现密码、长度或掩码字符数（decisions/012 C4）。
	a := passwordStateHint("x")
	b := passwordStateHint("a-very-long-password-1234567890")
	if a != b {
		t.Fatalf("提示文案不得随密码长度变化: %q vs %q", a, b)
	}
	if a == " " || a == "" {
		t.Fatalf("有密码时必须给出提示，得到 %q", a)
	}
}

// --- 编辑对话框校验（IP / 端口 / 凭据）-------------------------------------

func TestValidateEditCredentials(t *testing.T) {
	ip := "192.168.1.100"
	other := []model.Target{}

	cases := []struct {
		name       string
		user, pass string
		edited     bool
		ok         bool
	}{
		{"无凭据（都空）", "", "", false, true},
		{"用户名+密码", "ansmb", "ansmb", false, true},
		{"有用户名无密码", "ansmb", "", false, false},
		{"有密码无用户名（用户输入过）", "", "p", true, false},
		{"清空用户名但未碰密码框 ⇒ 视为移除凭据", "", "", false, true},
	}
	for _, c := range cases {
		_, err := validateEdit(ip, 4450, c.user, c.pass, c.edited, "", other)
		if c.ok && err != nil {
			t.Errorf("%s: 应通过，得到 %v", c.name, err)
		}
		if !c.ok && err == nil {
			t.Errorf("%s: 应被拒绝", c.name)
		}
	}
}

func TestValidateEditRejectsBadIPAndDuplicates(t *testing.T) {
	if _, err := validateEdit("", 4450, "", "", false, "", nil); err == nil {
		t.Error("空 IP 必须被拒绝")
	}
	if _, err := validateEdit("not-an-ip", 4450, "", "", false, "", nil); err == nil {
		t.Error("非法 IP 必须被拒绝")
	}
	// I1：集合内 IP 唯一（排除自身）
	all := []model.Target{{ID: "other", AndroidIP: addr(t, "10.0.0.7"), AndroidPort: 4450}}
	if _, err := validateEdit("10.0.0.7", 4450, "", "", false, "self", all); err == nil {
		t.Error("重复 IP 必须被拒绝")
	}
	if _, err := validateEdit("10.0.0.7", 4450, "", "", false, "other", all); err != nil {
		t.Errorf("排除自身后应通过，得到 %v", err)
	}
}

// --- draft 的凭据保留（新增 / 编辑 / 撤销删除）------------------------------

func TestDraftKeepsCredentialsAcrossEdits(t *testing.T) {
	d := newDraft()
	d.reset(nil)
	if err := d.add(addr(t, "192.168.1.100"), 4450, "ansmb", "ansmb"); err != nil {
		t.Fatal(err)
	}
	got := d.list()[0]
	if got.Username != "ansmb" || got.Password != "ansmb" {
		t.Fatalf("新增应带上凭据，得到 %+v", got)
	}

	// 只改端口：凭据必须原样保留（调用方传入 orig 密码）
	if !d.update(got.ID, addr(t, "192.168.1.100"), 4451, got.Username, got.Password) {
		t.Fatal("update 失败")
	}
	got = d.list()[0]
	if got.AndroidPort != 4451 || got.Username != "ansmb" || got.Password != "ansmb" {
		t.Fatalf("只改端口不得丢失凭据，得到 %+v", got)
	}
}

func TestDraftEditKeepsCredentialsOnPendingDeleteUndo(t *testing.T) {
	persisted := []model.Target{{
		ID: "t1", AndroidIP: addr(t, "10.0.0.1"), AndroidPort: 4450,
		Username: "u", Password: "p",
	}}
	d := newDraft()
	d.reset(persisted)

	d.remove("t1")
	if !d.isDeleted("t1") {
		t.Fatal("应进入待删除")
	}
	// 对"待删除"行做编辑 ⇒ 撤销删除并应用新值（凭据由调用方给出）
	if !d.update("t1", addr(t, "10.0.0.1"), 4450, "u", "p") {
		t.Fatal("update（撤销删除路径）失败")
	}
	if d.isDeleted("t1") {
		t.Fatal("编辑后不应仍处于待删除")
	}
	tgt, ok := d.byID("t1")
	if !ok || tgt.Username != "u" || tgt.Password != "p" {
		t.Fatalf("撤销删除后凭据必须保留，得到 %+v ok=%v", tgt, ok)
	}
}

func TestDraftUndoRemoveKeepsCredentials(t *testing.T) {
	persisted := []model.Target{{
		ID: "t1", AndroidIP: addr(t, "10.0.0.1"), AndroidPort: 4450,
		Username: "u", Password: "p",
	}}
	d := newDraft()
	d.reset(persisted)
	d.remove("t1")
	d.undoRemove("t1")
	tgt, ok := d.byID("t1")
	if !ok || tgt.Username != "u" || tgt.Password != "p" {
		t.Fatalf("撤销删除必须保留凭据，得到 %+v ok=%v", tgt, ok)
	}
	if d.hasUnsaved() {
		t.Fatal("撤销删除后应与磁盘一致（无未保存更改）")
	}
}
