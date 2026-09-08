# LLM 模块实现方案

本文以 [LLM 模块 README](../../llm/README.zh-CN.md)中的最终对象职责和调用关系为实现依据。业务与框架分离，允许删除旧入口，不保留兼容 Actor。

## 最终职责与契约

LLM Grain 统一编排 modelconfig.Catalog、llm.Registry、llm.Configuration。三个业务对象独立实现规则，不相互持有反向引用。Grain 处理 InitializeModels、ReadModels、ReplaceModels、ResolveAgent、注册命令及 Set/Read/RemoveConfiguration。ReadCall 在一次 mailbox 处理内返回执行配置与固定 Binding；Provider I/O 在 Worker 异步任务执行。

Catalog 负责配置来源、版本和覆盖；Configuration 仅保存 AgentID 对应的执行配置。Agent 关闭不删除配置，下次可复用；持久化不在本次内存保留语义内。显式删除使用 RemoveConfiguration。

不存在独立 ModelConfig/Configuration/Registry Grain，也不保留 Discovery 注册、ModelDiscovery 接口或 DiscoverModels。配置内的查询使用 Catalog.Read，远端模型列表复用 Provider ListModels。

Router 固定分发到 Worker；生成操作按 CallID 路由，查询按 RequestID 路由。Worker 只保留 Call、正在执行的 I/O 与取消/关闭信息，规则委托 Call/Binding。生成结果直接回复 AgentLoop，API 查询关联归 apiproxy/modelquery，不存在 LLM QueryGrain。

## 生成时序

Generation 内部读取调用快照、解析模型参数并打开流，返回 GenerationStarted；没有外部 Prepare/Open 两阶段协议。AgentLoop 提交实际请求参数后读取首块，每块提交 Session 确认后再组装与读取下一块。CloseCall 成功后提交完整 AssistantMessage。Session 内存提交与 Flush 持久化确认分开，API 消费 Session 提交通知。

## 错误与生命周期

同一 Call 只派发一次、不能并发读取与关闭；取消确认不等于关闭。失败关闭保留资源用于重试。CallID/RequestID 隔离迟到结果。操作回复等待有界，超时请求关闭同一 Call，不重复 Generation。Worker 复用，不创建每次调用专属 Actor。

## 验证与剩余工作

必须验证配置覆盖与版本错误、调用快照隔离、固定 Worker 复用、逐块 Session 提交屏障、取消与关闭重试、回复超时和旧回复隔离、模型查询并发及取消传播。LLM、AgentLoop 和 API ModelQuery 定向测试已通过。API 下发接线、Compaction、生产装配、完整 Turn 主驱动和异常停机回收仍未完成。

## 辅助生成

压缩摘要具有自身 provider/model/maxTokens，不能写入或临时覆盖 Agent 的执行配置。Generation.Settings 选择 Parameters 时显式携带本次完整参数，不调用 SetConfiguration；选择 AgentConfig 时读取该 Agent 的已登记配置。

两种配置选择共用 Router、Worker 活跃 Call 表和 GenerationStarted/ReadChunk/CancelCall/CloseCall 协议。辅助调用按 CallID 路由，Worker 选择指定 Provider Binding 后调用同一 NewCall/Open 业务路径；不存在第二套生成 Runtime 或临时配置身份。Parameters 在接收时复制，校验使用业务 ValidateCallConfig；结果返回实际参数。验证必须覆盖辅助模型与 Agent 已存模型不同、调用结束后 Agent 配置不变、读取与关闭仍复用原 Worker。

### 生成与配置选择

生成统一使用 Generation。普通对话和压缩摘要属于调用用途，不拆成不同生成消息。Generation 持有 Delivery、Input 和 Settings；Settings 是封闭的配置选择接口：AgentConfig 标识已登记配置的 Agent，Parameters 是本次调用的完整参数。两种选择互斥，不能同时传 AgentID 与临时参数，也不使用 any 或两组可选字段。

Worker 根据配置选择读取同一 LLM owner 的配置与 Binding，或仅选择本次参数的 Binding，随后进入同一个 llm.Call 流程。本次参数不写入已登记 Agent 配置；LLM 不识别或硬编码 Compaction 用途。删除旧生成类型及兼容分支，保留 CallID 路由、RequestID 关联、GenerationStarted 和流读取/关闭协议。该内部 Go 契约变化不改变客户端协议和 Session 持久化字段。
