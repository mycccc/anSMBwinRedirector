//go:build !windows

// 非 Windows 平台的桩入口：让 `go build ./...` / `go vet ./...` 在开发机（macOS）上保持可用。
package main

import (
	"fmt"
	"os"

	"ansmbwinhelper/internal/i18n"
)

func main() {
	// 用户可见文案同样经 localization（decisions/013）：开发机上没有 language.json 时
	// 走内置 fallback，**不影响启动**（这是开发机专用路径，产品只跑 Windows）。
	fmt.Fprintln(os.Stderr, i18n.LoadDefault().T("err.platform_unsupported"))
	os.Exit(1)
}
