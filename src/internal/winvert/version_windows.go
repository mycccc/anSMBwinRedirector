//go:build windows

package winvert

import (
	"errors"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// FileVersion 读取**文件**（.dll / .sys）的版本资源，返回 `a.b.c.d`。
//
// ⚠️ 这是**静态元数据**读取：只解析文件里的 VS_VERSIONINFO，⛔ **不打开、不加载任何驱动**
// （decisions/011 §9.3 的 D-1：只读诊断不得触达驱动）。
//
// 与"运行时驱动版本"是两回事：文件版本说明"这个文件是什么版本"，
// 运行时版本说明"当前**已加载**的驱动是什么版本"。前者可从文件得到，后者不行。
func FileVersion(path string) (string, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return "", err
	}
	var handle uint32
	size, _, callErr := procGetFileVersionInfoSizeW.Call(
		uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(&handle)))
	if size == 0 {
		return "", fmt.Errorf("GetFileVersionInfoSizeW 失败: %v", callErr)
	}
	buf := make([]byte, size)
	ok, _, callErr := procGetFileVersionInfoW.Call(
		uintptr(unsafe.Pointer(p)), 0, uintptr(size), uintptr(unsafe.Pointer(&buf[0])))
	if ok == 0 {
		return "", fmt.Errorf("GetFileVersionInfoW 失败: %v", callErr)
	}
	// 查询根块 ⇒ VS_FIXEDFILEINFO。
	sub, err := windows.UTF16PtrFromString(`\`)
	if err != nil {
		return "", err
	}
	var vptr unsafe.Pointer
	var vlen uint32
	ok, _, callErr = procVerQueryValueW.Call(
		uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(sub)),
		uintptr(unsafe.Pointer(&vptr)), uintptr(unsafe.Pointer(&vlen)))
	if ok == 0 || vptr == nil {
		return "", fmt.Errorf("VerQueryValueW 失败: %v", callErr)
	}
	if vlen < 16 {
		return "", errors.New("版本资源过短（无 VS_FIXEDFILEINFO）")
	}
	// VS_FIXEDFILEINFO：dwSignature(0) dwStrucVersion(4) dwFileVersionMS(8) dwFileVersionLS(12)
	ms := *(*uint32)(unsafe.Add(vptr, 8))
	ls := *(*uint32)(unsafe.Add(vptr, 12))
	return fmt.Sprintf("%d.%d.%d.%d", ms>>16, ms&0xffff, ls>>16, ls&0xffff), nil
}

var (
	versionDLL                  = windows.NewLazySystemDLL("version.dll")
	procGetFileVersionInfoSizeW = versionDLL.NewProc("GetFileVersionInfoSizeW")
	procGetFileVersionInfoW     = versionDLL.NewProc("GetFileVersionInfoW")
	procVerQueryValueW          = versionDLL.NewProc("VerQueryValueW")
)
