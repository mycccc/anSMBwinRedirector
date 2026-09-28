# anSMBwinHelper

anSMBwinHelper 是一个 Windows 侧的网络适配辅助程序，用于让 Windows 原生 SMB 客户端 / Windows Explorer 能够访问运行在 Android 上、使用非标准 TCP 端口的 `anSMBserver`。

本仓库目前处于早期阶段，README 将随项目阶段推进逐步完善。

## 项目简介

`anSMBserver` 运行在 Android 上。由于 Android 平台对普通应用绑定低于 1024 的特权端口存在限制，它使用 1024 以上的非标准 SMB 端口，当前默认为 `4450`。

Windows 原生 SMB 客户端通常通过 TCP `445` 访问 SMB 服务，并且普通 UNC 路径没有提供指定 SMB TCP 端口的机制。由此产生了 Windows 端访问 `anSMBserver` 的兼容性问题。

需要明确区分因果关系：`anSMBserver` 使用 `4450` 是 Android 平台限制导致的结果，**不是**为了避免与 Windows 的 `445` 端口冲突。

anSMBwinHelper 负责处理这一差异，概念链路如下：

```text
Windows Explorer / Windows Native SMB Client
                │
                │ TCP 445
                ▼
        anSMBwinHelper
                │
                │ TCP 4450
                ▼
          anSMBserver (Android)
```

## 当前阶段

项目当前处于：

> **项目初始化 / P0 技术可行性验证阶段**

当前尚未开始正式实现，仓库中还没有生产代码。P0 阶段的实验、测试与记录都在 `local_dev/` 中进行。

## 项目目标

让 Windows 用户能够通过 Windows 原生 SMB 客户端 / Windows Explorer 访问 `anSMBserver`，而无需手动配置外部端口转发工具，也无需改用支持自定义 SMB 端口的第三方 SMB 客户端。

期望的最终用户体验是可以直接访问：

```text
\\192.168.1.100\Internal
```

用户不需要知道 `anSMBserver` 实际监听在 `192.168.1.100:4450`。

项目定位于 Windows 网络适配层，其边界不包含 SMB Server、SMB Client、通用端口转发工具或自有 SMB 协议实现。

## P0 目标

P0 的唯一核心目标是：

> 证明 Windows 原生 SMB 客户端能够通过 anSMBwinHelper，可靠访问运行在 TCP `4450` 上的 `anSMBserver`。

P0 需要验证的内容包括但不限于：TCP 连接建立、SMB2/SMB3 实际通信、多连接、长连接、正常关闭与重连、目标服务器不可用时的行为、返回方向流量处理、非目标流量的隔离、运行所需权限，以及第三方组件的分发条件。

完整的验证清单与 P0 完成判定标准见 [`AGENTS.md`](AGENTS.md) 第 15 节。

**P0 目前尚未通过。**

## 目录结构

当前实际结构：

```text
anSMBwinHelper/
├── AGENTS.md       # AI Agent 开发规则（权威）
├── CODEBUDDY.md    # Agent 入口与速查
├── README.md       # 本文件
├── .gitignore
└── local_dev/      # 本地开发区，不进入 Git
```

以下目录属于规划，按实际需要再创建：

```text
docs/               # GitHub 公开文档
src/                # 正式源代码
third_party/        # 正式第三方依赖
scripts/            # 正式构建 / 发布脚本
assets/             # 正式资源
tests/              # 正式长期维护测试
```

目录职责的完整说明见 `local_dev/chatdocs/3目录结构规划-PROJECT_DIRECTORY_STRUCTURE.md`。

## 许可证

**待确认。** 项目许可证尚未确定，当前仓库不包含 `LICENSE` 文件。
