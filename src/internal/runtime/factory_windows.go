//go:build windows

package runtime

import "ansmbwinhelper/internal/model"

// platformFactory 创建真正的 WinDivert runtime。
type platformFactory struct{}

func (platformFactory) New(t model.Target) (Runtime, error) {
	return &winRuntime{target: t}, nil
}
