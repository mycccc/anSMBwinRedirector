# CODEBUDDY.md

This file provides guidance to CodeBuddy Code when working with code in this repository.

## 首先阅读

本仓库的**权威开发规则是 `AGENTS.md`**（中文）。开始任何任务前必须先读它，本文件只是入口与速查，不替代 `AGENTS.md`。

**项目怎么开发**见 `local_dev/docs/DEVELOPMENT_PLAN.md`（四阶段计划、开发原则、当前状态与下一步）。

项目背景文档在 `local_dev/chatdocs/`：

- `1.anSMBwinHelper 项目前期说明.md` — 立项说明与阶段规划
- `2.PROJECT_BRIEF_CN.md` — 项目 brief（中文，最完整）
- `2.PROJECT_BRIEF.md.md` — 英文版 brief
- `3目录结构规划-PROJECT_DIRECTORY_STRUCTURE.md` — 目录职责规划

## 当前仓库状态

> **阶段 1（技术验证）PASS → 阶段 2（最小实现）已在 Windows 实机验证 → 2.7 Packaging 已 APPROVED。**
> 正式源码在 `src/`；**产品入口 = GUI / 托盘 `cmd/ansmbwinredirector`**。

- 根目录：`AGENTS.md`、`CODEBUDDY.md`、`README.md`、`LICENSE`、`THIRD_PARTY_NOTICES.md`、`scripts/`、`.gitignore`、`src/`、`local_dev/`（最后一项被 `.gitignore` 整体忽略，**永不提交**）。
- `src/`（`module ansmbwinhelper`）：
  - `cmd/ansmbwinredirector/` — **产品入口**（GUI / 托盘）+ `anSMBwinRedirector.manifest` + `rsrc_amd64.syso`
  - `cmd/devconsole/` — **非产品**的控制台集成入口（原第一轮入口）
  - `internal/{app,controller,runtime,winvert,config,model,snapshot,diag,gui}`
- **可用命令（在 `src/` 下）**：`go test ./...`、`gofmt -l ./cmd ./internal`、`go vet ./...`；
  Windows 交叉编译：`GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build ./cmd/ansmbwinredirector`。
- **发布包**：`scripts/build-release.sh <version>` → `src/dist/release/`（6 个文件；`src/dist/` 被 `.gitignore` 忽略）。
- ⚠️ **manifest 是必需的**：GUI 框架 `lxn/walk` 需要 Common-Controls v6 清单，缺失会导致窗口**完全建不出来**；
  它以内嵌资源 `cmd/ansmbwinredirector/rsrc_amd64.syso` 形式提供，由 `go tool rsrc`（版本锁在 `go.mod` 的 `tool` 指令）**可复现地**生成。
- Go 工具链：Mac 上在 `~/sdk/go`（未改 shell profile，用 `export PATH="$HOME/sdk/go/bin:$PATH"`）。
- 当前状态与下一步见 `local_dev/docs/DEVELOPMENT_PLAN.md`。

## 项目是什么

`anSMBwinRedirector` 是 anSMB 生态中**独立的 Windows 侧网络适配层**，用于让 Windows 原生 SMB 客户端 / Explorer 访问运行在 Android 上、监听**非标准端口（默认 4450，可配置，不得写死）**的 `anSMBserver`。

```text
Windows SMB Client
        │ TCP: <动态临时端口> → AndroidIP:445
        ▼
anSMBwinRedirector
        │ 透明改写「目的端口」：AndroidIP:445 → AndroidIP:<AndroidPort>
        │ （<AndroidPort> 可配置，默认 4450；目的 IP 不变；不改 Windows 源端口）
        ▼
Android anSMBserver（真正监听该可配置端口）
        │ TCP 响应：AndroidIP:<AndroidPort> → Windows 动态临时端口
        ▼
anSMBwinRedirector
        │ 透明恢复「源端口」：<AndroidPort> → 445
        ▼
Windows SMB Client
```

根因：Android 应用无法绑定 < 1024 的特权端口，所以 anSMBserver 不能监听 445；而 Windows 原生 SMB 客户端只走 445，UNC 路径也无法指定端口。**端口可配不是为了避免与 Windows 445 冲突**，这是关键的因果方向，不要写反。

**关键澄清（曾被误读，务必注意）**：445 与 Android 实际监听端口**都在 Android IP 上**，其中 **Android 真正监听的是那个可配置端口（默认 4450），不是 445**（Android 从不监听 445）。**该端口不得写死为 4450**——anSMBserver 的监听端口可修改。**每条规则只有两项可配**：`Android IP`（规则集合内**唯一**）+ `Android SMB Port`；Windows 目的端口 **TCP 445** 与协议 **TCP** 是**全局固定项**。**同一个 Android IP 不得存在两条规则**（否则两条出站 filter 会争抢 `AndroidIP:445`）。**Windows 侧不监听 445**，只有 SMB 客户端的动态临时源端口。本项目的正式术语是**透明 TCP 目的端口重定向**，**不是**"Windows 本机 445→4450 端口转发"，也**不是**任意 `FROM→TO` 的通用 NAT；因此 `netsh interface portproxy` 与普通用户态 TCP proxy **不适用**。
⛔ **不得**把 `Windows TCP 445` 单独当成一条规则的**目标**；规则的 Windows 侧匹配目标是**派生**的 `AndroidIP:445`。

**项目边界（不得越界）**：本项目管理的是"445 → Android 实际监听端口 的网络适配"，它**不应**演变成 SMB Server、SMB Client、Windows 版 anSMBserver、通用端口转发工具或通用 NAT，也不应变成自有 SMB 协议实现。

## 架构大图

`AGENTS.md` §6 与 brief §6 把架构分成两层，这是理解本项目的关键：

```text
anSMBwinRedirector
├── 产品层       配置 / 目标管理 / Start-Stop / 状态 / 日志
└── 网络实现层   WinDivert（数据包拦截与重写）
```

WinDivert 是**底层技术组件**，项目本身是产品；不要让仓库变成一个 WinDivert demo。

### 阶段 1 已验证的核心问题（已完成，2026-09-29）

阶段 1（`local_dev/phases/p0/`，含 P0/P0.1/P0.1-C）的实机结论见 `local_dev/phases/p0/RESULT.md` 与 `decisions/004-v1-architecture-baseline.md`：

- **目的端口改写**：**只改目的端口**（目的 IP 不变）——`decisions/002`；checksum offload/LSO 实测**未造成问题**（判据是握手/保持/SMB 全部成功，而非抓包里的 checksum 字段）。
- **返回方向（Android → Windows）**：源端口 `<AndroidPort> → 445` 还原，实测成立。
- **是否需要 connection tracking**：映射本身**不需要**；但**无状态反滤波存在跨流误伤**（实测复现：直连 `AndroidIP:<AndroidPort>` 的返回包被误改成 445）。**已用最小 FlowTable（仅记录 OUT 建立的映射，IN 未命中即原样放行）修复**——此为**最小必要状态**，仍**不是**通用 NAT / connection tracking。
- 多连接、混合流、正常/异常断开、重连、停止/重启基线——均已实测。
- 运行所需 **Windows 权限**：需管理员（WinDivert 驱动加载）。
- **WinDivert 驱动**：按需安装、不随进程退出卸载（`P0.1-C`）；显式清理需带两项安全检查（见 `AGENTS.md` §2.2）。
- WinDivert 的**许可证与再分发**要求（版本 2.2.2，见下）。

### 参考材料

`local_dev/refer/WinDivert-2.2.2-A/` 是解压的 WinDivert 2.2.2 发布包（仅作参考，**不属于正式项目**）：

- `include/windivert.h`、`doc/WinDivert.html`、`LICENSE`、`CHANGELOG`
- `x64/`（及 `x86/`）含 `WinDivert.dll`、`WinDivert.lib`、`WinDivert64.sys` 与示例工具 `netdump.exe`、`flowtrack.exe`、`streamdump.exe`、`windivertctl.exe` 等

`local_dev/refer/detour/` 是 `LeeJeKyun/detour` 的源码副本（**GPLv3**）——它是本项目的 **GPLv3 上游代码基础 / 重要参考实现**，**允许在遵守 GPLv3 及第三方许可要求的前提下复用、修改、删减**；**不要求**保留其通用 NAT / UDP / 多规则能力。源码分析 + release 实机取证见 `local_dev/docs/research/002-detour-analysis.md`（**不要**修改该参考副本）。

## 硬性约束（来自 `AGENTS.md`）

违反这些规则的改动即使"技术上更好"也是错的。

1. **阶段门**：项目只有四个阶段——`阶段 1 技术验证 → 阶段 2 最小实现 → 阶段 3 实际使用验证 → 阶段 4 发布整理`。**没有通过技术验证，不进入正式实现**；失败时不得靠堆代码绕过可行性问题。不要人为拆分子阶段（P0.1/P0.2…）。
2. **阶段 1 禁止向 `src/` 写正式代码**。实验代码必须放 `local_dev/phases/p0/`（该目录即阶段 1）或 `local_dev/tests/`、`local_dev/tools/`。
3. **`local_dev/` 是开发沙盒，永不提交 Git**（`.gitignore` 应为 `/local_dev/`）。实验失败也要保留并记录原因，不要为了"干净"删除。
4. **技术栈：语言与网络机制均已确定**：实现语言 = **Go**、项目许可 = **GPLv3**、GUI 库 v1 = **`lxn/walk`**、**网络拦截机制 = WinDivert 2.2.2-A**（2026-09-29 经阶段 1 实机 PoC 验证后正式选定，见 `decisions/004-v1-architecture-baseline.md`）。**v1 架构细节仍未定**。C/C++、C#、Rust 已评估并**未采用**，不要重新引入。除已定项外不得宣布"项目采用 XXX"。
5. **实验成功 ≠ 正式实现**。`local_dev/phases/p0/` 的代码需经过 验证 → 总结 → 技术决策 → 重新设计，才可进入 `src/`；**禁止直接复制**。
6. **目录职责固定**：`local_dev/docs` 放开发文档（`decisions/`、`research/`，活计划为 `DEVELOPMENT_PLAN.md`），`docs/` 只放对外公开的正式文档，`src/` 只放已确认的生产代码。`third_party/`、`scripts/`、`assets/`、`tests/` **未到需要时不要提前创建**。
7. **重要技术决策必须落盘**到 `local_dev/docs/decisions/`，格式为：背景 / 问题 / 候选方案 / 验证 / 结果 / 决策 / 影响。不要只留在对话里。
8. **不要过度工程化**：阶段 1/2 优先验证核心问题。**没有实际验证证明必要，不得提前引入**通用 NAT、connection tracking、batching、overlapped I/O、多线程、复杂配置系统、GUI 框架、服务化。
9. **复用优先，遵守 License；项目已定 GPLv3**：优先复用成熟开源组件，不重复造轮子。`detour`（GPLv3）是**允许复用和修改的上游代码基础**，不要求保留其通用 NAT/UDP/多规则能力。**本项目采用 GPLv3**；第三方组件（`imgk/divert-go`、`lxn/walk`、WinDivert 等）的许可与随包要求须正确处理，清单见 `DEVELOPMENT_PLAN.md` §9。
10. **测试以风险覆盖为目的**，不为了阶段流程重复执行完全相同的测试；已有结果可跨阶段复用。
11. **GUI 先定信息架构再动代码**：v1 GUI 结构见 `decisions/003-gui-v1-information-architecture.md` 与 `decisions/008-v1-gui-interaction-contract.md`；**已实现的 GUI 在 `src/internal/gui` + `cmd/ansmbwinredirector`**，属已批准范围，**不要擅自重构或删除**（托盘 / 规则表 / Add·Edit·Delete / 多规则持久化 / 状态机等基础设施保留复用）。
    > ⚠️ 历史注（2026-10-07 收口审计）：本条原写"不要删除 `cmd/detour-gui` 的现有代码"——那是 **detour 参考实现**里的路径；本项目**并未复制 detour 的 GUI**，该路径在本仓库**不存在**。
12. **多目标规则（v1 必须）**：一条规则 = `Android IP` + `Android SMB Port`（+ 稳定 ID）；**Android IP 在规则集合内唯一**，重复须在配置层拒绝。**多规则 ≠ 通用 NAT** —— 不得引入任意 FROM/TO、UDP、TCP/UDP 选择、代理。网络模型 → 规则模型 → 配置模型 → GUI 模型 → filter 模型 五层必须一致（核对表见 `decisions/003` §0）。

### Agent 权限分级（`AGENTS.md` §13）

- **Level 1 直接执行**：在 `local_dev/` 内建目录、写实验代码、跑测试、存日志与结果、整理开发文档。
- **Level 2 先说再做**：改 `src/`、加正式依赖、改正式目录结构、创建 `third_party/`/`scripts/`/`assets/`、改构建方式、把实验代码转正式实现。需说明"为什么需要 / 改什么 / 影响什么 / 为什么现在做"。
- **Level 3 必须用户确认**：改项目范围或定位、换核心技术路线或网络架构、把项目变成 SMB Server/Client 或通用端口转发、跳过阶段 1 技术验证、大规模重构正式目录。

存在多个合理方案时，**先给出方案、依据、优劣与验证结果，不要替用户做核心架构决定**；无法确定时先问，不要猜。

### 阶段 1 完成判定

阶段 1 只有在 `AGENTS.md` §15 的清单全部确认后才可标记 PASS，并需产出 `local_dev/phases/p0/RESULT.md`（该目录即阶段 1；模板见 `AGENTS.md` §16，含测试环境、逐项结果、问题、结论 PASS/FAIL/PARTIAL、后续建议）。

## 待处理的仓库缺口

建议由用户确认后再处理，不要自行"修正"：

- **Git 提交策略未定（Release Gate 前的唯一开口项）**：仓库已有 2 个 commit（`f51cead`、`1c63552`），
  但 **`src/`、`LICENSE`、`THIRD_PARTY_NOTICES.md`、`scripts/` 仍是未跟踪/未提交状态** ——
  正式发布前需要用户裁定提交策略与发布提交的粒度。
- **许可证已定（2026-10-07）**：**GPL-3.0-only**（不含 "or any later version"）；`LICENSE` 为官方 GPLv3 全文，已随发布包分发。
- `AGENTS.md` 与本文件并存：两者都描述 Agent 工作规则。本文件刻意保持精简并指向 `AGENTS.md` 以避免内容分叉，**修改规则时请更新 `AGENTS.md`，不要只改这里**。
