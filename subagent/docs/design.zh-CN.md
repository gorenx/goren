# Subagent 领域设计导航

状态：导航页

本文不再复制 Subagent 接口、状态机、调用链和实现目录，避免与重构后的代码形成第二份事实来源。

- 当前架构、职责、接口、关键数据结构和上下文契约见[Subagent 架构与生命周期重构技术方案](../../zh-CN/Subagent架构与生命周期重构技术方案.md)。
- 按模块实施顺序和 Gate 见[Subagent 重构实施方案](../../zh-CN/Subagent重构实施方案.md)。
- 当前代码模块、运行关系和失败语义见[Subagent 包说明](../README.zh-CN.md)。
- 固定 DeepSeek Harness 基线的源 owner 与兼容证据见[源能力分析](../../zh-CN/subagent/01-source-capability-analysis.md)。
- 当前分支的完成状态只记录在[Subagent 重构进度矩阵](../../zh-CN/Subagent重构进度矩阵.md)。全局进度文档待方案最终确认后再同步。

Bound Subagent 已进入结构实施：当前代码已有独立 Mode、具体 `BoundStartCommand`、descriptor codec/projection/listing、`internal/bound.Service` 和 Plugin 装配。`bound.Service.Start` 仍是返回明确错误的 TODO；binding event/config source、parent observer、child 创建恢复与运行期语义尚未实现。其[需求文档](parent-bound-requirements.zh-CN.md)和[技术设计](parent-bound-design.zh-CN.md)记录完整目标，不能用来声称当前已有可运行 Bound child。
