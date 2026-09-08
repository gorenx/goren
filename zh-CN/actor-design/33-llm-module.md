# LLM 模块实现方案

本文以 [LLM 模块 README](../../llm/README.zh-CN.md)中的最终对象职责和调用关系为实现依据。业务与框架分离，允许删除旧入口，不保留兼容 Actor。

## 最终职责与契约

LLM Grain 统一编排 modelconfig.Catalog、llm.Registry、llm.Configuration。三个业务对象独立实现规则，不相互持有反向引用。Grain 处理 InitializeModels、ReadModels、ReplaceModels、ResolveAgent、注册命令及 Set/Read/RemoveConfiguration。ReadCall 在一次 mailbox 处理内返回执行配置与固定 Binding；Provider I/O 在 Worker 异步任务执行。

Catalog 负责配置来源、版本和覆盖；Configuration 仅保存 AgentID 对应的执行配置。Agent 关闭不删除配置，下次可复用；持久化不在本次内存保留语义内。显式删除使用 RemoveConfiguration。

不存在独立 ModelConfig/Configuration/Registry Grain，也不保留 Discovery 注册、ModelDiscovery 接口或 DiscoverModels。配置内的查询使用 Catalog.Read，远端模型列表复用 Provider ListModels。

Router 固定分发到 Worker；生成操作按 CallID 路由，查询按 RequestID 路由。Worker 只保留 Call、正在执行的 I/O 与取消/关闭信息，规则委托 Call/Binding。生成结果直接回复 AgentLoop，API 查询关联归 apiproxy/modelquery，不存在 LLM QueryGrain。

## 生成时序

Generate 内部读取调用快照、解析模型参数并打开流，返回 GenerationStarted；没有外部 Prepare/Open 两阶段协议。AgentLoop 提交实际请求参数后读取首块，每块提交 Session 确认后再组装与读取下一块。CloseCall 成功后提交完整 AssistantMessage。Session 内存提交与 Flush 持久化确认分开，API 消费 Session 提交通知。

## 错误与生命周期

同一 Call 只派发一次、不能并发读取与关闭；取消确认不等于关闭。失败关闭保留资源用于重试。CallID/RequestID 隔离迟到结果。操作回复等待有界，超时请求关闭同一 Call，不重复 Generate。Worker 复用，不创建每次调用专属 Actor。

## 验证与剩余工作

必须验证配置覆盖与版本错误、调用快照隔离、固定 Worker 复用、逐块 Session 提交屏障、取消与关闭重试、回复超时和旧回复隔离、模型查询并发及取消传播。LLM、AgentLoop 和 API ModelQuery 定向测试已通过。API 下发接线、Compaction、生产装配、完整 Turn 主驱动和异常停机回收仍未完成。
