# Subagent 领域设计导航

状态：导航页

本文不再复制 Subagent 接口、状态机、调用链和实现目录，避免与重构后的代码形成第二份事实来源。

- 当前代码模块、运行关系和失败语义见[Subagent 包说明](../README.zh-CN.md)。
- 固定 DeepSeek Harness 基线的源 owner 与兼容证据见[源能力分析](../../zh-CN/subagent/01-source-capability-analysis.md)。
- 当前实现和验证证据只记录在[08 实施进度](../../zh-CN/08-implementation-progress.md)。

Bound Subagent 已进入结构实施：当前代码已有独立 Mode、具体 `BoundStartCommand`、descriptor codec/projection/listing、`internal/bound.Service` 和 Plugin 装配。`bound.Service.Start` 仍是返回明确错误的 TODO；binding/config events及projection、parent observer、child 创建恢复与运行期语义尚未实现。其[需求文档](parent-bound-requirements.zh-CN.md)、[技术设计](parent-bound-design.zh-CN.md)和[Bound Config 管理详细设计](bound-config-management.zh-CN.md)记录完整目标，不能用来声称当前已有可运行 Bound child。
