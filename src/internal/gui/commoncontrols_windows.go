//go:build windows

package gui

import (
	"unsafe"

	"github.com/lxn/win"
)

// initCommonControls 注册我们依赖的 common-control 窗口类。
//
// ⚠️ 为什么需要它（在 Windows 实机上实测后加入）：
// walk 的窗口初始化只调用 `InitCommonControlsEx` 并仅仅带上
// `ICC_LINK_CLASS | ICC_LISTVIEW_CLASSES | ICC_PROGRESS_CLASS | ICC_TAB_CLASSES | ICC_TREEVIEW_CLASSES`
// （见 walk `window.go` 的 `initWindowWithCfg`），**没有 `ICC_BAR_CLASSES`**；
// 而 walk 的 `WidgetBase.init` 会为**每个控件**创建一个 `ToolTip`
// （`group.CreateToolTip()` → `AddTool`）。在未随包 application manifest 的情况下，
// TOOLTIPS_CLASS 未注册 ⇒ 创建 tooltip 失败 ⇒ `TTM_ADDTOOL failed` ⇒ 整个窗口建不起来。
//
// 这里在**创建任何窗口之前**补齐注册，使 walk 可以正常初始化。
//
// ⛔ 这不是产品能力，只是让 GUI 能起来的平台前置；
// 正式的 **application manifest（视觉样式 / DPI / 提权方式）属于 2.7 packaging**，
// 届时可重新评估本函数是否仍必要。
func initCommonControls() {
	var icc win.INITCOMMONCONTROLSEX
	icc.DwSize = uint32(unsafe.Sizeof(icc))
	icc.DwICC = win.ICC_BAR_CLASSES |
		win.ICC_LISTVIEW_CLASSES |
		win.ICC_TAB_CLASSES |
		win.ICC_PROGRESS_CLASS |
		win.ICC_TREEVIEW_CLASSES |
		win.ICC_LINK_CLASS
	win.InitCommonControlsEx(&icc)
}
