# API Proxy 子模块

`apiproxy` 是浏览器 wire 与 Go application/domain capability 之间的 anti-corruption boundary。权威设计见[`zh-CN/07-api-proxy-module.md`](../zh-CN/07-api-proxy-module.md)、[`zh-CN/13-harness-llm-runtime-and-deepseek-provider.md`](../zh-CN/13-harness-llm-runtime-and-deepseek-provider.md)、[`zh-CN/16-session-api-gateway-and-live-frames.md`](../zh-CN/16-session-api-gateway-and-live-frames.md)、[`zh-CN/17-approval-user-questions-and-interaction-gateway.md`](../zh-CN/17-approval-user-questions-and-interaction-gateway.md)与[`zh-CN/20-workspace-registry-and-api.md`](../zh-CN/20-workspace-registry-and-api.md)。

## 职责

- method-owned request decoder、typed `Outcome` 和 canonical RPC error；
- `host.describe` 与当前纳入的 `agentPreset.list`、`llm.providers`、`llm.models`、`session.*`、`workspace.*` handlers；
- `LLMGateway` 对 configurable provider directory、active route 和 model catalog 的 Host wire 投影；
- Session/Agent/Workspace/interaction facts 到 Mux/Host frame 的 projection；
- pending response correlation 与 reconnect replay；
- per-subscriber ordering、high-water 和 shutdown。

本包不运行 Agent Turn、生成 title、计算 domain projection、执行 Tool、调用模型或承载 HTTP/WebSocket。Echo carrier 位于 `internal/connection`。

## 工作原理

```mermaid
flowchart LR
    A[TypeScript Client] --> B[Connection Host]
    B --> C[Catalog decoder]
    C --> D[LLM / Session / Workspace / Interaction Gateway]
    D --> E[consumer-owned domain capability]
    E --> D
    D --> F[typed Outcome or frame]
    F --> B
    B --> A
```

Session list 合并 live Store 与 cold Persistence，history 对 live facts 取 detached snapshot、对 cold facts 取 validated inspection，再在 Gateway 内完成 wire pagination；两者只消费 projection capability，不拥有 projection fold。`session.create` 指定已有 cold identity 时触发 Agent resume。`session.rename` 把 raw title 交给 `sessiontitle.TitleService`；成功只映射 `{title, seq}`。Projection change 被映射为 `session/projection`，value 必须存在且是合法 JSON。

Workspace Gateway 将七个 `workspace.*` method 映射到 `workspace.Registry`/`Workspace`，并把 post-commit domain publication 投影为四个 Host frame。`session.create({workspaceId})` 只通过 Registry 读取 canonical path 并在创建后 accounting，不读取 Workspace SQLite 或复制 membership 规则。

`LLMGateway` 是持有 `LLMDirectory` capability 的状态对象。`llm.providers` 先按 configurable declaration 顺序投影 active/dormant route，再追加未声明但已注册的 route；`llm.models` 按 active provider 隔离目录失败并复用同一 `Catalog` 方法供 `session.models` 使用。它不保存第二份 LLM 状态，也不使用函数工厂复制源端 `buildModelCatalog()`。

`AgentPresetGateway` 持有可选的 `AgentPresetRoster` capability。当前默认组合未提供 roster，因此 `agentPreset.list` 按固定源 absent-provider 分支返回空列表并声明不可 author；这让原客户端隐藏 preset 控件而不把合法的无 preset 部署误报成失败。Preset 发现、Agent child scope composition、`agentPreset.select` 与 authoring API 尚未实现，空列表不代表这些能力已经完成。

## 上下游

- 上游：Connection dispatcher/event source、TypeScript wire requests。
- 下游：Agent Registry、Session Store、SessionPersistence、Workspace Registry、LLM、DefaultModel、SessionProjection、SessionTitle、Approval/UserQuestions。
- wire DTO 到此为止；下游包不依赖 RPC/frame 类型。

## 生命周期、错误、取消与背压

Gateway listener 和 hub 归 API Proxy Scope；subscriber 断开只取消该 stream，不取消 Agent Turn。显式 `session.cancel` 才进入 Agent cancellation。业务拒绝编码为 RPC error value，transport/内部故障返回技术 error。

每个 subscriber 有独立有序队列；Session event 的 high-water 只在 frame 成功生成后推进。已经 committed 的 Session/title/projection 派生通知不能因慢客户端或 frame 失败回滚。
