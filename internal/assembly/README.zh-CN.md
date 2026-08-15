# Server Assembly 子模块

`internal/assembly` 是服务端 composition root：把静态链接的 Plugin Factory、typed config 与 consumer/provider dependency 连接起来。权威设计见[`zh-CN/09-plugin-runtime-and-server-assembly.md`](../../zh-CN/09-plugin-runtime-and-server-assembly.md)。

## 职责

- 注册 shipped Factory Catalog；
- 严格解码每个插件的 typed config；
- 声明 `Provides/Requires/Optional`；
- 构造 Provider/Consumer 并通过 Plugin Runtime 结算；
- 将根级 `web.Site` 作为私有 `webFrontend` Service 接入默认 Connection；
- 组合默认 server slice，失败时反向卸载本次已声明插件。

本包不拥有任何 domain 规则、wire codec、存储逻辑或动态脚本 evaluator。`!!js` 与 Typert generator 不进入 composition。

## 工作原理

```mermaid
flowchart TD
    A[PluginSpec list] --> B[Factory Catalog]
    B --> C[strict typed config]
    C --> D[Runtime.Load]
    D --> E{required services active?}
    E -- no --> F[waiting]
    E -- yes --> G[Apply and Provide]
    G --> H[reconcile dependents]
    I[any load failure] --> J[unload accepted handles in reverse]
```

Session Projection 提供 `sessionProjections`；Session Title 消费 `sessions` 与 `sessionProjections` 后提供 `sessionTitle`；Session Persistence 插件消费 `sessions` 并提供 `sessionPersistence`；Workspace 插件消费 `sessions` 与 `sessionPersistence` 并提供 `workspaceRegistry`。Web 插件提供 `webFrontend`，默认 Connection 同时等待它和 `apiProxy`。Agent Loop 和 API Proxy 再按需消费其他 capability。Specs 可以故意乱序，依赖结算不能依赖文件或列表顺序。

默认 CLI 的 `--data-dir` 统一指定数据目录，Session 与 Workspace 默认分别使用其中的 `sessions.sqlite` 和 `workspaces.sqlite`；`--session-db`、`--workspace-db` 只作为具体数据库路径覆盖。根级 `Makefile` 的 `make run` 先构建 Web，再以 `DATA_DIR` 启动服务，`DATA_DIR` 默认是当前仓库目录。`SessionPersistenceConfig` 与 `WorkspaceConfig` 属于能力插件的 typed config；对应 Factory 在 composition root 内构造 SQLite `Backend` adapter，再分别连接 `SessionLogStore` 与 `DurableRegistry`。SQLite 不拥有 Factory、Manifest、Service key 或单独插件生命周期，assembly 也不实现存储、recovery 或 Workspace 业务规则。

## 上下游与生命周期

- 上游：`cmd/goren` 提供进程环境和 PluginSpec。
- 下游：`plugin.Runtime` 以及各 package 的构造函数。
- Factory 只做 config 与 wiring；业务 interface 由 Consumer/Definition package 拥有。

插件 Scope 负责 effect、service 和 listener 释放。Load transaction 中任一 create/load 失败都会反向卸载已经接受的 handle；运行时替换或 shutdown 仍由 Plugin Runtime 按依赖方向停止。
