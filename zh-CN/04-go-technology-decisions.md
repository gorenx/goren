# 04 Go 技术架构决策与技术选型

状态：Draft
选型核对日期：2026-08-14

本文记录实现 DeepSeek Harness Go 复刻所需的技术决策。协议字段和行为由[03 协议与 API 兼容设计](./03-protocol-and-api-compatibility.md)拥有；本文只决定如何实现，不借技术选型修改协议。

依赖版本以实施时的 `go.mod` 为唯一权威。下表的候选项目已在核对日期确认仍有可用 Go module；开始对应阶段时仍需重新审查 release notes、license、平台支持和传递依赖。

## 1. 决策摘要

| ID | 决策 | 状态 |
| --- | --- | --- |
| D-01 | 使用当前 module 的 Go toolchain，默认无 CGO、单一静态装配二进制 | Accepted |
| D-02 | Plugin 使用 Go interface、泛型注册函数和静态 Factory Catalog | Accepted |
| D-03 | 所有运行时配置使用 owner-defined Go typed config；不实现 `!!js` 或任何配置脚本 evaluator | Accepted |
| D-04 | JSON 使用标准库；JSON Schema 复用 `jsonschema/v6` | Accepted |
| D-05 | 把现有 `llm` 迁移成唯一 Harness LLM contract，不建立平行实现 | Accepted |
| D-06 | JSONL 保持 Session 事实日志；SQLite adapter 使用 sqlc 生成访问代码 | Accepted |
| D-07 | 文件监听使用 `fsnotify`，由上层维护递归目录与原子替换 | Proposed |
| D-08 | 子进程使用 `os/exec`，跨平台 PTY 候选为 `go-pty`，Sandbox 按平台 Provider 分离 | Proposed |
| D-09 | Connection Host 使用 Echo v5 与 `coder/websocket`；ACP、MCP、Typert 均 Deferred | Accepted |
| D-10 | 日志使用 `log/slog`；OpenTelemetry 作为可选 Plugin，默认关闭 export | Accepted |
| D-11 | Server CLI 首期使用标准库 `flag`；并发组合候选 `x/sync`，稳定 ID 候选 `google/uuid` | Proposed |
| D-12 | 不使用标准库 `plugin`、CGO SQLite、Node.js runtime、嵌入式脚本引擎或浏览器依赖 | Accepted |

`Accepted` 表示架构方向已经确定，不代表代码已经实现；`Proposed` 必须在首次引入依赖的代码提交中完成版本与 license 复核；`Deferred` 表示当前目标不依赖该能力，不能预先引入依赖或创建占位实现。

## 2. D-01：构建与平台基线

当前 module 声明的 Go 版本是实现基线。项目不预先承诺兼容更旧的 Go；降低版本必须由 CI 和依赖图证明，而不是修改 `go.mod` 数字。

默认交付约束：

- `CGO_ENABLED=0` 可构建；
- Linux、macOS、Windows 的 `amd64` 和 `arm64` 进入支持矩阵；
- 标准 Agent Server 构建是单一可执行文件，不在运行时依赖 Node.js；
- 平台能力通过 build-tagged Provider 分离，公共 Service 不出现平台分支；
- 不支持的安全能力必须在激活 deployment config 时失败，不能静默降级。

跨平台不是要求每个底层实现完全相同，而是公共 contract、错误类别和安全承诺一致。PTY、Sandbox、进程组和文件权限允许使用不同 Provider。

## 3. D-02：Plugin Runtime

采用[02 Go 运行时架构与插件模型](./02-runtime-architecture-and-plugin-model.md)中的 `Plugin`、`Factory`、`Scope` 与 `Disposer`：

- Factory 在 composition root 静态注册；
- deployment config 只能实例化 Catalog 中已有 Factory；
- Runtime 只通过 owner-defined Service/Event key 连接插件；
- 所有注册、goroutine、进程、文件监听和连接都是可撤销 effect；
- replacement 在 shadow scope 验证后原子切换；
- public extension contract 不暴露 Runtime 内部锁、map 或依赖图。

拒绝标准库 `plugin`，原因包括平台限制、不可卸载、toolchain/依赖一致性要求和 race detector 限制。也不默认引入进程外 Plugin RPC，因为这会创造一套新的公共协议、版本和故障模型。

## 4. D-03：Go typed config

### 4.1 决策

Goren 不复制 Cordis Profile 的动态配置语言，不识别或执行 `!!js`，也不引入 Goja、Node.js、模板脚本或另一种表达式 evaluator。配置格式不属于 TypeScript Client 与 Go Agent Server 的通信协议，因此这项偏离不影响当前兼容目标。

每个能力 owner 定义自己的命名 Go 配置类型。例如 Connection 拥有监听与 trust 配置，Session Store 拥有路径和 durability 配置，LLM Provider 拥有 endpoint、model 与 credential reference 配置。不得建立一个跨能力、充满可选字段的全局配置模型。

配置进入 Runtime 的过程是：

```text
CLI / environment / optional file
  -> strict decode
  -> named Go config value
  -> explicit defaults
  -> owner validation
  -> Factory.New
```

Factory 通过泛型注册保留具体配置类型；异构 Factory 只在 Catalog 内部用已注册 closure 擦除类型。Plugin 构造和业务逻辑只接收已经验证的具体配置值，不接收 `map[string]any`、裸 `json.RawMessage`、YAML node 或脚本返回值。

### 4.2 外部配置来源

首期 Server CLI 使用标准库 `flag`。环境变量和可选文件由 `internal/config` 读取并转换为 typed fields。配置文件确有需求时，YAML 候选为 [`go.yaml.in/yaml/v3`](https://pkg.go.dev/go.yaml.in/yaml/v3)；采用前仍需完成依赖准入。

无论来源如何，必须：

- 拒绝未知字段、重复 key、错误类型和无效枚举；
- 区分缺失、显式零值与敏感字段引用；
- 在错误中报告配置来源与字段路径，但不包含 secret value；
- 在 Plugin 激活前完成 cross-field validation；
- 先完整验证 replacement candidate，再原子替换 last-known-good instance；
- 让 config dump 只输出脱敏后的 typed value。

配置文件、环境变量和 CLI 的覆盖顺序属于部署 contract；在实现多来源加载前必须作为显式决策冻结，不能依赖库默认行为。

### 4.3 动态值的 Go 映射

源 `!!js` 用途改为显式 Go 机制：

| 源动态用途 | Go owner |
| --- | --- |
| `process.env.*` | `internal/config` 的 typed environment decoder |
| `process.cwd()`、`dshHomePath(...)` | boot/path resolver 计算默认值 |
| `process.platform` | composition root、`runtime.GOOS` 或 build-tagged Provider |
| 条件启用 Plugin | typed deployment config 选择 Factory |
| 条件派生 policy | 配置 owner 的纯 Go default/validation function |
| `ctx.<service>` 插值 | 显式 Factory dependency 或 Service interface |

这些函数接受明确输入、返回明确类型并可单独测试。配置中出现 `!!js` tag、表达式字符串或未知脚本字段时直接失败；不得求值、忽略或退化成普通字符串。

源 Cordis Profile 只能通过显式迁移进入 Goren：把每个动态值翻译为 typed field、环境变量绑定、默认值函数或 composition 选择。Goren 不承诺原 Profile 文件直接可运行。

### 4.4 System Prompt 插值不是配置 evaluator

System Prompt 保留源实现的 strict single-pass `{{name}}` 文本替换；它只读取 owner 注册的 typed `VariableProvider` 结果，不读取任意对象属性，不执行表达式、filter、function、条件或循环。替换结果也不会再次扫描。该能力属于模型输入 assembly，不属于配置加载。

core 不引入 [Gonja](https://github.com/nikolalohinski/gonja)。Gonja 的 Jinja control flow、filter、global function 和可扩展执行环境会扩大源 contract，并重新引入动态语言及安全面。未来真实 Plugin 若需要富模板，可在自身 typed provider 内预渲染普通文本，再交给 System Prompt；不得让模板 engine 进入 Factory config、Registry 或通信协议。完整边界见[11 System Prompt Registry 与 Assembly 模块设计](./11-system-prompt-registry-and-assembly.md)。

## 5. D-04：JSON 与 Schema

JSON 编解码使用 `encoding/json`，协议边界通过自定义 `UnmarshalJSON` 或 presence 类型表达判别 union、缺失/`null` 和数值约束。禁止在核心领域到处传递 `map[string]any`。

JSON Schema 复用仓库已有的 [`github.com/santhosh-tekuri/jsonschema/v6`](https://pkg.go.dev/github.com/santhosh-tekuri/jsonschema/v6)：

- API Proxy payload 和 Tool 输入在进入 use case 前校验；
- Factory config 由 owner-defined typed decoder 和 validation 在 `New` 前校验；
- JSON Schema 仅在 API、Tool 或外部配置工具确有机器可读 contract 时编译并缓存，不替代 Go 类型；Tool 的输入与输出 schema 在注册时各编译一次，执行期只验证 value；
- schema URL、ref loader 和最大输入受限，不能任意访问网络或本地文件；
- Go typed decode 与 schema validation 都必须成功，二者职责不能互相替代。

Tool contract 允许任意 JSON shape，因此参数、canonical value、schema 和 presentation metadata 在这条协议边界使用经过 lossless validation/clone 的 `json.RawMessage`。这不扩展为 `map[string]any` 业务模型：Definition、policy decision、callback、execution 和 result 都使用具体 interface；异构行为表不保存 `any` 或反射调用。完整约束见[12 Tools Registry 与执行流水线模块设计](./12-tools-registry-and-execution-pipeline.md)。

不引入第二个 JSON Schema engine，除非源 schema 使用当前库无法表达且有兼容 fixture 证明。

## 6. D-05：LLM Runtime

`llm` 是公共能力 owner；provider route、Message、Content、StreamChunk、finish reason、usage 和 retry 语义以源 Harness contract 为目标。

技术策略：

- Provider HTTP 默认使用 `net/http`，统一注入 timeout、proxy、TLS、User-Agent 和 telemetry transport；
- 不保留通用 OpenAI SDK/compatibility 公共层；每个供应商 Adapter 直接把自己的 wire 转换为唯一 Harness LLM contract；
- 每个供应商 Adapter 只拥有 wire、网络 I/O 与 vendor error 映射，不拥有 retry orchestration、Agent step 或 Session；
- LLM capability 拥有 RetryPolicy contract 和错误分类，Agent request owner 根据 policy、attempt 和是否已有模型可见输出执行重试；
- stream parser 设置单事件和累计响应预算，并在 `context.Context` 取消时关闭 body；
- secret 由 Credentials Service 提供，Model metadata 不携带明文 key。

迁移完成后只保留一套公共 `llm` contract。旧接口的删除与新接口迁移属于同一代码演进，不通过长期 wrapper 延缓所有权修正。

DeepSeek direct adapter 的配置、调用链、SSE 和失败映射由[13 Harness LLM Runtime 与 DeepSeek Provider 模块设计](./13-harness-llm-runtime-and-deepseek-provider.md)拥有。Echo 的“不使用原生 HTTP”决策只针对 inbound Host router/server；outbound Provider 继续以 `net/http` client/transport 作为可注入 I/O primitive，不引入第二个 server framework 或供应商 SDK。

## 7. D-06：Session 与持久化

### 7.1 JSONL

标准库完成 JSONL append：

- 一个 Session 一个串行 append owner；
- 每条记录完整编码后一次写入，并检查 short write；
- flush 与 durability policy 显式区分；
- adapter 检测并报告截断尾行、checksum/codec 与 I/O 错误，但不决定业务 repair；
- Session Recovery owner 根据已读取事件判断开放轮次，并通过普通 append 接口写入 `interrupted` 等恢复事件；
- atomic metadata 用同目录临时文件、flush 和 rename；
- reader 有单条 event 大小上限，不能使用 `bufio.Scanner` 默认 token limit。

JSONL adapter 不分配 `seq`、不创建 Session Event、不判断 turn/step 状态，也不实施 retention 或权限策略。它只持久化 Session owner 已经构造并验证的数据。

### 7.2 SQLite

SQLite 查询与写入统一由 [`sqlc`](https://sqlc.dev/) 按 SQLite dialect 根据 SQL schema/query 生成 `database/sql` Go 代码。默认 driver 候选仍为 [`modernc.org/sqlite`](https://pkg.go.dev/modernc.org/sqlite)，以保持无 CGO 和交叉构建；driver 的首次引入仍需完成依赖准入与目标平台验证。

SQLite adapter 只负责 source 中相应 store、projection storage 和 query 的持久化职责，不成为所有模块共享的“通用数据库”。每个 schema 有明确 owner、migration version 和 transaction boundary。JSONL 事实日志与 SQLite projection 的一致性通过 checkpoint/重建验证，不执行无法证明的双写事务。

Projection 的业务语义位于 Projection/Application owner：它决定哪些 Event 产生哪些 mutation、字段如何解释以及一个 use case 需要哪些操作原子完成。SQLite adapter 只执行这些已决定的 mutation/query 和调用方要求的 transaction，不在 SQL trigger、生成 query wrapper 或 row mapper 中隐藏业务状态机。

sqlc 使用规则：

- migration SQL 与 query SQL 位于拥有该 store/projection 的 adapter 内；
- migration 是 schema 的权威，sqlc 配置读取同一 schema 或由它生成的受检 snapshot；
- 常规查询写在 `.sql` 文件中，不在 Go 代码里散落手写 SQL；
- 生成包放在 repository-private 路径，不手工修改；
- 生成的 row、nullable wrapper 和 driver 类型只属于 adapter，返回前映射为 owner-defined 类型；
- transaction boundary 由 use case owner 决定，adapter 使用 sqlc 的 transaction-bound query handle 执行；
- sqlc 工具版本、配置和生成结果随首次 SQLite 实现一起提交；
- CI 重新生成并检查工作树无差异，再编译和运行 migration/query/integration tests。

引入前必须用目标平台基准验证写入延迟、文件锁、WAL、busy timeout、备份与崩溃恢复；若不满足要求，再以 ADR 更换 driver，不能让 driver 类型泄漏到 Service Definition。

## 8. D-07：文件监听

文件变化监听候选为 [`github.com/fsnotify/fsnotify`](https://pkg.go.dev/github.com/fsnotify/fsnotify)。它负责平台事件，不负责业务语义：

- 递归 watch 由 filesystem/settings/skills 等 Consumer 枚举目录；
- 监听目标文件时同时 watch parent directory，以处理编辑器的 rename-and-replace；
- debounce、rescan 和内容 diff 位于能力 owner；
- overflow、目录消失和权限变化触发有界全量重扫；
- watcher 必须绑定 Plugin disposer。

事件不能被视为可靠变更日志；最终状态总是由重新读取和校验确认。

## 9. D-08：Process、PTY 与 Sandbox

普通子进程基于 `os/exec`，公共 Process Service 拥有：

- argv/env/cwd 的结构化输入；
- stdin/stdout/stderr 流；
- deadline、取消与 exit classification；
- process tree 终止；
- 输出预算与 spill；
- credential redaction。

禁止默认拼接 shell 字符串。Shell Tool 显式选择 shell，参数和脚本来源进入审计事件。

PTY 候选为 [`github.com/aymanbagabas/go-pty`](https://pkg.go.dev/github.com/aymanbagabas/go-pty)，因为目标包含 Unix PTY 与 Windows ConPTY。首次采用前必须分别验证 resize、UTF-8、Ctrl-C、EOF、子进程退出和泄漏。

Sandbox 是独立 Service Definition：

- Linux Provider 可使用 namespace/seccomp/cgroup 等经过验证的组合；
- macOS Provider 使用系统可用的 profile/权限机制；
- Windows Provider 使用受限 token、Job Object 和 ACL；
- unsupported Provider 返回稳定 capability error。

Sandbox 不是 boolean 配置，也不能由路径字符串拼接代替。未完成的平台不得标记安全 parity。

## 10. D-09：Connection Host 与 Deferred adapter

### 10.1 Connection Host

Connection Host 是首期必需的入站 adapter：

| 需求 | 选择 | 所有权 |
| --- | --- | --- |
| HTTP framework | [`github.com/labstack/echo/v5`](https://echo.labstack.com/guide/quickstart/) | listener、路由、middleware、body limit、error mapping、graceful shutdown |
| WebSocket | [`github.com/coder/websocket`](https://pkg.go.dev/github.com/coder/websocket) | `/api/events.mux`、`/api/events.host` 的 server-to-client text frame 与 close policy |
| JSON | `encoding/json` | 四类 RPC message、`RpcResult`、`RpcReceipt` 和 frame union |
| Trust fence | custom Echo middleware | Host allowlist、Origin/Host 一致、`Sec-Fetch-Site` cross-site 拒绝和 privileged method loopback |

API Proxy 注册 canonical method、typed handler、pending interaction 与事件源；Connection 只处理 carrier、信封、`rpcId`、request/stream 取消和 teardown，不包含 Agent/Session 业务。双流 readiness 与 connection generation 由现有 TypeScript Client 拥有，不在 Go Host 增加镜像状态。Echo 只存在于 `internal/connection`，adapter 在路由边界把 Echo request 转换为协议输入；API Proxy、Agent、Session 和 Plugin 公共接口不得出现 `echo.Context`。

协议实现不得使用 Echo 默认 binder 或默认 HTTP error rendering。四类 RPC message 继续由 `encoding/json` strict codec 解码；custom Echo error handler、recovery middleware、404/405 handler 和 body-limit middleware 必须映射到[03 协议与 API 兼容设计](./03-protocol-and-api-compatibility.md)规定的精确 status/envelope/header。handler 使用底层 request context 传播 disconnect cancellation；客户端断开时取消 owned operation，但不得关闭共享 Runtime。

当前 carrier 已采用 Echo `v5.3.1`：官方 Recover middleware 只兜底 adapter/middleware panic，集中 `HTTPErrorHandler` 负责未提交的 framework error，`StartConfig.Start` 负责带超时的 graceful lifecycle。Envelope 与 payload 不使用 Echo Binder；WebSocket bridge 通过 Echo 暴露的底层 request/response 完成 upgrade。验证层级和实现边界由[06 Connection Host 模块设计与实现](./06-connection-host-module.md)拥有，当前证据见[08 实施进度](./08-implementation-progress.md)。

`coder/websocket` 需要底层 HTTP request/response。该访问只封装在 `internal/connection` 的 WebSocket bridge 中，普通 handler 和核心逻辑继续只使用 Echo adapter 提供的协议 DTO 与 `context.Context`。Echo 的 context 不得在 handler 返回后被 goroutine 持有。

WebSocket 不接收客户端业务消息。任一 event socket 断开时，服务端取消该 socket 对应的事件源；现有 TypeScript Client 观察到断开后废弃其当前 generation 并重建两条流。服务端不因单条 socket 断开主动关闭 sibling socket，也不拥有 Client generation。源 `toFetchHandler` 的 SSE 仅用于 in-process 测试 adapter，不作为浏览器网络 fallback。

Trust fence 只减少浏览器跨站和错误暴露风险，不是身份认证。若以后允许非 loopback 部署，必须另行决定 TLS、认证、授权和反向代理信任边界。

### 10.2 候选比较

| 候选 | 结论 | 原因 |
| --- | --- | --- |
| Echo v5 | Selected | framework handler/middleware、集中 error handling、graceful lifecycle，并可在 WebSocket bridge 访问底层 request/response |
| 直接 `net/http` | Rejected | 能精确控制协议但需要自行装配 router、middleware、error 与 lifecycle，且用户已明确排除 |
| Chi v5 | Rejected | 很轻且协议透明，但 handler 与 middleware 仍直接采用 `net/http`，不满足本项目选择 |
| Gin | Rejected | 功能成熟且可接 WebSocket，但其自有 Context/binding/rendering 对该 RPC carrier 没有比 Echo 更明确的收益 |
| Fiber v3 | Rejected | 基于 fasthttp；与 `net/http`/`coder/websocket` 需要适配层或更换 WebSocket 栈，增加协议差异面 |

### 10.3 Echo v5 依赖准入记录

核对日期：2026-08-14。

| 项目 | 结论 |
| --- | --- |
| 能力 owner | Connection Host；Echo 只存在于 `internal/connection` |
| 引入原因 | 项目已经排除直接装配 `net/http` router/server；现有依赖没有 listener、route、middleware、集中 error handling 与 graceful lifecycle |
| 精确版本 | `github.com/labstack/echo/v5 v5.3.1`，发布于 2026-07-21，要求 Go 1.25+ |
| checksum | module `h1:75maCxkQVGualckLc/5s/ihgpH1a1Dc6AuGWNVNs6bw=`；go.mod `h1:4iEGNQiPPZnkfYpNR/L6fINd3NLiGWUD5+eBotFALas=` |
| license / notice | Echo 为 MIT；发布物的第三方 license/NOTICE 清单必须保留 LabStack copyright 与许可文本 |
| 平台 / CGO | Echo 与当前使用路径是 pure Go、无 CGO；本次只在 `darwin/arm64` 验证，其他目标仍服从发布阶段平台 CI gate |
| 权限面 | 打开网络 listener，读取 HTTP header/body，并通过底层 `net/http` request/response 服务请求；当前路径不启动 subprocess、不读取业务文件、不使用 `unsafe` |
| 传递依赖 | `golang.org/x/time v0.15.0` 由 Echo middleware 引入；Echo 还把现有 `x/text` MVS 版本提升到 `v0.40.0`，二者均为 BSD-3-Clause；checksum 由 `go.sum` 固定 |
| 安全核对 | `govulncheck` 在 Go 1.26.5 发现 6 个可达标准库漏洞，均标明由 1.26.6 修复；module 已提升到 Go 1.26.6，复扫结果为 `No vulnerabilities found` |
| 最小 contract / failure test | `internal/connection/http_test.go` 覆盖 status、envelope、body limit、取消、technical failure；`trust_test.go` 覆盖 Host/Origin/cross-site；真实进程 smoke 已验证 `host.describe` |
| 替换成本 | HTTP 实现封装在 `internal/connection.HTTPHost` 后；替换 framework 不改变 `connection` 或 `RPCDispatcher`，但必须重跑全部 HTTP/WS differential fixtures |

当前引入 `middleware` 是为了使用 Echo 官方 Recover，并由 custom `HTTPErrorHandler` 保持源协议。未使用 Request Logger、Binder、模板、session、rate limiter 或其他 middleware 能力。

### 10.4 coder/websocket 依赖准入记录

核对日期：2026-08-14。

| 项目 | 结论 |
| --- | --- |
| 能力 owner | Connection Host；只在 `internal/connection` 实现浏览器 Host half 的 WebSocket bridge |
| 引入原因 | Echo 不提供 RFC 6455 codec；源浏览器 carrier 明确要求两条 WebSocket downlink，标准库没有 WebSocket 实现 |
| 精确版本 | `github.com/coder/websocket v1.8.15`，module metadata 时间为 2026-06-15，要求 Go 1.23+ |
| checksum | module `h1:6B2JPeOGlpff2Uz6vOEH1Vzpi0iUz20A+lPVhPHtNUA=`；go.mod `h1:NX3SzP+inril6yawo5CQXx8+fk145lPDC6pumgx0mVg=` |
| license / notice | ISC；发布物清单必须保留 2025 Coder copyright 与许可文本 |
| 平台 / CGO | pure Go、无 CGO、无传递依赖；当前只在 `darwin/arm64` 验证，发布仍需多平台 CI |
| 权限面 | 对已通过 trust fence 的 HTTP/1.1 请求执行 connection hijack，读取控制/客户端 data frame 并写下行 text/close frame；运行时代码不读取 filesystem、不启动 subprocess |
| 安全核对 | 项目级 `govulncheck` 在引入后复扫为 `No vulnerabilities found`；默认 origin 检查与项目 trust fence 同时保留 |
| 最小 contract / failure test | `internal/connection/websocket_test.go` 覆盖双流、426、1008、stream failure、trust、断线取消、新连接隔离、等待 cleanup 与 deadline |
| 替换成本 | 依赖封装在 `webSocketDownlinks`；替换 codec 不改变 `connection.RPCRequest`、API Proxy `EventStreams` 或核心能力，但必须重跑 WebSocket differential fixtures |

### 10.5 Deferred adapter

| 能力 | 未来候选 | 边界 |
| --- | --- | --- |
| ACP Server | [`github.com/coder/acp-go-sdk`](https://pkg.go.dev/github.com/coder/acp-go-sdk) | 映射 ACP connection/session 与内部 Agent/Session |
| MCP Client | [`github.com/modelcontextprotocol/go-sdk`](https://pkg.go.dev/github.com/modelcontextprotocol/go-sdk) | 只接入 tools，generation owner 管理重连和注册 |
| Typert Host | 显式 descriptor + Go Service/lookup/context registry | 只为已纳入的 Typert Remote endpoint 提供内部 dispatch |

上述能力不属于首期依赖闭包。未来采用 SDK 时，SDK 只负责协议编码、transport 与标准类型；内部领域对象不直接别名成 SDK 类型。Typert 继续复用 Connection 的 `/api` carrier，不自行启动另一套 HTTP 服务。

不采用 DSH SDK。Codex subagent 的私有进程 RPC 若保留，位于该 Provider 内部且不导出为通用 SDK。

## 11. D-10：日志与可观测性

结构化日志使用 `log/slog`：

- 日志字段包含 plugin、service、session、agent、turn、step、tool call 等稳定相关 ID；
- secret、完整 prompt、Tool 敏感输出默认不记录；
- error chain 在服务端保留，对外协议错误由 adapter 映射；
- Library 不配置 global logger；Runtime 注入 logger。

OpenTelemetry 是可选 Plugin，使用 [`go.opentelemetry.io/otel`](https://pkg.go.dev/go.opentelemetry.io/otel)：

- traces 和 metrics 可以进入稳定 contract；
- logs bridge 在依赖仍为 beta 时不作为首发兼容要求；
- 默认不配置 exporter，不因导出失败阻塞 Session durable append；
- span/metric label 不包含 prompt、credential 或无界 Tool 参数。

源 telemetry event 与 OpenTelemetry span 不是同一事实。需要进入 Session 或源 telemetry contract 的事件仍由其 owner 产生，OTel 只消费。

## 12. D-11：基础库

| 需求 | 选择 | 使用限制 |
| --- | --- | --- |
| Server CLI | 标准库 `flag` | 首期只拥有监听地址、配置位置、日志和退出码，不承载 use case |
| 多命令 CLI（Deferred） | [`github.com/spf13/cobra`](https://pkg.go.dev/github.com/spf13/cobra) | Headless 或管理命令进入范围后再评估 |
| 并发协调 | [`golang.org/x/sync`](https://pkg.go.dev/golang.org/x/sync) | `errgroup`/semaphore 服从父 context 与有界并发 |
| UUID | [`github.com/google/uuid`](https://pkg.go.dev/github.com/google/uuid) | owner 定义命名 ID 类型并在构造时校验 |

简单功能继续使用标准库。只有存在当前能力需求、维护状态可接受、license 兼容且能减少真实协议风险时才新增依赖。

## 13. 依赖准入与升级

每个第三方依赖的首次代码提交必须记录：

1. 由哪个能力 owner 引入；
2. 为什么标准库或现有依赖不足；
3. license 与 notice 要求；
4. 支持的平台和是否需要 CGO；
5. 网络、filesystem、subprocess、reflection/unsafe 等权限面；
6. release 活跃度、已知安全问题和替换成本；
7. 最小 contract test 与故障行为；
8. 精确 module version 和 checksum。

依赖升级不得与无关功能混合。协议 SDK 升级后必须重跑对应跨语言 fixture；storage、PTY、watcher 和 sandbox 升级必须重跑平台/故障测试。

## 14. 被拒绝的方案

| 方案 | 拒绝原因 |
| --- | --- |
| 不加判断地按 npm package 或源文件一比一创建 Go 目录 | 应保留源职责边界，但 TypeScript 构建、声明合并、发布和排除项不应制造空包 |
| Go 标准库 `plugin` | 平台、卸载、构建一致性和测试约束不满足 |
| 所有 Plugin 都改成进程外 RPC | 新增公共协议、延迟和复杂故障模型，当前无需求 |
| 运行 Node.js 执行整个 Profile/Plugin | 违背 Go 单二进制目标，并重新引入 TS Runtime |
| 实现 `!!js`、Goja 或替代表达式语言 | 配置不属于当前 wire/API 兼容面；脚本会扩大执行面、隐藏依赖并削弱类型检查 |
| 用 `map[string]any` 作为 Plugin 配置 | 把类型错误推迟到业务逻辑，失去 owner、严格解码和编译期迁移证据 |
| CGO SQLite 作为默认唯一实现 | 降低交叉构建与单二进制可移植性 |
| 在 Go adapter 中散落手写 SQLite 查询 | 绕过 sqlc 的 query/type 校验，增加 schema 漂移和重复映射 |
| 把 sqlc 生成类型暴露为 Session/领域 contract | 让数据库 schema 和生成工具拥有核心接口，阻碍 driver/schema 演进 |
| 在 JSONL/SQLite adapter 中实现 repair、projection 或 policy | 让存储细节拥有业务状态转换，导致接口反转并阻碍替换 |
| 复用现有 LLM API 再加 Harness wrapper | 两个公共身份长期并存，无法证明 model-visible parity |
| 把 ACP/MCP SDK 类型作为领域模型 | 让外部协议拥有 Agent、Session、Tool 边界 |
| 只实现 Typert Gateway 而不实现 Connection/API Proxy | Typert 只覆盖部分 auxiliary endpoint，不能让现有客户端完成 Agent 会话 |
