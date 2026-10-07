//go:build !windows

package runtime

import (
	"ansmbwinhelper/internal/diag"
	"ansmbwinhelper/internal/model"
)

// platformFactory 在非 Windows 平台不可用（开发机只跑单测，用测试替身工厂）。
type platformFactory struct{}

func (platformFactory) New(t model.Target) (Runtime, error) {
	return nil, diag.Errorf(diag.CodePlatformUnsupported, "WinDivert runtime 仅支持 Windows")
}
