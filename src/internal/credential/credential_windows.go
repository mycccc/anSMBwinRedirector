//go:build windows

package credential

import (
	"fmt"
	"os/exec"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// CRED_TYPE_DOMAIN_PASSWORD：`cmdkey /add:<host>` 写入的就是这一类
// （手测报告附录 A/D 实测：`CredRead` 必须用该类型才能读到）。
const credTypeDomainPassword = 2

// ERROR_NOT_FOUND：该主机上没有凭据。
const errorNotFound = 1168

// credentialW 对应 Win32 的 CREDENTIALW（字段顺序/对齐必须一致）。
type credentialW struct {
	Flags              uint32
	Type               uint32
	TargetName         *uint16
	Comment            *uint16
	LastWritten        windows.Filetime
	CredentialBlobSize uint32
	CredentialBlob     uintptr
	Persist            uint32
	AttributeCount     uint32
	Attributes         uintptr
	TargetAlias        *uint16
	UserName           *uint16
}

var (
	advapi32     = windows.NewLazySystemDLL("advapi32.dll")
	procCredRead = advapi32.NewProc("CredReadW")
	procCredFree = advapi32.NewProc("CredFree")
)

// win 是 Windows 上的 Access 实现。
type win struct{}

// New 返回当前平台的 Access 实现。
func New() Access { return win{} }

// Inspect 用 `CredRead` 查询该主机是否已有凭据。
//
// ⚠️ Windows **不提供**读回密码（实测 `CredentialBlobSize = 0`，见手测报告附录 D），
// 因此本方法**只能**报告"是否存在"与"用户名"，⛔ 无法判断该凭据是不是本程序写的。
func (win) Inspect(host string) (State, error) {
	if host == "" {
		return State{}, ErrHostRequired
	}
	target, err := windows.UTF16PtrFromString(host)
	if err != nil {
		return State{}, err
	}
	var p *credentialW
	r1, _, callErr := procCredRead.Call(
		uintptr(unsafe.Pointer(target)),
		uintptr(credTypeDomainPassword),
		0,
		uintptr(unsafe.Pointer(&p)),
	)
	if r1 == 0 {
		if errno, ok := callErr.(syscall.Errno); ok && uintptr(errno) == errorNotFound {
			return State{}, nil // 没有凭据：正常状态，不是错误
		}
		return State{}, fmt.Errorf("查询 Windows 凭据失败（%s）: %v", describe(host, ""), callErr)
	}
	defer procCredFree.Call(uintptr(unsafe.Pointer(p)))

	st := State{Exists: true}
	if p.UserName != nil {
		st.Username = windows.UTF16PtrToString(p.UserName)
	}
	return st, nil
}

// Store 通过 `cmdkey /add` 写入/覆盖该主机的凭据（decisions/012 §3.4）。
//
// ⚠️ 实现注意：密码会作为**命令行参数**传给 cmdkey。这是 decisions/012 §3.4 已批准的机制；
// 若将来改为 `CredWrite`，可同时消除参数暴露并把 `Comment` 用作归属标记（见 012 §2.3 备选）。
//
// ⛔ 返回的 error **绝不包含密码**：只包含主机、用户名与 cmdkey 的输出。
func (win) Store(host, username, password string) error {
	if err := Validate(host, username, password); err != nil {
		return err
	}
	cmd := exec.Command("cmdkey", "/add:"+host, "/user:"+username, "/pass:"+password)
	// ⛔ 不弹出控制台窗口；⛔ 不把密码写进任何日志。
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("cmdkey 写入失败（%s）: %v; 输出: %s",
			describe(host, username), err, sanitize(out))
	}
	return nil
}

// sanitize 把 cmdkey 的输出裁剪为可安全放入错误信息的形式。
//
// cmdkey 的输出**不含**密码（实测），但这里仍做一次长度裁剪，避免把大段文本塞进 ErrorInfo。
func sanitize(b []byte) string {
	const max = 200
	s := string(b)
	if len(s) > max {
		s = s[:max] + "…"
	}
	return s
}
