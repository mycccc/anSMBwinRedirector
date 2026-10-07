# anSMBwinRedirector

Windows 侧的**透明 TCP 目的端口重定向**小工具，让 Windows 原生 SMB 客户端 / Windows 资源管理器
能够访问运行在 Android 上、监听**非标准端口**（默认 `4450`，可配置）的 `anSMBserver`。

```text
Windows 资源管理器 / 原生 SMB 客户端
        │  TCP: <动态临时端口> → AndroidIP:445
        ▼
anSMBwinRedirector
        │  透明改写「目的端口」：AndroidIP:445 → AndroidIP:<Android SMB 端口>
        ▼
Android anSMBserver（实际监听该可配置端口）
        │  TCP: AndroidIP:<端口> → Windows 动态临时端口
        ▼
anSMBwinRedirector
        │  透明恢复「源端口」：<端口> → 445
        ▼
Windows 资源管理器 / 原生 SMB 客户端
```

**为什么需要它**：Android 应用无法绑定 < 1024 的特权端口，所以 `anSMBserver` 不能监听 `445`；
而 Windows 原生 SMB 客户端只走 `445`，UNC 路径也没有指定端口的机制。
（注意因果方向：`anSMBserver` 用非标准端口是 **Android 平台限制**的结果，
**不是**为了避免与 Windows 的 `445` 冲突。）

---

## 1. 运行要求

| 项 | 要求 |
| --- | --- |
| 操作系统 | **Windows x64（amd64）** —— 仅此一种 |
| 权限 | **管理员**（加载 WinDivert 驱动需要） |
| 同目录文件 | **`WinDivert.dll` 与 `WinDivert64.sys` 必须与 exe 同目录**，缺一不可（见 §4） |
| 网络 | 能访问目标 Android 设备的 IP |

> ⛔ **不支持 Windows on ARM64**：amd64 程序虽可在仿真下运行，但内核驱动必须是 ARM64，
> 本程序携带的 amd64 `WinDivert64.sys` 无法加载。程序会在启动最前**显式拒绝**并说明原因。
> ⛔ 不支持 32 位 Windows（amd64 可执行文件无法启动）。

---

## 2. 安装与目录结构

**解压即用，没有安装器。** 把发布包解压到任意目录（建议**非只读**、且路径不含特殊字符）：

```text
anSMBwinRedirector-<版本>-win-x64/
├── anSMBwinRedirector.exe
├── WinDivert.dll            ← 第三方（WinDivert 2.2.2-A），必须与 exe 同目录
├── WinDivert64.sys          ← 第三方，必须与 exe 同目录
├── LICENSE                  ← 本项目 GPLv3 全文
├── THIRD_PARTY_NOTICES.md   ← 第三方组件与许可全文
└── README.md                ← 本文件
```

程序**不写安装目录**，也**不写 `%PROGRAMDATA%`**。唯一的持久化文件是配置文件（见 §6）。

---

## 3. 首次运行

1. 右键 `anSMBwinRedirector.exe` → **以管理员身份运行**。
2. 主窗口出现（同时出现托盘图标）。
3. 点「添加」→ 填写目标：
   - **Android IP**：设备的 IPv4 地址（例如 `192.168.1.100`）；
   - **Android SMB 端口**：`anSMBserver` 实际监听的端口（默认 `4450`，可配置）。
     *同一个 Android IP 只能有一条规则。*
4. 点「应用更改」保存。
5. 点「启动」。

之后在资源管理器里访问 `\\<Android IP>\<共享名>` 即可 —— Windows 侧仍然走 `445`，
程序在背后把它重定向到 `<Android IP>:<你配置的端口>`。

> 程序不会修改系统代理，也不会使用 `netsh portproxy`。

---

## 4. 关于 WinDivert（**请务必读**）

本程序通过网络层拦截/改写数据包来实现重定向，这依赖第三方组件 **WinDivert 2.2.2-A**。

- 它由 **`WinDivert.dll` + `WinDivert64.sys`** 两个文件组成，**必须与 `anSMBwinRedirector.exe` 放在同一目录**。
- 程序启动时（以及每次「启动」）会校验这两个文件的 **SHA-256**：
  - **缺失** → 报 `RUNTIME_FILE_MISSING`（含完整路径）；
  - **内容被改动** → 报 `RUNTIME_FILE_CORRUPT`（同时给出**实际**与**期望**的 SHA-256）。
  - ⛔ 无论哪种情况，程序**只报告**：**不会**自动覆盖、**不会**重新下载、**不会**删除你的文件。
    你可以自行把正确的文件放回去，然后点「重试启动」。
- 首次运行时，WinDivert 驱动会**按需安装**（这会创建一个 Windows 服务，需要管理员权限）。
  服务与驱动的安装/加载由 WinDivert 自己完成，本程序不介入。

### 4.1 本程序**不提供** WinDivert 卸载功能

> **v1 不提供 WinDivert 驱动卸载功能。程序正常退出后不会主动停止或删除 WinDivert service，
> 也不会删除 `WinDivert.dll` / `WinDivert64.sys`。若驱动当前处于加载状态，系统重启会使
> 当前驱动实例随系统关闭而退出，但相关 service 注册和这两个文件可能继续保留。**

为避免误解，把三件事分开说：

| 层面 | 重启（真重启）的影响 |
| --- | --- |
| **当前加载的驱动实例** | 随系统关闭而退出（所以重启可以解除驱动版本冲突） |
| **WinDivert service 注册** | ⛔ **不会**因此自动删除 |
| **`WinDivert.dll` / `WinDivert64.sys` 文件** | ⛔ **不会**因此自动删除（它们是你解压出来的普通文件） |

残留的 service 注册属于**已知的、被接受的状态**；如需清理，请由你在知情前提下自行处理
（例如按 WinDivert 官方说明删除对应文件）。⚠️ **不要**把上面这段话理解成"重启就清理干净了"。

### 4.2 「检查 WinDivert（只读）」

主窗口的「检查 WinDivert（只读）」按钮会显示当前环境的事实，**只读、不修改任何东西**：

- WinDivert service 是否存在、其 `ImagePath` 指向哪里、是否指向本程序目录；
- 驱动状态与版本；
- 本程序目录下两个文件是否完整；
- 当前可枚举到的 WinDivert 使用者（`NETWORK` / `NETWORK_FORWARD` / `FLOW` / `SOCKET` 层）。

> ⚠️ 诚实声明：`REFLECT` 层的使用者**无法枚举**，因此**无法证明**系统中不存在其他 WinDivert 使用者。

---

## 5. 界面说明

**主窗口**

| 操作 | 说明 |
| --- | --- |
| 添加 / 编辑 / 删除 | 只修改**草稿**；删除后该行显示为「待删除」，可点「撤销删除」恢复 |
| **应用更改** | **唯一**的提交入口：校验 → 写盘（并生成 `.bak`）→ 生效。未保存时状态行显示 `⚠ 有未保存更改` |
| 启动 / 停止 | 启动/停止所有目标的重定向 |
| 连接测试 | 未运行时探测 `<AndroidIP>:<端口>`；运行中探测 `<AndroidIP>:445`（走完整链路）。失败是**非错误**，只更新「可达性」列 |
| 重试启动 | 对**失败**的目标重新启动（用磁盘上的配置）；其余目标不受影响 |
| 检查 WinDivert（只读） | 见 §4.2 |
| 复制详情 / 复制配置路径 | 把诊断信息 / 配置文件路径放进剪贴板 |
| 退出 | 若有未保存更改，会问「保存并应用 / 放弃 / 取消」 |

**托盘**：左键点击图标打开主窗口；右键菜单提供 状态、启动、停止、应用更改（仅在有未保存更改时出现）、
打开主窗口、退出。**关闭主窗口只是隐藏到托盘**，不会退出程序。

**配置不可用**：如果配置文件无法解析，程序会显示「配置不可用」，在修复前**不会**启动重定向、
也**不会**覆盖该文件。处置方式有两种：① 复制路径后人工修复；② 用空配置重新开始
（原文件会被**改名**为 `config.json.corrupt-<时间戳>`，内容完整保留、不删除）。
*若配置的 `schema_version` 高于本程序支持的版本，则**只提供**①。*

---

## 6. 配置文件

```text
%APPDATA%\anSMBwinRedirector\config.json
```

```json
{
  "schema_version": 1,
  "targets": [
    { "id": "<稳定 UUID>", "android_ip": "192.168.1.100", "android_port": 4450 }
  ]
}
```

- `id` 由程序生成并保持稳定；**`android_ip` 在规则集合内唯一**，重复会被拒绝。
- 每条规则只有两项可配：**Android IP** 与 **Android SMB 端口**。
  Windows 侧匹配的 **TCP 445** 是固定项，不可配置。
- 保存时会生成 `config.json.bak`。**读取配置的过程不会写任何文件。**

---

## 7. 网络模型（一句话版）

对每个目标，程序只做一件事：把**目的端口** `445` 改成你配置的端口（**目的 IP 不变、
不改 Windows 源端口**），并在返回方向把源端口改回来。
它**不是**"本机 445 → 4450 的端口转发"，也**不是**任意 `FROM → TO` 的通用 NAT；
`netsh interface portproxy` 之类的工具**不能**替代它。

---

## 8. 已知限制（v1）

- 仅 **Windows x64**；不提供 32 位、ARM64 支持（ARM64 会显式拒绝）。
- **不提供** WinDivert 卸载/清理功能（见 §4.1）。
- **不提供**安装器；**不提供**自动更新。
- 不做 SMB 服务器或客户端，不做通用端口转发/代理，不支持 UDP。
- 本项目**不会**启动/停止/删除 WinDivert service，也**不主动**卸载驱动。

---

## 9. 从源码构建

需要 Go（版本见 `src/go.mod`）。发布包由脚本组装（会校验哈希链并打印）：

```sh
scripts/build-release.sh 0.1.0        # 产物在 src/dist/release/
```

要点：

- **Manifest 是必需的**：GUI 框架 `lxn/walk` 需要 Common-Controls v6 清单，缺失会导致窗口**完全建不出来**。
  清单以 `.syso` 资源形式**内嵌**（`cmd/ansmbwinredirector/rsrc_amd64.syso`），
  该文件由 `go tool rsrc`（版本锁在 `go.mod` 的 `tool` 指令里）**可复现地**重新生成；
  构建脚本会重新生成并与其**逐字节比对**，不一致直接失败。
- **WinDivert 的 SHA-256 固化为编译期常量**（`src/internal/winvert/assets_common.go`）。
  升级 WinDivert 时必须同步更新这些常量 —— `go test ./internal/winvert` 会强制这一点。

---

## 10. 许可证

- 本项目：**GNU GPLv3 —— 具体为 `GPL-3.0-only`**（即"仅 GPLv3"，**不含** "or any later version"）。
  `LICENSE` 是 GNU 官方的 GPLv3 **全文**（未做任何修改，包含文末"How to Apply"一节的示例文本）。
- 随包分发的第三方组件及其许可全文：见 `THIRD_PARTY_NOTICES.md`
  （**WinDivert** 采用 LGPLv3 / GPLv2 双许可，本项目**择 LGPLv3**，并因此把它的
  `WinDivert.dll` / `WinDivert64.sys` 作为**独立文件**分发以保持可替换）。
