//go:build windows

package gui

import (
	"fmt"

	"github.com/lxn/walk"

	"ansmbwinhelper/internal/controller"
)

// onConnect 是「连接」的**唯一入口**（decisions/012 §3.4）。
//
// 「连接」按钮与表格右键菜单的 Connect 调用**完全相同**的流程：
//
//	PlanConnect（只读预检）→ 需要时弹一次"覆盖凭据"确认 → Connect(TargetID)
//
// ⛔ 不自动 Start（C1：要求该 Target 的 runtime 已在运行）；
// ⛔ 不在 GUI 里执行 `cmdkey` / `start` —— 一律经 Controller（decisions/008 G2）。
func (g *gui) onConnect() {
	r, ok := g.selectedRow()
	if !ok {
		return
	}
	g.connectTarget(r.id)
}

func (g *gui) connectTarget(id string) {
	plan, err := g.ctl.PlanConnect(id)
	if err != nil {
		// 前置条件不满足（未运行 / 未找到 / 配置不可用）⇒ 明确告知，不入库为错误状态。
		walk.MsgBox(g.mw, T("main.connect")+T("status.failed"), errText(err), walk.MsgBoxIconError)
		return
	}

	replace := false
	if plan.NeedsConfirm() {
		// 覆盖已有 Windows 凭据是**破坏性**动作 ⇒ 必须显式确认（decisions/012 C2）。
		if !g.beginDialog() {
			return
		}
		choice := confirm(g.mw, T("dlg.cred_overwrite_title"), credConfirmText(plan), walk.MsgBoxYesNo)
		g.endDialog()
		if choice != walk.DlgCmdYes {
			return
		}
		replace = true
	}

	g.runAsync(T("main.connect"), func() error { return g.ctl.Connect(id, replace) })
}

// credConfirmText 生成"将替换已有凭据"的确认文案。
//
// ⛔ 只包含**主机**与**已存在凭据的用户名**，绝不包含任何密码 / 掩码长度信息（decisions/012 §3.3）。
func credConfirmText(p controller.ConnectPlan) string {
	if p.DetectionFailed {
		// fail-safe：查不到 ⇒ 按"可能覆盖"提示（decisions/012 C2）。
		return fmt.Sprintf(T("dlg.cred_overwrite_text_unknown"), p.Host)
	}
	if p.ExistingUsername != "" {
		return fmt.Sprintf(T("dlg.cred_overwrite_text_user"), p.Host, p.ExistingUsername)
	}
	return fmt.Sprintf(T("dlg.cred_overwrite_text"), p.Host)
}
