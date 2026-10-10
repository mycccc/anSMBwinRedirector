package controller

import (
	"errors"
	"strings"
	"time"

	"ansmbwinhelper/internal/config"
	"ansmbwinhelper/internal/diag"
	"ansmbwinhelper/internal/model"
	"ansmbwinhelper/internal/snapshot"
)

// --- 连接（decisions/012 §3.4）---------------------------------------------
//
// `Connect(TargetID)` 是"打开这台服务器"的**唯一入口**：
//
//	确保凭据（该 Target 配置了凭据时）→ 打开 \\<AndroidIP>\
//
// 规则（decisions/012）：
//   - **要求 Redirector 处于 Running，且该 Target runtime 为 Running**（C1）；⛔ 不自动 Start。
//   - 该 Target **未配置凭据** ⇒ 跳过凭据步骤，只打开共享。
//   - 检测到该主机**已有 Windows 凭据** ⇒ 首次调用返回"需要确认"（C2）；⛔ 不静默覆盖。
//     （无法判断凭据来源，因此只能"检测到即提示"，见 012 §2.3）
//   - 失败**只影响该 Target**，**不改变全局模式**（007 L7）。
//
// ⛔ 密码绝不进入任何 error 文本、诊断条目或日志（decisions/012 §3.3）。

var (
	// ErrConnectNotFound 表示指定 ID 的目标不在已保存配置中。
	ErrConnectNotFound = errors.New("未找到该目标")
	// ErrConnectNeedsCredentialConfirm 表示该主机已有 Windows 凭据，需要用户确认是否覆盖。
	//
	// ⚠️ 这**不是运行错误**，而是"需要用户决定"的中间结果：**不写入 ErrorInfo**。
	// GUI 应据它弹一次确认，然后以 replaceExistingCredential=true 重试。
	ErrConnectNeedsCredentialConfirm = errors.New("该主机在 Windows 中已有凭据，连接会覆盖它")
)

// ConnectPlan 是 `PlanConnect` 的结果：GUI 据此决定是否要弹"覆盖凭据"确认。
//
// ⚠️ 不含密码；`ExistingUsername` 只是"已存在凭据的用户名"。
type ConnectPlan struct {
	TargetID string
	Host     string
	// Username 是本 Target 配置的用户名（无凭据时为空）。
	Username string
	// HasCredentials 表示本 Target 是否配置了凭据（= Username 非空）。
	HasCredentials bool
	// ExistingCredential 表示该主机在 Windows 中**已有**凭据。
	ExistingCredential bool
	// ExistingUsername 是已有凭据的用户名（未知时为空；⛔ 绝不含密码）。
	ExistingUsername string
	// DetectionFailed 表示"查询已有凭据"失败 ⇒ 按**可能覆盖**处理（fail-safe）。
	DetectionFailed bool
}

// NeedsConfirm 报告"若不确认就不应继续"。
func (p ConnectPlan) NeedsConfirm() bool {
	return p.HasCredentials && (p.ExistingCredential || p.DetectionFailed)
}

// PlanConnect 做**只读**预检：目标存在？正在运行？该主机是否已有凭据？
//
// ⛔ 不写凭据、不打开共享、不改变任何运行状态。
func (c *Controller) PlanConnect(id string) (ConnectPlan, error) {
	tgt, err := c.connectPrecheck(id)
	if err != nil {
		return ConnectPlan{}, err
	}
	plan := ConnectPlan{
		TargetID:       tgt.ID,
		Host:           tgt.AndroidIP.String(),
		Username:       tgt.Username,
		HasCredentials: tgt.HasCredentials(),
	}
	if !plan.HasCredentials {
		return plan, nil // 无凭据 ⇒ 不查询、不写入
	}

	st, ierr := c.opts.Credential.Inspect(plan.Host)
	if ierr != nil {
		plan.DetectionFailed = true
		c.mu.Lock()
		c.ring.Info(diag.SrcConnect, "查询 %s 已有凭据失败，按“可能覆盖”处理：%v", plan.Host, ierr)
		c.publishLocked()
		c.mu.Unlock()
		return plan, nil
	}
	plan.ExistingCredential = st.Exists
	plan.ExistingUsername = st.Username
	return plan, nil
}

// Connect 执行"打开这台服务器"。
//
// replaceExistingCredential=false 时，若检测到该主机已有凭据 ⇒ 返回
// `ErrConnectNeedsCredentialConfirm`（**不写任何凭据、不打开共享**）。
//
// ⚠️ **Reachability 更新规则（Gate 2026-10-09 裁定，方案 A）**：
//   - **仅当**凭据处理与 `\\<IP>\` 打开**全部成功**后，才把该 Target 的
//     `Reachability` 置为 `Reachable` 并更新 `LastCheckedAt`；
//   - 凭据写入失败 / 用户取消覆盖 / 打开共享失败 ⇒ **不更新**为 `Reachable`；
//   - **失败一律不写 `Unreachable`**（认证/凭据/Explorer 启动失败 ≠ 主机不可达）；
//   - 语义 = 「**最近一次 Connect 操作成功**」，**不是**独立网络探测结果。
func (c *Controller) Connect(id string, replaceExistingCredential bool) error {
	if err := c.begin(snapshot.OpConnect); err != nil {
		return err
	}
	defer c.end()

	tgt, err := c.connectPrecheck(id)
	if err != nil {
		c.recordConnectError(id, err)
		return err
	}
	host := tgt.AndroidIP.String()
	c.clearConnectErrors(id)

	if tgt.HasCredentials() {
		needConfirm := true
		if st, ierr := c.opts.Credential.Inspect(host); ierr == nil {
			needConfirm = st.Exists
		} else {
			c.mu.Lock()
			c.ring.Info(diag.SrcConnect, "查询 %s 已有凭据失败，按“可能覆盖”处理：%v", host, ierr)
			c.publishLocked()
			c.mu.Unlock()
		}
		if needConfirm && !replaceExistingCredential {
			return ErrConnectNeedsCredentialConfirm
		}
		if serr := c.opts.Credential.Store(host, tgt.Username, tgt.Password); serr != nil {
			// ⛔ 防御性脱敏：即使凭据实现把密码带进了错误文本，也不让它进入诊断/错误。
			detail := scrubSecret(serr.Error(), tgt.Password)
			c.recordError(diag.New(diag.CodeCredentialWriteFailed, diag.SrcConnect, detail, id))
			return diag.Errorf(diag.CodeCredentialWriteFailed, "写入 Windows 凭据失败（%s）：%s", host, detail)
		}
	}

	if oerr := c.opts.Opener.Open(host); oerr != nil {
		detail := scrubSecret(oerr.Error(), tgt.Password)
		c.recordError(diag.New(diag.CodeShareOpenFailed, diag.SrcConnect, detail, id))
		return diag.Errorf(diag.CodeShareOpenFailed, "打开 %s 失败：%s", host, detail)
	}

	c.mu.Lock()
	// ⚠️ Reachability 的唯一"成功"写入点（方案 A）：**只在完整成功后**写 Reachable。
	// ⛔ 失败路径（凭据写入失败 / 打开共享失败 / 需要确认）在上方已 return，均不写。
	c.reach[id] = snapshot.ReachEntry{State: snapshot.ReachReachable, LastChecked: time.Now()}
	c.ring.Info(diag.SrcConnect, "已连接 %s（用户名=%s）", host, tgt.Username)
	c.publishLocked()
	c.mu.Unlock()
	return nil
}

// connectPrecheck 校验前置条件并返回目标（decisions/012 C1）。
func (c *Controller) connectPrecheck(id string) (model.Target, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cfgState == config.StateUnavailable {
		return model.Target{}, ErrConfigUnavailable
	}
	for _, t := range c.persisted {
		if t.ID != id {
			continue
		}
		if c.mode != snapshot.ModeRunning || c.runtimeStateLocked(id) != snapshot.RTRunning {
			return model.Target{}, diag.Errorf(diag.CodeConnectNotRunning,
				"重定向未运行，无法连接目标 %s（请先启动）", id)
		}
		return t, nil
	}
	return model.Target{}, ErrConnectNotFound
}

// recordError 记录一条 ErrorInfo 并发布快照（连接类错误**不阻塞**，007 L7）。
func (c *Controller) recordError(e diag.ErrorInfo) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.agg.Add(e)
	c.ring.Error(diag.SrcConnect, "%s：%s", e.Code, e.Detail)
	c.publishLocked()
}

// recordConnectError 把"前置条件失败"也记进诊断。
func (c *Controller) recordConnectError(id string, err error) {
	code, ok := diag.CodeOf(err)
	if !ok {
		return
	}
	c.recordError(diag.New(code, diag.SrcConnect, err.Error(), id))
}

// clearConnectErrors 清除该目标上**连接类**的历史错误。
//
// ⚠️ 只清连接类：Connect 成功⛔ 不得抹掉无关的 runtime 失败（decisions/009 §4）。
func (c *Controller) clearConnectErrors(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, code := range []diag.Code{
		diag.CodeConnectNotRunning, diag.CodeCredentialWriteFailed, diag.CodeShareOpenFailed,
	} {
		c.agg.Remove(code, diag.ScopeTarget, id)
	}
}

// scrubSecret 把 secret 在 s 中的所有出现替换为掩码（decisions/012 §3.3 的硬边界）。
//
// ⛔ 这是**防御性**的：由本程序控制的实现本来就不会把密码写进错误文本；
// 但"密码绝不进入诊断/日志/错误文本"是硬要求，因此在边界处再做一次。
func scrubSecret(s, secret string) string {
	if secret == "" {
		return s
	}
	return strings.ReplaceAll(s, secret, "***")
}
