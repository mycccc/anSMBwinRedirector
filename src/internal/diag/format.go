package diag

import (
	"fmt"
	"strings"
	"time"
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

// WinDivertInfo 是 WinDivert / 驱动诊断信息（decisions/009 §10）。
type WinDivertInfo struct {
	Available        bool
	Err              string
	ServiceExists    bool
	ServiceImagePath string
	ImagePathIsOurs  bool
	DriverState      string
	DriverVersion    string
	// WinDivertDir 是 WinDivert 运行时文件（DLL/SYS）所在目录（decisions/010 §D）。
	WinDivertDir   string
	RuntimeFilesOK bool
	Consumers      []Consumer
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
// ⛔ 不得包含：数据包载荷 / SMB 内容 / 凭据 / 文件内容（decisions/009 §8.3）。
func FormatDetails(d Details) string {
	var b strings.Builder
	line := func(k, v string) {
		if v == "" {
			v = "-"
		}
		fmt.Fprintf(&b, "%-11s : %s\n", k, v)
	}

	b.WriteString("===== anSMBwinRedirector 诊断详情 =====\n")
	line("版本", fmt.Sprintf("%s (commit %s, build %s)", orDash(d.Version.Version), orDash(d.Version.Commit), orDash(d.Version.BuildTime)))
	line("工具链", fmt.Sprintf("%s %s (%s)", orDash(d.Version.GoVersion), orDash(d.Version.Platform), orDash(d.Version.Release)))
	line("系统", orDash(d.OS))
	line("时间", d.Time.Format("2006-01-02 15:04:05 -07:00"))
	b.WriteString("\n")

	b.WriteString("[全局状态]\n")
	line("模式", d.Mode)
	line("当前操作", d.Operation)
	line("聚合", d.Aggregate)
	b.WriteString("\n")

	b.WriteString("[当前错误]\n")
	if len(d.Errors) == 0 {
		b.WriteString("（无）\n")
	} else {
		for _, e := range d.Errors {
			fmt.Fprintf(&b, "错误码      : %s\n", e.Code)
			scope := string(e.Scope)
			if e.Scope == ScopeGlobal {
				ids := affectedOf(e)
				if len(ids) > 0 {
					scope = fmt.Sprintf("全局（影响 %d 个目标：%s）", len(ids), strings.Join(ids, ", "))
				}
			} else if e.TargetID != "" {
				scope = fmt.Sprintf("%s（%s）", e.Scope, e.TargetID)
			}
			line("分类 / 范围", fmt.Sprintf("%s / %s", e.Category, scope))
			line("摘要", e.Summary)
			line("详情", e.Detail)
			if e.Count > 1 {
				line("次数", fmt.Sprintf("%d（首次 %s）", e.Count, e.FirstAt.Format("15:04:05")))
			}
			b.WriteString("\n")
		}
	}

	b.WriteString("[配置]\n")
	line("路径", d.Config.Path)
	line("状态", fmt.Sprintf("%s   schema_version=%d   目标数=%d   备份: %s",
		orDash(d.Config.State), d.Config.SchemaVersion, d.Config.TargetCount, yesNo(d.Config.BackupExists)))
	for _, v := range d.Config.Violations {
		line("校验问题", v)
	}
	b.WriteString("\n")

	b.WriteString("[WinDivert]\n")
	if !d.WinDivert.Available {
		// ⚠️ 必须把"**尚未做过只读检查**"与"检查失败"分开说。
		// 本段在没有 Inspect 过时是零值（Available=false, Err=""），早先统一写成
		// "不可用：-"，容易被误读成"WinDivert 不可用"且没有任何原因
		// —— 这份文本是用户报问题时要附上的诊断（实机验证时发现）。
		if d.WinDivert.Err == "" {
			line("检查", "尚未检查（点「检查 WinDivert（只读）」后本段才有内容）")
		} else {
			line("检查", "不可用："+d.WinDivert.Err)
		}
	} else {
		line("服务", fmt.Sprintf("%s   ImagePath->本程序 runtime: %s",
			yesNo(d.WinDivert.ServiceExists), yesNo(d.WinDivert.ImagePathIsOurs)))
		line("服务路径", d.WinDivert.ServiceImagePath)
		line("驱动", fmt.Sprintf("%s   驱动版本: %s", orDash(d.WinDivert.DriverState), orDash(d.WinDivert.DriverVersion)))
		line("WinDivert 文件目录", fmt.Sprintf("%s   完整: %s", orDash(d.WinDivert.WinDivertDir), yesNo(d.WinDivert.RuntimeFilesOK)))
		if len(d.WinDivert.Consumers) == 0 {
			line("可观察使用者", "（无）")
		} else {
			for _, c := range d.WinDivert.Consumers {
				line("可观察使用者", fmt.Sprintf("PID=%d layer=%s 本程序=%s", c.PID, c.Layer, yesNo(c.IsOurs)))
			}
		}
		fmt.Fprintf(&b, "⚠ %s\n", NoteText(NoteReflectConsumersUnprovable))
		b.WriteString("   本程序不执行任何驱动卸载操作。\n")
	}
	b.WriteString("\n")

	b.WriteString("[目标]\n")
	if len(d.Targets) == 0 {
		b.WriteString("（无）\n")
	} else {
		for i, t := range d.Targets {
			fmt.Fprintf(&b, "%d) id=%s %s  运行态=%s  可达性=%s", i+1, t.ID, t.Endpoint, t.Runtime, t.Reachability)
			if t.LastChecked != "" {
				fmt.Fprintf(&b, "(%s)", t.LastChecked)
			}
			b.WriteString("\n")
			if t.Note != "" {
				fmt.Fprintf(&b, "       提示: %s\n", t.Note)
			}
		}
	}
	b.WriteString("\n")

	b.WriteString("[最近事件]\n")
	if len(d.Recent) == 0 {
		b.WriteString("（无）\n")
	} else {
		b.WriteString(FormatRecent(d.Recent))
	}
	b.WriteString("===== 完 =====\n")
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
		return "是"
	}
	return "否"
}
