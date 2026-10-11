package config

import (
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ansmbwinhelper/internal/model"
)

// --- 测试辅助 ---------------------------------------------------------------

func writeFileT(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// legacyConfigBytes 生成一份**合法**的 v2 配置（含凭据），用于迁移测试。
func legacyConfigBytes(t *testing.T) []byte {
	t.Helper()
	ip1 := parseIPMust(t, "10.1.1.1")
	ip2 := parseIPMust(t, "10.1.1.2")
	b, err := Marshal([]model.Target{
		{ID: "t1", AndroidIP: ip1, AndroidPort: 4450, Username: "ansmb", Password: "ansmbpw"},
		{ID: "t2", AndroidIP: ip2, AndroidPort: 4460, Username: "u2", Password: "p2-secret"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func parseIPMust(t *testing.T, s string) netip.Addr {
	t.Helper()
	v, err := parseIP(s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// 目录里除了给定的名字之外还有别的文件吗？（用于断言"探测不产生任何文件"）
func dirEntries(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range ents {
		out = append(out, e.Name())
	}
	return out
}

// --- 写探测（B1）------------------------------------------------------------

func TestProbeWritableSucceedsAndCreatesNothing(t *testing.T) {
	dir := t.TempDir()
	before := dirEntries(t, dir)
	if err := ProbeWritable(dir); err != nil {
		t.Fatalf("可写目录的探测应成功，得到 %v", err)
	}
	if after := dirEntries(t, dir); len(after) != len(before) {
		t.Fatalf("探测不得留下任何文件：before=%v after=%v", before, after)
	}
	if fileExists(filepath.Join(dir, "config.json")) {
		t.Fatal("⛔ 探测不得创建 config.json")
	}
}

func TestProbeWritableFailsWhenDirNotUsable(t *testing.T) {
	// 用"父路径是文件"来构造一个**跨平台**的不可用目录（Windows 上 chmod 对目录不可靠）。
	f := filepath.Join(t.TempDir(), "afile")
	writeFileT(t, f, []byte("x"))
	bad := filepath.Join(f, "sub")
	if err := ProbeWritable(bad); err == nil {
		t.Fatal("不可用目录的探测应失败")
	}
}

// --- 一次性迁移（A3）--------------------------------------------------------

func TestMigrateLegacyMovesValidConfigAndKeepsContent(t *testing.T) {
	dir := t.TempDir()
	newPath := filepath.Join(dir, "new", "config.json")
	legacy := filepath.Join(dir, "legacy", "config.json")
	raw := legacyConfigBytes(t)
	writeFileT(t, legacy, raw)

	out := MigrateLegacy(newPath, legacy)
	if !out.Attempted || !out.Migrated {
		t.Fatalf("应迁移成功：%+v", out)
	}
	if out.RenameWarning != "" {
		t.Fatalf("不应有改名告警：%s", out.RenameWarning)
	}
	if fileExists(legacy) {
		t.Fatal("迁移后旧路径不应再存在原名文件")
	}
	if !fileExists(out.LegacyMovedTo) {
		t.Fatalf("旧文件应被改名保留：%s", out.LegacyMovedTo)
	}
	// 旧文件内容逐字节保留。
	moved, err := os.ReadFile(out.LegacyMovedTo)
	if err != nil {
		t.Fatal(err)
	}
	if string(moved) != string(raw) {
		t.Fatal("改名后的旧文件内容必须与原来逐字节一致")
	}
	// 新配置内容完整（含密码），且能重新解析出同样的目标。
	nb, err := os.ReadFile(newPath)
	if err != nil {
		t.Fatal(err)
	}
	r := Parse(nb)
	if r.State != StateLoaded || len(r.Targets) != 2 {
		t.Fatalf("新配置应可加载且含 2 个目标：%+v", r)
	}
	if r.Targets[0].Password != "ansmbpw" || r.Targets[1].Password != "p2-secret" {
		t.Fatal("⛔ 迁移不得丢失密码")
	}
	if r.Targets[0].AndroidIP.String() != "10.1.1.1" || r.Targets[1].AndroidPort != 4460 {
		t.Fatal("迁移后目标字段必须完整")
	}
}

func TestMigrateLegacyInvalidLegacyIsNotMigratedNorTouched(t *testing.T) {
	dir := t.TempDir()
	newPath := filepath.Join(dir, "config.json")
	legacy := filepath.Join(dir, "legacy.json")
	bad := []byte("{ this is not json")
	writeFileT(t, legacy, bad)

	out := MigrateLegacy(newPath, legacy)
	if !out.Attempted || out.Migrated {
		t.Fatalf("无效旧配置不得迁移：%+v", out)
	}
	if !out.LegacyInvalid {
		t.Fatal("应标记旧配置无效")
	}
	if fileExists(newPath) {
		t.Fatal("⛔ 无效旧配置不得写出新文件")
	}
	got, err := os.ReadFile(legacy)
	if err != nil || string(got) != string(bad) {
		t.Fatal("旧文件必须原样保留")
	}
}

func TestMigrateLegacyKeepsNewWhenBothExist(t *testing.T) {
	dir := t.TempDir()
	newPath := filepath.Join(dir, "config.json")
	legacy := filepath.Join(dir, "legacy.json")
	newBytes := legacyConfigBytes(t)
	oldBytes := []byte("LEGACY-ORIGINAL")
	writeFileT(t, newPath, newBytes)
	writeFileT(t, legacy, oldBytes)

	out := MigrateLegacy(newPath, legacy)
	if out.Migrated {
		t.Fatal("两边都有配置时⛔ 绝不能迁移/覆盖")
	}
	if got, _ := os.ReadFile(newPath); string(got) != string(newBytes) {
		t.Fatal("新位置配置必须原样不动")
	}
	if got, _ := os.ReadFile(legacy); string(got) != string(oldBytes) {
		t.Fatal("旧配置必须原样不动")
	}
}

func TestMigrateLegacyWriteFailureKeepsLegacyIntact(t *testing.T) {
	dir := t.TempDir()
	newDir := filepath.Join(dir, "new")
	newPath := filepath.Join(newDir, "config.json")
	legacy := filepath.Join(dir, "legacy.json")
	raw := legacyConfigBytes(t)
	writeFileT(t, legacy, raw)

	// 让新路径的写入必然失败：把 `<config.json>.tmp` 做成**目录**（Save 的 OpenFile 会失败）。
	if err := os.MkdirAll(newPath+".tmp", 0o755); err != nil {
		t.Fatal(err)
	}

	out := MigrateLegacy(newPath, legacy)
	if out.Migrated {
		t.Fatal("写入失败时⛔ 不得报告迁移成功")
	}
	if out.Detail == "" {
		t.Fatal("应给出失败原因")
	}
	if fileExists(newPath) {
		t.Fatal("写入失败时不应留下新配置")
	}
	got, err := os.ReadFile(legacy)
	if err != nil || string(got) != string(raw) {
		t.Fatal("⛔ 迁移失败时旧配置必须逐字节完整")
	}
}

func TestMigrateLegacyRenameCollisionGetsSuffix(t *testing.T) {
	dir := t.TempDir()
	newPath := filepath.Join(dir, "config.json")
	legacy := filepath.Join(dir, "legacy.json")
	writeFileT(t, legacy, legacyConfigBytes(t))

	// 预先占用当前时刻（以及前后各 2 秒）的迁移目标名，确保必然撞名。
	for d := -2; d <= 2; d++ {
		ts := time.Now().Add(time.Duration(d) * time.Second).Format("20060102-150405")
		writeFileT(t, legacy+".migrated-"+ts, []byte("occupied"))
	}

	out := MigrateLegacy(newPath, legacy)
	if !out.Migrated {
		t.Fatalf("撞名时应自动换名而不是失败：%+v", out)
	}
	if !strings.Contains(out.LegacyMovedTo, ".migrated-") {
		t.Fatalf("改名目标应保留 .migrated-<时间戳> 形状：%s", out.LegacyMovedTo)
	}
	// 预先占用的那些文件不得被覆盖。
	for d := -2; d <= 2; d++ {
		ts := time.Now().Add(time.Duration(d) * time.Second).Format("20060102-150405")
		p := legacy + ".migrated-" + ts
		if got, err := os.ReadFile(p); err == nil && string(got) != "occupied" {
			t.Fatalf("⛔ 不得覆盖已存在的同名备份：%s", p)
		}
	}
}

// --- Prepare（组合：探测 + 迁移）--------------------------------------------

func TestPrepareNoLegacyIsNoopAndCreatesNoFile(t *testing.T) {
	dir := t.TempDir()
	st := NewStore(filepath.Join(dir, "config.json"))
	p := st.Prepare()
	if p.Unavailable || p.Migrated || len(p.Notes) != 0 {
		t.Fatalf("无旧配置时应为无操作：%+v", p)
	}
	if len(dirEntries(t, dir)) != 0 {
		t.Fatalf("Prepare 不得创建任何文件（探测文件也应清理）：%v", dirEntries(t, dir))
	}
}

func TestPrepareMigratesWhenOnlyLegacyExists(t *testing.T) {
	dir := t.TempDir()
	newPath := filepath.Join(dir, "exedir", "config.json")
	legacy := filepath.Join(dir, "appdata", "config.json")
	writeFileT(t, legacy, legacyConfigBytes(t))

	st := NewStore(newPath).SetLegacyPath(legacy)
	p := st.Prepare()
	if p.Unavailable {
		t.Fatalf("不应不可用：%+v", p)
	}
	if !p.Migrated {
		t.Fatalf("应完成迁移：%+v", p)
	}
	r := st.Load()
	if r.State != StateLoaded || len(r.Targets) != 2 {
		t.Fatalf("迁移后应能直接加载：%+v", r)
	}
}

func TestPrepareBothExistKeepsNewAndNotesOld(t *testing.T) {
	dir := t.TempDir()
	newPath := filepath.Join(dir, "config.json")
	legacy := filepath.Join(dir, "legacy.json")
	newBytes := legacyConfigBytes(t)
	writeFileT(t, newPath, newBytes)
	writeFileT(t, legacy, []byte("LEGACY"))

	st := NewStore(newPath).SetLegacyPath(legacy)
	p := st.Prepare()
	if p.Migrated || p.Unavailable {
		t.Fatalf("两边都有配置时不得迁移：%+v", p)
	}
	if len(p.Notes) == 0 || !strings.Contains(strings.Join(p.Notes, " "), "旧位置仍有配置") {
		t.Fatalf("应提示旧文件仍存在：%v", p.Notes)
	}
	if got, _ := os.ReadFile(newPath); string(got) != string(newBytes) {
		t.Fatal("新配置必须原样不动")
	}
	if got, _ := os.ReadFile(legacy); string(got) != "LEGACY" {
		t.Fatal("旧配置必须原样不动")
	}
}

func TestPrepareUnavailableWhenDirNotWritable(t *testing.T) {
	f := filepath.Join(t.TempDir(), "afile")
	writeFileT(t, f, []byte("x"))
	st := NewStore(filepath.Join(f, "sub", "config.json"))
	p := st.Prepare()
	if !p.Unavailable {
		t.Fatal("目录不可写 ⇒ 必须不可用")
	}
	if p.Code == "" || p.Detail == "" {
		t.Fatalf("必须给出可诊断的错误：%+v", p)
	}
}

func TestPrepareInvalidLegacyIsNotFatalAndKeepsFile(t *testing.T) {
	dir := t.TempDir()
	newPath := filepath.Join(dir, "config.json")
	legacy := filepath.Join(dir, "legacy.json")
	writeFileT(t, legacy, []byte("{ broken"))

	st := NewStore(newPath).SetLegacyPath(legacy)
	p := st.Prepare()
	if p.Unavailable {
		t.Fatalf("旧配置损坏不应让程序不可用（新位置本身可用）：%+v", p)
	}
	if p.Migrated {
		t.Fatal("损坏的旧配置不得迁移")
	}
	if len(p.Notes) == 0 {
		t.Fatal("⛔ 不得静默：应给出提示")
	}
	if got, _ := os.ReadFile(legacy); string(got) != "{ broken" {
		t.Fatal("旧文件必须原样保留")
	}
	// 新位置没有配置 ⇒ 正常的"首次启动空配置"。
	if r := st.Load(); r.State != StateEmpty {
		t.Fatalf("新位置应为 Empty：%+v", r)
	}
}

func TestPrepareDoesNotFallbackToLegacyPath(t *testing.T) {
	// 目录不可写时，⛔ 绝不能改成去读旧路径（那等于 fallback）。
	dir := t.TempDir()
	legacy := filepath.Join(dir, "legacy.json")
	writeFileT(t, legacy, legacyConfigBytes(t))

	f := filepath.Join(dir, "afile")
	writeFileT(t, f, []byte("x"))
	st := NewStore(filepath.Join(f, "sub", "config.json")).SetLegacyPath(legacy)

	p := st.Prepare()
	if !p.Unavailable {
		t.Fatal("目录不可写 ⇒ 必须不可用")
	}
	if p.Migrated {
		t.Fatal("⛔ 不可写时不得迁移（那会把旧路径当 fallback）")
	}
	if got, _ := os.ReadFile(legacy); string(got) != string(legacyConfigBytes(t)) {
		t.Fatal("旧文件必须原样保留")
	}
}

// --- 路径规则（decisions/006 §4.2 修订）------------------------------------

// `DefaultDir/DefaultPath` 必须指向**可执行文件所在目录**（与 WinDivert 文件、language.json 同一约定）。
func TestDefaultPathIsNextToExecutable(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Skipf("无法取得可执行文件路径：%v", err)
	}
	wantDir := filepath.Dir(exe)
	gotDir, err := DefaultDir()
	if err != nil {
		t.Fatal(err)
	}
	if gotDir != wantDir {
		t.Fatalf("DefaultDir = %q，期望 %q", gotDir, wantDir)
	}
	got, err := DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(wantDir, "config.json"); got != want {
		t.Fatalf("DefaultPath = %q，期望 %q", got, want)
	}
	// 旧路径仅用于迁移，必须与默认路径不同（否则迁移无意义）。
	if lp, err := LegacyPath(); err == nil && lp == got {
		t.Fatal("旧路径不应等于默认路径")
	}
}

// `Store` 的 .bak / .tmp 必须与 config.json **同目录**（替换语义要求）。
func TestBackupAndTmpAreInSameDir(t *testing.T) {
	st := NewStore(filepath.Join("X:", "dir", "config.json"))
	dir := filepath.Dir(st.Path())
	if filepath.Dir(st.BackupPath()) != dir || filepath.Dir(st.TmpPath()) != dir {
		t.Fatalf("bak/tmp 必须与 config.json 同目录：bak=%s tmp=%s", st.BackupPath(), st.TmpPath())
	}
}
