//go:build windows

package gui

import (
	"ansmbwinhelper/internal/i18n"
)

// tr 是本包的 localization 入口（decisions/013）。
//
// ⛔ GUI 里**不得**出现用户可见字面量：所有界面文案都要经 T()。
// ⛔ GUI **不直接读** language.json（与 008 G3 同款分离原则）：
// 加载与语言判定在 internal/i18n，由入口（cmd / app）在启动时注入一次。
var tr *i18n.Translator

// SetTranslator 注入本地化翻译器（由入口在启动时调用一次）。
//
// 未注入时 T() 走内置极少量 English fallback（013 §4.1），**不会 panic、不会阻止启动**。
func SetTranslator(t *i18n.Translator) { tr = t }

// Translator 返回当前注入的翻译器（可为 nil）。
func Translator() *i18n.Translator { return tr }

// T 把 localization key 解析为当前语言的文案。
func T(key string) string { return tr.T(key) }
