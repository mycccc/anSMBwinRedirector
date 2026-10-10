//go:build windows

package connect

import (
	"fmt"
	"os/exec"
	"syscall"
)

// win 是 Windows 上的 Opener 实现。
type win struct{}

// New 返回当前平台的 Opener 实现。
func New() Opener { return win{} }

// Open 等价于在命令行执行：`start "" \\<host>\`
//
// ⚠️ 故意**不加引号**：host 已由 ValidateHost 限定为 IPv4 字面量（无空白/引号），
// 加引号反而会让结尾的反斜杠在 cmd 解析时产生歧义。
func (win) Open(host string) error {
	if err := ValidateHost(host); err != nil {
		return err
	}
	target := `\\` + host + `\`
	cmd := exec.Command("cmd", "/c", "start", "", target)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("打开 %s 失败: %v; 输出: %s", target, err, string(out))
	}
	return nil
}
