package i18n

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// --- 语言解析（decisions/013 §1 / §10）-------------------------------------

func TestResolve(t *testing.T) {
	cases := map[string]string{
		"":        LanguageEnglish,
		"en":      LanguageEnglish,
		"en-US":   LanguageEnglish,
		"de-DE":   LanguageEnglish,
		"zh":      LanguageChineseSimplified,
		"zh-CN":   LanguageChineseSimplified,
		"zh-TW":   LanguageChineseSimplified, // ⛔ 不做地区细分
		"zh-Hans": LanguageChineseSimplified,
		"ZH-cn":   LanguageChineseSimplified,
	}
	for in, want := range cases {
		if got := Resolve(in); got != want {
			t.Errorf("Resolve(%q) = %q, 期望 %q", in, got, want)
		}
	}
}

// --- 解析与 BOM ------------------------------------------------------------

func TestParseCatalogHandlesBOM(t *testing.T) {
	plain := []byte(`{"en":{"a":"A"},"zh-CN":{"a":"甲"}}`)
	withBOM := append([]byte{0xEF, 0xBB, 0xBF}, plain...)
	for name, data := range map[string][]byte{"无BOM": plain, "有BOM": withBOM} {
		cat, err := ParseCatalog(data)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if cat[LanguageEnglish]["a"] != "A" || cat[LanguageChineseSimplified]["a"] != "甲" {
			t.Errorf("%s: 解析结果错误: %+v", name, cat)
		}
	}
}

func TestParseCatalogRejectsGarbage(t *testing.T) {
	if _, err := ParseCatalog([]byte(`{not json`)); err == nil {
		t.Fatal("非法 JSON 必须报错")
	}
}

// --- 降级链（decisions/013 §4）--------------------------------------------

func TestLoadFileMissingFallsBackWithoutFailing(t *testing.T) {
	tr := LoadFile(filepath.Join(t.TempDir(), "nope.json"))
	if tr.Language() != LanguageEnglish {
		t.Errorf("缺文件时应为 en，得到 %q", tr.Language())
	}
	if len(tr.Issues()) == 0 {
		t.Error("缺文件必须记 Issue")
	}
	if got := tr.T("app.title"); got != "anSMBwinRedirector" {
		t.Errorf("应命中内置 fallback，得到 %q", got)
	}
	if got := tr.T("main.start"); got != "main.start" {
		t.Errorf("内置也没有的 key 应返回 key 本身，得到 %q", got)
	}
}

func TestLoadFileCorruptFallsBackWithoutFailing(t *testing.T) {
	p := filepath.Join(t.TempDir(), FileName)
	if err := os.WriteFile(p, []byte("{broken"), 0o644); err != nil {
		t.Fatal(err)
	}
	tr := LoadFile(p)
	if len(tr.Issues()) == 0 {
		t.Error("损坏文件必须记 Issue")
	}
	if got := tr.T("error.localization_unavailable"); got == "" || strings.HasPrefix(got, "error.") {
		t.Errorf("应命中内置 fallback，得到 %q", got)
	}
}

func TestTKeyFallbackChain(t *testing.T) {
	cat := Catalog{
		LanguageEnglish:           {"a": "A-en", "only_en": "E"},
		LanguageChineseSimplified: {"a": "A-zh"},
	}
	tr := New(cat, LanguageChineseSimplified)
	if got := tr.T("a"); got != "A-zh" {
		t.Errorf("目标语言优先，得到 %q", got)
	}
	if got := tr.T("only_en"); got != "E" {
		t.Errorf("目标语言缺 ⇒ 回退 en，得到 %q", got)
	}
	if len(tr.Issues()) != 1 {
		t.Errorf("回退应记 1 条 Issue，得到 %+v", tr.Issues())
	}
	// 同类同 key 只记一次
	tr.T("only_en")
	if len(tr.Issues()) != 1 {
		t.Errorf("同类同 key 不应重复记录，得到 %d 条", len(tr.Issues()))
	}
	if got := tr.T("missing_everywhere"); got != "missing_everywhere" {
		t.Errorf("都不存在应返回 key 本身，得到 %q", got)
	}
}

func TestMissingLanguageBlockFallsBackQuietly(t *testing.T) {
	// decisions/013 §4：目标语言块不存在 ⇒ 退回 en，**不报错**。
	cat := Catalog{LanguageEnglish: {"a": "A"}}
	tr := New(cat, LanguageChineseSimplified)
	if tr.Language() != LanguageEnglish {
		t.Errorf("应为 en，得到 %q", tr.Language())
	}
	if got := tr.T("a"); got != "A" {
		t.Errorf("得到 %q", got)
	}
	if len(tr.Issues()) != 0 {
		t.Errorf("缺少语言块不应记为 Issue，得到 %+v", tr.Issues())
	}
}

func TestCatalogWithoutEnglishIsReported(t *testing.T) {
	tr := New(Catalog{LanguageChineseSimplified: {"a": "甲"}}, LanguageChineseSimplified)
	if len(tr.Issues()) == 0 {
		t.Error("缺少 en 块必须记 Issue")
	}
}

// --- 内置 fallback 必须保持"极少量"（decisions/013 §4.1 / §8 L2）-----------

func TestBuiltinFallbackStaysSmall(t *testing.T) {
	const maxKeys = 6
	if n := len(BuiltinFallbackKeys()); n > maxKeys {
		t.Fatalf("内置 fallback 已膨胀到 %d 个 key（上限 %d）—— 它只能覆盖\"启动所必需\"，"+
			"否则会变成第二份 English 表（decisions/013 §4.1）", n, maxKeys)
	}
}

// --- 随包资源本身的完整性（decisions/013 §6.1 判据 A / D）------------------

func loadAsset(t *testing.T) Catalog {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("assets", FileName))
	if err != nil {
		t.Fatalf("读取随包 language.json 失败: %v", err)
	}
	cat, err := ParseCatalog(data)
	if err != nil {
		t.Fatalf("解析随包 language.json 失败: %v", err)
	}
	return cat
}

// 判据 D：en 与 zh-CN 的 key 集合必须一致。
func TestAssetCatalogKeySetsMatch(t *testing.T) {
	cat := loadAsset(t)
	if _, ok := cat[LanguageEnglish]; !ok {
		t.Fatal("随包 language.json 缺少 en 块")
	}
	if _, ok := cat[LanguageChineseSimplified]; !ok {
		t.Fatal("随包 language.json 缺少 zh-CN 块")
	}
	if ok, diff := cat.KeySetsEqual(LanguageEnglish, LanguageChineseSimplified); !ok {
		t.Fatalf("en 与 zh-CN 的 key 集合必须一致（decisions/013 §6.1 D），差异:\n%s",
			strings.Join(diff, "\n"))
	}
}

// 内置 fallback 的 key 必须在随包 en 里存在（防止两处漂移）。
func TestBuiltinFallbackKeysExistInAsset(t *testing.T) {
	en := loadAsset(t)[LanguageEnglish]
	for _, k := range BuiltinFallbackKeys() {
		if _, ok := en[k]; !ok {
			t.Errorf("内置 fallback key %q 不在随包 en 中", k)
		}
	}
}

// 判据 A：源码中每个 T("key") 的 key 必须在随包 en 中存在。
//
// 说明：跳过 i18n 包自身目录（本文件的匹配模式就写在这里，避免自匹配）。
func TestSourceTKeysExistInEnglish(t *testing.T) {
	en := loadAsset(t)[LanguageEnglish]
	pkgDir, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Dir(filepath.Dir(pkgDir)) // src/

	// 用拼接构造，避免本文件里出现字面量调用形式而被自匹配。
	pattern := regexp.MustCompile(`\b` + "T" + `\(\s*"([^"]+)"\s*\)`)
	keyRE := regexp.MustCompile(`^[a-z0-9_]+(\.[a-z0-9_]+)+$`)

	missing := map[string][]string{}
	err = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		if filepath.Dir(path) == pkgDir {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range pattern.FindAllStringSubmatch(string(b), -1) {
			key := m[1]
			if !keyRE.MatchString(key) {
				continue // 非 localization key 形状（例如测试里的构造）忽略
			}
			if _, ok := en[key]; !ok {
				rel, _ := filepath.Rel(root, path)
				missing[key] = append(missing[key], rel)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) > 0 {
		keys := make([]string, 0, len(missing))
		for k, files := range missing {
			keys = append(keys, k+"  ← "+strings.Join(files, ", "))
		}
		sort.Strings(keys)
		t.Fatalf("以下 localization key 在随包 en 中不存在（decisions/013 §6.1 A）:\n%s",
			strings.Join(keys, "\n"))
	}
}
