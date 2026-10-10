package controller

import (
	"os"
	"testing"

	"ansmbwinhelper/internal/i18n"
)

// TestMain 把进程级翻译器固定为 **zh-CN** 并加载真实 `language.json`：
// 既有的中文断言（如状态条 `运行中 · 0 个目标`）继续有效，同时校验资源里确实有这些 key。
func TestMain(m *testing.M) {
	if data, err := os.ReadFile("../i18n/assets/language.json"); err == nil {
		if cat, err := i18n.ParseCatalog(data); err == nil {
			i18n.SetDefault(i18n.New(cat, i18n.LanguageChineseSimplified))
		}
	}
	os.Exit(m.Run())
}
