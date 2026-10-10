package diag

import (
	"fmt"
	"strings"
	"time"

	"ansmbwinhelper/internal/i18n"
)

// VersionInfo 是版本 / 构建信息（decisions/009 §14）。
type VersionInfo struct {
	Version   string
	Commit    string
	BuildTime string
	GoVersion string
	Platform  string
	Release   string
}

// Consumer 是一条 WinDivert 使用者记录（可观察层）。
//
// ⚠️ 该枚举**只能**看到 NETWORK / NETWORK_FORWARD / FLOW / SOCKET 四层；
// REFLECT 层使用者**不可见**（decisions/009 §10.1 / 007 §4.7.1）。
type Consumer struct {
	PID    uint32
	Layer  string
	IsOurs bool
}

// RuntimeFile 是 **WinDivert 运行时文件**的校验结果（decisions/011 §2.7）。
//
// ⚠️ Name / Path / SHA-256 全部是**诊断数据** ⇒ 原样呈现，⛔ 不本地化。
type RuntimeFile struct {
	// Name 是文件名（内部标识，不本地化），如 `WinDivert.dll`。
	Name string
	// Path 是实际检查的路径。
	Path string
	// Actual 是实际 SHA-256（小写十六进制）；读不到时为空。
	Actual string
	// Expected 是固化的期望 SHA-256。
	Expected string
	// Version 是**文件**的版本资源（如 `2.2.2.0`）；读不到时为空（上层显示"未知"）。
	//
	// ⚠️ 这是**文件版本**，⛔ 不是"当前已加载驱动的运行时版本"（decisions/011 §9.3 D-1）。
	Version string
	// OK 报告是否 "存在 且 哈希一致"。
	OK bool
	// Err 是读取/校验失败原因（诊断数据，不本地化）；OK=true 时为空。
	Err string
}

// WinDivertInfo 是 WinDivert / 驱动诊断信息（decisions/009 §10）。
type WinDivertInfo struct {
	Available        bool
	Err              string
	ServiceExists    bool
	ServiceImagePath string
	ImagePathIsOurs  bool
	DriverState      string
	// DriverRuntimeVersion 是**当前已加载驱动**的运行时版本。
	//
	// ⛔ 只能通过 REFLECT 句柄得到，而该句柄会**隐式安装并加载驱动**（decisions/011 §9.3 **D-1**），
	// 因此**只读诊断路径不再查询它** ⇒ 在 v1 里**恒为空**，上层显示"未查询"。
	// ⛔ 不得用文件版本或期望版本冒充它。
	DriverRuntimeVersion string
	// WinDivertDir 是 WinDivert 运行时文件（DLL/SYS）所在目录（decisions/010 §D）。
	WinDivertDir   string
	RuntimeFilesOK bool
	// Files 是运行时文件的逐项校验结果（decisions/011 §2.7：路径 + 实际/期望 SHA-256 + 文件版本）。
	Files []RuntimeFile
	// ConsumersQueried 报告"可观察使用者"**是否真的查询过**。
	//
	// ⛔ 枚举需要一个 REFLECT 句柄（同样会隐式安装驱动）⇒ v1 的只读路径**不查询** ⇒ 恒为 false。
	// 上层必须在未查询时明确显示"未查询"，⛔ **不得**把"没查"显示成"（无）"。
	ConsumersQueried bool
	Consumers        []Consumer
}

// ConfigInfo 是配置诊断信息（decisions/009 §11；⛔ 不含配置全文）。
type ConfigInfo struct {
	Path          string
	State         string
	SchemaVersion int
	TargetCount   int
	BackupExists  bool
	Violations    []string
}

// TargetInfo 是单个目标的诊断行（decisions/009 §7）。
type TargetInfo struct {
	ID           string
	Endpoint     string
	Runtime      string
	Reachability string
	LastChecked  string
	Note         string
}

// Details 是"复制详情"的全部输入（decisions/009 §7）。
type Details struct {
	Version   VersionInfo
	OS        string
	Time      time.Time
	Mode      string
	Operation string
	Aggregate string

	Errors    []ErrorInfo
	Config    ConfigInfo
	WinDivert WinDivertInfo
	Targets   []TargetInfo
	Recent    []Entry
}

// FormatDetails 按 decisions/009 §7 的**固定字段顺序**渲染纯文本。
//
// ⚠️ 字段**标签/模板**是用户可见文案 ⇒ 一律经 `i18n.T()`（decisions/013 §6.2）；
// **值**（路径 / 版本 / ErrorCode / PID / true·false / IP / 用户名 …）**原样输出**。
//
// ⛔ 不得包含：数据包载荷 / SMB 内容 / 凭据 / 文件内容（decisions/009 §8.3）。
func FormatDetails(d Details) string {
	var b strings.Builder
	line := func(k, v string) {
		if v == "" {
			v = "-"
		}
		fmt.Fprintf(&b, "%-11s : %s\n", k, v)
	}
	T := i18n.T

	b.WriteString(T("diag.header") + "\n")
	line(T("diag.label.version"), fmt.Sprintf("%s (commit %s, build %s)", orDash(d.Version.Version), orDash(d.Version.Commit), orDash(d.Version.BuildTime)))
	line(T("diag.label.toolchain"), fmt.Sprintf("%s %s (%s)", orDash(d.Version.GoVersion), orDash(d.Version.Platform), orDash(d.Version.Release)))
	line(T("diag.label.os"), orDash(d.OS))
	line(T("diag.label.time"), d.Time.Format("2006-01-02 15:04:05 -07:00"))
	b.WriteString("\n")

	b.WriteString(T("diag.section.global") + "\n")
	line(T("diag.label.mode"), d.Mode)
	line(T("diag.label.operation"), d.Operation)
	line(T("diag.label.aggregate"), d.Aggregate)
	b.WriteString("\n")

	b.WriteString(T("diag.section.errors") + "\n")
	if len(d.Errors) == 0 {
		b.WriteString(T("diag.none") + "\n")
	} else {
		for _, e := range d.Errors {
			fmt.Fprintf(&b, "%-11s : %s\n", T("diag.label.code"), e.Code)
			scope := ScopeText(e.Scope)
			if e.Scope == ScopeGlobal {
				ids := affectedOf(e)
				if len(ids) > 0 {
					scope = fmt.Sprintf(T("diag.scope_global_affected"), len(ids), strings.Join(ids, ", "))
				}
			} else if e.TargetID != "" {
				scope = fmt.Sprintf(T("diag.scope_target_fmt"), ScopeText(e.Scope), e.TargetID)
			}
			line(T("diag.label.category_scope"), fmt.Sprintf("%s / %s", CategoryText(e.Category), scope))
			line(T("diag.label.summary"), e.Summary)
			line(T("diag.label.detail"), e.Detail)
			if e.Count > 1 {
				line(T("diag.label.count"), fmt.Sprintf(T("diag.count_fmt"), e.Count, e.FirstAt.Format("15:04:05")))
			}
			b.WriteString("\n")
		}
	}

	b.WriteString(T("diag.section.config") + "\n")
	line(T("diag.label.path"), d.Config.Path)
	line(T("diag.label.state"), fmt.Sprintf(T("diag.config_summary_fmt"),
		orDash(d.Config.State), d.Config.SchemaVersion, d.Config.TargetCount, yesNo(d.Config.BackupExists)))
	for _, v := range d.Config.Violations {
		line(T("diag.label.violation"), v)
	}
	b.WriteString("\n")

	b.WriteString(T("diag.section.windivert") + "\n")
	if !d.WinDivert.Available {
		// ⚠️ 必须把"**尚未做过只读检查**"与"检查失败"分开说。
		// 本段在没有 Inspect 过时是零值（Available=false, Err=""），早先统一写成
		// "不可用：-"，容易被误读成"WinDivert 不可用"且没有任何原因
		// —— 这份文本是用户报问题时要附上的诊断（实机验证时发现）。
		if d.WinDivert.Err == "" {
			line(T("diag.label.check"), T("diag.check.not_done"))
		} else {
			line(T("diag.label.check"), T("diag.check.unavailable_prefix")+d.WinDivert.Err)
		}
	} else {
		line(T("diag.label.service"), fmt.Sprintf(T("diag.service_fmt"),
			yesNo(d.WinDivert.ServiceExists), yesNo(d.WinDivert.ImagePathIsOurs)))
		line(T("diag.label.service_path"), d.WinDivert.ServiceImagePath)
		line(T("diag.label.driver"), fmt.Sprintf(T("diag.driver_fmt"), orDash(d.WinDivert.DriverState),
			runtimeVersionText(d.WinDivert.DriverRuntimeVersion)))
		line(T("diag.label.windivert_dir"), fmt.Sprintf(T("diag.files_fmt"), orDash(d.WinDivert.WinDivertDir), yesNo(d.WinDivert.RuntimeFilesOK)))
		for _, f := range d.WinDivert.Files {
			line(T("diag.label.file"), fmt.Sprintf(T("diag.file_fmt"),
				f.Name, orDash(f.Path), orDash(f.Version), fileHashText(f)))
		}
		if !d.WinDivert.ConsumersQueried {
			line(T("diag.label.consumers"), T("diag.consumers_not_queried"))
		} else if len(d.WinDivert.Consumers) == 0 {
			line(T("diag.label.consumers"), T("diag.none"))
		} else {
			for _, c := range d.WinDivert.Consumers {
				line(T("diag.label.consumers"), fmt.Sprintf(T("diag.consumer_fmt"), c.PID, c.Layer, yesNo(c.IsOurs)))
			}
		}
		fmt.Fprintf(&b, T("diag.warn_fmt")+"\n", NoteText(NoteReflectConsumersUnprovable))
		b.WriteString(T("diag.no_uninstall") + "\n")
	}
	b.WriteString("\n")

	b.WriteString(T("diag.section.targets") + "\n")
	if len(d.Targets) == 0 {
		b.WriteString(T("diag.none") + "\n")
	} else {
		for i, t := range d.Targets {
			fmt.Fprintf(&b, T("diag.target_line_fmt"), i+1, t.ID, t.Endpoint, t.Runtime, t.Reachability)
			if t.LastChecked != "" {
				fmt.Fprintf(&b, "(%s)", t.LastChecked)
			}
			b.WriteString("\n")
			if t.Note != "" {
				fmt.Fprintf(&b, T("diag.target_note_fmt")+"\n", t.Note)
			}
		}
	}
	b.WriteString("\n")

	b.WriteString(T("diag.section.recent") + "\n")
	if len(d.Recent) == 0 {
		b.WriteString(T("diag.none") + "\n")
	} else {
		b.WriteString(FormatRecent(d.Recent))
	}
	b.WriteString(T("diag.footer") + "\n")
	return b.String()
}

// affectedOf 返回该错误影响的全部目标 ID（合并聚合列表与单目标字段）。
func affectedOf(e ErrorInfo) []string {
	ids := append([]string(nil), e.AffectedTargetIDs...)
	if e.TargetID != "" {
		ids = appendUnique(ids, e.TargetID)
	}
	return ids
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func yesNo(b bool) string {
	if b {
		return i18n.T("diag.yes")
	}
	return i18n.T("diag.no")
}

// runtimeVersionText 渲染"运行时驱动版本"：空 ⇒ 明确写"未查询"（⛔ 不用文件/期望版本冒充）。
func runtimeVersionText(v string) string {
	if strings.TrimSpace(v) == "" {
		return i18n.T("diag.driver_runtime_not_queried")
	}
	return v
}

// fileHashText 渲染单个运行时文件的校验结论（诊断数据，原样）。
func fileHashText(f RuntimeFile) string {
	if f.Err != "" {
		return f.Err
	}
	if f.Actual == "" || f.Expected == "" {
		return i18n.T("diag.file_hash_unknown")
	}
	if f.OK {
		return f.Actual
	}
	return f.Actual + " != " + f.Expected
}
