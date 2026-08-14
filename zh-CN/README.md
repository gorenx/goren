# DeepSeek Harness Go 复刻中文详设

状态：Draft

本目录是 Goren 当前主线设计的唯一入口。设计以 DeepSeek Harness TypeScript 基线 `47f943859bef60e4160492346772ded9b24f765a` 为源代码证据，首要目标是在不复制客户端实现的前提下，让现有 TypeScript 客户端与 Go Agent 服务端保持通信协议兼容；Web UI、SDK 和 Python 不进入实现。

## 阅读顺序与职责

- [01 复制范围与兼容基线](./01-porting-scope-and-baseline.md)：目标、非目标、纳入与排除范围、需求、不变量和源代码基线。
- [02 Go 运行时架构与插件模型](./02-runtime-architecture-and-plugin-model.md)：模块边界、依赖方向、Plugin interface、typed config、Service/Event Registry、Scope 与生命周期。
- [03 协议与 API 兼容设计](./03-protocol-and-api-compatibility.md)：Client Connection、API Proxy、RPC/stream、Session/LLM/Tool 契约、Deferred adapter 边界和跨语言兼容验证。
- [04 Go 技术架构决策与技术选型](./04-go-technology-decisions.md)：Connection carrier、typed config、标准库、第三方依赖、持久化、PTY、沙箱与可观测性决策。
- [05 复制路线图与验收](./05-porting-roadmap-and-acceptance.md)：分阶段交付、源包映射、测试策略、完成定义和未决事项。
- [06 Connection Host 模块设计与实现](./06-connection-host-module.md)：Connection wire contract、Echo inbound adapter、信任边界、验证所有权、取消与生命周期。
- [07 API Proxy 模块设计与实现](./07-api-proxy-module.md)：typed method Catalog、Provider 边界、`host.describe` 纵向切片与结果/错误映射。
- [08 实施进度](./08-implementation-progress.md)：阶段完成度、当前代码/测试证据、验证结果、阻塞项和下一步。
- [09 Plugin Runtime 与 Server Assembly 模块设计与实现](./09-plugin-runtime-and-server-assembly.md)：typed Service/Event、Scope/effect、依赖结算、replacement、Factory Catalog 与 Connection 插件装配。
- [10 Session Core 与生命周期模块设计](./10-session-core-and-lifecycle.md)：Header/Event、内存 append-only log、surface、Store 生命周期、订阅与 persistence 边界。

首次阅读按 `01`–`05` 顺序理解全局设计；进入实现时读取对应模块文档，再从 `08` 查看当前进度。实现单个能力时，先从 `01` 确认范围，再读其拥有契约的文档。DeepSeek Harness 的 Service Definition / Provider / Consumer、事件 owner 和生命周期是默认职责边界；Go 包不机械复制每个 npm 包，但没有明确证据时也不另起一套领域切分。

## 权威关系

- 本目录 `01`–`05` 拥有全局设计，`06`、`07`、`09`、`10` 拥有已进入实现的稳定模块设计，`08` 单独拥有日期性实施进度与证据。
- 根目录 `README.md` 与 `README.zh-CN.md` 只说明项目背景。
- TypeScript 的行为证据来自固定 commit；源仓库后续变化不会自动成为 Go 需求。
- Go 代码证明当前实现，跨语言 fixtures 和测试证明兼容性；设计状态不能代替实现或验收证据。
- `llm/docs/zh-CN/` 记录主线调整前的 LLM 实现状态，仅用于迁移审计，不拥有 Harness LLM API。

## 隔离的历史设计

[`memory-design/`](./memory-design/) 保存此前的 Memory Agent 设计。它不属于当前 Harness 复刻主线，不应被默认加载、引用或用来决定 Harness 的 Agent、Session、Tools、Workflow 和 Knowledge 边界；只有明确处理该历史主题时才读取。

## 文档规则

- 详设使用简体中文，两位数字文件名和一级标题表达阅读顺序。
- 公共名、事件名、JSON 字段、配置键和既有领域术语保持 TypeScript 的 canonical form。
- 一个事实只在拥有该职责的文档中定义，其他文档只链接。
- 实施完成度、验证命令、阻塞项和下一步只更新 `08`，不得散落到路线图或模块设计文档。
- 只为已经出现真实职责与代码边界的模块增加实现文档；不为规划目录或空 package 建文档占位。
- 模块文档至少记录职责/非职责、源 owner、依赖方向、上下游流程、生命周期、失败/取消语义、验证所有权和后续能力进入规则；代码/测试证据与未完成项只写入 `08`。
- 未确认的行为保留为显式未决事项，不写成已经实现或已经验证。
- 新增、删除、重命名、重排文档或改变文档职责时同步更新本索引。
