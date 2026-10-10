//go:build windows

package i18n

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	kernel32                     = windows.NewLazySystemDLL("kernel32.dll")
	procGetUserDefaultLocaleName = kernel32.NewProc("GetUserDefaultLocaleName")
)

// SystemLanguage 返回 Windows **用户默认区域名**（例如 `zh-CN`、`en-US`）。
//
// 用 `GetUserDefaultLocaleName`（区域名）而不是 `GetUserDefaultUILanguage`（LANGID），
// 因为本包只需要"是不是中文"这一个判断，区域名最直接（decisions/013 §1）。
//
// 失败 ⇒ 返回空串（调用方按 `en` 处理）。⛔ 不因此报错、不阻止启动。
func SystemLanguage() string {
	const localeNameMaxLength = 85
	buf := make([]uint16, localeNameMaxLength)
	n, _, _ := procGetUserDefaultLocaleName.Call(
		uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if n == 0 {
		return ""
	}
	// 返回值包含结尾的 NUL。
	return windows.UTF16ToString(buf[:n-1])
}
