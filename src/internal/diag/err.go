package diag

import (
	"errors"
	"fmt"
)

// CodedError 是携带**稳定错误码**的错误（decisions/009 §2.2）。
//
// 平台层（winvert / runtime）用它把系统错误映射到封闭码表，
// controller 据此选择聚合范围与呈现位置 —— 实现不得在别处临时造码。
type CodedError struct {
	Code   Code
	Detail string
	Err    error
}

func (e *CodedError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %s (%v)", e.Code, e.Detail, e.Err)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Detail)
}

func (e *CodedError) Unwrap() error { return e.Err }

// Errorf 构造一个带错误码的错误。
func Errorf(code Code, format string, a ...any) error {
	return &CodedError{Code: code, Detail: fmt.Sprintf(format, a...)}
}

// Wrap 在保留错误码的前提下附带底层错误。
func Wrap(code Code, err error, format string, a ...any) error {
	return &CodedError{Code: code, Detail: fmt.Sprintf(format, a...), Err: err}
}

// CodeOf 取出错误携带的错误码；未携带时返回 false。
func CodeOf(err error) (Code, bool) {
	var ce *CodedError
	if errors.As(err, &ce) {
		return ce.Code, true
	}
	return "", false
}

// CodeOr 取出错误码，未携带时返回 fallback。
func CodeOr(err error, fallback Code) Code {
	if c, ok := CodeOf(err); ok {
		return c
	}
	return fallback
}
