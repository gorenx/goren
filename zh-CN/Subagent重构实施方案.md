# Subagent 重构实施方案

状态：执行基线

## 1. 文档职责

本文只定义 Subagent 破坏性重构的模块顺序、每阶段变更边界和验证 Gate。它不记录完成百分比或当前状态。

- 最终职责和接口见[技术方案](./Subagent架构与生命周期重构技术方案.md)。
- 实际完成情况和检查结果见[进度矩阵](./Subagent重构进度矩阵.md)。
- 固定源兼容证据见[源能力分析](./subagent/01-source-capability-analysis.md)。

## 2. 强制约束

### 2.1 按模块迁移

实施顺序固定为：

```text
公共契约
  -> SeedBuilder
  -> common Execution
  -> OneShot
  -> Continuable
  -> unified Service
  -> ChildDirectory / Projection
  -> Tool adapters / Extensions
  -> Runtime Plugin
  -> 文档和全量验证
```

每个阶段先确定 owner 和依赖方向，再迁移实现、调用者和测试。不能同时在多个 owner 中散点增加 wrapper。

### 2.2 不保留过渡代码

本次允许破坏性更新：

- 删除旧接口后直接迁移所有调用者；
- 不保留 type alias、deprecated wrapper、双 Service publication 或新旧包并存；
- 不保留仅供旧测试使用的 callback adapter；
- 不要求中间提交可运行，但进入下一模块前必须完成静态引用清理；
- 最终 Gate 前必须恢复完整构建和测试。

### 2.3 Plugin 与业务分离

- 业务对象不能依赖 `plugin.Plugin`、Plugin Scope binding 或 event bus 实现。
- Plugin 负责依赖解析、capability publication、registration 和 Apply/Dispose。
- Tool Plugin 负责 Tool/Prompt registration；Tool 执行对象只依赖公开 Subagent capability。
- spawn/fork Builder 是纯业务策略；Plugin 只拥有其 exact registration。

### 2.4 状态与命名

- 生命周期阶段使用一个枚举字段，不使用 `started/closing/disposed/...` 布尔集合。
- 名称必须先说明对象是什么；职责混合时拆类型，不叠加关联对象和处理步骤。
- `Provider` 只允许出现在兼容事件名、wire/persisted 字段或 Service Provider/Consumer 架构术语中。
- `Activation`、`Catalog`、`Continuation Manager` 不再作为当前实现概念。
- 文件名对应文件内主要对象或职责，不建立一函数一文件。

## 3. S0：公共契约

### 目标

先固定两种实现共享的输入、生命周期、控制、目录、创建策略和扩展边界。

### 变更

1. 用 `Mode` 和 closed `StartCommand` 表达合法 OneShot/Continuable 启动 variant；命令构造时校验并复制 `ChildRequest`，不另设 snapshot 包。
2. 定义 common `Execution`、`ExecutionState`、`Terminal` 和 `Starter`。
3. 定义 `ChildControl`、`ParentReporter`、`ChildDirectory`。
4. 把创建输入策略命名为 `SeedBuilder`，保留兼容 provider token。
5. 把 child-scoped 扩展命名为 `ContinuableExtension`、`ExtensionInstallation`。
6. 保留 descriptor、event、MessageSource 和 error code 的兼容 shape。

### 删除

- Provider execution interface；
- OneShot `Run`/`Result` public lifecycle；
- Continuable-specific public Service；
- Activation 和 Catalog public vocabulary；
- capability bool 和 mode-specific return union。

### Gate

```text
go test ./subagent -run 'Test(StartCommands|Descriptor|RuntimeProvides)'
rg -n 'type (Provider|Run|Activation|Catalog|ContinuableService)' subagent --glob '*.go'
```

## 4. S1：SeedBuilder

### 目标

把 child Session 初始上下文构造与 Agent 创建、运行和 Plugin 生命周期彻底分开。

### 变更

1. 建立 `internal/seedbuilder.Registry`，拥有名称唯一性和 exact registration；不维护无 Consumer 的名称列表或顺序副本。
2. added event 可拒绝并回滚；removed event 在移除提交后 best-effort 发布。
3. `spawn.Builder` 返回空 seed。
4. `fork.Builder` 从 detached parent snapshot 选择最后一个 completed turn 前缀。
5. spawn/fork Plugin 只注册/注销 Builder。

### 删除

- Provider-owned `Start`；
- spawn/fork 内的 Agent Constructor、Approval、Execution 或 Driver；
- `internal/provider` 和 `internal/inprocess`。

### Gate

```text
go test ./subagent/internal/seedbuilder ./subagent/spawn ./subagent/fork
go test -race ./subagent/internal/seedbuilder ./subagent/spawn ./subagent/fork
```

架构检查必须证明 spawn/fork 业务文件不导入 Agent Constructor、AgentLoop、Approval 或 Plugin；只有各自 `plugin.go` 可以依赖 Plugin Runtime。

## 5. S2：Common Execution

### 目标

让 OneShot 与 Continuable 使用同一个执行状态机、terminal future 和停止事务。

### 变更

1. 建立 `internal/execution.Execution`。
2. 用单字段状态表达 `Starting -> Active -> Stopping -> Stopped`。
3. `Activate` 只在 initial Inbox message 已接受后提交 publication。
4. `Stop` 只 claim；`StopAndWait` 和 `Dispose` join 同一个 terminal transaction。
5. 建立 live Execution Registry，以 exact child/Agent identity 支持 interrupt 和 external Agent disposal。
6. RunID 与 child Session ID 生成并入该模块，不保留独立 identity 包。

### 删除

- OneShot 专用 Run state；
- Continuable Activation/disposal state；
- 多组 close/dispose bool；
- mode-specific terminal future。

### Gate

```text
go test ./subagent/internal/execution
go test -race ./subagent/internal/execution
```

## 6. S3：OneShot

### 目标

让 OneShot 实现完整拥有一次性 child 的构造、结果选择和自动释放。

### 变更

1. 在异步边界前 snapshot `ChildRequest`。
2. 解析 SeedBuilder 并构造 fresh seed。
3. 推导 lineage、child policy、descriptor appender 和可选 structured capture。
4. 通过 `agent.Constructor.Create` 创建 child，并接受 initial message。
5. initial acceptance 后激活并注册 common Execution。
6. 以 `agent.FoldConsumedWork` 和 `execution.SelectAssistantOutput` 形成 Terminal。
7. 正常结束、interrupt、module close 和 external Agent close 汇合到 common stop transaction。
8. terminal 后自动释放 exact Handle。

### 删除

- holder 必须二次 Dispose 的语义；
- Provider/in-process Driver dispatch；
- OneShot 与 Tool 之间的私有 Run adapter。

### Gate

```text
go test ./subagent/internal/oneshot ./subagent/internal/execution ./subagent -run 'TestForegroundOneShot'
go test -race ./subagent/internal/oneshot ./subagent/internal/execution
```

## 7. S4：Continuable

### 目标

把 durable child 的 fresh create、cold resume、消息、report 和 settlement 放入一个 Continuable implementation，同时复用 common Execution。

### 变更

1. 建立 per-child slot，仅串行化 materialization 和 current Execution 切换。
2. fresh create 调用 SeedBuilder；cold resume 从 Persistence、Header.SeedLength 和 descriptor 恢复。
3. create/resume 都通过 `agent.Constructor`，并传入 exact RuntimeParent。
4. Send 在同一 slot 内决定复用 current Execution 或 cold resume。
5. Interrupt 取消当前 turn 并保留 pending Inbox。
6. Report 校验 exact child 和 live direct parent。
7. settlement watcher 使用 Agent idle、Inbox pending 和 RuntimeDescendants 条件。
8. final flush failure 转交 diagnostics，不覆盖已完成 terminal outcome。
9. external Agent disposal 使用 `StopExternal`，不嵌套 Dispose exact Handle。

### 删除

- `internal/continuation`；
- Activation、ResidentChild、Manager 和第二套 residency registry；
- selected child、children 或 descendants drain API；
- Quiesce、nested Scope Dispose 或 Plugin Runtime 特例。

### Gate

```text
go test ./subagent/internal/continuable ./subagent -run 'Test(Continuable|SendMessage|InterruptAgent|Report|Settlement)'
go test -race ./subagent/internal/continuable ./subagent
```

## 8. S5：统一 Service

### 目标

提供稳定的 Subagent application owner，并用窄 capability view 服务不同 Consumer。

### 变更

1. `internal/subagents.Service` 在 `Open` 时接收完整 implementations、messenger、reporter 和 registries。
2. Start 只按 command mode 选择 implementation。
3. Interrupt 的 common lookup 与 authorization 放在统一 Service。
4. Send/Report 委派给 Continuable behavior，不把 mode 枚举暴露给 public Service。
5. Service 用单字段 admission state 关闭准入并 join active calls。
6. Close 逆序请求 implementations 收敛 current Executions。

### Gate

```text
go test ./subagent/internal/subagents ./subagent
go test -race ./subagent/internal/subagents
```

测试 double 应实现同一个私有 implementation interface，不以 callback 模拟生产接口。

## 9. S6：ChildDirectory 与 Projection

### 目标

保留 durable child 查询和客户端可见 projection，同时消除 Catalog 名称与投影内部协议泄漏。

### 变更

1. `ChildDirectory` 合并 live/persisted Session，并保持 live-preferred、稳定 sibling order、bounded cold reads 和 per-candidate diagnostics。
2. 查询只调用 Session LiveStore、Persistence 和 Projection Registry，不创建或恢复 Agent。
3. `internal/projection` 保留 identity/timing 两个纯 Unit。
4. Runtime 只通过 projection 模块提供的完整 Units 集合注册，不选择具体 Unit。
5. ChildDirectory 通过 projection 模块的 identity reader 消费结果，不直接知道 key 或 JSON codec。
6. timing projection 继续通过 Session API 和 live projection frame 对外发布。

### 删除

- Catalog 名称和旧 `internal/catalog`；
- ChildDirectory 对 projection key、concrete Unit 和 raw decode 的直接依赖；
- runtime 对 Unit 类型和顺序的重复枚举。

### Gate

```text
go test ./subagent/internal/childdirectory ./subagent/internal/projection ./subagent/tools/control
go test -race ./subagent/internal/childdirectory ./subagent/internal/projection
go test -tags contract ./subagent/internal/childdirectory ./subagent/internal/projection
```

## 10. S7：Child policy 与 Extensions

### 目标

让共享 child-local policy 与 Continuable 专属扩展分别拥有清晰对象，不建立通用 childscope owner。

### 变更

1. `internal/childpolicy` 只提供 approval、persona 和 Tool restriction Plugin adapter。
2. OneShot 业务包拥有 `EnvironmentBuilder` 消费端口；`subagent/plugin` 在该端口后组合 descriptor、structured output 与 child policy。
3. Continuable 业务包拥有区分创建/恢复的 `EnvironmentBuilder` 消费端口；`subagent/plugin` 在该端口后组合 child policy 与 Extension Provisioner。
4. `internal/extension.Registry` 负责有序 registration、安装、Commit 复核和 exact uninstall。
5. report Plugin 注册 `ContinuableExtension`；child Scope 中安装实际 report Tool/Prompt Plugin。

### 删除

- `internal/childscope`；
- ActivationExtension 命名；
- Extension 直接参加 Plugin lifecycle；
- child Plugin 通知另一个 Scope 执行嵌套 Dispose 的路径。

### Gate

```text
go test ./subagent/internal/childpolicy ./subagent/internal/extension ./subagent/tools/report ./subagent
go test -race ./subagent/internal/extension ./subagent/tools/report
```

## 11. S8：Tool 与 Plugin adapter

### 目标

按用例拆分模型 Tool，并把 Tool behavior、Plugin registration 和 Factory config 分层。

### 变更

1. `tools/delegation` 只创建新 child，调用 `Starter`。
2. `tools/control` 只包含 send、interrupt、list 三项已有 child 操作。
3. `tools/report` 只包含 child-to-parent report Tool 和 Continuable Extension adapter。
4. 每个包的 Plugin 只管理 Tool/Prompt/Extension registration。
5. 每个 Factory 只严格解码 owner-defined typed config 并构造未激活 Plugin。

### 删除

- 根级 `tool`、`control`、`report` 混合目录；
- Tool behavior 直接实现 lifecycle；
- Plugin struct 同时承载 schema/execute/registration 的大对象；
- callback test seam 与生产 interface 不一致。

### Gate

```text
go test ./subagent/tools/... ./subagent/spawn/... ./subagent/fork/...
go test -race ./subagent/tools/...
```

## 12. S9：Runtime composition

### 目标

让 Runtime Plugin 成为唯一 composition adapter，不保留业务转发层或 Plugin-aware business interface。

### 变更

1. 构造 stable registries、directory 和 unified Service。
2. Manifest 发布六个窄 Subagent capability。
3. Apply 解析依赖、注册 projections、启用 directory、构造 implementations 并 Open Service。
4. event adapter 分别发布 registration facts、Agent-scoped lifecycle facts和处理 `agent/disposed`。
5. Dispose 关闭业务准入和 current Executions，再释放 directory、extensions、builders 和 projections。
6. 默认 assembly 只连接 statically linked factories 和明确 specs。

### Gate

```text
go test ./subagent/plugin ./subagent/factory ./internal/assembly ./tests/architecture
go test -race ./subagent/plugin ./subagent/factory
```

## 13. S10：文档与最终验证

### 文档

1. 技术方案只描述最终架构。
2. 实施方案只描述顺序和 Gate。
3. 独立进度矩阵记录实现和验证状态。
4. 根包及所有真实 Subagent capability package 更新 `README.zh-CN.md`。
5. 术语文档删除 Activation、Catalog、Provider execution 和旧路径。
6. future parent-bound 文档明确其未实现状态，并以当前 Execution/ChildDirectory/SeedBuilder 术语重建后才能继续设计。
7. 暂不更新 `zh-CN/README.md` 和 `zh-CN/08-implementation-progress.md`。

### 静态审计

```text
rg -n 'internal/(provider|inprocess|continuation|catalog|childscope|identity)' subagent zh-CN/Subagent*.md
rg -n '\b(Activation|Catalog|ContinuableService|OneShotProvider|ContinuableProvider)\b' subagent --glob '*.go' --glob '*.md'
rg -n 'type .*Func|func\(' subagent --glob '*.go'
git diff --check
```

兼容 event/wire token、自然的 stateless callback 和工厂函数必须人工分类，不能仅按文本命中删除。

### 最终 Gate

```text
go fmt ./...
go test ./...
go test -race ./...
go vet ./...
go build ./...
go test -tags contract ./agent ./subagent ./subagent/internal/execution ./subagent/internal/projection ./subagent/internal/childdirectory
git diff --check
```

真实模型测试只在显式提供凭据时运行，且必须保持自跳过和不泄漏 secrets。

## 14. 提交边界

用户明确要求提交时再提交。建议保持：

1. 代码、测试和依赖文件组成一个实现提交；
2. 技术方案、实施方案、进度矩阵和 package README 组成独立文档提交；
3. 不把无关 web build artifacts 或其他用户改动混入 Subagent 提交；
4. 提交说明区分“代码已实现”与“全量 Gate 已通过”。
