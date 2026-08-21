# System Prompt

`systemprompt` 是模型输入中的 Prompt、runtime context、变量和 Tool schema assembly owner。详细契约见[11 System Prompt Registry 与 Assembly](../zh-CN/11-system-prompt-registry-and-assembly.md)，Plugin 生命周期和 overlay 子树见[09 Plugin Runtime 与 Server Assembly](../zh-CN/09-plugin-runtime-and-server-assembly.md)。

## 边界

本包提供 `Assembler` 只读能力和 `PromptRegistry` 精确变更能力。拥有 entry 的 Plugin 保存 `PromptHandle`，在 Dispose 中调用 `Unregister`；System Prompt 不提供按名称删除或公开 Scope 清理 API。Tools、Agent Loop、LLM、Session 和模板脚本不属于本包。

## 流程

```mermaid
flowchart TD
    Factory[Factory strict Config] --> Root[Root Registry Plugin]
    Root --> Layer[promptStore exact layer]
    Overlay[Overlay Registry Plugin] --> Layer2[Child exact layer]
    Overlay --> Parent[Require ancestor Assembler]
    Caller[Agent Loop] --> Assemble[Assembler.Assemble]
    Assemble --> Snapshot[root to current layers]
    Snapshot --> Waterfall[typed system-prompt/assemble Waterfall]
    Waterfall --> Result[detached PromptAssembly]
```

Store 只保护 entry membership；provider 求值和 Waterfall 在锁外执行。每次 assembly 固定 provider snapshot，执行失败返回整体错误，不返回半成品。complete section、suppression、Tool order 和 strict `{{name}}` 插值由本领域 owner 最终校验。
