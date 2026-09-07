# Goren Actor 架构与迁移方案

状态：目标实现方案已确定；生产迁移未完成，框架适配与业务协议仍按验收条件验证。
日期：2026-09-07。代码保存点 `c359000`，GoAkt 基线 v4.5.3。

[各 Actor 实现定义](./actor-design/11-actor-implementation.md)是完整 Actor 清单；
[GoAkt 运行时 与初始化](./actor-design/10-runtime-and-initialization.md)、[生命周期](./actor-design/07-lifecycle-implementation.md)、
[工具更新](./actor-design/08-tools-implementation.md)各自维护唯一完整协议。
现有 19 项实验只证明其明确记录的行为，不代表本版全部设计已实现。

## 1. 已确认的架构决策

| 决策 | 结论 |
| --- | --- |
| 总体方向 | 移除 Plugin Runtime，以 actor 作为主体运行时架构 |
| Actor 边界 | 独立运行期状态、并发命令与生命周期由 actor 拥有；值、纯计算及 I/O adapter 使用普通 Go 对象 |
| Agent 与 Session | 独立 owner；Agent 绑定 Session，不拥有其日志或生命周期 |
| 身份与创建 | AgentID = SessionID；收到消息先按 ID 查找或构造未就绪 Agent，再创建/恢复同 ID Session，绑定就绪后处理消息 |
| 正常关闭 | 停止接纳新业务，排空已接纳消息并完成结算、持久化；先关闭 Agent，再关闭 SessionActor，最后释放 ID 占用 |
| Tools Registry | 全局共享，维护各 Agent 的工具注册、有效集合及版本，集合变化通知相关 Agent |
| Tool Runtime | 每 Agent 独立，拥有 ToolActor 生命周期、调用路由、调度、取消与结算 |
| ToolActor | 每 Agent 的每个工具有独立实例，向共享 Tools Registry 自注册 |
| 工具调用 | Agent → 自己的 Tool Runtime → ToolActor；Registry 不在执行转发路径 |
| 工具注册与更新 | 进入该 Agent 的业务有序队列；前序用户消息完整结算后原子更新，更新完成再处理后续消息 |
| 工具更新实现 | ToolActor 身份稳定；内部准备/切换实现并释放旧资源，仅新增/删除工具才创建/停止 Actor |
| Prompt | 共享 Prompt Registry、每 Agent 独立 Prompt Runtime；Registry 管定义，Runtime 管应用与组装 |
| Session | 共享 Session Registry、独立 SessionActor；Registry 管身份与目录，SessionActor 管日志与提交 |
| GoAkt 运行时 | 维护组件寻址和异步消息入口；不向 AgentLoop 返回组件地址或对象 |
| AgentLoop 初始化 | 创建后经 Runtime 请求各组件配置/版本，必要项就绪后处理业务；不提前注入完整配置 |
| 通信 | Actor 间采用异步通信：需要结果用 Request + Then，无需回复用 Tell，变化事实用 PubSub；不采用同步 Ask/SendSync |
| Subagent 配置 | 创建时复制 parent 有效配置快照，再应用自定义覆盖并校验；parent 后续更新不自动传播 |
| 父子生命周期 | 保持既有父子关系和 child-first 关闭，不随配置隔离而解除 |
| Scope | 从目标架构移除；配置继承归创建用例，资源、策略与订阅归各 actor/Runtime owner |

用户先前否定的“每 Agent 独立 Tools Registry”不再作为候选。
SessionActor 已承担 Session 运行职责，不额外创建无独立状态的 Session Runtime。
本组文档是对讨论结果的整理，不将所有具体事务、消息名字和恢复机制标为 Accepted。
Session 首期使用显式绑定/解绑，不引入 lease；处理完一条消息本身不触发关闭。
工具更新的直接完成响应决定 Agent 何时继续；PubSub 的 ToolsChanged 只发布变化事实。

## 2. 设计导航

| 文档 | 职责 |
| --- | --- |
| [01 Actor、Registry 与 Runtime 所有权](./actor-design/01-ownership.md) | 术语、生命周期树、状态权威、发现/执行分离、Agent 隔离与共享资源 |
| [02 注册、快照与运行协议](./actor-design/02-capability-protocols.md) | ToolActor 自注册、集合换代、调用/取消、Prompt 组装、Session 提交和 PubSub 一致性 |
| [03 生命周期、迁移与验收](./actor-design/03-migration-and-validation.md) | 当前代码映射、创建/关闭、Plugin 语义迁移、实施阶段、证据与待决项 |
| [04 Subagent 配置继承与 Scope 移除](./actor-design/04-subagent-configuration.md) | 一次性快照继承、独立配置、父子生命周期、覆盖待决项及 Scope 职责去向 |
| [05 整体实现方案](./actor-design/05-implementation-overview.md) | 运行架构图、Grain 载体与模块、用户消息主线 |
| [06 通信机制与任务](./actor-design/06-messaging-and-tasks.md) | Request、延迟回复、接续、PipeToSelf、PubSub 与背压 |
| [07 生命周期实现](./actor-design/07-lifecycle-implementation.md) | 创建状态机、业务关闭、最终停用、身份与恢复 |
| [08 工具实现](./actor-design/08-tools-implementation.md) | 模块协作、批次发布、路由、退役及 MCP |
| [09 模块与迁移](./actor-design/09-module-implementation.md) | 包级职责、Prompt/Session/LLM、迁移批次与验收 |
| [11 各 Actor 实现定义](./actor-design/11-actor-implementation.md) | 每个 Actor 的身份、状态、请求、实现、失败及关闭 |
| [10 GoAkt 运行时 与初始化](./actor-design/10-runtime-and-initialization.md) | 组件寻址、创建后配置请求、就绪及失败处理 |
| [GoAkt 隔离实验](../experiments/goakt/README.zh-CN.md) | 固定 v4.5.3 的实验命令、既有测试结果与明确验证边界 |

执行顺序与验收出口见 [12 实施计划](./actor-design/12-implementation-plan.md)。

## 3. 兼容目标与适用范围

这次改变内部 runtime 与公开 Plugin 扩展方式；不自动改变现有客户端 HTTP/WebSocket 协议、
Session 事件和 persisted shape、模型输入重建、工具权限或结果语义。
固定 DSH 基线继续为保留的外部行为提供依据，但其 Cordis/Plugin 实现结构不再是目标运行时约束。
凡无法原样保持的行为，列出源符号、当前 Go caller、可观察影响与迁移方式后再定案。

首期设计建议采用单进程 standalone，暂不启用 cluster、Grain、actor relocation 或自动业务重放。
框架候选与实验使用 GoAkt v4.5.3；实验通过仅支持继续设计，不替代生产生命周期与兼容验收。
长期运行对象显式关闭自动闲置停用且不配置系统 eviction；具体 supervisor 策略仍需验证。

保留现有业务 owner：LLM、Credentials、Workspace、Approval、Interaction、Compaction、Subagent、
Query/Projection 等按实际调用链继续设计，不并入一个全局服务 actor，也不要求一包一 actor。
MCP 后续沿新 ToolActor/连接 owner 接入，当前协议适配和测试保留，不继续扩展旧 Plugin 装配。

## 4. 与现有仓库规范的关系

移除 Plugin 是用户显式确认的新方向，与当前 AGENTS.md 和编号文档中的 Plugin 强制规则冲突。
本轮授权是梳理方案，因此保留现有全局文档；正式实现前按最终确认方案更新冲突规则和权威索引。
本文及 actor-design 子文档是隔离的设计草案，不是已完成实现进度，不更新 zh-CN/README.md。

当前代码已保存于 checkpoint；实验和本组草案尚未提交。本轮不修改生产代码、依赖或修复历史命名问题。
下一步见[待决项与建议顺序](./actor-design/03-migration-and-validation.md#5-待决项)。

当前实施记录：[迁移基线](./actor-design/13-migration-baseline.md) · [局部接续设计与验证](./actor-design/14-continuation-design.md)。

首个生产能力切片：[Credentials 模块设计](./actor-design/15-credentials-module.md)。
