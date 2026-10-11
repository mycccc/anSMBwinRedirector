package config

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"ansmbwinhelper/internal/diag"
)

// 本文件实现 decisions/006 §4.2 的 **2026-10-10 修订**（配置迁移到 EXE 同目录）所需的两个动作：
//
//	① `ProbeWritable` —— 启动时的轻量写探测（裁定 B1）
//	② `MigrateLegacy`  —— 旧 `%APPDATA%` 配置的**一次性迁移**（裁定 A3）
//
// ⛔ 两者都**会写盘**，因此都不在 `Load()` 里：`Load` 仍然保证"读取路径不产生任何磁盘写入"。

// writeProbePrefix 是写探测文件名的前缀。
//
// ⚠️ 探测文件与用户文件**同名风险为零**（前缀不同 + 含 pid 与随机数），
// 且以 `O_EXCL` 创建 ⇒ **绝不覆盖任何已存在的文件**；只删除本次创建的那一个。
const writeProbePrefix = ".ansmbwinredirector-write-probe-"

// ProbeWritable 在 dir 内做一次轻量写探测（裁定 B1）。
//
// 语义：
//
//	创建**专用且唯一**的探测文件 → 写入少量字节 → `Sync` → 关闭 → 删除；
//	任何一步失败（含**清理失败**）都返回错误 —— 调用方据此进入 `ConfigUnavailable`。
//
// ⛔ 不使用 `config.json` / `.bak` / `.tmp` 这类用户文件名 ⇒ **不会**因探测产生空的 `config.json`。
// ⛔ 只删除本次创建的文件（`O_EXCL` 保证创建的就是我们自己的）。
//
// ⚠️ 探测成功**不代表**实际保存一定成功（配额、并发占用、替换语义等仍可能在保存时失败）——
// 保存路径必须独立检查所有错误（见 `Store.Save`）。
func ProbeWritable(dir string) error {
	suffix := make([]byte, 6)
	if _, err := rand.Read(suffix); err != nil {
		return fmt.Errorf("无法生成探测文件名: %w", err)
	}
	name := fmt.Sprintf("%s%d-%s.tmp", writeProbePrefix, os.Getpid(), hex.EncodeToString(suffix))
	full := filepath.Join(dir, name)

	f, err := os.OpenFile(full, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("配置目录不可写（%s）：%w", dir, err)
	}
	if _, err := f.Write([]byte("probe")); err != nil {
		f.Close()
		_ = os.Remove(full)
		return fmt.Errorf("配置目录不可写（%s）：写入探测文件失败: %w", dir, err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		_ = os.Remove(full)
		return fmt.Errorf("配置目录不可写（%s）：刷新探测文件失败: %w", dir, err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(full)
		return fmt.Errorf("配置目录不可写（%s）：关闭探测文件失败: %w", dir, err)
	}
	if err := os.Remove(full); err != nil {
		// 清理失败 ⇒ 报告（⛔ 不得当作"可写"）：目录里可能留下一个探测文件。
		return fmt.Errorf("配置目录可写但探测文件无法清理（残留 %s）：%w", full, err)
	}
	return nil
}

// MigrateOutcome 是一次性迁移的结果（裁定 A3）。
type MigrateOutcome struct {
	// Attempted 表示"确实尝试过迁移"（新路径无配置 + 旧路径存在）。
	Attempted bool
	// Migrated 表示迁移**完整成功**（新配置已保存并校验、旧文件已改名）。
	Migrated bool
	// LegacyInvalid 表示旧配置存在但**读取/校验失败** ⇒ 未迁移，旧文件原样保留。
	LegacyInvalid bool
	// Detail 是失败/无效原因（⛔ 不含密码）。
	Detail string
	// LegacyMovedTo 是迁移成功后旧文件的新路径。
	LegacyMovedTo string
	// RenameWarning 非空表示"新配置已就绪，但旧文件改名失败"（旧文件原样保留）。
	RenameWarning string
	// Notes 是供环形缓冲记录的说明（②层诊断数据，不本地化；⛔ 不含密码）。
	Notes []string
}

// MigrateLegacy 执行旧位置配置的**一次性迁移**（裁定 A3）。
//
// 顺序（⛔ 不得颠倒）：
//
//	① 读 + **校验**旧配置：不合法 ⇒ 直接返回（不写、不改名），旧文件原样保留；
//	② **安全写入**新路径（同目录临时文件 + 替换语义；新路径原本不存在 ⇒ 不产生 `.bak`）；
//	③ **读回并逐字节比对**：不一致 ⇒ 回滚本次创建的新文件并报失败；
//	④ 旧文件改名为 `config.json.migrated-<时间戳>`（重名自动加序号）。
//
// ⛔ 任何一步失败：**旧文件内容保持原样**，且 ⛔ **不得**报告迁移成功。
// ⛔ 前置由调用方保证：新路径**不存在**、旧路径**存在**（本函数会再次确认，避免误用）。
func MigrateLegacy(newPath, legacyPath string) MigrateOutcome {
	var out MigrateOutcome
	out.Attempted = true

	if fileExists(newPath) {
		// 以新路径为准，⛔ 绝不覆盖（调用方不该在此情况下调用；再确认一次更安全）。
		out.Detail = fmt.Sprintf("新位置已存在配置，未迁移：%s", newPath)
		return out
	}
	if !fileExists(legacyPath) {
		out.Attempted = false
		return out
	}

	// ① 读取 + 校验（复用 Parse：严格解码 + 全量校验 + schema 迁移判定）。
	raw, err := os.ReadFile(legacyPath)
	if err != nil {
		out.Detail = fmt.Sprintf("无法读取旧配置（原文件未改动）：%s：%v", legacyPath, err)
		out.Notes = append(out.Notes, "旧位置配置无法读取，未迁移（原文件未改动）："+legacyPath)
		return out
	}
	res := Parse(raw)
	if res.State != StateLoaded {
		out.LegacyInvalid = true
		out.Detail = fmt.Sprintf("旧配置不可用（未迁移、未改动）：%s：%s", legacyPath, res.Detail)
		out.Notes = append(out.Notes,
			fmt.Sprintf("旧位置的配置无法使用，未迁移（原文件未改动）：%s（%s）", legacyPath, res.Code))
		return out
	}

	// ② 安全写入新路径（复用 Store.Save：校验 → 同目录 tmp → 替换语义）。
	st := NewStore(newPath)
	if err := st.Save(res.Targets); err != nil {
		out.Detail = fmt.Sprintf("写入新配置失败（旧配置未改动）：%s：%v", newPath, err)
		out.Notes = append(out.Notes, "配置迁移失败，旧配置原样保留："+legacyPath)
		return out
	}

	// ③ 读回并逐字节比对（Marshal 是确定性的）。
	want, err := Marshal(res.Targets)
	if err != nil {
		rollbackNew(newPath)
		out.Detail = fmt.Sprintf("迁移后序列化失败（已回滚新文件）：%v", err)
		out.Notes = append(out.Notes, "配置迁移失败（已回滚新文件），旧配置原样保留："+legacyPath)
		return out
	}
	got, err := os.ReadFile(newPath)
	if err != nil || !bytes.Equal(got, want) {
		rollbackNew(newPath)
		out.Detail = fmt.Sprintf("迁移后校验不一致（已回滚新文件）：%s", newPath)
		out.Notes = append(out.Notes, "配置迁移校验失败（已回滚新文件），旧配置原样保留："+legacyPath)
		return out
	}

	// ④ 旧文件改名（内容完整保留、⛔ 不删除）。
	dst, rerr := renameLegacy(legacyPath)
	if rerr != nil {
		out.RenameWarning = fmt.Sprintf("旧文件改名失败（旧文件原样保留）：%s：%v", legacyPath, rerr)
		out.Detail = out.RenameWarning
		out.Notes = append(out.Notes,
			fmt.Sprintf("配置已迁移到新位置，但旧文件改名失败（旧文件原样保留）：%s", legacyPath))
		return out
	}
	out.Migrated = true
	out.LegacyMovedTo = dst
	out.Notes = append(out.Notes,
		fmt.Sprintf("已把旧位置的配置迁移到 %s（原文件改名为 %s，内容保留）", newPath, dst))
	return out
}

// rollbackNew 删除**本次迁移自己创建**的新配置文件（⛔ 不触碰旧文件）。
func rollbackNew(newPath string) {
	if err := os.Remove(newPath); err != nil {
		// 回滚失败也不能掩盖原问题；调用方已把失败写进 Detail/Notes。
		return
	}
}

// renameLegacy 把旧配置改名为 `config.json.migrated-<时间戳>`；重名时追加序号。
//
// ⚠️ 与 `Quarantine` 同一原则：**只改名**，不删除、不覆盖 —— 内容完整保留在新名字下。
func renameLegacy(path string) (string, error) {
	ts := time.Now().Format("20060102-150405")
	for i := 0; i < 100; i++ {
		name := fmt.Sprintf("%s.migrated-%s", path, ts)
		if i > 0 {
			name = fmt.Sprintf("%s.migrated-%s-%d", path, ts, i)
		}
		if fileExists(name) {
			continue // 重名 ⇒ 换一个
		}
		if err := os.Rename(path, name); err != nil {
			return "", err
		}
		return name, nil
	}
	return "", errors.New("同名备份文件过多（100 次重名），未做任何改动")
}

// PrepResult 是"加载前准备"（写探测 + 一次性迁移）的结果。
type PrepResult struct {
	// Unavailable 表示必须进入 `ConfigUnavailable`（⛔ 不 fallback、⛔ 不以空配置继续）。
	Unavailable bool
	Code        diag.Code
	Detail      string
	// Migrated 表示本次启动完成了一次性迁移。
	Migrated bool
	// Notes 供环形缓冲记录（②层诊断数据；⛔ 不含密码）。
	Notes []string
}

// Prepare 执行加载前的两个动作（decisions/006 §4.2 修订 / 裁定 B1 + A3）：
//
//	① 写探测：配置目录不可写 ⇒ `ConfigUnavailable`（⛔ 不 fallback 到旧路径）
//	② 一次性迁移：仅当**新路径无配置**且**旧路径有配置**时（`legacyPath` 为空则跳过）
//
// ⚠️ 本函数**会写盘**（探测文件；迁移时写新配置），因此必须与 `Load()` **分开**显式调用。
// ⛔ 新路径已有配置时：以新路径为准，⛔ 不改动旧文件。
func (s *Store) Prepare() PrepResult {
	var p PrepResult
	dir := filepath.Dir(s.path)

	// 与 `Save` 一致：目录不存在时先创建（exe 目录通常已存在，这里只为自洽与首次运行兜底）。
	if err := os.MkdirAll(dir, 0o755); err != nil {
		p.Unavailable = true
		p.Code = diag.CodeConfigIOError
		p.Detail = fmt.Sprintf("配置目录不可用（%s）：%v", dir, err)
		return p
	}

	if err := ProbeWritable(dir); err != nil {
		// ⛔ 不 fallback 到旧路径；⛔ 不以空配置继续（decisions/006 §6.10）。
		p.Unavailable = true
		p.Code = diag.CodeConfigIOError
		p.Detail = fmt.Sprintf("配置目录不可写（%s）：%v", dir, err)
		return p
	}

	if s.legacyPath == "" || s.legacyPath == s.path {
		return p
	}
	if fileExists(s.path) {
		// 以新路径为准；旧文件存在时只做一次**提示**，⛔ 不改动它。
		if fileExists(s.legacyPath) {
			p.Notes = append(p.Notes,
				fmt.Sprintf("检测到旧位置仍有配置；已按新位置优先，未改动旧文件：%s", s.legacyPath))
		}
		return p
	}
	if !fileExists(s.legacyPath) {
		return p
	}

	m := MigrateLegacy(s.path, s.legacyPath)
	p.Notes = append(p.Notes, m.Notes...)
	p.Migrated = m.Migrated
	if !m.Migrated && !m.LegacyInvalid && m.Detail != "" {
		// 迁移"尝试过但没成功"，且不属于"旧配置无效"（那已用 Note 说明）：
		// 新路径仍然没有配置 ⇒ 按"空配置"继续是可接受的（⛔ 旧文件未被改动、未丢数据）。
		p.Notes = append(p.Notes, "本次未完成配置迁移："+m.Detail)
	}
	return p
}

// LegacyDir 返回**历史的** per-user 配置目录 `%APPDATA%\anSMBwinRedirector`。
//
// ⚠️ 迁移之后的版本**不再**读写它；它只用于"一次性迁移"的读取来源（decisions/006 §4.2 修订）。
func LegacyDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, AppDirName), nil
}

// LegacyPath 返回历史的配置文件路径 `%APPDATA%\anSMBwinRedirector\config.json`。
func LegacyPath() (string, error) {
	dir, err := LegacyDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil
}
