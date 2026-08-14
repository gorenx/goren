# 05 复制路线图与验收

状态：Draft

本文把[01 复制范围与兼容基线](./01-porting-scope-and-baseline.md)落实为可连续交付的迁移阶段。当前最短目标不是先完成 Headless 或全部 Harness 能力，而是打通：

```text
Existing TypeScript Client
  -> Connection Host
  -> API Proxy
  -> Session / Agent
```

每个阶段仍以 DeepSeek Harness 的职责 owner 为默认边界，并使用[03 协议与 API 兼容设计](./03-protocol-and-api-compatibility.md)定义的证据分级。

阶段的当前完成度、全部交付/Gate 子目标状态、验证证据和下一步统一记录在[08 实施进度](./08-implementation-progress.md)，本文不维护日期性状态。阶段含多个子目标时，不得只用一个阶段级百分比或汇总状态代替逐项目标状态。

## 1. 实施原则

1. **协议先行**：先冻结 Connection/API wire contract，再实现 Go carrier 和 handler。
2. **纵向可运行**：每个阶段交付可执行路径或下一阶段可直接消费的完整能力，不创建空目录占位。
3. **源职责优先**：保持源 Service Definition / Provider / Consumer、事件 owner、Plugin lifecycle 和 canonical name。
4. **最小兼容面**：只实现当前 TypeScript Client 会话链所需方法；未纳入能力返回规范的 unsupported/unregistered 结果，不伪造成功。
5. **单一身份迁移**：现有 `llm` 等代码迁移到 Harness contract，不长期并行旧/新 API。
6. **先内存后持久**：先用 in-memory Provider 验证状态机，再添加 JSONL/SQLite adapter。
7. **失败路径同等重要**：取消、rollback、repair、权限拒绝、partial stream、重连和 shutdown 都是阶段 gate。
8. **排除项不进入依赖闭包**：Web UI、浏览器客户端实现、DSH SDK 和 Python 不以“暂时不启用”的方式引入。
9. **Deferred 不占位**：Headless、ACP、MCP、Typert 和编排能力只有进入实际范围后才创建包和依赖。
10. **配置无脚本**：所有配置严格解码为 owner-defined Go 类型；`!!js` 和替代表达式语言属于 Excluded。
10. **证据分级**：Implemented、Go Verified、Contract Verified、Environment Verified 分别记录。

## 2. 源职责到 Go 的映射规则

映射单位是职责，不是 npm 发布单元或单个文件：

| 源职责形态 | Go 映射 |
| --- | --- |
| Service Definition package | canonical 能力的公共 Go package，拥有最小 interface、类型和 key |
| Service Provider package | 能力 owner 下的 provider/adapter package，只实现 Definition |
| Consumer/Tool package | 依赖 Definition 的独立 Plugin，不反向导入 Provider |
| Core Plugin | 保持独立 owner，仅将 Cordis 注册机制替换为 Go Plugin API |
| Cordis declaration merging | owner-defined typed Service/Event key |
| `packages/client/connection` Host half | Go `connection` contract 与 `internal/connection` carrier |
| `packages/host/apiproxy` | Go `apiproxy` method contract 与 core-facing handler |
| npm-only barrel/build package | 若无运行时职责则不建立 Go package |
| 平台实现混合包 | 公共 Definition 不变，按 OS 拆 build-tagged Provider |
| Web/browser Client/SDK adapter | 删除，不把其职责吸收到 Core |

偏离源职责必须在代码提交或 ADR 中记录源符号、原 owner、Go 约束、新 owner、调用链、生命周期和兼容影响。

## 3. 阶段 0：基线与 Connection Contract Freeze

### 交付

- 固定源 commit、版本、许可证与本地参考路径；
- 建立 included/excluded surface manifest；
- 提取四类 RPC message、`RpcResult`、`RpcReceipt`、Mux/Host frame、HTTP/WS path 和 stable errors；
- 提取首期 `host.describe`、`session.*`、approval/question 与 `/api/respond` 的 payload/result；
- 建立 `contracts/deepseek-harness/<source-sha>/manifest.json` 和可重复的 TypeScript fixture 生成入口；
- 单列 full Web client 启动所需但首期未纳入的 Workspace、Settings、Goals 等能力；
- 记录当前 Go `llm` API 与目标 contract 的迁移差异；
- 确定 NOTICE/provenance 形式。

### Gate

- fixture 在干净源 checkout 上可重复生成且无手工修改；
- 每个 surface 能映射到源路径、符号和 owner；
- path、status、header、message discriminant、缺失/`null` 和错误码均有正负 fixture；
- 排除项未被 extractor 当作目标；
- “可进行 Agent 会话”与“完整原 Web 产品可运行”是两个独立 capability 状态；
- 未实现能力只标记 Planned 或 Deferred。

## 4. 阶段 1：Connection Host Carrier

### 源职责

- `packages/client/connection` 的 Host half；
- `packages/host/apiproxy` 的 RPC envelope、pending response 和 transport-facing contract；
- 与 Host/Origin trust fence、body budget 和 disconnect cancellation 相关的服务端行为。

### Go 交付

- `connection`：四类 message、`RpcResult`、`RpcReceipt`、窄 `RPCRequest` 和协议常量；
- `internal/connection`：Echo v5 + `coder/websocket` Host carrier；
- `apiproxy`：typed method registry 和一个 deterministic test handler；
- `apiproxy`：Mux/Host frame union 与 transport-neutral event streams；
- `POST /api/<method>` 与 `POST /api/respond`；
- `/api/events.mux` 与 `/api/events.host` 两条 server-to-client WebSocket；
- 两条独立 stream 的 socket-close cancellation、pending request correlation 与 bounded shutdown；
- Host allowlist、Origin/Host 一致、cross-site 拒绝与 privileged method loopback policy。

### Gate

- TypeScript Connection contract test 能对 Go server 完成 `host.describe` test handler 调用；
- method/path 不一致、错误 content type、非法 JSON、非法 envelope、未知方法和 handler failure 与源 status/envelope 一致；
- Echo 默认 binder/error/404/405/recovery 行为均被协议 adapter 接管，不改变源 status、header 或 JSON envelope；
- `rpcId` 不重写；`/api/respond` 的 accepted、not-pending、bad-response 均覆盖；
- WebSocket 客户端上行消息触发 policy close；
- 每条 socket 断开取消对应的 Go event source，Host teardown 等待全部 source cleanup；
- 现有 TypeScript Connection 观察任一 socket 结束后废弃 client-owned generation，重连不会消费旧 socket frame；
- HTTP 断开取消 owned handler，且不会关闭共享 Runtime；
- `echo.Context` 不越过 `internal/connection`，也不被异步 Agent/stream goroutine 持有；
- body budget、慢客户端、stream failure、shutdown 和 goroutine 泄漏有测试；
- trust fence 有 loopback、trusted host、Origin mismatch 和 cross-site 测试。

本阶段不实现 Agent 业务，也不实现 browser `ConnectionController`、SSE 网络 fallback 或 Typert。

## 5. 阶段 2：Plugin Runtime 与 Server Assembly

### 源职责

- Cordis Runtime 的 Service、Event、Effect、Scope、isolate 与 settlement 语义；
- `boot`、`preset`、`bundle/base` 中当前服务端组合实际需要的职责；
- Server composition、配置校验与 lifecycle diagnostics。

### Go 交付

- `plugin` 公共 package：Plugin、Factory、Manifest、Scope、Disposer 和 typed keys；
- Service dependency graph、typed Event modes、effect rollback、replacement 和 shutdown；
- Factory Catalog 与静态 composition root；
- 首期 Go typed config 和 strict validation；
- 只含 Connection Server 纵向路径的 assembly；
- lifecycle diagnostics 和 leak-oriented tests。

### Gate

- Service 缺失、重复提供、启动失败、卸载和替换均有测试；
- `emit`、`parallel`、`serial`、`bail`、`waterfall` 的顺序与错误语义有 fixture；
- Plugin 启动失败后没有 Registry contribution、goroutine、listener 或临时资源残留；
- Connection listener 与 API handlers 都由 Plugin effect 拥有并可撤销；
- 排除和 Deferred rows 不进入 Catalog 或依赖闭包；
- `!!js`、未知字段、错误类型和无效组合明确报错，不静默求值或忽略。

源 Cordis YAML/Profile 动态配置兼容属于 Excluded；迁移时必须显式转换为[04 Go 技术架构决策](./04-go-technology-decisions.md)定义的 typed config。

## 6. 阶段 3：Session/Agent 会话纵向切片

### 源职责

- `core/session`
- `core/system-prompt`
- `core/tools`
- `core/agent`
- `core/agent-loop`
- `core/scope`
- `core/agent-default-model`
- `core/agent-tool-presentation`
- `packages/host/apiproxy` 中首期 Session/Host handlers

### Go 交付

1. in-memory append-only Session；
2. System Prompt section registry 与 snapshot assembly；
3. Tool definition/registry/executor/policy waterfall；
4. Agent registry、inbox、scope 与实时事件；
5. 最小 Agent Loop；
6. fake LLM Adapter 和 deterministic Tool；
7. `host.describe`、首期 `session.*` handler；
8. Mux/Host frame 发布与 approval/question request/response；
9. client cancel、disconnect 和 Agent turn cancellation 的映射。

### Gate

- 真实 TypeScript Connection 可连接 Go server、创建或打开 Session、提交 prompt 并接收最终事件；
- `user/message` 到 `turn/end` 的完整流程可运行；
- 零 step、单 step、多 Tool step、拒绝、取消、模型失败和 Tool failure 有事件 golden；
- approval/question 使用 ServerRequest 与 `/api/respond` 形成双向闭环；
- 每个模型请求可由 Session 日志重建；
- reconnect 能读取 baseline，不能重复提交已 committed event；
- Agent Loop 不导入 Connection、storage driver、LLM vendor、CLI、ACP、MCP 或 filesystem Provider。

这一阶段只承诺“现有 TypeScript Connection 可以完成 Agent 会话”。若原 Web Client 在启动时强制调用尚未纳入的 Workspace、Settings 或其他 API，必须通过 capability matrix 明确记录，不能把整个 Web 产品标记为兼容。

## 7. 阶段 4：LLM Contract 与 DeepSeek Provider

### Go 交付

- Harness-compatible Message、Content、StreamChunk、finish reason、usage、GenerateOptions 和 provider route；
- LLM Adapter Registry 与 Runtime；
- DeepSeek adapter；
- 从现有 `llm` 提取可复用 transport，并迁移调用者；
- retry/error classification、partial stream、usage 与 cancellation；
- fake stream 与录制响应 fixtures。

### 迁移与 Gate

1. 用源 fixture 建立目标类型与编解码；
2. 在现有 `llm` owner 内完成新 Runtime；
3. 迁移 adapter、example 与调用者；
4. 删除旧 `Model`/`APIAdapter` 等重复入口；
5. 运行 AST 命名审计，避免变量与类型/函数仅大小写不同。

TypeScript/Go 双向 Message/StreamChunk fixture、所有 finish reason、partial failure、限流、超时、主动取消和 keyless replay test 必须通过。使用显式凭证的 DeepSeek smoke test 独立记录，不作为默认 CI 前提。

## 8. 阶段 5：Session 持久化与重连恢复

### Go 交付

- JSONL Session Store 的 append/flush/load adapter，以及 Session owner 的 fork/repair use case；
- reconnect 所需的 stream baseline、pending interaction replay 和 queue snapshot；
- SQLite projection/query 仅在纳入的 Client API 确实需要时加入，并统一使用 sqlc 生成 repository-private adapter；
- context transform、compaction、attachment/spill 及 model-visible budget；
- stable identity 与 scope。

### Gate

- append crash、截断尾行、未知 required/ignorable event、开放 turn repair 有测试；
- JSONL adapter 只报告技术读取状态；开放 turn 的 `interrupted` 决策由 Session Recovery 测试证明；
- reconnect 不丢 committed event，不把未提交 live state 冒充 durable fact；
- compaction 前后 current request、Tool 关联和来源 evidence 不丢失；
- 大事件、慢磁盘、锁冲突、磁盘满和取消不会产生伪成功；
- 若使用 SQLite，migration 与 sqlc query 生成可重复，生成后工作树无差异；
- sqlc row/nullable/driver 类型不泄漏到 Session 或领域 contract；
- Projection use case 决定 event-to-mutation 与 transaction intent，SQLite/sqlc adapter 只执行和映射；
- SQLite projection 可从 JSONL 事实流重建。

## 9. 阶段 6：按客户端需求扩展服务端能力

Workspace、Filesystem、Shell、PTY、LSP、Sandbox、Guard、Credentials、Attachment、Spill、Settings、Goals 等按 capability matrix 逐项进入，不按源目录批量复制。

每项能力必须：

- 先确认它由哪个已纳入 API、Agent preset 或 Tool 消费；
- 保留源 Service Definition / Provider / Consumer 的 owner；
- 让 effect-time enforcement 位于 permission/guard/sandbox owner；
- 覆盖 success、failure、cancel、shutdown 和不支持平台；
- 不把 credential 写入日志、Session、错误、fixture 或 telemetry；
- 在进入实现前补充对应技术依赖决策。

不支持的 Sandbox 必须明确失败，不能用无隔离执行冒充 parity。

## 10. 阶段 7：Deferred 能力的独立进入规则

下列能力不属于首期 release gate：

| 能力 | 进入条件 | 独立边界 |
| --- | --- | --- |
| Headless | 明确需要无客户端的一次性 CLI | 只作为 Agent/Session 入站 adapter，不改变 Core |
| ACP | 明确需要 ACP client 互操作 | stdio adapter 映射 Agent/Session，不泄漏 SDK 类型 |
| MCP | 明确需要外部 MCP tools | Tool Provider 管理 connection generation 与撤销 |
| Typert | 纳入的辅助 Client endpoint 在源基线中由 Remote 拥有 | 只实现该 endpoint 所需 descriptor/lookup/context dispatch，复用 Connection carrier |
| Jobs/Subagent/Workflow | 明确需要多任务编排 | 独立资源、预算、递归、取消和 Session owner |

进入任一能力时都要更新 01 范围、03 contract、04 技术决策、capability matrix 和本路线图。空 handler、固定成功响应、未使用 dependency 或空包不算进入实现。

## 11. 阶段 8：Parity Hardening 与发布

### 交付

- included capability matrix；
- 跨语言 replay/differential suite；
- 多平台 CI；
- race、fuzz、故障注入、泄漏和长时稳定性测试；
- dependency/license/NOTICE 清单；
- security threat review；
- 性能与资源预算；
- 安装、升级、Session migration 和故障恢复说明。

### Gate

- 每个 included surface 标明实际达到的 P0/P1/P2/P3；
- Connection 与首期 Agent 会话关键路径达到计划层级；
- Linux/macOS/Windows 支持项分别有测试，未支持项从发布声明移除；
- `go build ./...`、`go test ./...`、`go test -race ./...`、`go vet ./...`、格式和 contract suite 通过；
- 真实 TypeScript Client 与真实 DeepSeek Provider 验收分别记录；
- 依赖闭包和二进制扫描确认排除范围未进入；
- open decision 已解决、延期并限定影响，或从 release scope 移除。

## 12. 测试分层

| 层 | 目的 | 例子 |
| --- | --- | --- |
| Unit | 单一 owner 的状态与错误 | patch、event dispatch、stream parser、Tool policy |
| Lifecycle | ownership 与释放 | Plugin rollback、downlink stream cleanup、process cancellation |
| Contract | P0 wire/API | TS/Go Connection envelope、HTTP/WS、API payload/frame |
| Semantic | P1 状态转换 | turn、retry、repair、compaction、permission |
| API | P2 extension contract | 第三方测试 Plugin 只依赖公共 package |
| Model-visible | P3 输入输出 | request/session replay、system prompt snapshot |
| Platform | OS 差异 | PTY、sandbox、watcher、signals、filesystem |
| Real environment | 外部互操作 | TypeScript Client、DeepSeek；Deferred peer 仅在纳入后测试 |

Golden file 只固定协议稳定字段。时间、随机 ID、绝对路径等非稳定值由 fixture generator 规范化，不能在断言中删除整个对象。

Fuzz 至少覆盖 Connection 四类 message、API union/error、Session Event、LLM StreamChunk、Tool 参数 schema、typed config 严格解码、JSONL 截断/重复/超大记录和增量 Tool call 参数拼接。Typert descriptor 只有进入范围后才加入。

## 13. 能力状态表

| 状态 | 含义 |
| --- | --- |
| Planned | 只有设计或 backlog |
| Implemented | 有完整实现，但尚无要求级别的自动化 |
| Go Verified | 适用 Go tests/race/vet/build 已通过 |
| Contract Verified | 跨语言 P0/P1 fixtures 已通过 |
| Environment Verified | 真实 Provider/协议 peer/平台已验收 |
| Excluded | 明确不实现 |
| Deferred | 后续 release 或尚未进入当前目标，不能计入 parity |

每条记录包含 source package/symbol、Go owner、目标层级、当前状态、测试路径和最后验证日期。

## 14. 完成定义

一个能力只有同时满足以下条件才可声明复制完成：

- 源职责与 included symbols 已列明；
- Go owner、Provider、Consumer 和 composition 完整；
- success、failure、cancel、shutdown 和 recovery 无关键 TODO；
- canonical API/wire/config/session/model-visible 语义达到计划层级；
- 生命周期没有泄漏或幽灵注册；
- 自动化在目标平台通过；
- 真实外部依赖按 release 要求验收；
- 文档、capability matrix、provenance 和 license 已更新；
- 不依赖 Web UI、浏览器客户端实现、DSH SDK 或 Python。

阶段完成不自动代表项目完成。最终声明必须明确兼容的源 commit、API/能力列表、平台、协议层级和未包含项。

## 15. 提交与评审

- 一次实现提交围绕一个 source responsibility 或一条完整纵向路径；
- code、code-facing tests 和 dependency change 属于代码提交；
- design/index/说明性 prose 属于独立文档提交；
- 不把无关命名、目录整理或依赖升级混入复制提交；
- 公共名称或边界偏离必须给出源证据和迁移影响；
- 每次提交列出实际检查，未运行项明确说明；
- dirty worktree 中不属于本任务的文件不得被顺手修改或提交。
