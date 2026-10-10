//go:build windows

// Command devconsole 是**开发 / 集成测试用的控制台入口**。
//
// ⛔ 它**不是产品**，也不是 CLI 产品设计：没有子命令体系、没有 help 文档、
// 没有配置管理 UX。产品入口是 `cmd/ansmbwinredirector`（GUI / Tray，decisions/003 / 008）。
//
// 它存在的唯一理由：在没有交互桌面的环境下（SSH、CI）驱动同一套 Controller/Runtime
// 做集成验证，并在退出时输出诊断与分层计数。
package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"time"

	"ansmbwinhelper/internal/app"
	"ansmbwinhelper/internal/diag"
	"ansmbwinhelper/internal/i18n"
	ansmbrt "ansmbwinhelper/internal/runtime"
	"ansmbwinhelper/internal/snapshot"
)

// 由 -ldflags -X 注入（decisions/009 §14）。
var (
	version   = "1.0-dev"
	commit    = ""
	buildTime = ""
	release   = "dev"
)

func main() {
	// ⚠️ 正常退出走 `main()` 自然返回（不调用 os.Exit）：退出序列的最后一步是
	// ReleaseMutex，之后不应再执行任何语句（decisions/007 L2 的生命周期细化）。
	if code := run(); code != 0 {
		os.Exit(code)
	}
}

func run() int {
	// decisions/013：与产品入口一致，启动时确定语言并注入进程级翻译器
	// （否则 diag.NoteText 等会退化成 key 字符串）。
	i18n.SetDefault(i18n.LoadDefault())

	// 最小开关（仅开发/测试所需；不构成 CLI 产品）。
	configPath := flag.String("config", "", "覆盖配置文件路径（仅开发/测试用）")
	duration := flag.Duration("duration", 0, "运行指定时长后自动停止（0 = 直到 Ctrl+C）")
	verbose := flag.Bool("verbose", false, "打印每一次状态变化")
	flag.Parse()

	a, err := app.Start(app.Options{
		ConfigPath: *configPath,
		Version: diag.VersionInfo{
			Version: version, Commit: commit, BuildTime: buildTime,
			GoVersion: runtime.Version(), Platform: runtime.GOOS + "/" + runtime.GOARCH,
			Release: release,
		},
	})
	if err != nil {
		fmt.Printf("⛔ 启动失败 [%s]: %v\n", diag.CodeOr(err, diag.CodeInternalError), err)
		return 2
	}

	ctl := a.Ctl
	fmt.Printf("anSMBwinRedirector %s (%s)  [devconsole]\n", version, release)
	fmt.Printf("配置文件 : %s\n", a.Path)
	fmt.Printf("运行环境 : %s  %s\n", app.OSInfo(), runtime.Version())

	// 观察到互斥体处于 abandoned 状态（decisions/009 §13.1 的 NOTE_MUTEX_ABANDONED）。
	// ⚠️ 这**不**代表上一个实例异常退出 —— 本程序不做该判定。
	if a.Sig.MutexAbandoned {
		fmt.Printf("提示: %s\n", diag.NoteText(diag.NoteMutexAbandoned))
	}

	snap := ctl.Snapshot()
	if snap.Global.Mode == snapshot.ModeConfigUnavailable {
		// decisions/007 §4.8 P2：配置不可用**不是**启动失败，程序不退出。
		// 控制台入口没有 GUI，只能打印阻塞信息后退出；
		// ⛔ GUI 入口会保持运行并显示阻塞横幅 + 两个修复动作（decisions/008 §14）。
		fmt.Println()
		fmt.Println("⛔ 配置不可用 —— 在修复前不会启动重定向，也不会覆盖该文件：")
		fmt.Printf("   路径 : %s\n", a.Path)
		for _, v := range snap.Config.Violations {
			fmt.Printf("   问题 : %s\n", v)
		}
		fmt.Println("   处置 : 手工修复该文件，或用空配置重新开始（改名原文件）")
		a.Sig.Release()
		return 3
	}

	var lastLine string
	ctl.Subscribe(func(s snapshot.Snapshot) {
		if line := s.StatusLine(); *verbose || line != lastLine {
			lastLine = line
			fmt.Printf("[状态] %s  (op=%s)\n", line, s.Global.Operation)
		}
	})

	// 只读的 WinDivert 使用情况检查（decisions/007 §4.7.2：**只检查，不卸载**）。
	printInspect(ctl.Inspect())

	// P4 启动（按已保存配置；targets=[] 也合法 ⇒ `运行中 · 0 个目标`）。
	if err := ctl.Start(); err != nil {
		fmt.Printf("启动失败: %v\n", err)
		a.Sig.Release()
		return 4
	}
	fmt.Printf("[状态] %s\n", ctl.Snapshot().StatusLine())

	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt)
	if *duration > 0 {
		timer := time.NewTimer(*duration)
		defer timer.Stop()
		select {
		case <-ch:
			fmt.Println("收到停止信号")
		case <-timer.C:
			fmt.Println("运行时长到期")
		}
	} else {
		<-ch
		fmt.Println("收到停止信号")
	}

	// ---------- 退出序列（decisions/007 §4.6）----------
	// ⛔ 不执行 sc stop / sc delete，不卸载驱动，不删除 runtime 文件（L5/L6）。
	// 计数必须在 Shutdown **之前**采集：StopAll 会丢弃 runtime，之后就取不到了。
	type counterLine struct {
		id string
		c  ansmbrt.Counters
	}
	var counters []counterLine
	for _, t := range ctl.Snapshot().Persisted {
		if c, ok := a.Counters(t.ID); ok {
			counters = append(counters, counterLine{id: t.ID, c: c})
		}
	}
	fmt.Println(ctl.CopyDetails())
	ctl.Shutdown(2 * time.Second)
	for _, cl := range counters {
		fmt.Printf("[计数] %s out=%d in=%d pass=%d noMatch=%d notApplicable=%d recvErr=%d sendErr=%d\n",
			cl.id, cl.c.OutRewritten, cl.c.InRewritten, cl.c.PassThrough, cl.c.NoMatch,
			cl.c.NotApplicable, cl.c.RecvErr, cl.c.SendErr)
	}
	fmt.Printf("[状态] %s\n", ctl.Snapshot().StatusLine())
	fmt.Println("已退出；按设计未卸载 WinDivert 驱动，也未删除 runtime 文件。")

	// 退出序列的最后一步：释放互斥体（decisions/007 L2 的生命周期细化）。
	a.Sig.Release()
	return 0
}

func printInspect(info diag.WinDivertInfo) {
	fmt.Println()
	fmt.Println("[WinDivert 只读检查]")
	if !info.Available {
		fmt.Printf("   不可用: %s\n", info.Err)
		return
	}
	fmt.Printf("   服务存在 : %v\n", info.ServiceExists)
	if info.ServiceImagePath != "" {
		fmt.Printf("   服务路径 : %s\n", info.ServiceImagePath)
	}
	fmt.Printf("   ImagePath 属于本程序 runtime : %v\n", info.ImagePathIsOurs)
	fmt.Printf("   驱动状态 : %s   运行时驱动版本: %s\n", info.DriverState, info.DriverRuntimeVersion)
	fmt.Printf("   WinDivert 文件目录 : %s (完整=%v)\n", info.WinDivertDir, info.RuntimeFilesOK)
	if len(info.Consumers) == 0 {
		fmt.Println("   可观察使用者 : （无）")
	} else {
		for _, c := range info.Consumers {
			fmt.Printf("   可观察使用者 : PID=%d layer=%s 本程序=%v\n", c.PID, c.Layer, c.IsOurs)
		}
	}
	fmt.Printf("   ⚠ %s\n", diag.NoteText(diag.NoteReflectConsumersUnprovable))
	fmt.Println("   本程序不执行任何驱动卸载操作。")
}
