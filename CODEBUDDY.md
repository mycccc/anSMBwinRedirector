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

> 这是一个**尚未开始编码**的绿地仓库，不要假设存在构建系统或测试体系。

- 目前**没有任何源代码、构建配置、测试或 CI**。根目录只有 `AGENTS.md`、`CODEBUDDY.md`、`README.md`、`.gitignore` 与 `local_dev/`。
- **仓库已 `git init`（分支 `main`），但尚无任何 commit**；`local_dev/` 由 `.gitignore` 整体忽略。
- 无构建、无 lint、无测试命令可用。不要虚构这类命令。
- 因此不存在"运行单个测试"的方法——在产生正式工具链之前，验证手段是 `local_dev/` 内的实验程序与手工测试。

## 项目是什么

`anSMBwinHelper` 是 anSMB 生态中**独立的 Windows 侧网络适配层**，用于让 Windows 原生 SMB 客户端 / Explorer 访问运行在 Android 上、监听**非标准端口 4450** 的 `anSMBserver`。

```text
Windows SMB Client
        │ TCP: <动态临时端口> → AndroidIP:445
        ▼
anSMBwinHelper
        │ 透明改写「目的端口」：AndroidIP:445 → AndroidIP:4450
        │ （目的 IP 不变；不改 Windows 源端口）
        ▼
Android anSMBserver（真正监听 TCP 4450）
        │ TCP 响应：AndroidIP:4450 → Windows 动态临时端口
        ▼
anSMBwinHelper
        │ 透明恢复「源端口」：4450 → 445
        ▼
Windows SMB Client
```

根因：Android 应用无法绑定 < 1024 的特权端口，所以 anSMBserver 用 4450；而 Windows 原生 SMB 客户端只走 445，UNC 路径也无法指定端口。**4450 不是为了避免与 Windows 445 冲突**，这是关键的因果方向，不要写反。

**关键澄清（曾被误读，务必注意）**：445 与 4450 **都是 Android IP 上的目的端口**，其中 **4450 是 Android 真正监听的端口，445 不是**（Android 从不监听 445）。**Windows 侧不监听 445**，只有 SMB 客户端的动态临时源端口。本项目的正式术语是**透明 TCP 目的端口重定向**，**不是**"Windows 本机 445→4450 端口转发"；因此 `netsh interface portproxy` 与普通用户态 TCP proxy **不适用**。

**项目边界（不得越界）**：本项目管理的是"445 → 4450 的网络适配"，它**不应**演变成 SMB Server、SMB Client、Windows 版 anSMBserver、通用端口转发工具，或自有 SMB 协议实现。

## 架构大图

`AGENTS.md` §6 与 brief §6 把架构分成两层，这是理解本项目的关键：

```text
anSMBwinHelper
├── 产品层       配置 / 目标管理 / Start-Stop / 状态 / 日志
└── 网络实现层   WinDivert（数据包拦截与重写）
```

WinDivert 是**底层技术组件**，项目本身是产品；不要让仓库变成一个 WinDivert demo。

### 阶段 1 要回答的核心问题

这些不能靠推理下结论，必须实验验证后再写入决策记录。其中部分已由 `decisions/002`、`research/001`、`research/002` 收敛：

- **目的端口改写**：已确定为**只改目的端口**（目的 IP 不变）——见 `decisions/002`；但仍需实测 checksum 处理（含 **checksum offload / LSO**）。
- **返回方向（Android → Windows）**：源端口 `4450 → 445` 还原；模型已定，仍需实测。
- **是否需要 connection tracking**：**映射本身不需要**（Windows 源端口不变）；但无状态 reverse filter 存在**跨流误伤边界**（可能匹配来自 `AndroidIP:4450` 的其他直接连接），需在阶段 1 验证真实环境是否存在该冲突，**不提前引入状态**——见 `research/002` §21。
- 多连接、长连接、正常/异常断开、重连下的行为；
- 目标不可用时的降级行为；
- 运行所需的 **Windows 权限**（普通用户 vs 管理员、驱动加载、UAC、退出清理）；
- **WinDivert 会自动安装驱动**：需确认常驻/按需、卸载条件与系统残留——见 `research/002` §15；
- WinDivert 的**许可证与再分发**要求（版本 2.2.2，见下）。

### 参考材料

`local_dev/refer/WinDivert-2.2.2-A/` 是解压的 WinDivert 2.2.2 发布包（仅作参考，**不属于正式项目**）：

- `include/windivert.h`、`doc/WinDivert.html`、`LICENSE`、`CHANGELOG`
- `x64/`（及 `x86/`）含 `WinDivert.dll`、`WinDivert.lib`、`WinDivert64.sys` 与示例工具 `netdump.exe`、`flowtrack.exe`、`streamdump.exe`、`windivertctl.exe` 等

`local_dev/refer/detour/` 是 `LeeJeKyun/detour` 的源码副本（**GPLv3**，仅作**参考实现**，**不作为代码依赖、不得复制**）。分析结论见 `local_dev/docs/research/002-detour-analysis.md`。

## 硬性约束（来自 `AGENTS.md`）

违反这些规则的改动即使"技术上更好"也是错的。

1. **阶段门**：项目只有四个阶段——`阶段 1 技术验证 → 阶段 2 最小实现 → 阶段 3 实际使用验证 → 阶段 4 发布整理`。**没有通过技术验证，不进入正式实现**；失败时不得靠堆代码绕过可行性问题。不要人为拆分子阶段（P0.1/P0.2…）。
2. **阶段 1 禁止向 `src/` 写正式代码**。实验代码必须放 `local_dev/phases/p0/`（该目录即阶段 1）或 `local_dev/tests/`、`local_dev/tools/`。
3. **`local_dev/` 是开发沙盒，永不提交 Git**（`.gitignore` 应为 `/local_dev/`）。实验失败也要保留并记录原因，不要为了"干净"删除。
4. **技术栈不锁定**：语言、UI/网络框架、具体架构在验证前不得宣布"项目采用 XXX"。WinDivert 目前是**强候选但未正式选定**，不是已定架构。
5. **实验成功 ≠ 正式实现**。`local_dev/phases/p0/` 的代码需经过 验证 → 总结 → 技术决策 → 重新设计，才可进入 `src/`；**禁止直接复制**。
6. **目录职责固定**：`local_dev/docs` 放开发文档（`decisions/`、`research/`，活计划为 `DEVELOPMENT_PLAN.md`），`docs/` 只放对外公开的正式文档，`src/` 只放已确认的生产代码。`third_party/`、`scripts/`、`assets/`、`tests/` **未到需要时不要提前创建**。
7. **重要技术决策必须落盘**到 `local_dev/docs/decisions/`，格式为：背景 / 问题 / 候选方案 / 验证 / 结果 / 决策 / 影响。不要只留在对话里。
8. **不要过度工程化**：阶段 1/2 优先验证核心问题。**没有实际验证证明必要，不得提前引入**通用 NAT、connection tracking、batching、overlapped I/O、多线程、复杂配置系统、GUI 框架、服务化。
9. **复用优先，但遵守 License**：优先复用成熟开源组件，不重复造轮子；**参考实现 ≠ 可复制代码**。`detour`（GPLv3）是**参考实现，不是代码依赖**，不得复制其源码。本项目 License 尚未确定。
10. **测试以风险覆盖为目的**，不为了阶段流程重复执行完全相同的测试；已有结果可跨阶段复用。

### Agent 权限分级（`AGENTS.md` §13）

- **Level 1 直接执行**：在 `local_dev/` 内建目录、写实验代码、跑测试、存日志与结果、整理开发文档。
- **Level 2 先说再做**：改 `src/`、加正式依赖、改正式目录结构、创建 `third_party/`/`scripts/`/`assets/`、改构建方式、把实验代码转正式实现。需说明"为什么需要 / 改什么 / 影响什么 / 为什么现在做"。
- **Level 3 必须用户确认**：改项目范围或定位、换核心技术路线或网络架构、把项目变成 SMB Server/Client 或通用端口转发、跳过阶段 1 技术验证、大规模重构正式目录。

存在多个合理方案时，**先给出方案、依据、优劣与验证结果，不要替用户做核心架构决定**；无法确定时先问，不要猜。

### 阶段 1 完成判定

阶段 1 只有在 `AGENTS.md` §15 的清单全部确认后才可标记 PASS，并需产出 `local_dev/phases/p0/RESULT.md`（该目录即阶段 1；模板见 `AGENTS.md` §16，含测试环境、逐项结果、问题、结论 PASS/FAIL/PARTIAL、后续建议）。

## 待处理的仓库缺口

建议由用户确认后再处理，不要自行"修正"：

- **`LICENSE` 待确认**：项目许可证尚未确定，不得自行选择 MIT、Apache-2.0 等。在确认前不要创建 `LICENSE` 文件。
- **Git 提交策略待确认**：仓库已 `git init` 但尚无首次 commit，提交策略未定。
- `AGENTS.md` 与本文件并存：两者都描述 Agent 工作规则。本文件刻意保持精简并指向 `AGENTS.md` 以避免内容分叉，**修改规则时请更新 `AGENTS.md`，不要只改这里**。
