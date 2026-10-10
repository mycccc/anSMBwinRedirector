// Package credential 封装"向 Windows 下发/查询服务器凭据"的能力，对应 decisions/012 §3.4。
//
// 职责边界（⚠️ 很重要）：
//   - 本包**只**负责"某个服务器地址上是否已有凭据 / 写入一条凭据"。
//   - ⛔ 不实现 SMB 协议、不解析共享、不做任何网络 I/O。
//   - 凭据身份 = **主机地址（AndroidIP）**，与端口无关（decisions/012 C6）。
//
// 安全边界（decisions/012 §3.3 / 013 §6.1-C）：
//   - 密码**只以参数形式传入**，⛔ 绝不写入日志/诊断/错误信息；
//     本包返回的所有 error 文本**都不包含密码**。
//   - `Inspect` 的结果**只含用户名**，⛔ 不含密码（Windows 也不提供读回密码，见手测报告附录 D）。
package credential

import (
	"errors"
	"fmt"
	"strings"
)

// State 是"某主机上已有凭据"的**只读**快照。
//
// ⚠️ 只包含用户名；⛔ 不含密码（Windows 不提供读回密码）。
type State struct {
	// Exists 报告该主机上是否已存在凭据。
	Exists bool
	// Username 是已存在凭据的用户名（Exists=false 时为空）。
	Username string
}

// Access 是凭据访问的抽象（便于 controller 注入假实现做单测）。
type Access interface {
	// Inspect 查询该主机上是否已有凭据（只读，不修改任何状态）。
	Inspect(host string) (State, error)
	// Store 写入/覆盖该主机的凭据。
	//
	// ⚠️ 实现必须保证：密码 ⛔ 不出现在任何返回的 error、日志或诊断文本中。
	Store(host, username, password string) error
}

var (
	// ErrUnsupported 表示当前平台不支持（产品只跑在 Windows）。
	ErrUnsupported = errors.New("当前平台不支持凭据访问")
	// ErrHostRequired 表示主机地址为空。
	ErrHostRequired = errors.New("主机地址不能为空")
	// ErrUsernameRequired 表示用户名为空（有密码必须有用户名，decisions/012 C5）。
	ErrUsernameRequired = errors.New("用户名不能为空")
	// ErrPasswordRequired 表示密码为空（decisions/012 C5：只做基本非空校验）。
	ErrPasswordRequired = errors.New("密码不能为空")
)

// Validate 做**基本非空校验**（decisions/012 C5：⛔ 不做长度/字符集/强度策略）。
//
// ⚠️ 返回的 error **不包含密码**，也不会回显用户名以外的内容。
func Validate(host, username, password string) error {
	if strings.TrimSpace(host) == "" {
		return ErrHostRequired
	}
	if username == "" {
		return ErrUsernameRequired
	}
	if password == "" {
		return ErrPasswordRequired
	}
	return nil
}

// describe 生成**不含密码**的可诊断描述（供错误与诊断使用）。
func describe(host, username string) string {
	return fmt.Sprintf("主机=%s 用户名=%s", host, username)
}
