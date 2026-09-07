# Session 业务状态与 Actor 迁移

状态：源码边界已核对，以下是模块实施设计；已实现 Session Grain 内存提交、持久化及业务关闭；恢复已接入，Registry 授权和生产调用链尚未接通。
跨模块规则归[生命周期方案](./07-lifecycle-implementation.md)和[模块方案](./09-module-implementation.md)。本文件只细化 Session，不修改历史设计。

## 现有实现证据

- session_context.go：sessionContext 组合 eventLog 与 sessionLifecycle，不是独立的第三份日志。
- session_context_commit.go：Commit 进入生命周期串行区；WritePlan 根据队首快照生成事件；commitBatch 原子写入内存，之后逐条发送 Appended 通知。
- event_log.go：eventLog 拥有头信息、连续事件序列、surface 和派生模型消息缓存；不执行持久化。
- memory_store_lifecycle.go：Flush 捕获当前 WriteBarrier，经 publisher.Flush 请求持久化；Release 在 flush 成功后才完成释放。失败不得报告关闭成功。
- agentloop/rla_turn.go：Agent 选择 TurnStarted 等业务事实并请求 Session 提交；Agent 不分配 Session 事件序号。
- agentloop/rla_step.go：Agent 当前持有整个 Session Context；目标消息边界应返回值快照，不把该可变对象传入 Prompt。

因此，Commit 成功表示内存事实及派生视图已提交；Flush 成功表示指定前缀满足存储持久化保证。这两个完成边界必须保留。

## 模块架构与对象职责

| 对象 | 状态和行为 | 不拥有 |
| --- | --- | --- |
| Session 业务对象 | 从现有 eventLog 收敛日志、头信息、surface、消息派生和原子提交规则 | 框架身份、锁、订阅者、存储任务、Agent 对象 |
| Session Grain | 独占业务对象，处理提交/读取/flush/关闭消息，安排持久化任务并结算回复 | Turn/Step 决策、模型调用、工具执行 |
| Session Registry | 创建及关闭授权、Session 目录、身份占用，关闭成功后允许回收 | 日志副本、事件序号、模型消息投影 |
| 存储 adapter | 编解码、文件或数据库 I/O、请求的持久化操作 | 事件含义、提交时点、修复决策、Agent 生命周期 |

不在原 sessionContext 外再包一层带相同生命周期的 Actor。迁移时保留原日志规则，删除被邮箱替代的串行控制、重入 context 标记与 Plugin publisher。现存 memoryStore、sessionLifecycle 的运行状态需要沿其真实调用方迁移后移除，不能把它们整体注入 Grain。

## 接口设计

消息使用具名输入和回复，error 不转换成字符串。以下为目标契约语义，具体 Go 声明随对应实现一起迁移，不创建占位类型。

| 请求 | 输入 | 返回 | 上游契约 |
| --- | --- | --- | --- |
| 初始化 | 原创建身份、SessionID、创建配置或恢复后的验证数据 | 创建身份、当前 WriteBarrier、error | 同一次创建重试不能重复追加 seed；需要 Registry 授权 |
| 提交 | 原创建身份、WritePlan | WriteResult、error | Plan 必须确定、无外部 I/O，不保留 Session 对象；事件含义由用例 owner 决定 |
| 读取 | 原创建身份、要求可见的 WriteBarrier | Snapshot、error | 返回完整值快照；不能用另一个 Session 或尚不存在的前缀冒充成功 |
| 持久化 | 原创建身份、WriteBarrier | 已持久化前缀、error | 成功覆盖请求前缀；请求超时不撤销内存事实 |
| 关闭 | 原创建身份 | 关闭结果、error | Agent 处理完当前业务后提出；封闭新写入，完成 flush 和资源释放后确认 |

Actor 间使用 RequestGrain；需要等待 I/O 的请求使用 DeferResponse。存储任务只接收分离的头信息/事件值，不捕获业务 Session、GrainContext 或可变容器；完成经 PipeToSelf 回邮箱，由 Grain 推进持久化前缀及回复。

业务对象内部沿用 WritePlan、EventDraft、WriteResult、Snapshot 和 WriteBarrier，不引入第二套“Actor 日志 DTO”。存储 adapter 的数据库行、文件句柄不出现在消息中。

## 关键数据结构与不变量

- 事件序列连续且只追加；一次批次任何校验失败都不改变日志、surface 或派生缓存。
- Snapshot 的 Events、Surface、Barrier 对应同一已提交前缀，并与调用方可变字节隔离。
- WriteBarrier 已有 SessionID 和 NextSeq；不再添加同义 sequence 字段。
- Grain 的持久化进度表示已满足 durability 的前缀，不复制事件日志。存储任务持有本次捕获的输入值和框架取消/回复资源。
- 框架稳定身份不等于业务初始化授权。旧创建身份的迟到提交、flush 或关闭不得影响同 SessionID 的新实例。
- 持久化只允许一个未结算写操作；append 与 flush 顺序统一。具体任务合并和并发 flush 回复管理须随 adapter 调用链验证，不提前添加通用 queue/waiters 框架。

## 交互与关闭顺序

```mermaid
sequenceDiagram
    participant A as Agent Grain
    participant S as Session Grain
    participant B as Session 业务对象
    participant IO as 存储任务
    A->>S: 提交 WritePlan
    S->>B: 验证并原子追加
    B-->>S: WriteResult 与分离事件值
    S-->>A: 内存提交完成
    S->>IO: 持久化已提交事件
    IO-->>S: 实际写入结果
    A->>A: 完整用户消息处理结束
    A->>S: 关闭 Session
    S->>S: 停止接纳新写入
    S->>IO: 完成目标前缀并 flush、释放资源
    IO-->>S: 成功或失败
    S-->>A: 真实关闭结果
```

持久化失败不回滚已经可见的内存事实。Flush/关闭失败保留恢复所需状态和身份占用；后续显式重试继续满足原前缀。不能因请求超时、收到取消或 TopicActor 注销确认就停用 Session 并允许重新激活。

Session 不替 Agent 判断“整条用户消息已处理完”。Agent 负责等待模型、工具、交互和必要提交结束，再请求 Session 关闭。Parent/subagent 生命周期依旧由 Agent Registry 管理。

## 实施顺序与验收

1. 收敛日志业务对象：沿现有测试保留批次原子性、seed、surface 与重建语义；不改外部持久化格式。
2. 核对 persistence 的真实 Appended/Flush/Release 消费链，把同步事件副作用替换为具名存储操作契约。
3. 实现 Session Grain 的提交、快照及单写持久化调度，再实现 flush/关闭延迟回复；删除被替代的生命周期同步。
4. 迁移 Agent、查询和投影调用方，移除可变 Session Context 传递及旧 Plugin 装配。
5. 真实 Actor 与存储 adapter 验证：批次失败不提交；提交与 flush 分界；并发发送的实际接纳顺序；I/O 失败及重试；取消不谎报退出；旧身份迟到消息；JSONL/SQLite 恢复和释放失败；Agent 工作结束前不关闭 Session。

仅通过日志单测、或用内存 fake 返回 flush 成功，都不足以证明生产关闭链路完成。

## 本轮实施记录

log_commit.go 已承接 WritePlan 的快照构建、完整校验、取消检查、原子提交和 WriteResult 生成。sessionContext 只负责现存接纳规则以及提交后的通知，业务提交不再读取生命周期 publisher。通知 panic 返回独立的发布错误，保留已经提交的事件和 barrier；接纳结束用 defer 保证后续请求不被卡住。

本步没有新增 Actor 包装或第二套日志，也未新增锁。eventLog 原有读取锁与 sessionLifecycle 尚由旧并发调用路径使用，生产 Actor 独占访问及旧调用方迁移后再移除，不能宣称已经完成无锁 Session。go test -race -count=1 ./session/... 通过，覆盖原子性、取消、恢复、持久化和新增的通知失败不回滚用例；这些仍是现有 Session 实现的验证，不证明目标 Actor flush/关闭链路已接通。

### 日志对象与内存 Grain 入口

现有 eventLog 直接更名并导出为 Log，不复制状态或算法；NewLog 构造该业务对象，Commit、Snapshot、Barrier 提供 Actor 所需的真实操作。旧 New/sessionContext 在其调用方迁移前仍使用同一个 Log 实现，不增加兼容别名。Log 的现存读取锁暂用于旧 Context 的并发读者；Grain 自身不引入锁、队列或自建运行阶段。

session/actor.Grain 按 SessionID 作为框架 Name，Initialize 从 Self.Name 取得身份，消息仅携带原创建 ID 与创建配置；重复同创建 ID 不再创建日志，其他创建 ID 不能覆盖已有日志。Commit 和 Read 均核对原创建 ID；未初始化或身份不符明确失败。Read 的可选最低 WriteBarrier 必须属于本 Session 且不超过已提交前缀。返回快照可以比最低前缀新，但内部必须一致，不伪造历史精确版本。

本步只实现内存提交与读取，立即回复，不实现假的 Flush/Close 成功。外部测试可 AskGrain 驱动；Actor 调用方仍用 RequestGrain。没有 commit 去重承诺，超时不能盲目重发 WritePlan。创建 ID 校验只能隔离当前 Grain 内的旧命令；完整 Registry 授权、强制停用后的初始化防护和持久化恢复仍待接入，不能把本步称为完整生命周期。

内存入口实施验证：真实请求 Actor 通过 RequestGrain 调用 Session Grain，覆盖初始化前拒绝、框架名称决定 SessionID、重复初始化不清空、不同创建身份拒绝、最低 barrier 校验、原子批次失败与读写结果字节隔离。go test -race -count=1 ./session/... 通过；没有据此声明强制重激活安全或持久化关闭已完成。

### 持久化任务与 flush 实施契约

Session Grain 的 Initialize 必须由组合根/创建协调者提供具名 Storage 端口（Load、AppendBatch），现有 Backend 可直接满足，不再注入 SessionLogStore Plugin。该端口仅用于本机初始化，不能进入用户配置、公开协议或 AgentCreating 事件。共享数据库 adapter 的 Close 由组合根负责，单 Session 不关闭整个数据库。

Commit 仍立即确认内存提交，然后由 Grain 为待持久化前缀启动至多一个 PipeToSelf 任务。任务仅持有分离 Header/Snapshot、存储接口和取消 context；任务完成回邮箱后推进 durable 前缀，后续提交合并到下一次批次，不复制 liveWriter 的 timer、mutex 或 pending 事件队列。

Flush 指定原创建 ID 与 WriteBarrier，用 DeferResponse 保留框架回复，直到持久化前缀覆盖要求。多个 flush 仅记录各自要求和框架回复，不自行调度存储队列。外部请求取消不取消共享写任务；强制停用取消任务并返回不可用，不声明正常排空。写错误不回滚内存，flush 返回原 error；显式重试前从 Backend.Load 核对已存在的相同前缀，避免“实际写入但回复失败”后重复追加。不同头信息、非匹配前缀或 torn-tail 明确拒绝，恢复修复仍交由恢复用例，不在 adapter 中猜测业务。

现存 SQLite AppendBatch 在事务提交成功后返回，无独立 Sync 接口；本步骤的 Flushed 表示该 Backend 原有耐久性保证。未产生事件的空日志沿用现有惰性 materialize 行为，不为 flush 额外创建空 Session 记录。

持久化实施验证：Session Grain 已接 Storage.Load/AppendBatch 与 PipeToSelf，真实 SQLite 阻塞写入测试确认邮箱继续服务 Read/Commit、Flush 等待目标前缀；不确定写入结果测试确认 Load 核对后不会二次 AppendBatch。尚无业务 Close、恢复授权或生产调用方接入。

### Session 业务关闭实施契约

Close 携带原创建 ID。首次有效请求捕获当前 WriteBarrier 并封闭 Commit；Initialize 也不能重新打开该日志。已提交前缀全部持久化、且实际存储任务完成回邮箱后才返回 Closed。关闭不取消原写任务，不提前释放其预算或存储引用。

Grain 只保留一个 shutdown 业务操作（固定 barrier 与框架延迟回复）。没有独立的 active/closing/closed 标志组合：存在 shutdown 就表示写接纳永久封闭；持久化前缀覆盖目标且无在途写操作才表示该关闭已完成。重复关闭共享目标，可收到各自框架回复；写失败返回 error 并保留 shutdown，重试 Close 继续核对/写入原前缀。成功后释放 Grain 对共享 Storage 的引用，不调用共享 adapter.Close。

Read 可继续查询关闭后的不可变内存事实；合法 Flush 若其前缀已持久化直接成功。Registry 后续负责在业务关闭成功后停用 Grain 并管理重新激活授权。本步不允许仅凭框架停用替代该协议。

业务关闭实施验证：真实 RequestGrain 与 SQLite 测试确认延迟关闭、并发重复关闭、错误后保持写入封闭、显式重试及共享 Backend 保持可用。Session Registry 的创建授权和停用后身份防护仍未实现，不能据此宣布完整生命周期完成。

### 恢复入口与既有修复规则

Resume 使用具名 RecoveryStorage（Storage 加 Revision、CommitRepair）读取原 SessionID。恢复算法复用 persistence 的已知事件、surface、请求头与中断工具结果规则；独立的 Recovery 值携带创建配置和请求 adapter 执行的 LogRepair。修复决策留在业务层，adapter 只执行事务。

核对源码后确认：旧 preparedSource 会补齐中断 Turn/Step，并用修复后事件作为 seed 构造本次 Session；必要的 end-seed 标记由现有日志构造产生。目标保留该行为，不把恢复解释为禁止追加任何标记。

Resume 通过 PipeToSelf 执行读取/校验/版本核对/必要修复及重新读取。修复成功后必须重读其 revision 和完整日志，不复用可能已过时的输入。任务构造独立 Log，完成后才将唯一所有权转给 Grain；任务不修改已激活日志。读取、校验或修复失败不安装半成品，允许同创建请求重试。未完成恢复期间拒绝第二次初始化/恢复；关闭协调者需等待原恢复结果后按创建身份关闭，不将未初始化错误当作关闭成功。该协调流程仍由后续 Registry 实现。

恢复后的 durable 前缀等于实际存储事件数，新产生的 end-seed 经现有后台持久化路径追加。恢复绝不重新执行历史工具效果；未知结果使用既有 TOOL_OUTCOME_UNKNOWN 等事件表达。完整旧身份授权仍未由 Resume 的本地字段检查解决。

恢复实施验证：persistence.Recover 已同时服务原 preparedSource 和新 Resume 任务；真实 SQLite 测试确认修复结果与既有 seed 生命周期一致，修复失败不安装日志并可重试。go test -race -count=1 ./session/... 通过。
