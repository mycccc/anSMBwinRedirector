package config

import (
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ansmbwinhelper/internal/diag"
	"ansmbwinhelper/internal/model"
)

func mustTarget(t *testing.T, id, ip string, port uint16) model.Target {
	t.Helper()
	return model.Target{ID: id, AndroidIP: netip.MustParseAddr(ip), AndroidPort: port}
}

// --- 解析与校验（decisions/006 §6.2 / §6.7 / §6.8 / §6.9 / §6.10）----------

func TestParseValidConfig(t *testing.T) {
	data := []byte(`{
	  "schema_version": 1,
	  "targets": [
	    { "id": "a", "android_ip": "192.168.1.100", "android_port": 4450 },
	    { "id": "b", "android_ip": "192.168.1.101", "android_port": 4450 }
	  ]
	}`)
	r := Parse(data)
	if r.State != StateLoaded {
		t.Fatalf("State=%v Detail=%s", r.State, r.Detail)
	}
	if len(r.Targets) != 2 {
		t.Fatalf("目标数 = %d", len(r.Targets))
	}
	if r.Targets[0].AndroidPort != 4450 {
		t.Errorf("端口解析错误: %d", r.Targets[0].AndroidPort)
	}
}

func TestParseSchemaVersionMissingMeansOne(t *testing.T) {
	r := Parse([]byte(`{"targets":[{"id":"a","android_ip":"10.0.0.1","android_port":4450}]}`))
	if r.State != StateLoaded {
		t.Fatalf("缺失 schema_version 应按 1 处理，得到 %v/%s", r.State, r.Detail)
	}
}

func TestParseRejectsHigherSchemaVersion(t *testing.T) {
	// 当前支持到 v2（decisions/012 §3.2）⇒ v3 必须不可用。
	r := Parse([]byte(`{"schema_version":3,"targets":[]}`))
	if r.State != StateUnavailable || r.Code != diag.CodeConfigSchemaUnsupported {
		t.Fatalf("更高 schema_version 必须不可用，得到 %v/%s", r.State, r.Code)
	}
}

func TestParseRejectsUnknownFields(t *testing.T) {
	cases := map[string]string{
		"顶层未知字段":      `{"schema_version":1,"targets":[],"extra":1}`,
		"target 未知字段": `{"schema_version":1,"targets":[{"id":"a","android_ip":"10.0.0.1","android_port":4450,"name":"x"}]}`,
		"拼写错误":        `{"schema_version":1,"targets":[{"id":"a","android_ip":"10.0.0.1","android_portt":4450}]}`,
	}
	for name, data := range cases {
		r := Parse([]byte(data))
		if r.State != StateUnavailable {
			t.Errorf("%s: 期望不可用，得到 %v", name, r.State)
			continue
		}
		if r.Code != diag.CodeConfigValidationFailed {
			t.Errorf("%s: 期望校验失败码，得到 %s", name, r.Code)
		}
	}
}

func TestParseCollectsAllViolations(t *testing.T) {
	// decisions/006 §6.10：必须**列出全部**违规项，而不是只报第一条。
	data := []byte(`{"schema_version":1,"targets":[
	  {"id":"a","android_ip":"999.1.1.1","android_port":4450},
	  {"id":"b","android_ip":"10.0.0.1"},
	  {"id":"c","android_ip":"224.0.0.1","android_port":4450}
	]}`)
	r := Parse(data)
	if r.State != StateUnavailable {
		t.Fatal("期望不可用")
	}
	if len(r.Violations) < 3 {
		t.Fatalf("应列出全部违规项，得到 %d 条: %v", len(r.Violations), r.Violations)
	}
}

func TestParseRejectsDuplicateIPAndID(t *testing.T) {
	dupIP := `{"schema_version":1,"targets":[
	  {"id":"a","android_ip":"10.0.0.1","android_port":4450},
	  {"id":"b","android_ip":"10.0.0.1","android_port":4460}]}`
	if r := Parse([]byte(dupIP)); r.State != StateUnavailable {
		t.Fatal("重复 Android IP（不同端口）必须拒绝（decisions/005 I1）")
	}
	dupID := `{"schema_version":1,"targets":[
	  {"id":"same","android_ip":"10.0.0.1","android_port":4450},
	  {"id":"same","android_ip":"10.0.0.2","android_port":4450}]}`
	if r := Parse([]byte(dupID)); r.State != StateUnavailable {
		t.Fatal("重复 ID 必须拒绝（decisions/006 §6.6）")
	}
}

func TestParseGeneratesMissingIDInMemoryOnly(t *testing.T) {
	r := Parse([]byte(`{"schema_version":1,"targets":[{"android_ip":"10.0.0.1","android_port":4450}]}`))
	if r.State != StateLoaded {
		t.Fatalf("缺 id 应自动生成而非报错，得到 %v/%s", r.State, r.Detail)
	}
	if !r.GeneratedIDs {
		t.Error("应标记 GeneratedIDs（仅在下次成功保存时回写）")
	}
	if len(r.Targets[0].ID) != 36 {
		t.Errorf("生成的 ID 应为 UUIDv4 形式，得到 %q", r.Targets[0].ID)
	}
}

func TestParseAllowsAndroidPort445WithoutNormalization(t *testing.T) {
	// decisions/006 §6.9：原样保存、不归一化。
	r := Parse([]byte(`{"schema_version":1,"targets":[{"id":"a","android_ip":"10.0.0.1","android_port":445}]}`))
	if r.State != StateLoaded {
		t.Fatalf("AndroidPort=445 必须合法，得到 %v/%s", r.State, r.Detail)
	}
	if r.Targets[0].AndroidPort != 445 {
		t.Errorf("端口被改写为 %d，不得归一化", r.Targets[0].AndroidPort)
	}
	b, err := Marshal(r.Targets)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"android_port": 445`) {
		t.Errorf("round-trip 必须无损，得到:\n%s", b)
	}
}

func TestParseEmptyTargetsIsLegal(t *testing.T) {
	for _, data := range []string{`{"schema_version":1,"targets":[]}`, `{}`} {
		r := Parse([]byte(data))
		if r.State != StateLoaded {
			t.Errorf("%s: 空 targets 必须合法，得到 %v/%s", data, r.State, r.Detail)
		}
	}
}

// --- Store：读取路径绝不写盘（decisions/006 §6.5/§6.3）---------------------

func TestLoadMissingFileIsEmptyAndCreatesNothing(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")
	s := NewStore(p)

	r := s.Load()
	if r.State != StateEmpty {
		t.Fatalf("State=%v, 期望 StateEmpty", r.State)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatal("读取路径不得创建文件")
	}
	if fileExists(p + ".bak") {
		t.Fatal("读取路径不得创建备份")
	}
}

func TestLoadDoesNotWrite(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")
	orig := []byte(`{"schema_version":1,"targets":[{"id":"a","android_ip":"10.0.0.1","android_port":4450}]}`)
	if err := os.WriteFile(p, orig, 0o644); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(p)

	s := NewStore(p)
	if r := s.Load(); r.State != StateLoaded {
		t.Fatalf("加载失败: %v/%s", r.State, r.Detail)
	}
	after, _ := os.Stat(p)
	if !before.ModTime().Equal(after.ModTime()) || before.Size() != after.Size() {
		t.Error("加载不得修改文件")
	}
	got, _ := os.ReadFile(p)
	if string(got) != string(orig) {
		t.Error("加载不得改写文件内容")
	}
}

// --- Store：写入的替换与备份语义（decisions/006 §6.11）---------------------

func TestSaveThenLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")
	s := NewStore(p)

	want := []model.Target{
		mustTarget(t, "a", "192.168.1.100", 4450),
		mustTarget(t, "b", "192.168.1.101", 4460),
	}
	if err := s.Save(want); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	if fileExists(s.TmpPath()) {
		t.Error("保存后不应残留 .tmp")
	}
	if fileExists(s.BackupPath()) {
		t.Error("首次保存没有旧内容 ⇒ 不应产生 .bak")
	}
	got := s.Load()
	if got.State != StateLoaded || len(got.Targets) != 2 {
		t.Fatalf("回读失败: %v %v", got.State, got.Targets)
	}
	if got.Targets[1].AndroidPort != 4460 || got.Targets[1].ID != "b" {
		t.Errorf("round-trip 不一致: %+v", got.Targets[1])
	}
}

func TestSaveKeepsPreviousVersionAsBackup(t *testing.T) {
	// decisions/006 §6.11：.bak = "最近一次成功持久化之前的有效配置"。
	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")
	s := NewStore(p)

	v1 := []model.Target{mustTarget(t, "a", "10.0.0.1", 4450)}
	if err := s.Save(v1); err != nil {
		t.Fatal(err)
	}
	v2 := []model.Target{mustTarget(t, "a", "10.0.0.1", 4451), mustTarget(t, "b", "10.0.0.2", 4450)}
	if err := s.Save(v2); err != nil {
		t.Fatal(err)
	}
	bak, err := os.ReadFile(s.BackupPath())
	if err != nil {
		t.Fatalf("第二次保存必须产生 .bak: %v", err)
	}
	if !strings.Contains(string(bak), `"android_port": 4450`) ||
		!strings.Contains(string(bak), `"android_ip": "10.0.0.1"`) {
		t.Errorf(".bak 应为上一版内容，得到:\n%s", bak)
	}
	cur := s.Load()
	if len(cur.Targets) != 2 {
		t.Errorf("当前配置应为新版本，得到 %+v", cur.Targets)
	}
}

func TestSaveRejectsInvalidTargets(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")
	s := NewStore(p)
	bad := []model.Target{mustTarget(t, "a", "0.0.0.0", 4450)}
	if err := s.Save(bad); err == nil {
		t.Fatal("非法配置必须被拒绝写入")
	}
	if fileExists(p) {
		t.Fatal("被拒绝的写入不得产生文件")
	}
}

func TestSaveFailureDoesNotDamageCurrentFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")
	s := NewStore(p)
	good := []model.Target{mustTarget(t, "a", "10.0.0.1", 4450)}
	if err := s.Save(good); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(p)

	// 触发一次失败：制造一个同名目录作为 .tmp 路径，使临时文件写入失败。
	if err := os.MkdirAll(s.TmpPath(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := s.Save([]model.Target{mustTarget(t, "b", "10.0.0.2", 4450)}); err == nil {
		t.Skip("该平台下未能构造写入失败，跳过")
	}
	after, _ := os.ReadFile(p)
	if string(before) != string(after) {
		t.Fatal("保存失败时不得破坏当前有效的 config.json")
	}
}

// --- schema v2：凭据字段与 v1 → v2 迁移（decisions/012 §3.1 / §3.2）--------

func mustTargetCred(t *testing.T, id, ip string, port uint16, user, pass string) model.Target {
	t.Helper()
	return model.Target{
		ID: id, AndroidIP: netip.MustParseAddr(ip), AndroidPort: port,
		Username: user, Password: pass,
	}
}

func TestParseV2WithCredentials(t *testing.T) {
	data := []byte(`{
	  "schema_version": 2,
	  "targets": [
	    { "id": "a", "android_ip": "192.168.1.100", "android_port": 4450,
	      "username": "ansmb", "password": "s3cret" }
	  ]
	}`)
	r := Parse(data)
	if r.State != StateLoaded {
		t.Fatalf("State=%v Detail=%s", r.State, r.Detail)
	}
	if r.MigratedFrom != 0 {
		t.Errorf("v2 文件不应标记迁移，得到 MigratedFrom=%d", r.MigratedFrom)
	}
	got := r.Targets[0]
	if got.Username != "ansmb" || got.Password != "s3cret" {
		t.Errorf("凭据解析错误: %+v", got)
	}
	if !got.HasCredentials() {
		t.Error("HasCredentials 应为 true")
	}
}

func TestParseV1MigratesInMemoryOnly(t *testing.T) {
	// decisions/012 §3.2：v1 ⇒ 内存迁移（补空凭据），读取路径**不写盘**。
	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")
	orig := []byte(`{"schema_version":1,"targets":[{"id":"a","android_ip":"10.0.0.1","android_port":4450}]}`)
	if err := os.WriteFile(p, orig, 0o644); err != nil {
		t.Fatal(err)
	}
	s := NewStore(p)
	r := s.Load()
	if r.State != StateLoaded {
		t.Fatalf("v1 必须可加载（内存迁移），得到 %v/%s", r.State, r.Detail)
	}
	if r.MigratedFrom != SchemaVersionLegacyV1 {
		t.Errorf("应标记 MigratedFrom=1，得到 %d", r.MigratedFrom)
	}
	if r.Targets[0].HasCredentials() {
		t.Error("v1 迁移后凭据应为空（无凭据）")
	}
	after, _ := os.ReadFile(p)
	if string(after) != string(orig) {
		t.Error("读取路径不得改写 v1 文件（迁移只在内存，保存时才写 v2）")
	}
}

func TestParseV1RejectsCredentialFields(t *testing.T) {
	// decisions/006 §6.2 + 012：v1 里出现 v2 字段属**未知字段** ⇒ 拒绝（按版本严格判定）。
	data := []byte(`{"schema_version":1,"targets":[{"id":"a","android_ip":"10.0.0.1","android_port":4450,"username":"x"}]}`)
	r := Parse(data)
	if r.State != StateUnavailable || r.Code != diag.CodeConfigValidationFailed {
		t.Fatalf("v1 含凭据字段必须被判未知字段，得到 %v/%s", r.State, r.Code)
	}
}

func TestParseCredentialConsistency(t *testing.T) {
	// decisions/012 C5：只做基本非空校验。
	base := `{"schema_version":2,"targets":[{"id":"a","android_ip":"10.0.0.1","android_port":4450%s}]}`
	cases := map[string]struct {
		extra string
		ok    bool
	}{
		"无凭据（都空）": {``, true},
		"用户名+密码":  {`,"username":"u","password":"p"`, true},
		"有密码无用户名": {`,"password":"p"`, false},
		"有用户名无密码": {`,"username":"u"`, false},
	}
	for name, c := range cases {
		r := Parse([]byte(fmt.Sprintf(base, c.extra)))
		if c.ok && r.State != StateLoaded {
			t.Errorf("%s: 应合法，得到 %v/%s", name, r.State, r.Detail)
		}
		if !c.ok && r.State != StateUnavailable {
			t.Errorf("%s: 应被拒绝，得到 %v", name, r.State)
		}
	}
}

func TestMarshalWritesSchemaV2AndRoundTripsCredentials(t *testing.T) {
	want := []model.Target{mustTargetCred(t, "a", "192.168.1.100", 4450, "ansmb", "s3cret")}
	b, err := Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"schema_version": 2`) {
		t.Errorf("必须写 schema_version=2，得到:\n%s", b)
	}
	got := Parse(b)
	if got.State != StateLoaded || got.Targets[0].Username != "ansmb" || got.Targets[0].Password != "s3cret" {
		t.Fatalf("round-trip 不一致: %v %+v", got.State, got.Targets)
	}
}

func TestSaveUpgradesV1FileToV2(t *testing.T) {
	// decisions/012 §3.2：v1 加载后**成功保存**才升级为 v2。
	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")
	if err := os.WriteFile(p, []byte(`{"schema_version":1,"targets":[{"id":"a","android_ip":"10.0.0.1","android_port":4450}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	s := NewStore(p)
	r := s.Load()
	if r.State != StateLoaded || r.MigratedFrom != 1 {
		t.Fatalf("前置条件失败: %v/%d", r.State, r.MigratedFrom)
	}
	// 用户补上凭据后保存
	updated := r.Targets
	updated[0].Username = "u"
	updated[0].Password = "p"
	if err := s.Save(updated); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(p)
	if !strings.Contains(string(after), `"schema_version": 2`) {
		t.Errorf("保存后应升级为 v2，得到:\n%s", after)
	}
	back := s.Load()
	if back.MigratedFrom != 0 || back.Targets[0].Username != "u" {
		t.Errorf("回读失败: %+v", back)
	}
	if fileExists(s.BackupPath()) != true {
		t.Error("第二次保存应产生 .bak（上一版为 v1）")
	}
}
