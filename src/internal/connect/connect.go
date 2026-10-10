// Package connect 封装"用 Windows 原生方式打开某个服务器的共享"，对应 decisions/012 §3.4 的第 3 步。
//
// 职责边界：⛔ 不实现 SMB 协议、不做任何网络 I/O —— 只是调用 Windows 自带能力
// （`start \\<host>\`），把访问交给系统 SMB 客户端与 Explorer。
package connect

import (
	"errors"
	"net/netip"
)

// Opener 是"打开共享"的抽象（便于 controller 注入假实现做单测）。
type Opener interface {
	// Open 打开 `\\host\`（host 为 IPv4 字面量）。
	Open(host string) error
}

var (
	// ErrUnsupported 表示当前平台不支持（产品只跑在 Windows）。
	ErrUnsupported = errors.New("当前平台不支持打开共享")
	// ErrHostInvalid 表示主机地址非法（只接受 IPv4 字面量，防注入）。
	ErrHostInvalid = errors.New("主机地址非法")
)

// ValidateHost 只接受**规范形式的 IPv4 字面量**（点分十进制、无前导零）。
//
// ⚠️ 这既是一次输入校验，也是一道**注入防护**：host 会被拼进命令行，
// 必须保证它不含空白/引号/控制字符等可改变命令语义的字符。
// 用 `netip.ParseAddr` + `a.String() == host` 同时排除
// `192.168.1.`、`192.168.001.1`、`host-name`、`1.2.3.4/24` 等形态。
func ValidateHost(host string) error {
	a, err := netip.ParseAddr(host)
	if err != nil || !a.Is4() || a.String() != host {
		return ErrHostInvalid
	}
	return nil
}
