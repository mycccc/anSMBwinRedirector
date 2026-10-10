// Package controller 是 v1 的应用层：编排配置持久化与 runtime 生命周期，
// 并把状态与错误发布为 Snapshot。严格对应 decisions/006 §6.12、007 §4.2–§4.4、008 §2/§8.2。
//
// ⛔ 核心契约（decisions/008 **G11**）：
//
//	"保存并应用" = 单一 Apply(DraftConfig) 命令。
//	GUI/入口 **不得**把它拆成独立的 Save + Apply 两个可并发命令。
//
// ⛔ GUI 不得越过本层（decisions/008 §2.2）：
// 不碰 WinDivert、不执行系统命令、不直接读写配置文件、不自行推断运行态。
package controller

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"ansmbwinhelper/internal/config"
	"ansmbwinhelper/internal/connect"
	"ansmbwinhelper/internal/credential"
	"ansmbwinhelper/internal/diag"
	"ansmbwinhelper/internal/i18n"
	"ansmbwinhelper/internal/model"
	"ansmbwinhelper/internal/snapshot"
)

// RuntimeManager 由 runtime 包实现；controller 只依赖这个接口，便于单测替换。
//
// 实现必须保证（decisions/007）：
//   - "已停止" = 两个 handle 的 Close 已返回 **且** 两个循环 goroutine 已退出（L4）
//   - IN handle 打开失败必须回滚已打开的 OUT handle（L11）
//   - 同一 Android IP 至多一个 OUT/IN owner（L3）
//   - replacement set 必须"先停全部 → 确认关闭 → 再起全部"
type RuntimeManager interface {
	// Apply 把运行中的 runtime 集合收敛到 desired。
	// 返回受影响 Target 的失败原因；**单个失败不影响其他 Target**（L7）。
	Apply(desired []model.Target) map[string]error
	// StopAll 停止全部 runtime 并等待"确认关闭"，**然后**停止并移除 WinDivert 服务
	// （decisions/011 §2.4）。返回的是"服务生命周期"这一步的失败原因（可为 nil）。
	StopAll() error
	// Status 返回当前所有 runtime 的状态。
	Status() map[string]snapshot.RuntimeEntry
}

// Inspector 由平台层实现：**只读**的 WinDivert 使用情况检查（decisions/007 §4.7.3）。
//
// ⛔ v1 没有卸载能力（decisions/007 **L6**），实现里不得出现 sc stop / sc delete。
type Inspector interface {
	Inspect() diag.WinDivertInfo
}

// Options 是构造参数。
type Options struct {
	Store     *config.Store
	Manager   RuntimeManager
	Inspector Inspector
	Version   diag.VersionInfo
	OSInfo    string
	// DialTimeout 是连接测试超时；<=0 时使用默认值。
	DialTimeout time.Duration
	// Credential 是凭据访问实现（decisions/012 §3.4）；nil ⇒ 用平台默认实现。
	Credential credential.Access
	// Opener 是"打开共享"实现（decisions/012 §3.4）；nil ⇒ 用平台默认实现。
	Opener connect.Opener
	// Repairer 是 WinDivert 修复/重新接管实现（decisions/011 §2.7）；nil ⇒ 该动作不可用。
	Repairer Repairer
	// SystemOps 是 Windows 系统服务操作（decisions/011 §2.7）；nil ⇒ 该动作不可用。
	SystemOps SystemOps
}

// ErrBusy 表示已有操作在进行（decisions/007 L8：串行化，不排队）。
var ErrBusy = errors.New("已有操作正在进行")

// ErrConfigUnavailable 表示配置不可用 ⇒ 禁止 Start / 禁止保存（decisions/006 §6.10）。
var ErrConfigUnavailable = errors.New("配置不可用：在修复前不会启动重定向，也不会覆盖该文件")

// ViolationsError 携带全部校验违规项（decisions/006 §6.10）。
type ViolationsError struct{ Violations []string }

func (e *ViolationsError) Error() string {
	return "配置校验失败: " + fmt.Sprint(e.Violations)
}

// Controller 是应用层。
type Controller struct {
	mu   sync.Mutex
	busy bool
	op   snapshot.Operation

	opts Options

	persisted     []model.Target
	cfgState      config.State
	cfgViolations []string
	cfgCode       diag.Code
	cfgDetail     string

	mode  snapshot.GlobalMode
	reach map[string]snapshot.ReachEntry
	wd    diag.WinDivertInfo

	agg  *diag.Aggregator
	ring *diag.Ring
	pub  *snapshot.Publisher
}

// New 创建 controller（初始为 Stopped）。
func New(o Options) *Controller {
	if o.DialTimeout <= 0 {
		o.DialTimeout = 5 * time.Second
	}
	if o.Credential == nil {
		o.Credential = credential.New()
	}
	if o.Opener == nil {
		o.Opener = connect.New()
	}
	c := &Controller{
		opts: o,
		mode: snapshot.ModeStopped,
		// ⚠️ 必须显式初始化：Operation 的零值是空串，而 GUI/任何消费者都按
		// `op != OpNone` 判断"是否有操作在进行"（decisions/008 G5）。
		// 留空会把"空闲"误判成"忙"，从而禁用全部控件（实机验证时被发现）。
		op:    snapshot.OpNone,
		reach: map[string]snapshot.ReachEntry{},
		agg:   diag.NewAggregator(),
		ring:  diag.NewRing(diag.DefaultRingCapacity),
	}
	c.pub = snapshot.NewPublisher(c.buildSnapshotLocked())
	return c
}

// Subscribe 订阅快照（未来的 GUI / Tray）。
func (c *Controller) Subscribe(fn func(snapshot.Snapshot)) { c.pub.Subscribe(fn) }

// Snapshot 返回当前快照（runtime → UI 的唯一状态出口）。
func (c *Controller) Snapshot() snapshot.Snapshot { return c.pub.Current() }

// --- 加载 -----------------------------------------------------------------

// LoadConfig 加载配置。
//
// ⛔ 读取路径**不产生任何磁盘写入**（decisions/006 §6.5）。
// 失败 ⇒ ConfigUnavailable：不加载任何 Target、禁止 Start/Save、绝不覆盖（decisions/006 §6.10）。
func (c *Controller) LoadConfig() {
	c.mu.Lock()
	defer c.mu.Unlock()

	r := c.opts.Store.Load()
	switch r.State {
	case config.StateLoaded:
		c.persisted = r.Targets
		c.cfgState = config.StateLoaded
		c.cfgViolations, c.cfgCode, c.cfgDetail = nil, "", ""
		c.ring.Info(diag.SrcStartup, "配置加载成功：%d 个目标", len(r.Targets))
		if r.GeneratedIDs {
			c.ring.Warn(diag.SrcStartup, "有目标缺少稳定 ID，已在内存中生成（将在下次成功保存时回写）")
		}
	case config.StateEmpty:
		c.persisted = nil
		c.cfgState = config.StateEmpty
		c.cfgViolations, c.cfgCode, c.cfgDetail = nil, "", ""
		c.ring.Info(diag.SrcStartup, "未找到配置文件（首次启动，空配置）")
	default:
		c.persisted = nil
		c.cfgState = config.StateUnavailable
		c.cfgViolations = r.Violations
		c.cfgCode = r.Code
		c.cfgDetail = r.Detail
		c.agg.Add(diag.New(r.Code, diag.SrcStartup, r.Detail, ""))
		c.ring.Error(diag.SrcStartup, "配置不可用（%s）：%s", r.Code, r.Detail)
	}
	c.publishLocked()
}

// --- Apply（**唯一**的提交入口，decisions/008 G11）------------------------

// Apply 是"保存并应用"的**单一命令**：校验 → 写盘 → 收敛 runtime。
//
// 失败语义（decisions/006 §6.12）：
//   - 校验失败 / 写盘失败 ⇒ 磁盘与 runtime **都不变**（"什么都没发生"）
//   - runtime 收敛失败 ⇒ 磁盘已更新（意图已保存），以 per-Target 失败表达（§6.13）
func (c *Controller) Apply(draft []model.Target) error {
	if err := c.begin(snapshot.OpApply); err != nil {
		return err
	}
	defer c.end()

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.cfgState == config.StateUnavailable {
		return ErrConfigUnavailable
	}
	// 1. 校验（不写盘、不动 runtime）
	if v := model.CollectViolations(draft); len(v) > 0 {
		return &ViolationsError{Violations: v}
	}
	// 2. 写盘（原子替换语义，decisions/006 §6.11）
	if err := c.opts.Store.Save(draft); err != nil {
		c.ring.Error(diag.SrcApply, "写入配置失败：%v", err)
		return diag.Wrap(diag.CodeConfigIOError, err, "写入配置失败")
	}
	c.persisted = append([]model.Target(nil), draft...)
	c.cfgState = config.StateLoaded
	c.cfgViolations, c.cfgCode, c.cfgDetail = nil, "", ""
	c.ring.Info(diag.SrcApply, "配置已保存（%d 个目标）", len(draft))

	// 3. 仅当处于运行中才收敛 runtime（decisions/006 §6.13：Apply 不改变全局模式）
	if c.mode == snapshot.ModeRunning {
		c.clearGlobalStartErrorsLocked()
		c.reconcileLocked()
	}
	c.publishLocked()
	return nil
}

// --- Start / Stop ---------------------------------------------------------

// Start 按**已保存配置**启动全部 Target runtime（decisions/007 §4.3）。
//
// targets 为空时仍然进入 Running（`运行中 · 0 个目标`）——
// ⛔ 不得实现成"没有目标就不让启动"。
func (c *Controller) Start() error {
	if err := c.begin(snapshot.OpStart); err != nil {
		return err
	}
	defer c.end()

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.cfgState == config.StateUnavailable {
		return ErrConfigUnavailable
	}
	c.clearGlobalStartErrorsLocked()
	// 即使全部失败也进入 Running：用户确实按下了启动，失败以 per-Target 表达（L7）。
	c.mode = snapshot.ModeRunning
	c.reconcileLocked()
	c.publishLocked()
	return nil
}

// Stop 停止全部 runtime，**然后**停止并移除 WinDivert 服务（decisions/011 §2.4）。
// **不写盘、不改配置。**
func (c *Controller) Stop() error {
	if err := c.begin(snapshot.OpStop); err != nil {
		return err
	}
	defer c.end()

	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.opts.Manager.StopAll(); err != nil {
		// 服务生命周期失败是**全局类**错误：不影响"runtime 已停止"这一事实，
		// 但必须让用户看到（decisions/011 §2.5）。
		c.agg.Add(diag.New(diag.CodeWinDivertTakeoverFailed, diag.SrcStop, err.Error(), ""))
		c.ring.Error(diag.SrcStop, "停止 WinDivert 服务失败：%v", err)
	}
	c.mode = snapshot.ModeStopped
	c.agg.ClearByScope(diag.ScopeTarget)
	c.syncRuntimeLocked()
	c.publishLocked()
	return nil
}

// --- 连接测试 / 只读检查 ---------------------------------------------------

// TestConnection 对某个目标做一次连接测试（decisions/007 §4.11、008 §12）。
//
// 探测目标取决于该 Target **当时的运行态**：
//
//	Running      → AndroidIP:445       （验证完整透明重定向链路）
//	非 Running    → AndroidIP:<Port>    （只验证 Android 侧可达）
//
// ⚠️ 调用方**不得**在 UI 线程上阻塞等待（decisions/008 G4）。
// 结果记入 Reachability；**失败是提示而不是错误**（decisions/009 §2.3）。
func (c *Controller) TestConnection(ctx context.Context, id string) (snapshot.ReachEntry, error) {
	c.mu.Lock()
	var tgt model.Target
	found := false
	for _, t := range c.persisted {
		if t.ID == id {
			tgt, found = t, true
			break
		}
	}
	running := false
	if found {
		running = c.runtimeStateLocked(id) == snapshot.RTRunning
	}
	c.mu.Unlock()

	if !found {
		return snapshot.ReachEntry{}, fmt.Errorf("未找到目标 %s", id)
	}
	probe := probePort(running, tgt.AndroidPort)
	addr := net.JoinHostPort(tgt.AndroidIP.String(), fmt.Sprint(probe))

	d := net.Dialer{Timeout: c.opts.DialTimeout}
	conn, err := d.DialContext(ctx, "tcp", addr)

	entry := snapshot.ReachEntry{LastChecked: time.Now()}
	if err != nil {
		entry.State = snapshot.ReachUnreachable
	} else {
		entry.State = snapshot.ReachReachable
		_ = conn.Close()
	}

	c.mu.Lock()
	c.reach[id] = entry
	c.ring.Info(diag.SrcInspect, "连接测试 %s (%s): %v", id, addr, entry.State)
	c.publishLocked()
	c.mu.Unlock()
	return entry, nil
}

// Inspect 执行**只读**的 WinDivert 使用情况检查。
//
// ⛔ v1 不提供卸载能力（decisions/007 L6）：本方法只读取与报告。
func (c *Controller) Inspect() diag.WinDivertInfo {
	info := c.opts.Inspector.Inspect()
	c.mu.Lock()
	c.wd = info
	c.ring.Info(diag.SrcInspect, "WinDivert 只读检查：服务=%v 归属本程序=%v（不打开驱动句柄）",
		info.ServiceExists, info.ImagePathIsOurs)
	c.publishLocked()
	c.mu.Unlock()
	return info
}

// --- 退出 -----------------------------------------------------------------

// Shutdown 停止全部 runtime 并退出。**不卸载驱动、不删除 runtime 文件**（decisions/007 §4.6）。
//
// 若已有操作在进行，最多等待 waitFor（decisions/007 §4.8 的 Exit 语义）。
func (c *Controller) Shutdown(waitFor time.Duration) {
	deadline := time.Now().Add(waitFor)
	for {
		c.mu.Lock()
		busy := c.busy
		c.mu.Unlock()
		if !busy || time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = c.begin(snapshot.OpExit)
	defer c.end()

	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.opts.Manager.StopAll(); err != nil {
		c.ring.Error(diag.SrcExit, "退出时停止 WinDivert 服务失败：%v", err)
	}
	c.mode = snapshot.ModeStopped
	c.syncRuntimeLocked()
	c.ring.Info(diag.SrcExit, "已停止全部目标并移除 WinDivert 服务（decisions/011 §2.4）")
	c.publishLocked()
}

// --- 诊断 -----------------------------------------------------------------

// Details 组装"复制详情"的全部输入（decisions/009 §7）。
func (c *Controller) Details() diag.Details {
	c.mu.Lock()
	defer c.mu.Unlock()

	rows := c.buildSnapshotLocked().Rows()
	targets := make([]diag.TargetInfo, 0, len(rows))
	for _, r := range rows {
		ti := diag.TargetInfo{
			ID:           r.ID,
			Endpoint:     fmt.Sprintf("%s:%d", r.AndroidIP, r.AndroidPort),
			Runtime:      string(r.Runtime),
			Reachability: r.Reachability.Display(),
		}
		if !r.LastChecked.IsZero() {
			ti.LastChecked = r.LastChecked.Format("15:04:05")
		}
		if r.AndroidPort == model.WindowsMatchPort {
			ti.Note = diag.NoteText(diag.NoteAndroidPortEqualsWindowsPort)
		}
		if r.Reachability == snapshot.ReachUnreachable {
			ti.Note = diag.NoteText(diag.NoteTargetUnreachable)
		}
		targets = append(targets, ti)
	}

	snap := c.buildSnapshotLocked()
	total, running, failed := snap.Aggregate()
	agg := fmt.Sprintf(i18n.T("snapshot.aggregate_n"), total, running, failed)

	return diag.Details{
		Version:   c.opts.Version,
		OS:        c.opts.OSInfo,
		Time:      time.Now(),
		Mode:      string(c.mode),
		Operation: string(c.op),
		Aggregate: agg,
		Errors:    c.agg.List(),
		Config: diag.ConfigInfo{
			Path:          c.opts.Store.Path(),
			State:         c.cfgState.String(),
			SchemaVersion: config.SchemaVersion,
			TargetCount:   len(c.persisted),
			BackupExists:  c.opts.Store.BackupExists(),
			Violations:    c.cfgViolations,
		},
		WinDivert: c.wd,
		Targets:   targets,
		Recent:    c.ring.Snapshot(),
	}
}

// CopyDetails 返回可直接复制的诊断详情文本。
func (c *Controller) CopyDetails() string { return diag.FormatDetails(c.Details()) }

// --- 内部 -----------------------------------------------------------------

func (c *Controller) begin(op snapshot.Operation) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.busy {
		return fmt.Errorf("%w（%s）", ErrBusy, c.op)
	}
	c.busy = true
	c.op = op
	c.publishLocked()
	return nil
}

func (c *Controller) end() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.busy = false
	c.op = snapshot.OpNone
	c.publishLocked()
}

// reconcileLocked 让 runtime 收敛到 persisted，并把结果写进错误聚合。
func (c *Controller) reconcileLocked() {
	errs := c.opts.Manager.Apply(c.persisted)

	for _, t := range c.persisted {
		id := t.ID
		if err, bad := errs[id]; bad && err != nil {
			code := diag.CodeOr(err, diag.CodeTargetStartFailed)
			e := diag.New(code, diag.SrcStart, err.Error(), id)
			c.agg.Add(e)
			c.ring.Error(diag.SrcStart, "目标 %s 启动失败（%s）：%v", id, code, err)
			continue
		}
		// 成功 ⇒ 清除该目标的历史错误（decisions/009 §4：错误是状态，必须能清除）。
		c.agg.ClearTarget(id)
		c.ring.Info(diag.SrcStart, "目标 %s 已建立运行时", id)
	}
	c.syncRuntimeLocked()
}

// clearGlobalStartErrorsLocked 清掉上一轮 Start/Apply 留下的全局类错误，
// 由本轮结果重新决定（decisions/009 §4 的清除规则）。
func (c *Controller) clearGlobalStartErrorsLocked() {
	for _, e := range c.agg.List() {
		if e.Scope == diag.ScopeGlobal && (e.Source == diag.SrcStart || e.Source == diag.SrcApply) {
			c.agg.Remove(e.Code, diag.ScopeGlobal, "")
		}
	}
}

func (c *Controller) runtimeStateLocked(id string) snapshot.RuntimeState {
	return c.opts.Manager.Status()[id].State
}

func (c *Controller) syncRuntimeLocked() {
	st := c.opts.Manager.Status()
	// 目标被删除后，历史可达性不应残留。
	for id := range c.reach {
		if _, ok := st[id]; !ok {
			if !c.hasTargetLocked(id) {
				delete(c.reach, id)
			}
		}
	}
}

func (c *Controller) hasTargetLocked(id string) bool {
	for _, t := range c.persisted {
		if t.ID == id {
			return true
		}
	}
	return false
}

func (c *Controller) buildSnapshotLocked() snapshot.Snapshot {
	mode := c.mode
	if c.cfgState == config.StateUnavailable {
		mode = snapshot.ModeConfigUnavailable
	}
	rt := c.opts.Manager.Status()
	errs := c.agg.List()
	// 把已分类的 Target 级错误回填到该行的运行态上：
	// 保证"行内错误"与"聚合错误"出自同一份数据，不会漂移（decisions/008 §3/§13）。
	for i := range errs {
		e := errs[i]
		if e.Scope == diag.ScopeTarget && e.TargetID != "" {
			if entry, ok := rt[e.TargetID]; ok {
				cp := e
				entry.Err = &cp
				rt[e.TargetID] = entry
			}
		}
	}
	return snapshot.Snapshot{
		Global:    snapshot.Global{Mode: mode, Operation: c.op},
		Config:    c.configInfoLocked(),
		Persisted: append([]model.Target(nil), c.persisted...),
		Runtime:   rt,
		Reach:     copyReach(c.reach),
		WinDivert: c.wd,
		Errors:    errs,
		Recent:    c.ring.Snapshot(),
	}
}

func (c *Controller) configInfoLocked() snapshot.Config {
	return snapshot.Config{
		Path:          c.opts.Store.Path(),
		State:         c.cfgState.String(),
		SchemaVersion: config.SchemaVersion,
		TargetCount:   len(c.persisted),
		BackupExists:  c.opts.Store.BackupExists(),
		Violations:    append([]string(nil), c.cfgViolations...),
	}
}

func (c *Controller) publishLocked() { c.pub.Publish(c.buildSnapshotLocked()) }

func copyReach(in map[string]snapshot.ReachEntry) map[string]snapshot.ReachEntry {
	out := make(map[string]snapshot.ReachEntry, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// probePort 决定连接测试的探测端口（decisions/007 §4.11）：
//
//	Target 运行中 ⇒ 探测 AndroidIP:445（走完整透明重定向链路）
//	否则         ⇒ 探测 AndroidIP:<AndroidPort>（只验证 Android 侧可达）
//
// 两者测的是**两个不同问题**，因此结果必须与该 Target 当时的运行态一起解读。
func probePort(running bool, androidPort uint16) uint16 {
	if running {
		return model.WindowsMatchPort
	}
	return androidPort
}

// --- 配置不可用时的显式动作（decisions/006 §6.10）-------------------------

// ConfigCode 返回最近一次配置加载失败的错误码（配置可用时为空串）。
//
// 用途：GUI 需要据此决定是否提供"以空配置重新开始"（006 §6.10 规定
// schema_version 过新时**不提供**该动作）。
func (c *Controller) ConfigCode() diag.Code {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cfgCode
}

// QuarantineConfig 执行 decisions/006 §6.10 的**动作 ②**：
// 把不可用的配置文件改名为 `config.json.corrupt-<ts>`，然后以空配置重新加载。
//
// ⛔ 仅在"配置不可用"且**不是** schema_version 过新时可用。
// ⛔ 只改名：不删除、不覆盖任何文件内容；失败时原文件保持可用。
func (c *Controller) QuarantineConfig() (string, error) {
	// 复用同一套串行化（decisions/007 L8）。
	if err := c.begin(snapshot.OpApply); err != nil {
		return "", err
	}
	defer c.end()

	c.mu.Lock()
	unavailable := c.cfgState == config.StateUnavailable
	code := c.cfgCode
	c.mu.Unlock()

	switch {
	case !unavailable:
		return "", errors.New("当前配置可用，无需以空配置重新开始")
	case code == diag.CodeConfigSchemaUnsupported:
		return "", errors.New("配置的 schema_version 高于本程序支持的版本；" +
			`按 decisions/006 §6.10 不提供"以空配置重开"（请升级程序，或手工处理该文件）`)
	}

	dst, err := c.opts.Store.Quarantine()
	if err != nil {
		return "", err
	}
	c.LoadConfig()
	return dst, nil
}
