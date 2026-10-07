//go:build !windows

package config

import "os"

// replaceFile 在非 Windows 平台完成切换（仅供开发机单测使用）。
//
// 语义与 Windows 实现一致：**先复制出 .bak（不是 rename）**，再做覆盖式改名，
// 因此 `dest` 在切换过程中始终存在。
func replaceFile(dest, tmp, bak string) error {
	if fileExists(dest) {
		if err := copyFile(dest, bak); err != nil {
			return err
		}
	}
	return os.Rename(tmp, dest)
}
