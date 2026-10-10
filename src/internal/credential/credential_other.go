//go:build !windows

package credential

// unsupported 是非 Windows 平台的 Access 实现（产品只跑在 Windows）。
//
// 它为的是让 controller 的单元测试能在 macOS/Linux 上编译；
// 真实实现见 credential_windows.go。
type unsupported struct{}

// New 返回当前平台的 Access 实现。
func New() Access { return unsupported{} }

func (unsupported) Inspect(string) (State, error) { return State{}, ErrUnsupported }

func (unsupported) Store(string, string, string) error { return ErrUnsupported }
