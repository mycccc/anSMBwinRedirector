//go:build windows

// Command ansmbwinredirector 是**产品入口**（GUI / Tray，decisions/003 + 008）。
//
// ⛔ 它本身不做任何网络 / 配置 / 驱动操作：
// 启动序列与装配全部在 `internal/app`，界面与交互全部在 `internal/gui`，
// 两者都只能经 `internal/controller` 工作。
package main

import (
	"fmt"
	"os"
	"runtime"

	"github.com/lxn/walk"

	"ansmbwinhelper/internal/app"
	"ansmbwinhelper/internal/diag"
	"ansmbwinhelper/internal/gui"
	"ansmbwinhelper/internal/i18n"
)

// 由 -ldflags -X 注入（decisions/009 §14）。
var (
	version   = "1.0-dev"
	commit    = ""
	buildTime = ""
	release   = "dev"
)

func main() {
	// ⚠️ 正常退出走 `main()` **自然返回**（不调用 os.Exit）：退出序列的最后一步是
	// ReleaseMutex，之后不应再执行任何语句（decisions/007 L2 的生命周期细化）。
	if code := run(); code != 0 {
		os.Exit(code)
	}
}

func run() int {
	// decisions/013：**启动时确定语言**（⛔ 不做运行时切换）。
	// 加载失败/文件缺失 ⇒ 内置 fallback，**不阻止启动**。
	tr := i18n.LoadDefault()
	gui.SetTranslator(tr)
	// ⚠️ 进程级默认翻译器：snapshot / diag / app 等**非 GUI** 的用户可见文案产出点
	// 也经它取文案（decisions/013 §6.2）。必须在 `app.Start` **之前**注入。
	i18n.SetDefault(tr)

	a, err := app.Start(app.Options{
		Version: diag.VersionInfo{
			Version: version, Commit: commit, BuildTime: buildTime,
			GoVersion: runtime.Version(), Platform: runtime.GOOS + "/" + runtime.GOARCH,
			Release: release,
		},
	})
	if err != nil {
		// 启动序列失败（平台 / 权限 / 单实例 / 路径）⇒ 明确的阻塞式提示，含可操作信息。
		walk.MsgBox(nil, gui.T("app.start_failed_title"),
			fmt.Sprintf("[%s] %v", diag.CodeOr(err, diag.CodeInternalError), err),
			walk.MsgBoxIconError)
		return 2
	}

	code := gui.Run(a) // 内部完成 runtime 停机（⛔ 不卸载驱动、不删 runtime 文件）
	// 退出序列的**最后一步**（decisions/007 L2 的生命周期细化）。
	a.Sig.Release()
	return code
}
