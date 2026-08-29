# Bound Subagent 测试跟踪

> 状态：完成。最后复验：2026-08-28。本文记录Bound测试范围、纵向用例、问题根因、最终方案与验证结果；不替代[需求](./parent-bound-requirements.zh-CN.md)、[技术设计](./parent-bound-design.zh-CN.md)或[投递设计](./session-event-delivery.zh-CN.md)。

## 1. 测试目标和边界

本轮验证以下完整行为链：

1. 用户Session启动后，根据全局Definition建立多个直接属于该Session的Bound Binding；
2. Bound child创建后保持idle，parent完整turn通过TurnRelay和公开`bound.Inbox`进入child；
3. direct user输入、parent可见回复、隐藏block和report-only turn按投递设计处理；
4. disabled、Plugin重新挂载以及receipt已提交而cursor Flush失败时能够恢复；
5. 同一parent下不同Bound独立运行；
6. parent关闭或Runtime shutdown时，阻塞中的child执行被取消并完整释放；
7. Definition replacement与下一条interaction不会跨epoch并发；
8. 默认装配使用真实SQLite Session/Definition Store时，进程级重建能恢复同一child与cursor。

纵向测试使用真实Plugin Runtime、AgentLoop、Session持久化、SQLite Definition Store、Bound Service和TurnRelay。只有LLM transport替换成可控制响应、阻塞和调用次数的测试adapter；因此这些测试验证的是公开能力与真实调用链，而不是直接调用Bound私有方法。

## 2. 纵向测试矩阵

| ID | 行为 | 测试 | 当前证据 |
| --- | --- | --- | --- |
| BND-I01 | parent closed turn投递给child，保留direct user和visible assistant，过滤reasoning | `TestBoundRelaysCompletedParentTurnToChild` | 通过 |
| BND-I02 | 同一parent的多个Bound分别接收同一turn，不共享worker或cursor | `TestMultipleBoundsReceiveParentTurnIndependently` | 通过 |
| BND-I03 | child report引发的report-only parent turn不回送child | `TestBoundReportOnlyParentTurnDoesNotEchoToChild` | 通过 |
| BND-R01 | disabled期间保留pending turn，enable后原Input只接纳一次 | `TestBoundDisabledTurnIsDeliveredAfterDefinitionIsEnabled` | 通过 |
| BND-R02 | TurnRelay卸载期间缺失wakeup，重新挂载后从durable log追赶 | `TestBoundTurnRelayCatchesUpAfterPluginReload` | 通过 |
| BND-R03 | child receipt durable而parent cursor Flush失败，重试只补cursor | `TestBoundRelayReusesReceiptWhenCursorFlushRetries` | 通过 |
| BND-L01 | parent关闭取消阻塞child，并注销parent/child Agent与Session | `TestBoundChildExecutionStopsWhenParentCloses` | 通过 |
| BND-L02 | Runtime shutdown取消运行中的Bound child并释放全部Agent与Session | `TestRuntimeShutdownCancelsRunningBoundChild` | 通过 |
| BND-C01 | parent maintenance阻塞不阻止Definition binding和child materialization | `TestBoundBindingDoesNotWaitForParentMaintenance` | 通过 |
| BND-C02 | replacement等待旧epoch收敛，下一条interaction仅进入新revision epoch | `TestBoundReplacementSerializesRunningEpochAndNextInteraction` | 通过 |
| BND-E01 | 默认装配、HTTP Definition API与SQLite全重启恢复同一Bound child并继续投递 | `TestDefaultCompositionRestoresBoundDeliveryAcrossSQLiteRestart` | 通过 |

测试分别位于：

- [`bound_interaction_integration_test.go`](../bound_interaction_integration_test.go)
- [`bound_recovery_integration_test.go`](../bound_recovery_integration_test.go)
- [`bound_lifecycle_integration_test.go`](../bound_lifecycle_integration_test.go)
- [`bound_concurrency_integration_test.go`](../bound_concurrency_integration_test.go)
- [`bound_integration_fixture_test.go`](../bound_integration_fixture_test.go)
- [`bound_restart_e2e_test.go`](../../internal/assembly/bound_restart_e2e_test.go)

## 3. 需求验收映射

本表逐项对应[Bound需求第9节](./parent-bound-requirements.zh-CN.md#9-迁移和验收)。纵向测试验证公开调用链，owner单元测试验证难以稳定注入到完整运行时的并发、不变量和错误分支；两者不能互相替代。

| 需求项 | 直接证据 | 结论 |
| --- | --- | --- |
| 1. Definition跨用户Session独立建child；同一Session多Definition唯一且并行 | `TestOneDefinitionBindsIndependentlyToMultipleUserSessions`、`TestSessionStartedBindsAndMaterializesDefinitionsConcurrently`、BND-I02 | 已覆盖 |
| 2. child Session不递归创建Bound | `TestBoundChildSessionDoesNotRecursivelyCreateBindings` | 已覆盖 |
| 3. frozen prefix；创建无Inbox、turn或LLM | `TestBindingFreezesCompletedPrefixAndCreatesIdleChild`、BND-I01在parent输入前断言模型请求数为零 | 已覆盖 |
| 4. receipt durable后推进cursor；crash window不重复acceptance | `TestSourceCursorDoesNotExposeInputBeforeSourceFlush`、`TestInboxDeliveryIsIdempotentByInputIdentity`、BND-R03 | 已覆盖 |
| 5. disable/enable backlog；replace复用child Session并发布新epoch | `TestDefinitionReplacementReusesSessionAndRejectsDisabledInput`、BND-R01 | 已覆盖 |
| 6. Binding走Session FIFO且不调用maintenance；失败pending可追赶 | `TestReconcileUsesSessionFIFOWithoutParentMaintenance`、`TestMaterializationFailureIsContainedAndReported` | 已覆盖 |
| 7. Plugin关闭不遗留activation/reconcile worker | `TestCloseCancelsBlockedBoundActivation`；BND-L01补充验证运行中child关闭 | 已覆盖 |
| 8. v1事件被明确拒绝 | `TestBoundProjectionRejectsRemovedEventVersion` | 已覆盖 |
| 9. Bound核心不依赖`session.EventAppended`；其他输入源可直调Inbox | `TestManifestConsumesInboxAndOwnsSessionObservation`确认观察属于TurnRelay；`TestInboxDeliveryIsIdempotentByInputIdentity`绕过TurnRelay直接调用Inbox | 已覆盖 |

## 4. 测试发现的问题

### BND-D01：Service后台任务继承Plugin回调context

- **触发与现象**：通过真实Plugin Runtime启动Bound并首次materialize child时，Runtime返回`Plugin callbacks cannot mutate Runtime topology`。
- **根因**：`subagent.Plugin.Apply`传入的context携带Runtime的topology mutation标记。旧实现把它传给registry和scheduler；`context.WithoutCancel`只移除取消传播，不删除context value，因此异步child构造仍被识别为Plugin回调内的拓扑变更。
- **影响**：正常组合根下的Bound child无法创建；直接调用构造器且使用普通context的单元测试无法暴露该问题。
- **排除方案**：不绕过Runtime guard，不识别或删除Runtime私有context key，也不只给失败构造增加重试；这些做法都保留错误的生命周期所有权。
- **最终方案**：registry与scheduler分别创建由Bound Service拥有的独立lifecycle context，并由`Service.Close`显式取消和等待。`Bound.New`的调用context只用于同步初始化、Definition Store访问和失败回滚。
- **修复位置**：`internal/bound/registry.go`、`internal/bound/reconcile_scheduler.go`和`internal/bound/service.go`。
- **回归证据**：BND-I01通过公开Definition服务、真实Plugin Runtime和Agent构造链完成首次child materialization；全部纵向用例都经过同一构造路径。

### BND-D02：parent结构关闭与child idle形成循环等待

- **触发与现象**：child LLM请求仍阻塞时关闭parent Agent，`Dispose`不能返回。
- **根因**：Agent lifecycle发布`agent.Disposed`并等待observer；observer以`StopExternal`调用`StopAndWait`；旧Bound terminator没有取消child便等待idle，而child的结构teardown要在observer返回后才继续，形成循环等待。
- **影响**：parent关闭可能永久阻塞，child execution、Session和相关goroutine无法释放。
- **排除方案**：不通过测试超时、跳过`WhenIdle`或在observer内抢先Dispose结构Handle掩盖问题；前两者破坏收敛保证，后者侵犯Agent lifecycle所有权。
- **最终方案**：除`StopNormal`外，Bound resident epoch在等待idle前都取消child。`StopExternal`完成execution收敛但不重复Dispose Handle，结构关闭仍由Agent lifecycle完成。
- **修复位置**：`internal/bound/resident_epoch.go`。
- **回归证据**：BND-L01在真实阻塞请求下验证parent关闭、child取消以及Agent和Session注销。

### BND-D03：空的selected Extension被折叠成common模式

- **触发与现象**：没有配置named Extension的Bound child安装两次common report Extension，随后因重复注册`tool:report` prompt section而materialization失败。
- **根因**：Extension Provisioner用`nil selected`表达common模式、用non-nil slice表达selected模式；旧snapshot写法把空slice重新变成nil，丢失“明确选择零个named Extension”的业务语义。
- **影响**：所有未选择named Extension且同时安装common Extension的child都可能重复安装，冲突不局限于report工具。
- **排除方案**：不在Bound materializer中特判report，也不在prompt/tool registry静默去重；这会隐藏Extension选择契约错误，并改变其他owner的冲突语义。
- **最终方案**：`NewSelectedProvisioner`保留空但non-nil的选择快照；common和selected Provisioner继续是两个明确入口。
- **修复位置**：`internal/extension/provisioner.go`。
- **回归证据**：`TestEmptySelectedExtensionsDoNotInstallCommon`固定选择语义，全部Bound纵向用例验证common Extension只安装一次。

### BND-D04：一次性delegation seed被建模成resident Plugin

- **触发与现象**：默认装配通过HTTP创建Definition后，Bound child materialization失败并报告`delegated child Agent is unavailable`；简化测试装配没有启用approval，因此未暴露此路径。
- **根因**：旧`delegationPolicy`被作为child-scoped Plugin挂载，并在`Apply`期间通过Plugin服务解析`agent.Agent`。delegation approval实际是写入尚未发布child Session的一次性构造行为，不是resident Plugin资源；构造Scope已经直接拥有准确Agent，不应绕到服务解析。
- **影响**：默认approval配置下，fresh Bound、OneShot与Continuable child都可能无法创建；cold resume不受重复seed影响。
- **排除方案**：不从默认装配移除approval，不修改Plugin可见性或挂载顺序，也不增加materialization重试；这些方案没有修复职责错误。
- **最终方案**：`childpolicy.DelegationSeed`作为一次性`agent.Provisioner`，直接使用`agent.Scope.Agent().SessionValue()`写入durable override且不持有Scope资源；三个模式owner只在fresh构造时组合它，resident policy继续由`childpolicy.Plugins`提供。
- **修复位置**：`internal/childpolicy/approval.go`、`internal/oneshot/provisioner.go`、`internal/continuable/materializer.go`和`internal/bound/materializer.go`。
- **回归证据**：`TestDelegationSeedUsesUnpublishedScopeAgent`固定owner边界；BND-E01在启用默认approval的完整组合根中验证fresh构造和重启resume。

## 5. 验证记录

以下结果来自2026-08-28的当前工作树复验：

| 验证 | 命令 | 状态 |
| --- | --- | --- |
| Bound纵向用例 | `go test ./subagent -run '^TestBound' -count=10 -timeout=60s` | 通过 |
| Bound race重复用例 | `go test -race ./subagent -run '^TestBound' -count=3 -timeout=90s` | 通过 |
| 新增并发与shutdown用例 | `go test ./subagent -run '<新增Bound用例>' -count=20 -timeout=120s` | 通过 |
| SQLite全重启用例 | `go test ./internal/assembly -run '^TestDefaultCompositionRestoresBoundDeliveryAcrossSQLiteRestart$' -count=10 -timeout=90s` | 通过 |
| 新增用例race重复运行 | `go test -race ./subagent -run '<新增Bound用例>' -count=5 -timeout=120s`、`go test -race ./internal/assembly -run '^TestDefaultCompositionRestoresBoundDeliveryAcrossSQLiteRestart$' -count=3 -timeout=90s` | 通过 |
| 全量测试 | `go test ./...` | 通过 |
| 全量race测试 | `go test -race ./...` | 通过 |
| 静态检查 | `go vet ./...` | 通过 |
| 全量构建 | `go build ./...` | 通过 |
| 格式与空白 | `go fmt ./...`、`git diff --check` | 通过 |
| 文档链接 | 本轮变更Markdown的相对文件链接检查 | 通过 |

本文件只记录实际执行结果。窄范围测试不能替代全量或race验证，测试通过也不能反向证明未被用例覆盖的设计承诺。
