// Package config 是 v1 的配置持久化层，严格对应 decisions/006（+ decisions/012 的 schema v2 修订）。
//
// 路径：%APPDATA%\anSMBwinRedirector\config.json（经由 os.UserConfigDir()）
//
//	{
//	  "schema_version": 2,
//	  "targets": [
//	    { "id": "...", "android_ip": "192.168.1.100", "android_port": 4450,
//	      "username": "ansmb", "password": "ansmb" }
//	  ]
//	}
//
// 关键规则（decisions/006 + 012）：
//   - schema_version 缺失 ⇒ 按 1 处理；**> 支持值 ⇒ 配置不可用（绝不覆盖）**
//   - **严格拒绝未知字段**（扩展走 schema_version 递增）——**按版本分别判定**：
//     `schema_version == 1` 的文件里出现 `username` / `password` 属未知字段 ⇒ 拒绝
//   - `schema_version == 1` ⇒ **内存迁移**到 v2（补空凭据）；**读取路径绝不写盘**，
//     下一次**成功保存**时才写成 v2（decisions/012 §3.2）
//   - 缺 id ⇒ 仅在**内存**生成；**任何读取路径都不得产生磁盘写入**
//   - 重复 IP / 重复 ID / 非法字段 ⇒ 整份配置不可用，**列出全部违规项**，且不做部分加载
//   - 写入：同目录临时文件 + Windows 文件替换语义（**不声称原子性**）+ 保留一份 .bak
//
// ⚠️ `config.json` 与其 `.bak` **同属敏感文件**（含明文密码，decisions/012 §3.3）。
// 密码 ⛔ 绝不进入日志 / 诊断 / 复制详情 / 错误信息。
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

// SchemaVersion 是当前支持的配置 schema 版本（decisions/012 §3.2：v1 → v2，因 Target 增加凭据字段）。
const SchemaVersion = 2

// SchemaVersionLegacyV1 是"无凭据字段"的旧版本；读到它时**内存迁移**到 v2。
const SchemaVersionLegacyV1 = 1

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
	// MigratedFrom 非 0 表示本结果由该旧版本 schema **内存迁移**而来
	// （decisions/012 §3.2）：下一次成功保存会写成 SchemaVersion。
	MigratedFrom int
}

// fileSchemaV1 是 schema_version == 1 的严格形状（**没有**凭据字段）。
type fileSchemaV1 struct {
	SchemaVersion *int             `json:"schema_version"`
	Targets       []targetSchemaV1 `json:"targets"`
}

type targetSchemaV1 struct {
	ID          string  `json:"id"`
	AndroidIP   string  `json:"android_ip"`
	AndroidPort *uint16 `json:"android_port"`
}

// fileSchema 是 schema_version >= 2 的严格形状。
type fileSchema struct {
	SchemaVersion *int           `json:"schema_version"`
	Targets       []targetSchema `json:"targets"`
}

type targetSchema struct {
	ID          string  `json:"id"`
	AndroidIP   string  `json:"android_ip"`
	AndroidPort *uint16 `json:"android_port"`
	Username    string  `json:"username"`
	Password    string  `json:"password"`
}

// versionProbe 只用于**先读出版本号**，不启用严格字段检查。
type versionProbe struct {
	SchemaVersion *int `json:"schema_version"`
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

// rawTarget 是"解码后、校验前"的统一形状（v1 的凭据字段为空）。
type rawTarget struct {
	ID          string
	AndroidIP   string
	AndroidPort *uint16
	Username    string
	Password    string
}

// errTrailingContent 表示 JSON 对象之后还有多余内容。
var errTrailingContent = errors.New("配置文件在首个 JSON 对象之后还有多余内容")

// decodeStrict 做**严格**解码：拒绝未知字段，且拒绝尾随内容（decisions/006 §6.2）。
func decodeStrict(data []byte, dst any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errTrailingContent
	}
	return nil
}

// Parse 解析配置字节。纯函数，便于单测；**不产生任何磁盘写入**。
//
// 两段式：先只读 `schema_version`，再按版本做**严格**解码 —— 这样
// `schema_version == 1` 的文件里出现 `username`/`password` 会被正确判为"未知字段"。
func Parse(data []byte) Result {
	// ① 版本探测（不启用严格字段检查）。
	var probe versionProbe
	if err := json.Unmarshal(data, &probe); err != nil {
		return Result{
			State:      StateUnavailable,
			Code:       diag.CodeConfigParseError,
			Detail:     err.Error(),
			Violations: []string{err.Error()},
		}
	}
	ver := SchemaVersionLegacyV1 // 缺失 ⇒ 按 1 处理（decisions/006 §6.2）
	if probe.SchemaVersion != nil {
		ver = *probe.SchemaVersion
	}
	if ver > SchemaVersion {
		// decisions/006 §6.2：由更高版本写入 ⇒ 不可用，且**不提供**"以空配置重开"。
		return Result{
			State:  StateUnavailable,
			Code:   diag.CodeConfigSchemaUnsupported,
			Detail: fmt.Sprintf("schema_version=%d，本程序支持到 %d", ver, SchemaVersion),
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

	// ② 按版本严格解码。
	var rawTargets []rawTarget
	switch ver {
	case SchemaVersionLegacyV1:
		var f fileSchemaV1
		if err := decodeStrict(data, &f); err != nil {
			return strictDecodeFailure(err)
		}
		for _, ts := range f.Targets {
			rawTargets = append(rawTargets, rawTarget{ID: ts.ID, AndroidIP: ts.AndroidIP, AndroidPort: ts.AndroidPort})
		}
	default:
		var f fileSchema
		if err := decodeStrict(data, &f); err != nil {
			return strictDecodeFailure(err)
		}
		for _, ts := range f.Targets {
			rawTargets = append(rawTargets, rawTarget{
				ID: ts.ID, AndroidIP: ts.AndroidIP, AndroidPort: ts.AndroidPort,
				Username: ts.Username, Password: ts.Password,
			})
		}
	}

	// ③ 逐项校验。
	var (
		targets    []model.Target
		violations []string
		genIDs     bool
	)
	for i, ts := range rawTargets {
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
		targets = append(targets, model.Target{
			ID: id, AndroidIP: ip, AndroidPort: *ts.AndroidPort,
			Username: ts.Username, Password: ts.Password,
		})
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

	res := Result{State: StateLoaded, Targets: targets, GeneratedIDs: genIDs}
	if ver < SchemaVersion {
		// decisions/012 §3.2：内存迁移。**读取路径不写盘**；下一次成功保存才写成 v2。
		res.MigratedFrom = ver
	}
	return res
}

// strictDecodeFailure 把严格解码错误映射为 diag 码。
func strictDecodeFailure(err error) Result {
	code := diag.CodeConfigParseError
	if strings.Contains(err.Error(), "unknown field") {
		// decisions/006 §6.2：未知字段 ⇒ 校验失败（而不是"悄悄忽略"）。
		code = diag.CodeConfigValidationFailed
	}
	return Result{
		State:      StateUnavailable,
		Code:       code,
		Detail:     err.Error(),
		Violations: []string{err.Error()},
	}
}

// Marshal 生成待写入的配置字节（固定缩进、末尾换行）。
//
// ⚠️ 输出**包含明文密码**（decisions/012 §3.3 的明确取舍）；调用方 ⛔ 不得把返回值
// 写入日志 / 诊断 / 错误信息。
func Marshal(ts []model.Target) ([]byte, error) {
	f := fileSchema{SchemaVersion: intPtr(SchemaVersion), Targets: make([]targetSchema, 0, len(ts))}
	for _, t := range ts {
		p := t.AndroidPort
		f.Targets = append(f.Targets, targetSchema{
			ID:          t.ID,
			AndroidIP:   t.AndroidIP.String(),
			AndroidPort: &p,
			Username:    t.Username,
			Password:    t.Password,
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
