# 迁移基线与首批实现范围

状态：P0 已开始；已核对核心调用链并记录基线，完整监听器及外部兼容 fixture 清单随所属模块补齐。
执行顺序见[实施计划](./12-implementation-plan.md)。本文件只记录当前 Actor 迁移，不改写历史设计。

## 1. 工作约束

每个模块开始实现之前，先形成模块设计：架构、接口、关键数据结构、设计决策、上下游契约及验收。
遇到失败先定位根因及权威 owner，禁止用额外包装掩盖职责问题。
对象之间只以身份、消息或消费者接口关联，禁止循环嵌套；这不仅检查 Go import，也检查结构体对象图。
文件名对应职责，同一对象的方法跨文件时使用公共前缀。结构体声明和 keyed literal 字段纵向排列。
命名首先说明对象是什么，概念叠加时检查并拆分职责。

## 2. 当前生产调用链与迁移出口

以下为当前 Go 代码事实及目标映射，不是已完成兼容声明。外部基线仍由
[固定基线](../01-porting-scope-and-baseline.md)拥有，包含 Compaction 与 MCP 的专项例外。

| 当前入口 | 当前职责 | 目标及删除条件 |
| --- | --- | --- |
| `internal/assembly/server.go` 的 BuildServer | Factory 构造完整 Plugin 树 | 组合根启动框架与业务 owner，P8 删除旧构造 |
| `internal/assembly/catalog.go` 的 NewCatalog / DefaultSpecs | 工厂登记与配置默认值 | typed config 保留；不改造成技术地址表 |
| `agent/registry_lifecycle.go` 的 beginConstruction / construct | 身份占用、父子关联、构造发布与失败清理 | Agent Registry + AgentLoop 初始化协议通过后替换 |
| `agentloop/rla_model_request.go` 的 executeModelRequest | 流读取、Session chunk 提交、重试与工具批次 | AgentLoop 异步状态推进，P5 对话验收后删除旧循环 |
| `tools/runtime.go` 的 Prepare / Dispatch / Finalize | 策略、执行、结果结算 | Tool Runtime 保留业务阶段，调用变异步 |
| `tools/layer_tool_set.go` 的 Replace / Close | 工具集合替换与注销 | Registry 原子发布 + 稳定 ToolActor 内切换实现 |
| `systemprompt/layer.go` 的 Assemble / publishChanged | Prompt 组装及变化交付 | 组装与目录分属 Runtime/Registry；可否决逻辑不能换成通知 |
| `session/session_context_commit.go` 的 Commit | 串行提交及事件形成 | SessionActor 唯一提交权威，数据格式保持 |
| `session/memory_store_lifecycle.go` 的 flush / runRelease | 持久化请求、释放与失败重试 | SessionActor 与 Registry 的关闭事务 |
| `credentials/credentials.go` 的 Manager | 环境优先级、存储读写规则 | CredentialsActor 拥有策略，LiveStore 保持纯存储 |

后续每个模块在自身设计中记录具体请求、旧消费者迁移和 fixture，不批量删除 Factory 或修改全部公共接口。

## 3. 2026-09-07 根模块基线

在生产代码未改动时运行 `go test -count=1 ./...`，失败于 `tests/architecture` 的命名约束检查。
诊断共 5 处：`mcpclient/protocol_session.go:126`、`mcpclient/protocol_session_test.go:308` 的 result，
`tools/definition.go:56`、`tools/result_processor.go:153`、`tools/result_processor.go:196` 的 replace。
这些是已保存 MCP/工具工作中的命名冲突；不是 Actor 改动引入。本批不以关闭检查或扩大重命名消除基线失败。
后续迁入相关模块时修正并重新运行根检查。

本次基线命令不证明 race、vet、build 或真实 provider 可用；嵌套实验模块不在根测试覆盖范围。

## 4. 首个代码批次

先实施[局部异步接续验证设计](./14-continuation-design.md)，在 `experiments/goakt` 验证任务寿命、
停用和迟到结果，不新增生产 Runtime。P1 的授权/释放竞争和 Registry 故障恢复仍需独立验证，
通过局部接续测试不代表整个 P1 完成，也不能跳过各能力模块设计直接进入批量 Actor 改造。
