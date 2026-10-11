package controller

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ansmbwinhelper/internal/config"
	"ansmbwinhelper/internal/diag"
	"ansmbwinhelper/internal/model"
	"ansmbwinhelper/internal/snapshot"
)

// newPrepController 造一个 Controller，其 Store 指向 dir/config.json，并可选绑定旧路径。
func newPrepController(dir string, legacy string) *Controller {
	st := config.NewStore(filepath.Join(dir, "config.json"))
	if legacy != "" {
		st.SetLegacyPath(legacy)
	}
	return New(Options{
		Store:      st,
		Manager:    newFakeManager(),
		Inspector:  fakeInspector{},
		Version:    diag.VersionInfo{Version: "1.0-test"},
		OSInfo:     "test",
		Credential: &fakeCred{},
		Opener:     &fakeOpener{},
	})
}

// 目录不可写 ⇒ `LoadConfig` 必须进入 `ConfigUnavailable`，⛔ 且**不得**回头去读旧路径（fallback）。
func TestLoadConfigUnavailableWhenDirNotWritable(t *testing.T) {
	dir := t.TempDir()
	// "父路径是文件" ⇒ 跨平台的不可写/不可用目录。
	f := filepath.Join(dir, "afile")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 旧位置放一份**有效**配置：用来证明不可写时也⛔ 不去读它。
	legacy := filepath.Join(dir, "legacy.json")
	lb, err := config.Marshal([]model.Target{tgt("L", "10.9.9.9", 4450)})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy, lb, 0o644); err != nil {
		t.Fatal(err)
	}

	c := newPrepController(filepath.Join(f, "sub"), legacy)
	c.LoadConfig()

	snap := c.Snapshot()
	if snap.Global.Mode != snapshot.ModeConfigUnavailable {
		t.Fatalf("不可写目录必须进入 ConfigUnavailable，得到 %v", snap.Global.Mode)
	}
	if len(snap.Persisted) != 0 {
		t.Fatal("不可用状态下不得加载任何 Target")
	}
	if len(snap.Errors) == 0 {
		t.Fatal("必须记录一条可诊断的错误")
	}
	// ⛔ 不得 fallback：旧文件原样、且没有被读成配置。
	if got, err := os.ReadFile(legacy); err != nil || string(got) != string(lb) {
		t.Fatal("⛔ 不可写时不得改动/读取旧路径作为 fallback")
	}
}

// 新位置无配置 + 旧路径有有效配置 ⇒ 启动时**一次性迁移**，随后正常加载。
func TestLoadConfigMigratesLegacyConfigOnce(t *testing.T) {
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, "exedir")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(dir, "appdata", "config.json")
	if err := os.MkdirAll(filepath.Dir(legacy), 0o755); err != nil {
		t.Fatal(err)
	}
	lb, err := config.Marshal([]model.Target{
		tgtCred("t1", "10.1.1.1", 4450, "ansmb", "ansmbpw"),
		tgtCred("t2", "10.1.1.2", 4460, "u2", "p2"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy, lb, 0o644); err != nil {
		t.Fatal(err)
	}

	c := newPrepController(cfgDir, legacy)
	c.LoadConfig()

	snap := c.Snapshot()
	if snap.Global.Mode == snapshot.ModeConfigUnavailable {
		t.Fatalf("迁移后不应不可用：%+v", snap.Errors)
	}
	if len(snap.Persisted) != 2 {
		t.Fatalf("应加载 2 个目标，得到 %d", len(snap.Persisted))
	}
	if snap.Persisted[0].Password != "ansmbpw" || snap.Persisted[1].Username != "u2" {
		t.Fatal("⛔ 迁移不得丢失凭据")
	}
	// 新位置有配置；旧位置的原名文件已不存在（被改名保留）。
	if _, err := os.Stat(filepath.Join(cfgDir, "config.json")); err != nil {
		t.Fatalf("迁移后新位置应有配置：%v", err)
	}
	if _, err := os.Stat(legacy); err == nil {
		t.Fatal("迁移后旧路径原名文件应已被改名")
	}
	// 环形缓冲里必须留下迁移说明（⛔ 不得静默）。
	joined := ""
	for _, e := range snap.Recent {
		joined += e.Message + "\n"
	}
	if !strings.Contains(joined, "迁移") {
		t.Fatalf("应记录迁移说明：%s", joined)
	}
}
