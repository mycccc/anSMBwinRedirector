//go:build windows

package gui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ansmbwinhelper/internal/i18n"
)

// TestTableColumnTitlesAreLocalized 是 v1.0 实机缺陷（2026-10-17 手动测试发现）的回归测试。
//
// 缺陷：`tableColumns` 曾是包级 `var`，`T(...)` 在**包初始化**阶段求值 —— 那时入口还没有
// 调用 `gui.SetTranslator` / `i18n.SetDefault`，`T()` 只能返回 key 本身，于是列标题被永久
// 冻结成 `table.runtime` 之类，界面上直接显示原始键。
//
// 本测试同时覆盖两点：
//  1. en / zh-CN 下每列标题都解析成真实文案（非空、不是 `table.*`）；
//  2. **切换语言后标题会随之变化** —— 若退回包级 var，第二次循环仍是英文，测试即失败。
func TestTableColumnTitlesAreLocalized(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "i18n", "assets", "language.json"))
	if err != nil {
		t.Fatalf("读取 language.json 失败: %v", err)
	}
	cat, err := i18n.ParseCatalog(raw)
	if err != nil {
		t.Fatalf("解析 language.json 失败: %v", err)
	}

	prev := Translator()
	defer SetTranslator(prev)

	want := map[string][]string{
		i18n.LanguageEnglish:           {"SMB Port", "Username", "Runtime", "Last connect", "Note"},
		i18n.LanguageChineseSimplified: {"SMB 端口", "用户名", "运行态", "最近连接", "说明"},
	}

	// 语言集合固定写死（而不是遍历 map），保证"两种语言都检查过"这件事本身不被改掉。
	for _, lang := range []string{i18n.LanguageEnglish, i18n.LanguageChineseSimplified} {
		exp, ok := want[lang]
		if !ok {
			t.Fatalf("缺少 %s 的期望文案", lang)
		}
		SetTranslator(i18n.New(cat, lang))
		cols := tableColumns()

		if len(cols) != len(exp)+1 { // +1 = 不本地化的 "Android IP"
			t.Fatalf("%s: 列数 = %d，期望 %d", lang, len(cols), len(exp)+1)
		}
		for i, c := range cols {
			if strings.TrimSpace(c.Title) == "" {
				t.Fatalf("%s: 第 %d 列标题为空", lang, i)
			}
			if strings.HasPrefix(c.Title, "table.") {
				t.Fatalf("%s: 第 %d 列仍是原始 key %q（初始化时机回归）", lang, i, c.Title)
			}
			if c.Width <= 0 {
				t.Fatalf("%s: 第 %d 列宽度非法: %d", lang, i, c.Width)
			}
		}
		if cols[0].Title != "Android IP" {
			t.Fatalf("%s: 首列 = %q，期望 %q（技术名，不本地化）", lang, cols[0].Title, "Android IP")
		}
		for i, w := range exp {
			if got := cols[i+1].Title; got != w {
				t.Fatalf("%s: 第 %d 列 = %q，期望 %q", lang, i+1, got, w)
			}
		}
	}
}
