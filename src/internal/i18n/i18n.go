// Package i18n 是 v1 的 GUI 本地化层，严格对应 decisions/013。
//
// 载体：**单文件 `language.json`**（EXE 同目录，UTF-8）。
//
//	{
//	  "en":     { "main.start": "Start" },
//	  "zh-CN":  { "main.start": "启动" }
//	}
//
// 关键规则（decisions/013）：
//   - **扁平点号 key**（`main.start`），不是嵌套树；代码里 key 是稳定字符串
//   - v1 语言 = `en` + `zh-CN`；**默认 English**；Windows UI 语言为中文 ⇒ `zh-CN`；其它 ⇒ `en`
//   - **启动时确定语言**；⛔ 不做运行时切换
//   - `language.json` **不是用户配置**（无 schema_version、⛔ GUI 不得修改、⛔ 不做 hash 校验）
//   - 文件缺失/损坏 ⇒ **不阻止启动**：用**极少量内置 English fallback** + 记 Issue
//   - 缺 key ⇒ 逐级 fallback（目标语言 → `en` → 内置）并记 Issue
//   - 读取时**容忍 UTF-8 BOM**；写出（本包不写）统一无 BOM
//
// ⛔ 内部契约（Error Code / JSON 字段名 / 服务名 / 文件名 / 协议常量）**不本地化**。
//
// 本包是纯逻辑（除 SystemLanguage 的平台实现外），可在任意平台单测。
package i18n

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// FileName 是语言资源文件名（EXE 同目录，decisions/013 §5）。
const FileName = "language.json"

// 支持的语言标识（decisions/013 §1）。
const (
	LanguageEnglish           = "en"
	LanguageChineseSimplified = "zh-CN"
)

// Catalog 是 language.json 的内存形状：语言 → (key → 文案)。
type Catalog map[string]map[string]string

// IssueKind 是本地化降级事件的种类（**内部标识，不本地化**）。
type IssueKind string

const (
	// IssueResourceUnavailable：文件缺失 / 无法解析 / 缺少 `en` 块。
	IssueResourceUnavailable IssueKind = "LANGUAGE_RESOURCE_UNAVAILABLE"
	// IssueKeyMissing：某个 key 在目标语言与 `en` 中都缺失。
	IssueKeyMissing IssueKind = "LANGUAGE_KEY_MISSING"
)

// Issue 是一条本地化降级事件（供上层转成 DiagnosticEntry，decisions/013 §4）。
type Issue struct {
	Kind   IssueKind
	Detail string
}

// --- 进程级默认翻译器 -------------------------------------------------------
//
// ⚠️ 为什么需要它：用户可见文案的**产出点不止 GUI 包** —— 状态摘要由 snapshot 生产、
// 错误摘要/提示/复制详情模板由 diag 生产、启动阻塞提示由 app 生产（decisions/013 §6.2）。
// 这些包不能各自去加载语言文件，因此提供一个**启动时注入一次**的进程级默认翻译器。
//
// ⛔ 仍然 **不做运行时切换**（decisions/013 §1）：入口在启动时 SetDefault 一次。
var (
	defaultMu sync.RWMutex
	defaultTr *Translator
)

// SetDefault 设置进程级默认翻译器（入口在启动时调用一次，注入 nil 会被忽略）。
func SetDefault(t *Translator) {
	if t == nil {
		return
	}
	defaultMu.Lock()
	defaultTr = t
	defaultMu.Unlock()
}

// Default 返回进程级默认翻译器；未注入时返回一个"只有内置 fallback"的实例
// （⇒ 不 panic、不阻止启动，decisions/013 §4.1）。
func Default() *Translator {
	defaultMu.RLock()
	t := defaultTr
	defaultMu.RUnlock()
	if t != nil {
		return t
	}
	return New(Catalog{}, LanguageEnglish)
}

// T 用默认翻译器解析 key（供**非 GUI** 的用户可见文案产出点使用）。
func T(key string) string { return Default().T(key) }

// --- 内置 fallback ---------------------------------------------------------
//
// ⚠️ ⛔ **不得扩充为完整的 English 文案表** —— 那会产生"第二份真相"。
// 它只保证"语言资源不可用时程序仍能启动并说明情况"。
var builtinFallback = map[string]string{
	"app.title":                      "anSMBwinRedirector",
	"error.localization_unavailable": "Language resource unavailable (language.json missing or invalid).",
	"action.quit":                    "Quit",
	"action.retry":                   "Retry",
}

// BuiltinFallbackKeys 返回内置 fallback 的 key 集合（只读用途，测试据此防止其膨胀）。
func BuiltinFallbackKeys() []string {
	ks := make([]string, 0, len(builtinFallback))
	for k := range builtinFallback {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// Translator 把 key 解析为当前语言的文案。
type Translator struct {
	lang     string
	cat      Catalog
	issues   []Issue
	reported map[string]struct{}
}

// Resolve 把 Windows UI 语言（如 `zh-CN` / `zh-TW` / `en-US`）映射为受支持的语言标识。
//
// ⛔ **不做 zh-TW / zh-HK 细分**（decisions/013 §1 / §10）：任何 `zh*` ⇒ `zh-CN`，其余 ⇒ `en`。
func Resolve(uiLanguage string) string {
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(uiLanguage)), "zh") {
		return LanguageChineseSimplified
	}
	return LanguageEnglish
}

// New 用给定 catalog 与语言构造 Translator（纯函数，便于单测）。
//
// 若 catalog 缺少 `en` 块 ⇒ 记 IssueResourceUnavailable（仍可用内置 fallback）。
func New(cat Catalog, lang string) *Translator {
	if cat == nil {
		cat = Catalog{}
	}
	t := &Translator{lang: lang, cat: cat, reported: map[string]struct{}{}}
	if _, ok := cat[LanguageEnglish]; !ok {
		t.issue(IssueResourceUnavailable, "language.json 缺少 en 块")
		t.lang = LanguageEnglish
		return t
	}
	if _, ok := cat[lang]; !ok {
		// decisions/013 §4：目标语言块不存在 ⇒ 退回 en，**不报错**。
		t.lang = LanguageEnglish
	}
	return t
}

// LoadDefault 从 **EXE 同目录** 加载 `language.json`，语言由系统决定。
//
// ⛔ 任何失败都**不阻止启动**，只记录 Issue。
func LoadDefault() *Translator {
	p, err := DefaultPath()
	if err != nil {
		t := New(Catalog{}, LanguageEnglish)
		t.issue(IssueResourceUnavailable, "无法定位程序目录: "+err.Error())
		return t
	}
	return LoadFile(p)
}

// DefaultPath 返回 EXE 同目录下的 `language.json` 路径。
func DefaultPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(exe), FileName), nil
}

// LoadFile 读取并解析语言资源；语言由 SystemLanguage() 决定。
func LoadFile(path string) *Translator {
	data, err := os.ReadFile(path)
	if err != nil {
		t := New(Catalog{}, LanguageEnglish)
		t.issue(IssueResourceUnavailable, "读取失败: "+err.Error())
		return t
	}
	cat, err := ParseCatalog(data)
	if err != nil {
		t := New(Catalog{}, LanguageEnglish)
		t.issue(IssueResourceUnavailable, "解析失败: "+err.Error())
		return t
	}
	return New(cat, Resolve(SystemLanguage()))
}

// ParseCatalog 解析语言资源字节；**容忍 UTF-8 BOM**（用户可能用记事本另存）。
func ParseCatalog(data []byte) (Catalog, error) {
	data = stripBOM(data)
	var cat Catalog
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&cat); err != nil {
		return nil, err
	}
	return cat, nil
}

func stripBOM(b []byte) []byte {
	return bytes.TrimPrefix(b, []byte{0xEF, 0xBB, 0xBF})
}

// Language 返回当前生效的语言标识。
func (t *Translator) Language() string {
	if t == nil {
		return LanguageEnglish
	}
	return t.lang
}

// T 把 key 解析为文案，降级链：目标语言 → `en` → 内置 → **key 本身**。
//
// 命中"目标语言缺失但 en 有"或"两者都缺"时记录 Issue（同类同 key 只记一次，避免刷屏）。
func (t *Translator) T(key string) string {
	if t == nil {
		return fallbackOrKey(key)
	}
	if m := t.cat[t.lang]; m != nil {
		if s, ok := m[key]; ok && s != "" {
			return s
		}
	}
	if t.lang != LanguageEnglish {
		if m := t.cat[LanguageEnglish]; m != nil {
			if s, ok := m[key]; ok && s != "" {
				t.issueOnce("key:"+key, IssueKeyMissing, fmt.Sprintf("%s 缺少 key %q，已回退到 %s", t.lang, key, LanguageEnglish))
				return s
			}
		}
	}
	return fallbackOrKey(key)
}

// fallbackOrKey 返回内置 fallback；都没有 ⇒ 返回 key 本身（可见但可诊断）。
func fallbackOrKey(key string) string {
	if s, ok := builtinFallback[key]; ok {
		return s
	}
	return key
}

// Issues 返回本次本地化过程中记录的事件（**不含**密码等敏感数据）。
func (t *Translator) Issues() []Issue {
	if t == nil {
		return nil
	}
	return t.issues
}

func (t *Translator) issue(kind IssueKind, detail string) {
	t.issues = append(t.issues, Issue{Kind: kind, Detail: detail})
}

func (t *Translator) issueOnce(dedupKey string, kind IssueKind, detail string) {
	if _, seen := t.reported[dedupKey]; seen {
		return
	}
	t.reported[dedupKey] = struct{}{}
	t.issue(kind, detail)
}

// KeySetsEqual 报告两个语言块的 key 集合是否完全一致（decisions/013 §6.1 判据 D）。
func (c Catalog) KeySetsEqual(a, b string) (bool, []string) {
	ma, mb := c[a], c[b]
	var diff []string
	for k := range ma {
		if _, ok := mb[k]; !ok {
			diff = append(diff, a+" 有而 "+b+" 缺: "+k)
		}
	}
	for k := range mb {
		if _, ok := ma[k]; !ok {
			diff = append(diff, b+" 有而 "+a+" 缺: "+k)
		}
	}
	sort.Strings(diff)
	return len(diff) == 0, diff
}
