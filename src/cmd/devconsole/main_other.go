//go:build !windows

// 非 Windows 平台的桩入口：让 `go build ./...` / `go vet ./...` 在开发机（macOS）上保持可用。
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "anSMBwinRedirector 仅支持 Windows x64（本机为非 Windows 平台）")
	os.Exit(1)
}
