//go:build !windows

package connect

// unsupported 是非 Windows 平台的 Opener 实现（产品只跑在 Windows）。
type unsupported struct{}

// New 返回当前平台的 Opener 实现。
func New() Opener { return unsupported{} }

func (unsupported) Open(string) error { return ErrUnsupported }
