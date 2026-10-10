// Package winvert 是**唯一**的 WinDivert 适配层（decisions/008 §2.2 第 1 条）。
//
// ⛔ 只有本包可以 import WinDivert 绑定、创建/关闭 handle。
// controller / runtime / cmd 一律通过本包的 Session / Inspector 访问网络层。
//
// WinDivert 运行时文件的处置对应 **decisions/010 §B（外置发布模型）**：
//
//	WinDivert.dll / WinDivert64.sys 与可执行文件**同目录**发布
//	  （**不再内嵌、不再释放到 %PROGRAMDATA%、不再有 runtime-<hash> 目录**）
//	  → 使用前做**哈希校验**（期望值固化在二进制里，见下）
//
// 本文件只放**跨平台可测**的部分（文件名 + 期望哈希 + 校验）；
// 路径解析与 DLL 加载在 assets_windows.go。
package winvert

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"
)

// prepareState 实现"**只缓存成功**"的幂等语义。
//
// ⚠️ 为什么失败**不能**缓存（2026-10-07 Windows 实机验证时发现的缺陷）：
// 启动失败的目标要靠「重试启动」再试（decisions/007 §4.9）；而"运行时文件缺失/损坏"
// 是**用户可以自行修复**的 —— 按 decisions/010 §B 本程序**只报告、不覆盖**，
// 修复动作本就留给用户（补齐或替换同目录的 WinDivert.dll / WinDivert64.sys）。
// 一旦把失败缓存下来，用户修好文件后点「重试启动」仍会拿到同一条旧错误、
// 直到重启程序为止 —— 那等于让「重试启动」对这类失败永久失效。
type prepareState struct {
	mu   sync.Mutex
	done bool
}

// prep 保证 Prepare 的**成功**只做一次；**失败不缓存**（见下方 prepareState）。
//
// ⚠️ 放在本文件（跨平台）而不是 assets_windows.go：`InvalidatePrepare` 需要在
// 非 Windows 平台也可编译（供 stub 与单测使用）。
var prep prepareState

// do 在尚未成功时执行 fn：失败**不缓存**（下次会真正重试），成功后不再执行。
func (p *prepareState) do(fn func() error) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.done {
		return nil
	}
	if err := fn(); err != nil {
		return err
	}
	p.done = true
	return nil
}

// reset 丢弃"已成功"标记（供 Repair 使用：用户可能刚替换了同目录的运行时文件）。
func (p *prepareState) reset() {
	p.mu.Lock()
	p.done = false
	p.mu.Unlock()
}

// InvalidatePrepare 使下一次 `Prepare()` 重新校验运行时文件。
//
// ⚠️ 为什么需要它（decisions/011 §2.7 的 `Repair`）：`Prepare()` 对**成功**是幂等的，
// 而 Repair 的意义正是"用户替换/补齐了 WinDivert 文件后重新校验并重新接管" ——
// 若不复位成功标记，Repair 之后的下一次 Start 仍会用旧的校验结论。
func InvalidatePrepare() { prep.reset() }

const (
	// DLLName / SysName 是 WinDivert 运行时文件名（decisions/010 §B.1）。
	DLLName = "WinDivert.dll"
	SysName = "WinDivert64.sys"
)

// 期望哈希：**编译期常量**，取自本仓库 `internal/winvert/assets/` 里
// 我们验证过的 WinDivert 2.2.2-A x64 两个文件。
//
// ⚠️ 为什么是常量而不是"读旁边的清单文件"：清单文件就在旁边，无法提供额外保证。
// 把期望值**固化在二进制里**，才能真正发现"下载不完整 / 32 位与 64 位混放 /
// DLL 与 SYS 版本不匹配"这类实际故障（decisions/010 §B.3）。
//
// ⛔ 不得在运行期修改这两个值，也不得"发现不一致就自动覆盖或重新下载"。
//
// `assets_test.go` 会断言它们与 `assets/` 中的文件一致 —— 换 WinDivert 版本时
// 必须同时更新这里，否则测试失败。
const (
	ExpectedDLLSHA256 = "c1e060ee19444a259b2162f8af0f3fe8c4428a1c6f694dce20de194ac8d7d9a2"
	ExpectedSysSHA256 = "8da085332782708d8767bcace5327a6ec7283c17cfb85e40b03cd2323a90ddc2"
)

// expectedHash 返回某个运行时文件的期望哈希（小写十六进制）；未知文件返回空串。
func expectedHash(name string) string {
	switch name {
	case DLLName:
		return ExpectedDLLSHA256
	case SysName:
		return ExpectedSysSHA256
	default:
		return ""
	}
}

// HashMatches 报告 data 的 SHA-256 是否等于该文件的期望值。
func HashMatches(name string, data []byte) bool {
	want := expectedHash(name)
	if want == "" {
		return false
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]) == want
}

// hashMismatchDetail 生成统一的校验失败文案（含实际值，便于诊断）。
func hashMismatchDetail(name string, data []byte) string {
	sum := sha256.Sum256(data)
	return fmt.Sprintf("%s 内容与预期不符：实际 SHA-256=%s，期望=%s",
		name, hex.EncodeToString(sum[:]), expectedHash(name))
}
