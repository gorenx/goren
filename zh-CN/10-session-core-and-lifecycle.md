# 10 Session Core 与生命周期模块设计

状态：Accepted

本文拥有 `session` 的 Header、Event envelope、内存 append-only log、surface、Store Service Definition、生命周期事件及其上下游交互。Plugin 的通用调度语义见[09 Plugin Runtime 与 Server Assembly 模块设计与实现](./09-plugin-runtime-and-server-assembly.md)，wire projection 见[03 协议与 API 兼容设计](./03-protocol-and-api-compatibility.md)，实施状态与验证证据只见[08 实施进度](./08-implementation-progress.md)。

## 1. 固定源与职责映射

基线固定为 `47f943859bef60e4160492346772ded9b24f765a`，主要源 owner 为：

| 源路径 / symbol | Go owner | 保留职责 |
| --- | --- | --- |
| `packages/core/session/src/types.ts` | `session.Header`、`Event`、typed event key | format version、Header、Event envelope、surface metadata |
| `packages/core/session/src/index.ts` 的 `Session` | `session.Session` | seed 接受、连续 `seq`、快照、append commit |
| 同文件的 `SessionStore` | `session.Store`、`MemoryStore` | prepare/enter/announce、live membership、flush、detach |
| `packages/core/session/src/surface.ts` | `session.SurfaceOperation` 与内部 surface fold | append/replace、provenance、原子转换 |
| Cordis 的 `session/created`、`disposed`、`event`、`flush` | `OnCreated`、`OnDisposed`、`OnEvent`、`OnFlush` | listener ownership、创建 veto、post-commit feed、durability barrier |
| 源 `@deepseek-ai/dsh-session` Plugin | `internal/assembly` 的 Session Factory | 提供 canonical `sessions` Service |

Go 不复制 TypeScript declaration merging、WeakMap attachment、对象冻结或 Typert lookup。对应语义分别由 owner-defined 泛型 key、包内 attachment、入口 JSON snapshot 加返回值深拷贝，以及显式 Go Service interface 实现。没有引入反射型 payload registry。

## 2. 职责与非职责

`session` 拥有：

- `Header.version=0`、Session identity、创建时间和 lineage metadata；
- Event 的 `type/seq/time/data/sourceEventSeqs/surfaceOp/ignorable` 信封；
- seed 验证、`firstLiveSeq` 与必要的 `session/end-seed` marker；
- 单 Session 内连续 `seq` 分配、lossless JSON snapshot 和 append-only history；
- model-visible surface 的 append、replace、provenance 与 replacement generation；
- live membership、创建公告、post-commit 观察、flush barrier 和 disposer。

`session` 不拥有：

- 谁开始 Turn、何时调用 LLM、Tool policy 或 Agent inbox；
- Echo、WebSocket、RPC、Mux/Host frame 或浏览器 projection；
- JSONL/SQLite 文件格式、sqlc 类型、事务、重试或 durability buffer；
- Provider Message/Content/StreamChunk 的定义；
- fork/repair policy、request header fold 和 derived LLM history 的尚未进入能力。

后四项不能通过给 `Session` 增加可选 storage/transport 字段来提前实现。Storage adapter 只能观察已经 committed 的 Event，并在 `session/flush` 中完成自己拥有的 durability 工作。

## 3. 数据契约

### 3.1 Header

持久化 Header 位于事件日志之外，字段保持源拼写：

```json
{
  "version": 0,
  "id": "session-1",
  "createdAt": 1786743546510,
  "cwd": "/workspace",
  "parentSession": "session-parent",
  "seedLength": 12,
  "origin": "subagent",
  "delegationDepth": 1,
  "agentPreset": "default"
}
```

`createdAt` 是 Unix epoch milliseconds 的非负 JavaScript safe integer，不是 RFC 3339 字符串。可选字符串使用指针表达 presence，避免把“缺失”和合法空字符串在 wire 上合并；`cwd` present 时必须是绝对路径，`origin` present 时只能是 `subagent`。

### 3.2 Event envelope

`Event.Data` 在接受边界只保存一次 detached `json.RawMessage`。泛型 `EventKey[D]` / `SurfaceEventKey[D]` 约束 live append 的 payload 类型，运行时私有状态不保存 `reflect.Type`；JSON 标量、数组和对象仍经过一次 lossless scan，拒绝 duplicate key、负零、非有限数字、多 JSON value 和无效 JSON。

```json
{
  "type": "user/message",
  "seq": 12,
  "time": 1786743546510,
  "data": {},
  "sourceEventSeqs": [9, 10, 11],
  "surfaceOp": "append",
  "ignorable": true
}
```

`time` 与 `createdAt` 一样是 epoch milliseconds number。`seq` 从 `0` 开始且等于接受前日志长度。`ignorable` 缺失表示 required；只有丢弃后不影响 reconstruction 的信息事件才可写 `true`。返回的 Event、`Events()`、Header 和 Surface 都是 detached copy；修改调用方 payload 或返回切片不能改写日志。

### 3.3 seed 与 firstLiveSeq

构造 seed 必须从 `seq=0` 连续、每项携带 lossless JSON data，并按同一 surface transition 逐项接受。`firstLiveSeq` 记录本进程构造时 seed 长度。如果非空 seed 的最后一项不是 `session/end-seed`，构造器在 attachment 之前追加 marker；因此该 marker 不进入 live `session/event` feed，重复打开一个已经以 marker 结束的 seed 也不会继续增长日志。

## 4. Surface

只有 `user/message`、`assistant/message` 和 `tool/result` 是 source-compatible surface event。普通 typed key 不能占用这三个名字；对应 key 由 Session/LLM contract owner 导出后，Consumer 只能复制 key 值使用。

两种 `surfaceOp` 保持 wire union：

- `"append"`：把当前 Event `seq` 加入 surface 尾部；
- `{"op":"replace","start":n,"end":m}`：用当前 Event shadow 当前 surface 上从 `start` 到 `end` 的连续节点。

replace 必须在修改前完成全部检查：边界节点存在且顺序正确、`sourceEventSeqs` 唯一且只引用更早 Event、所有 shadowed node 都被引用。`tool/result` replace 还只能改一个当前 result 的 content，call identity、metadata 和其他字段必须保持不变。任何失败都不能增长日志、改变 nodes 或增加 replacement generation。

```text
surface [0, 3, 5]
  + Event(seq=8, replace 3..5, sources=[3,5])
  -> validate without mutation
  -> commit Event 8
  -> surface [0, 8]
  -> replaceGeneration + 1
```

Surface 是从 append-only facts 派生的当前模型视图，不是删除历史。Transcript、审计或 persistence 仍读取完整 Event log。

## 5. Append 事务边界

```text
typed payload
  -> JSON marshal + lossless snapshot
  -> lock one Session
  -> assign seq/time
  -> plan surface transition
  -> reject reentrant publication
  -> capture session/event listener set
  -> append Event + apply surface atomically
  -> unlock Session
  -> publish session/event to captured live attachment
  -> contain observer failure
```

Event 进入内存日志并完成 surface transition 后就是 Session 语义上的 committed；observer error 不回滚 Event，也不能阻止后续 listener 收到同一条 feed。Durable committed 是另外的 checkpoint：调用方通过 `Store.Flush` 等待所有 `session/flush` listener，二者不能混成“写数据库成功才分配 seq”的事务。

`session/event` listener 在原 append 的同步 publication 尚未结束时再次 append 会明确失败，避免事件顺序取决于 callback 嵌套。不同 Session 不共享 append lock。当前同一 Session 的并发 append 在 publication window 内也拒绝，由 Agent loop 维持单 writer；未来若出现真实多 writer 需求，应在 Session owner 增加明确 queue，而不是让 storage adapter 排序。

## 6. Store 生命周期

`StoreService` 使用 canonical `sessions` key。`MemoryStore` 只保存 live membership 和 attachment，不持久化业务数据。

创建拆成三个责任明确的步骤：

```text
Prepare
  -> mint/check id
  -> validate Header and seed
  -> return detached unpublished Session

Enter
  -> re-check same-id collision
  -> install append publication attachment
  -> add live membership
  -> return idempotent detach disposer

Announce
  -> mark announcement begun exactly once
  -> serial session/created dispatch
  -> listener error vetoes publication
```

`Create` 只是把三步放入调用方 Plugin Scope 的一个 effect：先获得 `Enter` disposer，再执行 `Announce`。创建 listener 返回 error 或 panic 时，effect setup 立即调用 disposer；因为 announcement 已经开始，已观察到 created 的 Consumer 会得到配对 disposed。未 announce 的 prepared/entered Session 被撤销时不伪造 disposed。

detach 若发生在 created 或 event callback 内，只设置请求标记；等当前 publication unwind 后再从 Store 删除并发出 disposed，避免 callback 中途让同一生命周期失去 attachment。owner Scope unload 时，Session effect 先 detach，而 stopping Scope 的 listener effect 仍有效，因而 owner 自己仍能观察最终 disposed；listener disposer 随后按 LIFO 注销。

## 7. Event mode 与错误语义

| Session event | Plugin mode | 失败语义 |
| --- | --- | --- |
| `session/created` | serial | 首个 error 停止并 veto；调用方 rollback |
| `session/event` | emit | Event 已 committed；全部 observer error 被汇总并交给 reporter |
| `session/disposed` | emit | membership 已删除；observer error 被包含 |
| `session/flush` | parallel | 全部 listener 启动并等待；结束后返回是否有 listener 参与及聚合 error |

Go 的 `session/created` registration 被 `OnCreated` 包装，Consumer 不直接处理 Plugin `Decision`。`Decision.Bail` 仍只属于通用 serial/bail 决策链；Session 创建 veto 直接用 error，避免把创建成功与否编码成结果零值。

## 8. 上下游依赖与 server composition

```text
Session Factory
  -> MemoryStore
  -> Provide(sessions)

Agent / API use case / persistence Consumer
  -> Require(sessions)
  -> Create, Append, Flush, Get/List

API Proxy
  -> Require(sessions)
  -> host.describe projects len(Store.List())

JSONL or SQLite adapter (later)
  -> OnEvent buffers exact committed Event
  -> OnFlush writes/commits its own records
  -> never assigns seq or rewrites surface
```

默认 composition 按 Connection、API Proxy、System Prompt、Session 声明。Runtime 先让 Connection/API Proxy 等待；System Prompt 无当前依赖而独立激活；Session 发布 `sessions` 后，API Proxy 激活并发布 `apiProxy`，最后 Connection 激活。这证明新增 Provider 没有恢复为文件顺序装配。

## 9. 后续能力进入规则

- Harness-compatible Message/Content contract 确定后，由 `session` 导出三个 core surface event key；不得先绑定当前待迁移的旧 `llm` 形态再做兼容分支。
- Agent instance 出现时，直接使用现有 `Scope.Child`、opaque lineage 与 scoped listener filter；不得建立 Session 私有的第二套 scope registry。
- fork、repair、request header fold 和 derived messages 进入时复用当前 Header/Event/surface owner，不另建“持久化 Session”模型。
- JSONL/SQLite/sqlc adapter 只能依赖 `Store` 和生命周期事件，不得让 driver/sqlc 类型进入本包。
- API Proxy 只做 `session.Event -> apiproxy.SessionEvent` projection；不得让浏览器 frame 成为 Session 的领域类型。
