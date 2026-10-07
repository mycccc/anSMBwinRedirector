package diag

import (
	"strings"
	"testing"
	"time"
)

func TestAllCodesAreInClosedSet(t *testing.T) {
	if len(AllCodes()) == 0 {
		t.Fatal("封闭错误码集合不应为空")
	}
	for _, c := range AllCodes() {
		if !Known(c) {
			t.Errorf("%s 不在码表内", c)
		}
		m := Meta(c)
		if m.Summary == "" {
			t.Errorf("%s 缺少摘要", c)
		}
		if m.Category == "" || m.Scope == "" {
			t.Errorf("%s 缺少分类/范围", c)
		}
	}
	// 未知码必须被识别为未知（供实现审核发现"临时造码"）。
	if Known("SOME_MADE_UP_CODE") {
		t.Fatal("未知码不应出现在封闭集合中")
	}
}

func TestBlockingCodesMatchDecision009(t *testing.T) {
	// decisions/009 §2.2：权限 / 驱动 / 运行时文件 / 配置 / 平台 / 单实例 属阻塞类。
	blocking := []Code{
		CodePlatformUnsupported, CodeAdminRequired, CodeSingleInstanceConflict,
		CodeRuntimeFileMissing, CodeRuntimeFileCorrupt,
		CodeDriverSignatureInvalid, CodeDriverVersionIncompatible, CodeDriverBlocked, CodeBFENotRegistered,
		CodeConfigIOError, CodeConfigParseError, CodeConfigSchemaUnsupported, CodeConfigValidationFailed,
	}
	for _, c := range blocking {
		if !Meta(c).Blocking {
			t.Errorf("%s 应为阻塞类", c)
		}
	}
	// 单目标类**不得**阻塞全局（decisions/007 L7）。
	for _, c := range []Code{CodeTargetStartFailed, CodeRuntimeInternalError, CodeResourceExhausted} {
		if Meta(c).Blocking {
			t.Errorf("%s 不应为阻塞类", c)
		}
		if Meta(c).Scope != ScopeTarget {
			t.Errorf("%s 应为 Target 范围", c)
		}
	}
}

func TestNotesAreNotErrors(t *testing.T) {
	for _, n := range []Note{
		NoteTargetUnreachable, NoteAndroidPortEqualsWindowsPort,
		NoteMutexAbandoned, NoteReflectConsumersUnprovable,
	} {
		if NoteText(n) == "" {
			t.Errorf("%s 缺少文案", n)
		}
	}
}

func TestAggregatorMergesSameKey(t *testing.T) {
	a := NewAggregator()
	a.Add(New(CodeAdminRequired, SrcStart, "第一个目标失败", "t1"))
	a.Add(New(CodeAdminRequired, SrcStart, "第二个目标失败", "t2"))
	list := a.List()
	if len(list) != 1 {
		t.Fatalf("全局类错误应归并为 1 条，得到 %d 条", len(list))
	}
	e := list[0]
	if e.Count != 2 {
		t.Errorf("Count = %d, 期望 2", e.Count)
	}
	if len(e.AffectedTargetIDs) != 2 {
		t.Errorf("受影响目标数 = %d, 期望 2", len(e.AffectedTargetIDs))
	}
	if e.TargetID != "" {
		t.Error("全局类错误的 TargetID 应为空")
	}
}

func TestAggregatorKeepsTargetErrorsSeparate(t *testing.T) {
	a := NewAggregator()
	a.Add(New(CodeTargetStartFailed, SrcStart, "失败 A", "t1"))
	a.Add(New(CodeTargetStartFailed, SrcStart, "失败 B", "t2"))
	if got := len(a.List()); got != 2 {
		t.Fatalf("Target 级错误不得跨目标归并，得到 %d 条", got)
	}
	// 同一目标的同码错误才归并。
	a.Add(New(CodeTargetStartFailed, SrcStart, "失败 A2", "t1"))
	if got := len(a.List()); got != 2 {
		t.Fatalf("同一目标同码应归并，得到 %d 条", got)
	}
}

func TestAggregatorClearSemantics(t *testing.T) {
	// decisions/009 §4：错误必须能被清除（条件消失时）。
	a := NewAggregator()
	a.Add(New(CodeAdminRequired, SrcStart, "权限", "t1"))
	a.Add(New(CodeTargetStartFailed, SrcStart, "失败", "t1"))
	a.Add(New(CodeConfigParseError, SrcStartup, "坏文件", ""))

	if !a.HasBlocking() {
		t.Fatal("应存在阻塞类错误")
	}
	a.ClearTarget("t1")
	if got := len(a.List()); got != 2 {
		t.Fatalf("清除目标级错误后应剩 2 条，得到 %d", got)
	}
	a.ClearByScope(ScopeConfig)
	if got := len(a.List()); got != 1 {
		t.Fatalf("清除配置类错误后应剩 1 条，得到 %d", got)
	}
	a.ClearByScope(ScopeGlobal)
	if got := len(a.List()); got != 0 {
		t.Fatalf("应清空，得到 %d", got)
	}
	if a.HasBlocking() {
		t.Fatal("清空后不应再有阻塞类错误")
	}
}

func TestRingIsBoundedAndNewestFirst(t *testing.T) {
	r := NewRing(5)
	for i := 1; i <= 12; i++ {
		r.Add(Entry{Level: LevelInfo, Message: string(rune('a' + i - 1))})
	}
	if r.Len() != 5 {
		t.Fatalf("环形缓冲长度 = %d, 期望 5", r.Len())
	}
	snap := r.Snapshot()
	if len(snap) != 5 {
		t.Fatalf("快照长度 = %d, 期望 5", len(snap))
	}
	// 最新的是第 12 条（'l'）。
	if snap[0].Message != "l" {
		t.Errorf("最新条目 = %q, 期望 %q", snap[0].Message, "l")
	}
	if snap[4].Message != "h" {
		t.Errorf("最旧条目 = %q, 期望 %q", snap[4].Message, "h")
	}
}

func TestFormatDetailsHasFixedSections(t *testing.T) {
	d := Details{
		Version:   VersionInfo{Version: "0.1.0", GoVersion: "go1.27.1", Platform: "windows/amd64", Release: "release"},
		OS:        "Windows 10 x64",
		Time:      time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC),
		Mode:      "运行中",
		Operation: "none",
		Aggregate: "2 个目标（2 运行中 · 0 失败）",
		Errors:    []ErrorInfo{New(CodeDriverBlocked, SrcStart, "被安全软件阻止", "t1")},
		Config:    ConfigInfo{Path: `C:\x\config.json`, State: "Loaded", SchemaVersion: 1, TargetCount: 2},
		WinDivert: WinDivertInfo{Available: true, ServiceExists: true, ImagePathIsOurs: true,
			DriverState: "Loaded", DriverVersion: "2.2.2", RuntimeFilesOK: true},
		Targets: []TargetInfo{{ID: "t1", Endpoint: "192.168.1.100:4450", Runtime: "Running", Reachability: "可达"}},
		Recent:  []Entry{{Level: LevelWarn, Time: time.Now(), Source: SrcStart, Message: "目标 t1 启动失败"}},
	}
	s := FormatDetails(d)

	for _, want := range []string{
		"===== anSMBwinRedirector 诊断详情 =====",
		"[全局状态]", "[当前错误]", "[配置]", "[WinDivert]", "[目标]", "[最近事件]",
		"DRIVER_BLOCKED", "驱动 / 全局（影响 1 个目标：t1）",
		"192.168.1.100:4450", "===== 完 =====",
		NoteText(NoteReflectConsumersUnprovable),
		"本程序不执行任何驱动卸载操作",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("诊断详情缺少 %q", want)
		}
	}
	// 空错误 / 空目标也必须渲染出小节，而不是略过。
	if !strings.Contains(FormatDetails(Details{Time: time.Now()}), "[当前错误]") {
		t.Error("空详情也应包含 [当前错误] 小节")
	}
}

// 「尚未做过只读检查」不得被写成「不可用」—— 这份文本是用户报问题时要附上的诊断，
// 把"没检查过"说成"不可用"会误导（实机验证时发现）。
func TestFormatDetailsDistinguishesNotInspectedFromUnavailable(t *testing.T) {
	notInspected := FormatDetails(Details{Time: time.Now()})
	if !strings.Contains(notInspected, "尚未检查") {
		t.Errorf("未检查过时应写明\"尚未检查\"：\n%s", notInspected)
	}
	if strings.Contains(notInspected, "不可用：-") {
		t.Error("不得输出\"不可用：-\"这种既像故障又无原因的文本")
	}

	failed := FormatDetails(Details{Time: time.Now(), WinDivert: WinDivertInfo{Available: false, Err: "权限不足"}})
	if !strings.Contains(failed, "不可用：权限不足") {
		t.Errorf("真失败时应带原因：\n%s", failed)
	}
}
