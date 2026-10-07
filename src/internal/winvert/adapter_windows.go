//go:build windows

package winvert

import (
	"errors"

	"github.com/imgk/divert-go"

	"ansmbwinhelper/internal/diag"
)

// Session 是一个方向上的 WinDivert handle 及其收发缓冲。
//
// ⛔ 这是**唯一**对 divert-go 的 handle 操作入口；上层只接触 []byte。
type Session struct {
	h    *divert.Handle
	addr divert.Address
	buf  []byte
}

// DefaultBufSize 是单包接收缓冲大小。
const DefaultBufSize = 65535

// OpenSession 打开一个 NETWORK 层的 handle。
//
// filter 语义由 decisions/005 §6.3 固定（每 Target 一对，极简）。
func OpenSession(filter string, bufSize int) (*Session, error) {
	if bufSize <= 0 {
		bufSize = DefaultBufSize
	}
	h, err := divert.Open(filter, divert.LayerNetwork, divert.PriorityDefault, divert.FlagDefault)
	if err != nil {
		return nil, MapError(err, "打开 WinDivert handle 失败（filter=%q）", filter)
	}
	return &Session{h: h, buf: make([]byte, bufSize)}, nil
}

// Recv 阻塞接收一个包；返回的切片由本 Session 复用，调用方不得长期持有。
func (s *Session) Recv() ([]byte, error) {
	n, err := s.h.Recv(s.buf, &s.addr)
	if err != nil {
		return nil, err
	}
	return s.buf[:n], nil
}

// Send 注入一个包。
//
// 注入前重算校验和：checksum offload 下捕获到的校验和可能无效
// （P0 实测未造成问题，但 WinDivert 要求注入前校验和正确）。
func (s *Session) Send(pkt []byte) error {
	divert.CalcChecksums(pkt, &s.addr, divert.ChecksumDefault)
	_, err := s.h.Send(pkt, &s.addr)
	return err
}

// FixChecksums 仅重算校验和（不发送）。
func (s *Session) FixChecksums(pkt []byte) {
	divert.CalcChecksums(pkt, &s.addr, divert.ChecksumDefault)
}

// Shutdown 请求停止；注意：对**空闲** handle 它可能唤不醒阻塞中的 Recv
// （P0.1 实测），因此调用方必须准备"等待超时后强制 Close"。
func (s *Session) Shutdown() error {
	return s.h.Shutdown(divert.ShutdownBoth)
}

// Close 关闭 handle。
func (s *Session) Close() error {
	return s.h.Close()
}

// DriverVersion 通过一个**只查询、不安装驱动**的 REFLECT handle 读取驱动版本。
//
// 依据：WINDivert 官方对 WINDIVERT_FLAG_NO_INSTALL 的说明 ——
// "This flag is useful for querying the WinDivert state using a WINDIVERT_LAYER_REFLECT handle."
func DriverVersion() (string, error) {
	h, err := divert.Open("true", divert.LayerReflect, divert.PriorityDefault,
		divert.FlagSniff|divert.FlagRecvOnly|divert.FlagNoInstall)
	if err != nil {
		return "", MapError(err, "查询 WinDivert 驱动版本失败")
	}
	defer h.Close()

	maj, err := h.GetParam(divert.VersionMajor)
	if err != nil {
		return "", err
	}
	min, err := h.GetParam(divert.VersionMinor)
	if err != nil {
		return "", err
	}
	return versionString(maj, min), nil
}

func versionString(maj, min uint64) string {
	return itoa(uint32(maj>>16)) + "." + itoa(uint32(maj&0xffff)) + "." + itoa(uint32(min))
}

func itoa(v uint32) string {
	if v == 0 {
		return "0"
	}
	var b [12]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	return string(b[i:])
}

// MapError 把 WinDivert 的系统错误映射到**封闭错误码表**（decisions/009 §2.2）。
//
// ⛔ 实现不得在别处临时造码。
func MapError(err error, format string, a ...any) error {
	if err == nil {
		return nil
	}
	code := diag.CodeInternalError
	switch {
	case errors.Is(err, divert.ErrAccessDenied):
		code = diag.CodeAdminRequired
	case errors.Is(err, divert.ErrFileNotFound):
		code = diag.CodeRuntimeFileMissing
	case errors.Is(err, divert.ErrInvalidImageHash):
		code = diag.CodeDriverSignatureInvalid
	case errors.Is(err, divert.ErrDriverFailedPriorUnload):
		code = diag.CodeDriverVersionIncompatible
	case errors.Is(err, divert.ErrDriverBlocked):
		code = diag.CodeDriverBlocked
	case errors.Is(err, divert.ErrNotRegistered):
		code = diag.CodeBFENotRegistered
	case errors.Is(err, divert.ErrInvalidParameter),
		errors.Is(err, divert.ErrInsufficientBuffer):
		code = diag.CodeInternalError
	}
	return diag.Wrap(code, err, format, a...)
}

// IsShutdownErr 判定该错误是否属于**停机路径的正常表现**
// （decisions/007 §4.5 / decisions/009 §3.3）：
// 这类错误不得上报为用户可见错误。
func IsShutdownErr(err error) bool {
	switch {
	case errors.Is(err, divert.ErrOperationAborted):
		return true
	case errors.Is(err, divert.ErrNoData):
		return true
	case errors.Is(err, divert.ErrInvalidHandle):
		return true
	}
	return false
}
