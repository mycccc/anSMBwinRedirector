// Package config 是 v1 的配置持久化层，严格对应 decisions/006。
//
// 路径：%APPDATA%\anSMBwinRedirector\config.json（经由 os.UserConfigDir()）
//
//	{
//	  "schema_version": 1,
//	  "targets": [ { "id": "...", "android_ip": "192.168.1.100", "android_port": 4450 } ]
//	}
//
// 关键规则（decisions/006）：
//   - schema_version 缺失 ⇒ 按 1 处理；**> 支持值 ⇒ 配置不可用（绝不覆盖）**
//   - **严格拒绝未知字段**（扩展走 schema_version 递增）
//   - 缺 id ⇒ 仅在**内存**生成；**任何读取路径都不得产生磁盘写入**
//   - 重复 IP / 重复 ID / 非法字段 ⇒ 整份配置不可用，**列出全部违规项**，且不做部分加载
//   - 写入：同目录临时文件 + Windows 文件替换语义（**不声称原子性**）+ 保留一份 .bak
package config

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"time"

	"ansmbwinhelper/internal/diag"
	"ansmbwinhelper/internal/model"
)

// SchemaVersion 是当前支持的配置 schema 版本。
const SchemaVersion = 1

// AppDirName 是配置与 runtime 目录使用的产品名。
const AppDirName = "anSMBwinRedirector"

// State 是配置加载结果的状态（decisions/006 §6.10）。
type State int

const (
	// StateLoaded：加载成功（含"文件存在但 targets 为空"）。
	StateLoaded State = iota
	// StateEmpty：文件不存在 ⇒ 正常首次启动（空配置）。
	StateEmpty
	// StateUnavailable：读取/解析/校验失败 ⇒ 不加载任何 Target、禁止 Start、禁止保存、绝不覆盖。
	StateUnavailable
)

func (s State) String() string {
	switch s {
	case StateLoaded:
		return "Loaded"
	case StateEmpty:
		return "Empty"
	default:
		return "Unavailable"
	}
}

// Result 是一次加载的结果。
type Result struct {
	State      State
	Targets    []model.Target
	Code       diag.Code
	Detail     string
	Violations []string
	// GeneratedIDs 表示有 Target 的 ID 是内存中生成的 ⇒ 下一次**成功保存**时才回写。
	GeneratedIDs bool
}

type fileSchema struct {
	SchemaVersion *int           `json:"schema_version"`
	Targets       []targetSchema `json:"targets"`
}

type targetSchema struct {
	ID          string  `json:"id"`
	AndroidIP   string  `json:"android_ip"`
	AndroidPort *uint16 `json:"android_port"`
}

// NewID 生成一个 UUIDv4 形式的稳定 ID（crypto/rand，无第三方依赖）。
//
// ID 是身份：创建时生成一次、不可编辑、持久化、跨 IP/端口编辑保持不变
// （decisions/005 §6.1 / 006 §6.5）。
func NewID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant RFC4122
	h := hex.EncodeToString(b[:])
	return fmt.Sprintf("%s-%s-%s-%s-%s", h[0:8], h[8:12], h[12:16], h[16:20], h[20:32]), nil
}

// Parse 解析配置字节。纯函数，便于单测；**不产生任何磁盘写入**。
func Parse(data []byte) Result {
	var f fileSchema
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		if strings.Contains(err.Error(), "unknown field") {
			// decisions/006 §6.2：未知字段 ⇒ 校验失败（而不是"悄悄忽略"）。
			return Result{
				State:      StateUnavailable,
				Code:       diag.CodeConfigValidationFailed,
				Detail:     err.Error(),
				Violations: []string{err.Error()},
			}
		}
		return Result{
			State:      StateUnavailable,
			Code:       diag.CodeConfigParseError,
			Detail:     err.Error(),
			Violations: []string{err.Error()},
		}
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return Result{
			State:      StateUnavailable,
			Code:       diag.CodeConfigParseError,
			Detail:     "配置文件在首个 JSON 对象之后还有多余内容",
			Violations: []string{"配置文件尾部有多余内容"},
		}
	}

	ver := SchemaVersion
	if f.SchemaVersion != nil {
		ver = *f.SchemaVersion
	}
	if ver > SchemaVersion {
		// decisions/006 §6.2：由更高版本写入 ⇒ 不可用，且**不提供**"以空配置重开"。
		return Result{
			State:  StateUnavailable,
			Code:   diag.CodeConfigSchemaUnsupported,
			Detail: fmt.Sprintf("schema_version=%d，本程序支持 %d", ver, SchemaVersion),
		}
	}
	if ver < 1 {
		return Result{
			State:      StateUnavailable,
			Code:       diag.CodeConfigValidationFailed,
			Detail:     fmt.Sprintf("schema_version=%d 非法", ver),
			Violations: []string{fmt.Sprintf("schema_version=%d 非法", ver)},
		}
	}

	var (
		targets    []model.Target
		violations []string
		genIDs     bool
	)
	for i, ts := range f.Targets {
		where := fmt.Sprintf("targets[%d]", i)

		id := ts.ID
		if id == "" {
			// decisions/006 §6.5：缺 id ⇒ 自动生成，但**只存在于内存**。
			gen, err := NewID()
			if err != nil {
				return Result{State: StateUnavailable, Code: diag.CodeConfigIOError, Detail: err.Error()}
			}
			id = gen
			genIDs = true
		}

		ip, err := parseIP(ts.AndroidIP)
		if err != nil {
			violations = append(violations, fmt.Sprintf("%s.android_ip: %v", where, err))
			continue
		}
		if ts.AndroidPort == nil {
			// 端口必须显式给出：静默套用默认值会让"重定向到哪"变得不可预期。
			violations = append(violations, fmt.Sprintf("%s.android_port: 缺少该字段", where))
			continue
		}
		targets = append(targets, model.Target{ID: id, AndroidIP: ip, AndroidPort: *ts.AndroidPort})
	}

	if len(targets) > 0 {
		// 对"已成功解析的子集"也做模型校验，尽量把问题报全
		// （decisions/006 §6.10：列出全部违规项，便于一次修完）。
		violations = append(violations, model.CollectViolations(targets)...)
	}
	if len(violations) > 0 {
		// 整份配置无效：**不做部分加载**（decisions/006 §6.10）。
		return Result{
			State:      StateUnavailable,
			Code:       diag.CodeConfigValidationFailed,
			Detail:     strings.Join(violations, "; "),
			Violations: violations,
		}
	}
	return Result{State: StateLoaded, Targets: targets, GeneratedIDs: genIDs}
}

// Marshal 生成待写入的配置字节（固定缩进、末尾换行）。
func Marshal(ts []model.Target) ([]byte, error) {
	f := fileSchema{SchemaVersion: intPtr(SchemaVersion), Targets: make([]targetSchema, 0, len(ts))}
	for _, t := range ts {
		p := t.AndroidPort
		f.Targets = append(f.Targets, targetSchema{
			ID:          t.ID,
			AndroidIP:   t.AndroidIP.String(),
			AndroidPort: &p,
		})
	}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// Store 负责配置文件的读与写。
type Store struct {
	path string
}

// DefaultDir 返回 %APPDATA%\anSMBwinRedirector。
func DefaultDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, AppDirName), nil
}

// DefaultPath 返回 %APPDATA%\anSMBwinRedirector\config.json。
func DefaultPath() (string, error) {
	dir, err := DefaultDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil
}

// NewStore 创建 Store。
func NewStore(path string) *Store { return &Store{path: path} }

// Path 返回配置文件路径。
func (s *Store) Path() string { return s.path }

// BackupPath 返回备份文件路径（decisions/006 §6.11）。
func (s *Store) BackupPath() string { return s.path + ".bak" }

// TmpPath 返回临时文件路径（与目标同目录，保证替换语义）。
func (s *Store) TmpPath() string { return s.path + ".tmp" }

// BackupExists 报告是否存在备份文件。
func (s *Store) BackupExists() bool {
	_, err := os.Stat(s.BackupPath())
	return err == nil
}

// Load 读取并解析配置。
//
// ⛔ **不产生任何磁盘写入**（decisions/006 §6.5）：文件不存在 ⇒ StateEmpty，不创建文件。
func (s *Store) Load() Result {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Result{State: StateEmpty}
		}
		return Result{
			State:  StateUnavailable,
			Code:   diag.CodeConfigIOError,
			Detail: err.Error(),
		}
	}
	return Parse(data)
}

// Save 原子写入配置：校验 → 写临时文件 → 同目录替换（并保留上一版为 .bak）。
//
// ⚠️ 不声称"原子性"（decisions/006 §6.11）：v1 只保证
// ① 不先删除当前有效文件；② 以尽量少的 OS 调用完成切换。
// **保存失败不得破坏当前有效的 config.json。**
func (s *Store) Save(ts []model.Target) error {
	if err := model.ValidateTargets(ts); err != nil {
		return fmt.Errorf("拒绝写入非法配置: %w", err)
	}
	data, err := Marshal(ts)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	tmp := s.TmpPath()
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return replaceFile(s.path, tmp, s.BackupPath())
}

func parseIP(s string) (netip.Addr, error) {
	if strings.TrimSpace(s) == "" {
		return netip.Addr{}, errors.New("缺少该字段")
	}
	a, err := netip.ParseAddr(strings.TrimSpace(s))
	if err != nil {
		return netip.Addr{}, fmt.Errorf("不是合法的 IPv4 地址: %s", s)
	}
	if !a.Is4() {
		return netip.Addr{}, fmt.Errorf("必须是 IPv4 字面地址: %s", s)
	}
	return a, nil
}

func intPtr(v int) *int { return &v }

// copyFile 逐字节复制（用于生成 .bak；**不是** rename，因此不会出现"目标不存在"的窗口）。
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// Quarantine 把当前配置文件改名为 `config.json.corrupt-<timestamp>`，
// 使程序可以在"空配置"下重新开始（decisions/006 §6.10 的**动作 ②**）。
//
// ⚠️ 只做**改名**：不删除、不覆盖任何内容 —— 原文件内容完整保留在新名字下。
// ⛔ 本函数是**用户显式动作**的唯一实现；任何读取路径都不得调用它。
func (s *Store) Quarantine() (string, error) {
	if !fileExists(s.path) {
		return "", fmt.Errorf("配置文件不存在：%s", s.path)
	}
	dst := fmt.Sprintf("%s.corrupt-%s", s.path, time.Now().Format("20060102-150405"))
	if fileExists(dst) {
		return "", fmt.Errorf("目标文件名已存在，未做任何改动：%s", dst)
	}
	if err := os.Rename(s.path, dst); err != nil {
		return "", fmt.Errorf("改名失败（原文件未改动）：%w", err)
	}
	return dst, nil
}
