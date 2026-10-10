//go:build !windows

// 非 Windows 平台的桩：让 `go build ./...` / `go vet ./...` 在开发机（macOS）上保持可用。
//
// ⛔ 这些桩**不是**产品行为，也不得被当作"支持的平台"。
package app

import (
	"runtime"

	"ansmbwinhelper/internal/controller"
	"ansmbwinhelper/internal/diag"
	"ansmbwinhelper/internal/i18n"
)

// SingleInstance 在非 Windows 平台上不存在。
type SingleInstance struct{ MutexAbandoned bool }

// Release 是空操作。
func (s *SingleInstance) Release() {}

// Start 在非 Windows 平台上立即失败。
func startUnsupported() error {
	return diag.Errorf(diag.CodePlatformUnsupported,
		i18n.T("app.platform_unsupported_detail"), runtime.GOOS, runtime.GOARCH)
}

func checkPlatform() error { return startUnsupported() }
func checkAdmin() error    { return startUnsupported() }

func acquireSingleInstance() (*SingleInstance, error) { return nil, startUnsupported() }

// OSInfo 返回当前平台。
func OSInfo() string { return runtime.GOOS + "/" + runtime.GOARCH }

// NativeArch 返回当前架构。
func NativeArch() string { return runtime.GOARCH }

func newManager() controller.RuntimeManager { return nil }
func newInspector() controller.Inspector    { return nil }
