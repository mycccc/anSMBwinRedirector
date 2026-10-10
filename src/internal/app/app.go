// Package app 提供两个入口共用的**启动序列与装配**：
//
//	cmd/ansmbwinhelper   GUI / Tray（产品入口）
//	cmd/devconsole       控制台（开发 / 集成测试用的非产品入口）
//
// 启动序列（decisions/007 §4.8）：
//
//	P0 平台检查 → P1 权限检查 → P2 单实例 → P3 加载配置 → Stopped
//
// ⛔ 启动阶段**不触发** `WinDivertOpen`（因而不加载驱动，decisions/007 §4.8）。
// ⛔ 配置加载失败 ⇒ `ConfigUnavailable`，**不是**启动失败，程序继续运行（§4.8 P2）。
package app

import (
	"ansmbwinhelper/internal/config"
	"ansmbwinhelper/internal/controller"
	"ansmbwinhelper/internal/diag"
	"ansmbwinhelper/internal/i18n"
	ansmbrt "ansmbwinhelper/internal/runtime"
)

// App 是一次成功启动的结果。
//
// ⚠️ `Sig` 必须在**正常退出序列的最后一步**才调用 `Release()`
// （全部 runtime 已"确认关闭"、最终诊断已输出之后），之后**不再执行任何语句**。
type App struct {
	Sig  *SingleInstance
	Ctl  *controller.Controller
	Path string

	// Mgr 保留具体 manager，供**开发入口**读取诊断计数；
	// 产品（GUI）入口不使用它 —— GUI 只读 Snapshot（decisions/008 G6/G12）。
	Mgr controller.RuntimeManager
}

// Counters 返回某 Target 的分层归因计数（仅诊断；无则返回 false）。
func (a *App) Counters(id string) (ansmbrt.Counters, bool) {
	type counterSource interface {
		Counters(string) (ansmbrt.Counters, bool)
	}
	if c, ok := a.Mgr.(counterSource); ok {
		return c.Counters(id)
	}
	return ansmbrt.Counters{}, false
}

// Options 是启动参数。
type Options struct {
	// ConfigPath 为空时使用默认路径（decisions/006 §6.3）。
	ConfigPath string
	Version    diag.VersionInfo
}

// Start 执行 P0–P3 启动序列并装配 Controller。
//
// 返回的错误已携带 `diag.Code`，可直接用于诊断展示（decisions/009 §2）。
func Start(o Options) (*App, error) {
	// P0 平台检查（最先：在不受支持的平台上，后面的检查都没有意义）。
	if err := checkPlatform(); err != nil {
		return nil, err
	}
	// P1 权限检查。
	if err := checkAdmin(); err != nil {
		return nil, err
	}
	// P2 单实例。互斥体在退出序列的最后一步才释放（decisions/007 L2 的生命周期细化）。
	sig, err := acquireSingleInstance()
	if err != nil {
		return nil, err
	}

	// P3 配置。
	path := o.ConfigPath
	if path == "" {
		path, err = config.DefaultPath()
		if err != nil {
			sig.Release()
			return nil, diag.Wrap(diag.CodeInternalError, err, "%s", i18n.T("app.config_path_failed"))
		}
	}
	mgr := newManager()
	ctl := controller.New(controller.Options{
		Store:     config.NewStore(path),
		Manager:   mgr,
		Inspector: newInspector(),
		Version:   o.Version,
		OSInfo:    OSInfo(),
		// 调试窗口的动作（decisions/011 §2.7）：修复 WinDivert / 重启 Workstation。
		Repairer:  newRepairer(),
		SystemOps: newSystemOps(),
	})
	ctl.LoadConfig()
	return &App{Sig: sig, Ctl: ctl, Path: path, Mgr: mgr}, nil
}
