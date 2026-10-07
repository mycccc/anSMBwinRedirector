// Package diag 是 v1 的错误分类与诊断模型，严格对应 decisions/009。
//
//	§2 错误码（封闭集合，Code 是**对外契约**）
//	§3 错误聚合
//	§6 DiagnosticEntry（有界内存环形缓冲）
//	§7 "复制详情"的固定文本模板
//	§10 WinDivert 诊断（数据结构；采集在平台层）
//
// 本包是纯逻辑，不依赖 Windows。
package diag

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

	// --- 资源 / 目标 ---
	CodeResourceExhausted    Code = "RESOURCE_EXHAUSTED"
	CodeTargetStartFailed    Code = "TARGET_START_FAILED"
	CodeRuntimeInternalError Code = "RUNTIME_INTERNAL_ERROR"

	// --- 内部（编程错误，不应出现）---
	CodeInternalError Code = "INTERNAL_ERROR"

	// --- 配置 ---
	CodeConfigIOError           Code = "CONFIG_IO_ERROR"
	CodeConfigParseError        Code = "CONFIG_PARSE_ERROR"
	CodeConfigSchemaUnsupported Code = "CONFIG_SCHEMA_UNSUPPORTED"
	CodeConfigValidationFailed  Code = "CONFIG_VALIDATION_FAILED"
)

// Category 是错误分类（decisions/009 §2.1）。
type Category string

const (
	CatPlatform       Category = "平台"
	CatPermission     Category = "权限"
	CatSingleInstance Category = "单实例"
	CatRuntimeFile    Category = "运行时文件"
	CatDriver         Category = "驱动"
	CatResource       Category = "资源"
	CatConfig         Category = "配置"
	CatTarget         Category = "目标态"
	CatInternal       Category = "内部"
)

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

// Source 标记错误产生的阶段（decisions/009 §2.1）。
type Source string

const (
	SrcStartup Source = "Startup"
	SrcStart   Source = "Start"
	SrcApply   Source = "Apply"
	SrcRuntime Source = "Runtime"
	SrcInspect Source = "Inspect"
	SrcExit    Source = "Exit"
)

type codeMeta struct {
	Category Category
	Scope    Scope
	Summary  string
	// Blocking 表示该错误会阻止 Start/Apply（decisions/006 §6.10 / 008 §14）。
	Blocking bool
}

// codeTable 是 Code ↔ Category / Scope / 摘要 的**唯一来源**。
// 映射固定，实现不得另建一套。
var codeTable = map[Code]codeMeta{
	CodePlatformUnsupported: {CatPlatform, ScopeStartupBlock,
		"当前平台不受支持（v1 仅支持 Windows x64）", true},
	CodeAdminRequired: {CatPermission, ScopeGlobal,
		"需要管理员权限", true},
	CodeSingleInstanceConflict: {CatSingleInstance, ScopeStartupBlock,
		"anSMBwinRedirector 已在运行", true},

	CodeRuntimeFileMissing: {CatRuntimeFile, ScopeGlobal,
		"WinDivert 运行时文件缺失", true},
	CodeRuntimeFileCorrupt: {CatRuntimeFile, ScopeGlobal,
		"WinDivert 运行时文件已损坏（内容与预期不符）", true},

	CodeDriverSignatureInvalid: {CatDriver, ScopeGlobal,
		"WinDivert 驱动签名无效", true},
	CodeDriverVersionIncompatible: {CatDriver, ScopeGlobal,
		"系统中已加载不兼容版本的 WinDivert 驱动", true},
	CodeDriverBlocked: {CatDriver, ScopeGlobal,
		"WinDivert 驱动无法加载（可能被安全软件阻止或环境不支持）", true},
	CodeBFENotRegistered: {CatDriver, ScopeGlobal,
		"基础筛选引擎（BFE）服务未运行", true},

	CodeResourceExhausted: {CatResource, ScopeTarget,
		"资源不足（句柄或内存）", false},
	CodeTargetStartFailed: {CatTarget, ScopeTarget,
		"该目标的运行时启动失败", false},
	CodeRuntimeInternalError: {CatInternal, ScopeTarget,
		"运行时内部错误，该目标已停止", false},
	CodeInternalError: {CatInternal, ScopeTarget,
		"内部错误（请报告）", false},

	CodeConfigIOError: {CatConfig, ScopeConfig,
		"配置文件读写失败", true},
	CodeConfigParseError: {CatConfig, ScopeConfig,
		"配置文件解析失败", true},
	CodeConfigSchemaUnsupported: {CatConfig, ScopeConfig,
		"配置文件由更高版本写入，本程序无法读取", true},
	CodeConfigValidationFailed: {CatConfig, ScopeConfig,
		"配置校验失败", true},
}

// Meta 返回 Code 的分类信息；未知 Code 归入内部错误（不应发生）。
func Meta(c Code) codeMeta {
	if m, ok := codeTable[c]; ok {
		return m
	}
	return codeMeta{Category: CatInternal, Scope: ScopeTarget, Summary: string(c), Blocking: false}
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

// noteText 是提示的固定文案。
var noteText = map[Note]string{
	NoteTargetUnreachable:            "目标不可达（仅表示最近一次连接测试结果，与运行时状态无关）",
	NoteAndroidPortEqualsWindowsPort: "Android SMB 端口与 Windows SMB 端口相同，无需端口重定向",
	NoteMutexAbandoned:               "观察到单实例互斥体处于 abandoned 状态（本程序不据此判定上次运行是否异常结束）",
	NoteReflectConsumersUnprovable:   "REFLECT 层使用者无法枚举，因此无法证明系统中不存在其他 WinDivert 使用者",
}

// NoteText 返回提示文案。
func NoteText(n Note) string { return noteText[n] }
