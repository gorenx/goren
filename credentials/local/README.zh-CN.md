# Local Credentials LiveStore

`credentials/local` 是 `credentials.LiveStore` 的 owner-only JSON 文件适配器。详细安全边界见[22 Credentials 与 API Key 管理](../../zh-CN/22-credentials-and-api-key-management.md)，实施证据见[08 实施进度](../../zh-CN/08-implementation-progress.md)。

## 职责

- 校验绝对文档路径和已有文档权限；
- 执行 `Load`、`Save`、`Delete` 文件 I/O；
- 使用同目录临时文件、`Sync` 和 rename 原子替换；
- 拒绝非法 JSON、非法凭据引用、空值和过宽文件权限。

本包不读取进程环境，不决定凭据优先级或可写性，不实现 Plugin、Provider 或 Factory。

## I/O 流程

```mermaid
flowchart LR
    Manager[credentials.Manager] -->|credentials.LiveStore| Store[local.LiveStore]
    Store -->|read and validate| File[owner-only JSON]
    Store -->|write temp sync rename| File
```

取消会在文件操作前返回。同一 LiveStore 实例使用 mutex 串行化 read-modify-write；当前不承诺跨进程 writer lock 或 watcher。
