# Subagent 领域设计导航

状态：导航页

本文不再复制 Subagent 接口、状态机、调用链和实现目录，避免与重构后的代码形成第二份事实来源。

- 当前架构、职责、接口、关键数据结构和上下文契约见[Subagent 架构与生命周期重构技术方案](../../zh-CN/Subagent架构与生命周期重构技术方案.md)。
- 按模块实施顺序和 Gate 见[Subagent 重构实施方案](../../zh-CN/Subagent重构实施方案.md)。
- 当前代码模块、运行关系和失败语义见[Subagent 包说明](../README.zh-CN.md)。
- 固定 DeepSeek Harness 基线的源 owner 与兼容证据见[源能力分析](../../zh-CN/subagent/01-source-capability-analysis.md)。
- 当前分支的完成状态只记录在[Subagent 重构进度矩阵](../../zh-CN/Subagent重构进度矩阵.md)。全局进度文档待方案最终确认后再同步。

Parent-bound Subagent 尚未进入当前实现，其独立需求与待重审设计仍位于[需求文档](parent-bound-requirements.zh-CN.md)和[技术草案](parent-bound-design.zh-CN.md)。两份草案仍需按 common Execution、SeedBuilder 和 ChildDirectory 重新评审；不得用其中旧术语或 Proposed 内容解释当前 OneShot、Continuable 或 Agent 生命周期。
