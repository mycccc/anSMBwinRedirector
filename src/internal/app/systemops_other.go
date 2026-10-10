//go:build !windows

// 非 Windows 平台的桩：让 `go build ./...` / `go vet ./...` / `go test ./...` 在开发机上保持可用。
//
// ⛔ 这些桩**不是**产品行为（v1 仅支持 Windows x64）。
package app

import "ansmbwinhelper/internal/controller"

// newRepairer 在非 Windows 平台上不可用。
func newRepairer() controller.Repairer { return nil }

// newSystemOps 在非 Windows 平台上不可用。
func newSystemOps() controller.SystemOps { return nil }
