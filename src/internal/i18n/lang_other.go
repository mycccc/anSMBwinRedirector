//go:build !windows

package i18n

// SystemLanguage 在非 Windows 平台恒返回空串 ⇒ Resolve 得到 `en`。
//
// 该实现只为让本包在 macOS/Linux 上可单测；产品只跑在 Windows。
func SystemLanguage() string { return "" }
