//go:build !windows

// Package winvert 的**非 Windows** 桩实现。
//
// 它只为了让 `go build ./...` / `go vet ./...` / `go test ./...` 能在开发机（macOS）上完整跑通；
// 真正的 WinDivert 适配只在 `*_windows.go` 中。
package winvert

import "ansmbwinhelper/internal/diag"

func platformErr() error {
	return diag.Errorf(diag.CodePlatformUnsupported, "WinDivert 仅支持 Windows")
}

// Prepare 在非 Windows 平台不可用。
func Prepare() error { return platformErr() }

// WinDivertDir 在非 Windows 平台不可用。
func WinDivertDir() (string, error) { return "", platformErr() }

// DescribeWinDivertFiles 用于诊断。
func DescribeWinDivertFiles() string { return "(非 Windows 平台)" }

// VerifyWinDivertFiles 在非 Windows 平台不可用。
func VerifyWinDivertFiles() (bool, error) { return false, nil }

// Inspector 在非 Windows 平台报告不可用。
type Inspector struct{}

// Inspect 返回"不可用"。
func (Inspector) Inspect() diag.WinDivertInfo {
	return diag.WinDivertInfo{Available: false, Err: "仅 Windows 支持 WinDivert 检查"}
}
