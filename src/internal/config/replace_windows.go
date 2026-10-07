//go:build windows

package config

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// decisions/006 §6.11：Windows 上的文件替换语义。
//
// 三件事必须分开：
//
//	A. 不先删除当前有效的 config.json      ← 必须做到
//	B. 以尽量少的 OS 调用完成"切换到新文件"  ← 必须做到
//	C. crash-durable / filesystem-level atomicity ← v1 **不承诺**
//
// ⛔ 绝不使用 Remove(dest) + Rename(tmp, dest)，也绝不 Rename(dest, bak)
// （后者会出现"config.json 不存在"的窗口）。

const replaceFileIgnoreMergeErrors = 0x00000002

var (
	kernel32        = windows.NewLazySystemDLL("kernel32.dll")
	procReplaceFile = kernel32.NewProc("ReplaceFileW")
)

// replaceFile 用 Windows 原生"替换 + 备份"一体语义完成切换。
//
// 首选 ReplaceFileW：一次调用完成 replacement + backup。
// 注意其前提：**被替换文件必须已存在** —— 首次保存时它会失败，此时退化为
// 单次覆盖式移动（无旧内容可备份，天然没有"先删"窗口）。
func replaceFile(dest, tmp, bak string) error {
	if fileExists(dest) {
		d, err1 := windows.UTF16PtrFromString(dest)
		t, err2 := windows.UTF16PtrFromString(tmp)
		b, err3 := windows.UTF16PtrFromString(bak)
		if err1 == nil && err2 == nil && err3 == nil {
			r1, _, _ := procReplaceFile.Call(
				uintptr(unsafe.Pointer(d)),
				uintptr(unsafe.Pointer(t)),
				uintptr(unsafe.Pointer(b)),
				replaceFileIgnoreMergeErrors, 0, 0)
			if r1 != 0 {
				return nil
			}
		}
		// 备选路径：先 **copy** 出 .bak（不是 rename），再做覆盖式替换。
		if err := copyFile(dest, bak); err != nil {
			return err
		}
	}
	// 覆盖式移动：目标已存在时替换之；不存在时直接落下。全程不预先删除 dest。
	from, err := windows.UTF16PtrFromString(tmp)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(dest)
	if err != nil {
		return err
	}
	if err := windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING); err != nil {
		return err
	}
	return nil
}
