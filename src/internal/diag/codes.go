// Package diag 是 v1 的错误分类与诊断模型，严格对应 decisions/009。
//
//	§2 错误码（封闭集合，Code 是**对外契约**）
//	§3 错误聚合
//	§6 DiagnosticEntry（有界内存环形缓冲）
//	§7 "复制详情"的固定文本模板
//	§10 WinDivert 诊断（数据结构；采集在平台层）
//
// 本包是纯逻辑，不依赖 Windows。
//
// ⚠️ 本地化（decisions/013 §6.2）：**用户可见文案**（错误摘要 / 提示 / 分类 / 范围 / 复制详情
// 模板与字段标签）统一经 `i18n.T()` 取得 —— 因此码表里存的是 **key**，渲染时才翻译。
// ⛔ **内部契约不本地化**：`Code` / `Scope` / `Source` 的取值、`Summary` 的原始 detail。
package diag

import "ansmbwinhelper/internal/i18n"

// Code 是稳定的错误码（decisions/009 §2.1）。
//
// ⚠️ **Code 是对外契约**：改文案不改 Code；新增 Code 必须回到 decisions/009 修订。
type Code string

const (
	// --- 平台 / 启动阻塞 ---
	CodePlatformUnsupported    Code = "PLATFORM_UNSUPPORTED"
	CodeAdminRequired          Code = "ADMIN_REQUIRED"
	CodeSingleInstanceConflict Code = "SINGLE_INSTANCE_CONFLICT"

	// --- 运行时文件 ---
	CodeRuntimeFileMissing Code = "RUNTIME_FILE_MISSING"
	CodeRuntimeFileCorrupt Code = "RUNTIME_FILE_CORRUPT"

	// --- 驱动 ---
	CodeDriverSignatureInvalid    Code = "DRIVER_SIGNATURE_INVALID"
	CodeDriverVersionIncompatible Code = "DRIVER_VERSION_INCOMPATIBLE"
	CodeDriverBlocked             Code = "DRIVER_BLOCKED"
	CodeBFENotRegistered          Code = "BFE_NOT_REGISTERED"
	// CodeWinDivertTakeoverFailed：WinDivert 服务**接管失败**（Start 前置）或**移除失败**（Stop/Exit）。
	// decisions/011 §2.5 / T-e：⛔ 不得静默复用旧服务、⛔ 不得 fallback。
	CodeWinDivertTakeoverFailed Code = "WINDIVERT_TAKEOVER_FAILED"
	// CodeRepairRequiresStop：要求"修复 WinDivert"时重定向仍在运行（有活动 handle）⇒ 拒绝。
	// decisions/011 §2.7：修复 = 接管流程，必须在**没有活动 handle** 时进行。
	CodeRepairRequiresStop Code = "REPAIR_REQUIRES_STOP"

	// CodeWorkstationRestartFailed：重启 Windows Workstation 服务失败（decisions/011 §2.7 的调试动作）。
	CodeWorkstationRestartFailed Code = "WORKSTATION_RESTART_FAILED"

	// --- 资源 / 目标 ---
	CodeResourceExhausted    Code = "RESOURCE_EXHAUSTED"
	CodeTargetStartFailed    Code = "TARGET_START_FAILED"
	CodeRuntimeInternalError Code = "RUNTIME_INTERNAL_ERROR"

	// --- 连接（decisions/012；2026-10-08 新增）---
	// ⚠️ 全部是**单目标、非阻塞**：连接失败不影响 runtime（007 L7）。
	CodeConnectNotRunning     Code = "CONNECT_NOT_RUNNING"
	CodeCredentialWriteFailed Code = "CREDENTIAL_WRITE_FAILED"
	CodeShareOpenFailed       Code = "SHARE_OPEN_FAILED"

	// --- 内部（编程错误，不应出现）---
	CodeInternalError Code = "INTERNAL_ERROR"

	// --- 配置 ---
	CodeConfigIOError           Code = "CONFIG_IO_ERROR"
	CodeConfigParseError        Code = "CONFIG_PARSE_ERROR"
	CodeConfigSchemaUnsupported Code = "CONFIG_SCHEMA_UNSUPPORTED"
	CodeConfigValidationFailed  Code = "CONFIG_VALIDATION_FAILED"
)

// Category 是错误分类（decisions/009 §2.1）。
//
// ⚠️ 取值是**稳定内部标识**（英文，与 Scope / Source 一致），**不是**展示文案；
// 展示一律经 `CategoryText()`（decisions/013 §6.2：分类属用户可见标签）。
type Category string

const (
	CatPlatform       Category = "Platform"
	CatPermission     Category = "Permission"
	CatSingleInstance Category = "SingleInstance"
	CatRuntimeFile    Category = "RuntimeFile"
	CatDriver         Category = "Driver"
	CatResource       Category = "Resource"
	CatConfig         Category = "Config"
	CatTarget         Category = "Target"
	CatConnect        Category = "Connect"
	CatSystem         Category = "System"
	CatInternal       Category = "Internal"
)

// categoryKey 把分类标识映射到本地化 key（展示用）。
var categoryKey = map[Category]string{
	CatPlatform:       "diag.category.platform",
	CatPermission:     "diag.category.permission",
	CatSingleInstance: "diag.category.single_instance",
	CatRuntimeFile:    "diag.category.runtime_file",
	CatDriver:         "diag.category.driver",
	CatResource:       "diag.category.resource",
	CatConfig:         "diag.category.config",
	CatTarget:         "diag.category.target",
	CatConnect:        "diag.category.connect",
	CatSystem:         "diag.category.system",
	CatInternal:       "diag.category.internal",
}

// CategoryText 返回分类的**展示文案**（未知分类原样返回，便于诊断）。
func CategoryText(c Category) string {
	if k, ok := categoryKey[c]; ok {
		return i18n.T(k)
	}
	return string(c)
}

// Scope 决定呈现位置（decisions/008 §13 / decisions/009 §5）。
type Scope string

const (
	// ScopeGlobal：影响全部目标 ⇒ 顶部横幅（可归并）+ 受影响目标列表。
	ScopeGlobal Scope = "Global"
	// ScopeTarget：影响单个目标 ⇒ 行内标记 + 详情条。
	ScopeTarget Scope = "Target"
	// ScopeConfig：配置层 ⇒ 阻塞式横幅（decisions/008 §14）。
	ScopeConfig Scope = "Config"
	// ScopeStartupBlock：启动阻塞 ⇒ 提示后退出。
	ScopeStartupBlock Scope = "StartupBlock"
)

// scopeKey 把 Scope 映射到本地化 key（展示用；⛔ 不改 Scope 取值本身）。
var scopeKey = map[Scope]string{
	ScopeGlobal:       "diag.scope.global",
	ScopeTarget:       "diag.scope.target",
	ScopeConfig:       "diag.scope.config",
	ScopeStartupBlock: "diag.scope.startup_block",
}

// ScopeText 返回范围的**展示文案**（未知范围原样返回）。
func ScopeText(s Scope) string {
	if k, ok := scopeKey[s]; ok {
		return i18n.T(k)
	}
	return string(s)
}

// Source 标记错误产生的阶段（decisions/009 §2.1）。
type Source string

const (
	SrcStartup Source = "Startup"
	SrcStart   Source = "Start"
	SrcStop    Source = "Stop"
	SrcApply   Source = "Apply"
	SrcRuntime Source = "Runtime"
	SrcInspect Source = "Inspect"
	SrcExit    Source = "Exit"
	// SrcConnect 是"连接某个目标"（decisions/012；2026-10-08 新增）。
	SrcConnect Source = "Connect"
	// SrcDebug 是「调试」窗口里的动作（修复 / 重新接管 WinDivert、重启 Workstation；
	// decisions/011 §2.7；2026-10-09 新增）。
	SrcDebug Source = "Debug"
)

type codeMeta struct {
	Category Category
	Scope    Scope
	// summaryKey 是用户可见摘要的**本地化 key**（decisions/013 §6.2）。
	summaryKey string
	// Blocking 表示该错误会阻止 Start/Apply（decisions/006 §6.10 / 008 §14）。
	Blocking bool
}

// Summary 返回当前语言下的摘要文案。
func (m codeMeta) Summary() string { return i18n.T(m.summaryKey) }

// codeTable 是 Code ↔ Category / Scope / 摘要 key 的**唯一来源**。
// 映射固定，实现不得另建一套。
var codeTable = map[Code]codeMeta{
	CodePlatformUnsupported: {CatPlatform, ScopeStartupBlock,
		"diag.err.platform_unsupported", true},
	CodeAdminRequired: {CatPermission, ScopeGlobal,
		"diag.err.admin_required", true},
	CodeSingleInstanceConflict: {CatSingleInstance, ScopeStartupBlock,
		"diag.err.single_instance", true},

	CodeRuntimeFileMissing: {CatRuntimeFile, ScopeGlobal,
		"diag.err.runtime_file_missing", true},
	CodeRuntimeFileCorrupt: {CatRuntimeFile, ScopeGlobal,
		"diag.err.runtime_file_corrupt", true},

	CodeDriverSignatureInvalid: {CatDriver, ScopeGlobal,
		"diag.err.driver_signature_invalid", true},
	CodeDriverVersionIncompatible: {CatDriver, ScopeGlobal,
		"diag.err.driver_version_incompatible", true},
	CodeDriverBlocked: {CatDriver, ScopeGlobal,
		"diag.err.driver_blocked", true},
	CodeBFENotRegistered: {CatDriver, ScopeGlobal,
		"diag.err.bfe_not_registered", true},
	CodeWinDivertTakeoverFailed: {CatDriver, ScopeGlobal,
		"diag.err.windivert_takeover_failed", true},
	CodeRepairRequiresStop: {CatDriver, ScopeGlobal,
		"diag.err.repair_requires_stop", false},
	CodeWorkstationRestartFailed: {CatSystem, ScopeGlobal,
		"diag.err.workstation_restart_failed", false},

	CodeResourceExhausted: {CatResource, ScopeTarget,
		"diag.err.resource_exhausted", false},
	CodeTargetStartFailed: {CatTarget, ScopeTarget,
		"diag.err.target_start_failed", false},
	CodeRuntimeInternalError: {CatInternal, ScopeTarget,
		"diag.err.runtime_internal", false},

	CodeConnectNotRunning: {CatConnect, ScopeTarget,
		"diag.err.connect_not_running", false},
	CodeCredentialWriteFailed: {CatConnect, ScopeTarget,
		"diag.err.credential_write_failed", false},
	CodeShareOpenFailed: {CatConnect, ScopeTarget,
		"diag.err.share_open_failed", false},
	CodeInternalError: {CatInternal, ScopeTarget,
		"diag.err.internal", false},

	CodeConfigIOError: {CatConfig, ScopeConfig,
		"diag.err.config_io", true},
	CodeConfigParseError: {CatConfig, ScopeConfig,
		"diag.err.config_parse", true},
	CodeConfigSchemaUnsupported: {CatConfig, ScopeConfig,
		"diag.err.config_schema_unsupported", true},
	CodeConfigValidationFailed: {CatConfig, ScopeConfig,
		"diag.err.config_validation", true},
}

// Meta 返回 Code 的分类信息；未知 Code 归入内部错误（不应发生）。
func Meta(c Code) codeMeta {
	if m, ok := codeTable[c]; ok {
		return m
	}
	// 未知 Code：摘要回退为 Code 本身（`i18n.T` 查不到 key 时返回 key）。
	return codeMeta{Category: CatInternal, Scope: ScopeTarget, summaryKey: string(c), Blocking: false}
}

// Known 报告 Code 是否在本决策的封闭集合内。
func Known(c Code) bool {
	_, ok := codeTable[c]
	return ok
}

// AllCodes 返回封闭集合中的全部错误码（供实现审核核对）。
func AllCodes() []Code {
	out := make([]Code, 0, len(codeTable))
	for c := range codeTable {
		out = append(out, c)
	}
	return out
}

// --- 非错误的提示（decisions/009 §2.3）--------------------------------------
//
// ⚠️ 这些**不是错误**，不得用错误样式渲染（decisions/008 G10）。
// 典型：可达性与 runtime 状态正交，"不可达"不是失败。

// Note 是非错误提示的标识。
type Note string

const (
	NoteTargetUnreachable            Note = "NOTE_TARGET_UNREACHABLE"
	NoteAndroidPortEqualsWindowsPort Note = "NOTE_ANDROID_PORT_EQUALS_WINDOWS_PORT"
	// NoteMutexAbandoned 只表示"本次等待观察到的 named mutex 处于 abandoned 状态"。
	//
	// ⚠️ 它**不能**解释为"上一个实例一定异常退出"（decisions/009 §13.1 实机修正）：
	// 命名内核对象随**最后一个句柄关闭**被销毁，因此在本程序的真实使用场景里
	// （系统上只有本程序一个句柄）进程被杀后 `CreateMutex` 得到的是**全新对象**，
	// `WAIT_ABANDONED` 根本不会出现。仅当另有进程持有时才可能观察到。
	NoteMutexAbandoned             Note = "NOTE_MUTEX_ABANDONED"
	NoteReflectConsumersUnprovable Note = "NOTE_REFLECT_CONSUMERS_UNPROVABLE"
)

// noteKey 是提示的本地化 key（decisions/013 §6.2：提示属用户可见文案）。
var noteKey = map[Note]string{
	NoteTargetUnreachable:            "diag.note.target_unreachable",
	NoteAndroidPortEqualsWindowsPort: "diag.note.port_equals",
	NoteMutexAbandoned:               "diag.note.mutex_abandoned",
	NoteReflectConsumersUnprovable:   "diag.note.reflect_unprovable",
}

// NoteText 返回当前语言下的提示文案。
func NoteText(n Note) string { return i18n.T(noteKey[n]) }
