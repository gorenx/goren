# 01 复制范围与兼容基线

状态：Draft
基线核对日期：2026-08-14

本文拥有 Goren 复制 DeepSeek Harness 的目标、需求、纳入范围、排除范围、兼容基线和全局不变量。首要目标是 TypeScript 客户端与 Go Agent 服务端通信协议兼容。运行时结构见[02 Go 运行时架构与插件模型](./02-runtime-architecture-and-plugin-model.md)，具体协议见[03 协议与 API 兼容设计](./03-protocol-and-api-compatibility.md)，实施阶段见[05 复制路线图与验收](./05-porting-roadmap-and-acceptance.md)。

## 1. 系统定位

Goren 的当前主线不是设计一个新的 Agent 产品，也不是按 TypeScript 文件逐行翻译。目标是在 Go 中重建 DeepSeek Harness Agent 服务端，使现有 TypeScript 客户端可以按原 Connection/API 协议建立连接、提交 prompt、接收事件、处理交互并取消请求。Headless、ACP 与 MCP 可以在未来复用同一核心，但不属于当前兼容目标。

源代码权威固定为：

- 仓库：[`deepseek-ai/deepseek-harness`](https://github.com/deepseek-ai/deepseek-harness)
- commit：[`47f943859bef60e4160492346772ded9b24f765a`](https://github.com/deepseek-ai/deepseek-harness/tree/47f943859bef60e4160492346772ded9b24f765a)
- 源版本：`0.1.0-rc.5`
- 本地只读参考：`../deepseek-harness`

源仓库后续提交不会自动成为需求。升级基线必须先生成差异清单，区分协议变化、行为修复、TypeScript 专属机制和排除范围，再修改本文的基线。

## 2. 术语

| 术语 | 本项目含义 |
| --- | --- |
| 源实现 | 固定 commit 上的 DeepSeek Harness TypeScript 代码、测试和生成目录 |
| 复刻 | 用 Go 重新实现相同行为，不要求使用相同语言机制 |
| included surface | 本文明确纳入的 API、事件、配置、持久化和协议面 |
| wire parity | JSON 字段、缺省/缺失语义、discriminant、错误、顺序和取消语义一致 |
| semantic parity | 调用者、插件、模型或持久化消费者观察到的状态转换和结果一致 |
| source parity | Go 包职责与源 Service Definition / Provider / Consumer 的所有权一致 |
| Plugin | 通过 Go `interface` 挂载到 Runtime、拥有可撤销副作用的组件 |
| typed config | 由能力 owner 定义的 Go 配置类型；外部输入严格解码、校验后才能创建 Plugin |
| 源 Profile | Cordis Bundle/Patch 组成的插件树；职责可参考，但其动态配置格式不属于兼容面 |

## 3. 用户目标

1. 让现有 TypeScript 客户端能够按源 Connection 与 API Proxy contract 连接 Go Agent 服务端。
2. 用 Go 运行 DeepSeek Harness 的核心 Agent、Tools、Session 和 LLM。
3. 保留纳入 API 的 HTTP/WebSocket、RPC 信封、方法 payload/result、事件、错误、取消与重连语义，使 TypeScript fixtures 能验证 Go 行为。
4. 以 Go `interface` 实现插件机制，而不是移植 Cordis、使用 TypeScript Runtime 或加载 `.so`。
5. 所有配置使用 owner-defined Go typed config，不执行 `!!js`、Node.js 或其他配置脚本。
6. 得到默认无 CGO、可构建为单一可执行文件的 Agent Server Runtime。
7. 排除 Web UI、浏览器客户端实现、DeepSeek Harness SDK 和 Python；保留位于 `packages/client/connection` 中但实际属于 Host 的协议职责。
8. 让后续能力仍按 Plugin、Service Definition、Provider、Consumer 和事件 seam 扩展，而不是把行为堆入 Agent loop。
9. 以源 Harness 已有职责划分作为默认实现地图；除 Go 约束、已证明的依赖缺陷和本文排除项外，不改变能力 owner。

## 4. 参与者与入口

| 参与者 | 入口 | 允许行为 |
| --- | --- | --- |
| 本地用户或自动化（Deferred） | `cmd/dsh` Headless CLI | 选择 typed deployment config、提交任务、等待 Session 停稳、取得退出码与最终文本 |
| TypeScript Client | Connection HTTP/WebSocket contract | 建立 generation、调用纳入的 API、接收 Session/Host frame、回答 approval/question、取消请求 |
| ACP Client（Deferred） | ACP stdio Server | 创建会话、提交文本、接收已提交消息、取消、处理权限请求 |
| 外部 MCP Server（Deferred） | MCP Client Bridge | 公布工具并接收工具调用；不获得内部 Runtime 对象 |
| Go 应用装配者 | Go package API | 组装已编译 Plugin Factory、替换 Provider、嵌入 Runtime |
| Plugin 作者 | Plugin/Service/Event/Tool API | 注册可撤销贡献；不能绕过所属能力的执行策略 |
| 运维与测试 | 配置、日志、metrics、Session 文件 | 观察、回放和诊断，不修改运行中领域状态 |

## 5. 主流程

### 5.1 TypeScript Client Connection

1. Client 同时打开 `/api/events.mux` 与 `/api/events.host` 两条只下行 WebSocket。
2. 两条流就绪后调用 `host.describe`；三者都成功才发布一个可用 connection generation。
3. 一元调用以 `POST /api/<method>` 发送 `ClientRequest`，服务端返回相同 `rpcId` 的 `ServerResponse`。
4. `session.prompt` 驱动 Agent turn；提交的 Session Event、运行状态、队列和 interaction 通过下行 frame 返回。
5. approval/question 等可回答的 `ServerRequest` 由 Client 使用 `POST /api/respond` 回传 `ClientResponse`。
6. HTTP 断开传播取消；任一 WebSocket 断开使 generation 失效，Client 重建两条流并重新读取基线。

Typert 不拥有上述协议。它只是源 Host 在某些 `/api` endpoint 已从 API Proxy 迁移为 Remote 方法时使用的内部 dispatcher。

### 5.2 Agent 轮次

轮次语义保持源实现：`turn/start` 在领取输入前写入；一次 step 写入 `step/start`、模型请求与输出、Tool 调用/结果及 `step/end`；仍有欠缺工作时进入下一 step；最终写入 `turn/end`。首次输入被拒绝或改写为空时也必须形成一个没有 step 的持久轮次。

### 5.3 插件替换

运行中的配置只可选择、挂载、替换和卸载已经编译进 Factory Catalog 的 Plugin。每个 Factory 先把输入严格解码为自己拥有的 Go 配置类型并完成校验；候选配置和依赖全部有效后才原子发布，失败时保留旧实例。卸载按副作用获取的逆序执行并等待资源静止。

### 5.4 可选 Headless 任务

Headless 是不启动 Client Connection 的一次性 CLI adapter：读取一个 task，创建 Session/Agent，等待运行和 flush，打印最后一条 Assistant 文本并返回退出码。它复用 Core，但不是客户端协议兼容的前置条件，首期标记为 Deferred。

## 6. 纳入范围

### 6.1 第一优先级：可运行主干

- `core/session`：仅追加事件日志、surface、fork、repair、内存 Session Store。
- `core/system-prompt`：Prompt section、Tool schema 和 Context snapshot 组装。
- `core/tools`：Tool Registry、执行模式、策略流水线、结果与展示意图。
- `core/agent`：Agent 接口、Registry、inbox、作用域与实时事件。
- `core/agent-loop`：默认轮次/步骤驱动器。
- `core/scope`：Agent 作用域注册与隔离。
- `llm/llm`、DeepSeek Provider、retry、token meter；其他 Provider 按现有产品需要迁移。
- `bundle/base` 去除 Web UI rows 后的服务端基座，以及 Connection Server boot；`bundle/headless` 不是首期前置。
- Go typed server/plugin config、严格解码、显式默认值与 owner-defined validation。

### 6.2 Agent 运行能力

- approval、permission 与 user questions：支撑可回答的 server-request。
- attachment 与 spill：支撑 `session.prompt` 的 image/content budget。
- compaction、guard 和基本 Tool execution：支撑 Agent turn 的正确运行。
- shell、filesystem、LSP、sandbox、code-runtime 等本地能力按目标 Agent composition 逐项进入 capability matrix，不因源目录存在而自动成为首期要求。

### 6.3 Session 持久化与查询

- Session backend-neutral persistence、默认 SQLite facts、repair、projection、list/history/query 与 title；
- reconnect 所需的 stream baseline、pending interaction replay 和 queue snapshot；
- storage、identity 与 runtime invariants 中被上述路径实际消费的职责；
- JSONL raw artifact/export、SQLite search、stats 和 telemetry 不是协议握手前置，按实际客户端能力要求进入后续切片；SQLite 能力进入后统一通过领域目录内的 sqlc 配置生成 repository-private adapter。

### 6.4 Client Connection 与 API

- Connection Host carrier：复制 `/api` HTTP unary、`rpcId` 关联、`RpcResult`/`RpcReceipt`、两条 WebSocket downlink、取消、body limit 与 Host/Origin trust fence。
- API Proxy contract：保留纳入方法的 canonical method、payload/result schema、错误码与 frame union；Go handler 直接调用核心 Service。
- 当前兼容切片包含 `host.describe`、`session.*`、七个 `workspace.*`、`events.mux`、`events.host`、`respond`，以及它们依赖的 Session/Workspace/approval/question frame。
- Workspace 只纳入服务端 Registry、SQLite adapter、Session accounting 与协议/API；不复制浏览器 Workspace manager、目录选择 UI 或项目文件索引。
- TypeScript Client 代码不复制，但源 Client contract tests 作为 Go Server 的外部兼容验收方。

Connection Server composition 会打开协议端口，但不提供 HTML、JavaScript bundle、React、静态资源或浏览器客户端。这是 Agent 协议服务端，不是 Web 产品。Deferred Headless composition 若实现，仍不监听端口。

### 6.5 Deferred 能力

- Headless CLI；
- ACP Server 与 MCP Client Bridge；
- Typert Host Gateway；只有纳入 endpoint 在固定源基线中由 Typert Remote 拥有时，才实现该 endpoint 所需的最小 descriptor 与 Host dispatch；
- Subagent、Settings、Credentials、Goals、Remote extension 等非核心客户端功能；
- Codex/Claude Code/ACP subagent、Workflow、Schedule 等编排能力。

Deferred 不算已复制，也不能通过空 handler 或固定成功响应占位。进入范围时仍保持源职责划分。

## 7. 明确排除范围

以下内容不实现，也不能为了复用而把其依赖重新拉入主线：

- `packages/web/*`，包括 Web search、fetch Provider 和 `tool-web`；
- `packages/client/*` 的浏览器侧实现、`apps/web`、`bundle/web-app` 及 React/client runtime；`packages/client/connection` 的 Host wire 行为是协议证据，不因目录名而排除；
- `packages/sdk/*`、`python/*` 及 DeepSeek Harness SDK 的 JSON-RPC runtime/client/server；
- directory picker、frontend static、浏览器资源 Host 与完整 WebServer 产品；协议所需的 `/api` 与 WebSocket server 由 Go adapter 实现；
- `extensions/cordis-client-runner`、`extensions/ui-cordis`、客户端测试 Runtime 和 React session export；
- `subagent-dsh-sdk`；
- website、前端构建、发布 npm/Python 包和浏览器可视化测试；
- Typert generator/loader 的 TypeScript compiler、Node/npm discovery、decorator、声明合并、SRC 参数名弱解析、`.d.ts` 与 Remote Client 代码生成；
- 源 Cordis Profile 的 `!!js` tag、JavaScript evaluator、动态 `ctx` 插值和配置脚本兼容；源配置必须显式迁移为 Go typed config；
- `ClientRemote`、浏览器 Connection controller 与客户端 API 实现；Host `/api` carrier、`rpcId` 和 server downlink 明确保留；
- 在运行时安装并执行新的 Go 源码、Go `.so` 插件或 TypeScript 插件。

源 `bundle/base` 中的 Web rows 必须在 Go 默认 composition 中缺席，不能仅设置 `disabled: true`，因为禁用条目仍会污染 Factory Catalog、依赖闭包和配置目录。

## 8. 当前仓库状态

当前 Go module 已有 `llm`、OpenAI-compatible adapters、测试和示例。这些代码证明部分 Provider transport 能工作，但其公共模型与源 Harness 不一致：

- 当前使用 `Model` + `APIAdapter` 路由，源 Harness 以 provider route + `LlmAdapter` 注册；
- 当前 Stream Event 与源 `StreamChunk` discriminants 不同；
- 当前 Message/Content、reasoning、error 和 replay 语义不等同；
- 当前没有 Session、Agent、Tools、Plugin Runtime 或 Connection Server 组合。
- 当前没有与 TypeScript Connection/API Proxy 兼容的 Go Agent server。

因此，现有 `llm` 只可作为迁移材料。目标是让唯一的 `llm` owner 采用 Harness API，而不是增加第二套 `harness/llm` 或长期兼容 wrapper。

## 9. 全局不变量

- **H-I01 范围封闭**：任何实现提交都必须能映射到纳入范围；排除包不得出现在 runtime dependency closure。
- **H-I02 基线固定**：兼容声明必须标明 TypeScript commit；未审计的新提交不能改变目标行为。
- **H-I03 契约同一**：纳入 API 的 canonical name、事件名、JSON 字段、discriminant、缺失语义和错误码不得为了 Go 风格而重命名。
- **H-I03A Connection 同一**：HTTP/WebSocket path、四类 RPC message、`rpcId` 回显、carrier/business error 分层、stream direction 与 generation handshake 必须由 TypeScript fixture 证明。
- **H-I04 模型可见即已记录**：进入模型请求的内容必须能从 Session 日志重建；新增模型可见输入必须有 Session Event。
- **H-I05 日志仅追加**：`seq` 连续单调，已发布 Event 不修改；surface 替换只通过 `surfaceOp` 表达。
- **H-I06 注册可撤销**：每个 Service、Listener、Provider、Adapter、Tool 和 Prompt section 都由一个 Plugin effect 所有，并有幂等 disposer。
- **H-I07 生命周期原子**：Plugin 启动失败反向回滚全部已取得资源；替换失败不破坏 last-known-good 实例。
- **H-I08 能力 seam 完整**：能力包含 Service Definition、至少一个 Provider 或明确外部端口，以及当前 Consumer；Consumer 不依赖具体 Provider。
- **H-I09 Loop 保持最小**：新功能优先使用既有 Plugin、Event、Tool、Provider 或 Policy seam；修改 Agent loop 必须同时修改轮次设计和兼容 fixtures。
- **H-I10 单一公共身份**：同一职责不得同时保留旧 API、新 API 和 wrapper；先修正 owner，再迁移调用者。
- **H-I11 Go Plugin 安全**：运行时只挂载编译进 Catalog 的 Factory，不使用标准库 `plugin` 或 unsafe symbol loading。
- **H-I12 机密不落盘**：credential 不进入配置 dump、日志、Session Event、错误、fixture 或 telemetry。
- **H-I13 源职责优先**：源 Service Definition / Provider / Consumer、事件 owner 和生命周期边界默认保留；偏离必须记录源码证据、Go 约束、影响和迁移后的 owner。
- **H-I14 配置类型化**：每个配置由消费它的 Factory/能力 owner 定义 Go 类型；未知字段、类型错误和无效组合在激活前失败，Plugin 业务逻辑不接收 `map[string]any`、裸 JSON 或脚本结果。
- **H-I15 存储适配纯净**：JSONL、SQLite/sqlc 等 adapter 只实现 Consumer-owned persistence interface 与技术性 I/O 语义；不创建业务事件、不决定 repair/projection/policy，也不把存储类型泄漏到 Session 或领域接口。

## 10. 异常与恢复

| ID | 场景 | 处理 |
| --- | --- | --- |
| H-E01 | Plugin 配置或依赖无效 | 在激活前失败；不得部分发布 Service |
| H-E02 | Plugin `Apply` 中途失败 | 逆序回滚 effect，聚合 cleanup 错误，保留原实例 |
| H-E03 | Provider stream 运行期失败 | 归一化为源 `finish`/failure 语义，保留已提交 partial 与 usage |
| H-E04 | 未知 required Session Event | 拒绝恢复；未知且 `ignorable: true` 的 Event 可跳过 |
| H-E05 | Session crash 留下开放轮次 | repair 写入 `interrupted`，不删除已有事件 |
| H-E06 | Tool 参数、权限或执行失败 | 在 Tool executor 处拒绝并记录规范 Tool Result，不靠 Prompt 隐藏 |
| H-E07 | Deferred MCP 重连失败 | 进入实现范围后保留上一正常代直到预算耗尽；候选代失败不形成部分注册 |
| H-E08 | Deferred Headless 被取消或退出 | 进入实现范围后先取消工作、等待所有 owned operation 静止、flush Session，再退出 |
| H-E09 | TypeScript fixture 无法被 Go 解码 | 兼容门禁失败，不通过放宽未知字段或静默丢弃修复 |
| H-E10 | 配置包含 `!!js`、未知字段或类型错误 | 启动/更新失败并报告来源与字段路径；不求值、不忽略、不把表达式当字符串 |

## 11. 非功能要求

- 默认支持 Linux、macOS 和 Windows；目标架构至少覆盖 `amd64` 与 `arm64`。
- 默认构建不依赖 Node.js、TypeScript Runtime、嵌入式脚本引擎、浏览器或 CGO；TypeScript 只允许出现在兼容 fixture 生成流程。
- Connection Server composition 监听 `/api` HTTP/WebSocket；Deferred Headless 若实现，默认不监听端口。
- Runtime shutdown 有界且可观测，不遗留 goroutine、子进程、PTY、watcher、数据库事务或临时注册。
- 同一 Session 的 Event append 串行化；不同 Session 可以并行。
- 配置、协议和持久化边界严格验证；同进程 typed interface 不做无证据的重复 runtime validation。
- 自动化状态必须区分“计划”“已实现”“Go 自动化通过”“跨语言 parity 通过”和“真实 Provider 验收通过”。

## 12. 许可与归属

DeepSeek Harness 与 Goren 都使用 MIT License。复制或实质派生源代码时，代码提交必须同时：

1. 保留 DeepSeek 的 MIT copyright 和 permission notice；
2. 记录源 commit 与对应源路径；
3. 生成并维护第三方依赖 notices；
4. 不把“按行为重新实现”错误标记成未引用源实现的原创代码。

具体使用文件级 header、`NOTICE` 还是集中 provenance manifest，由首次实质代码移植提交确定；在此之前不得复制大段源代码。

## 13. 未决事项

- **O-01 基线升级节奏**：按 release、按月还是按需求升级；当前维持固定 commit。
- **O-02 客户端 API 扩展顺序**：第一兼容切片以 `host.describe`、`session.*`、两条 event stream 和 `respond` 为主；其他客户端功能按实际需求加入 capability matrix。
- **O-03 Windows 沙箱完成度**：是否首发即要求与源受限 token/ACL 相同，还是先以 Linux/macOS 为 release gate；不得用无沙箱实现冒充 parity。
- **O-04 外部 Go Plugin 分发**：当前只支持自定义二进制静态链接；是否提供生成 composition root 的命令在实际用户需求出现后决定。
- **O-05 typed config 输入**：首期是否需要配置文件、使用 YAML 还是 JSON，以及 defaults/file/environment/CLI 的覆盖顺序；无论选择如何，严格 Go 类型与禁止脚本已经确定。
