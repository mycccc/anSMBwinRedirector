//go:build windows

// 本文件是 winvert 的**平台相关**部分：**定位**与可执行文件同目录的
// WinDivert 运行时文件、校验完整性、并按绝对路径加载用户态 DLL
// （decisions/010 §B：外置发布模型）。
//
// ⛔ 不再有任何"释放 / 写入"逻辑 —— 外置模型下这两个文件是**随包发布**的，
// 本程序**只读**它们，绝不创建、覆盖或删除（decisions/007 §4.7 的"拒绝优先"）。

package winvert

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"

	"ansmbwinhelper/internal/diag"
)

// prep 保证 Prepare 的**成功**只做一次；**失败不缓存**（见 assets_common.go 的 prepareState）。
var prep prepareState

// WinDivertDir 返回 WinDivert 运行时文件**应当**所在的目录 —— 可执行文件所在目录。
//
// 依据（decisions/010 §B.1）：`divert-go` 用 `windows.LoadDLL("WinDivert.dll")`（裸名），
// Windows 的默认搜索顺序**先搜可执行文件所在目录**，因此同目录即可命中。
func WinDivertDir() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", diag.Wrap(diag.CodeRuntimeFileMissing, err, "无法确定可执行文件路径")
	}
	return filepath.Dir(exe), nil
}

// Prepare 校验并加载 WinDivert 运行时文件。
//
// 顺序（decisions/010 §B.3）：
//
//  1. 同目录存在 WinDivert.dll / WinDivert64.sys？
//  2. 两者 SHA-256 与**固化在二进制里的期望值**一致？
//  3. 按**绝对路径**加载 DLL，确保 divert-go 后续的裸名 LoadDLL 命中的就是刚校验过的那个文件。
//
// ⚠️ Prepare **不加载驱动**：它只校验 + 装载用户态 DLL；
// 驱动仍由后续带默认 flag 的 `WinDivertOpen` **按需安装**（decisions/007 §4.8）。
// ⛔ 校验失败一律**只报告**，不自动覆盖、不重新下载。
//
// ⚠️ 幂等语义：**成功**只做一次；**失败不缓存** —— 用户修复文件后，「重试启动」
// 必须能真正再校验一次（否则该按钮对这类失败永久失效）。见 prepareState。
func Prepare() error {
	return prep.do(prepareLocked)
}

func prepareLocked() error {
	dir, err := WinDivertDir()
	if err != nil {
		return err
	}
	if err := verifyInDir(dir); err != nil {
		return err
	}
	// 按绝对路径加载：与 divert-go 的 LoadDLL("WinDivert.dll") 命中同一模块。
	if _, err := windows.LoadDLL(filepath.Join(dir, DLLName)); err != nil {
		return diag.Wrap(diag.CodeRuntimeFileMissing, err, "加载 %s 失败", DLLName)
	}
	return nil
}

// verifyInDir 校验目录中的两个运行时文件是否存在且哈希一致。
func verifyInDir(dir string) error {
	for _, name := range []string{DLLName, SysName} {
		p := filepath.Join(dir, name)
		data, err := os.ReadFile(p)
		if err != nil {
			return diag.Wrap(diag.CodeRuntimeFileMissing, err,
				"缺少 WinDivert 运行时文件 %s（应与可执行文件放在同一目录）", p)
		}
		if !HashMatches(name, data) {
			return diag.Errorf(diag.CodeRuntimeFileCorrupt, "%s", hashMismatchDetail(name, data))
		}
	}
	return nil
}

// VerifyWinDivertFiles 校验当前目录中的两个运行时文件是否完整且与期望一致。
//
// 用于启动期的只读检查（decisions/007 §4.7.3）：**只报告，不修改任何文件**。
func VerifyWinDivertFiles() (bool, error) {
	dir, err := WinDivertDir()
	if err != nil {
		return false, err
	}
	if err := verifyInDir(dir); err != nil {
		return false, err
	}
	return true, nil
}

// DescribeWinDivertFiles 返回用于诊断的人类可读路径（WinDivert 文件所在目录）。
func DescribeWinDivertFiles() string {
	d, err := WinDivertDir()
	if err != nil {
		return fmt.Sprintf("(不可用: %v)", err)
	}
	return d
}
