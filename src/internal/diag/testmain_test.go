package diag

import (
	"os"
	"testing"

	"ansmbwinhelper/internal/i18n"
)

// TestMain 把进程级翻译器固定为 **zh-CN** 并加载真实 `language.json`：
//   - 既有的断言文案是中文原文，固定 zh-CN 后继续有效；
//   - 同时校验 zh-CN 目录里确实存在这些 key（缺失会退化成 key 字符串 ⇒ 断言立即失败）。
func TestMain(m *testing.M) {
	if data, err := os.ReadFile("../i18n/assets/language.json"); err == nil {
		if cat, err := i18n.ParseCatalog(data); err == nil {
			i18n.SetDefault(i18n.New(cat, i18n.LanguageChineseSimplified))
		}
	}
	os.Exit(m.Run())
}
