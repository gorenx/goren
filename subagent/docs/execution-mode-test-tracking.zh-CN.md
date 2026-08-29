# OneShot 与 Continuable 集成测试跟踪

> 状态：完成。最后复验：2026-08-28。本文只记录OneShot与Continuable纵向测试范围和结果；Bound测试见[Bound测试跟踪](./bound-test-tracking.zh-CN.md)。

## 1. 验证边界

测试使用真实Plugin Runtime、AgentLoop、Subagent Service、delegation/control/report Tool、Agent Registry、Session LiveStore和SQLite Session持久化。LLM transport由可控adapter替代，用于稳定注入正常响应、模型错误和阻塞请求。

纵向链路包括：parent执行delegation Tool、child构造与执行、child接收parent后续消息、child report或terminal settlement进入parent、child变cold或被释放，以及parent/Runtime关闭时的取消传播。

## 2. 新增测试矩阵

| ID | 模式 | 行为 | 测试 | 当前证据 |
| --- | --- | --- | --- | --- |
| ONS-I01 | OneShot | foreground模型错误返回Tool failure、发布`StopError`并释放child | `TestForegroundOneShotModelFailureReleasesChild` | 通过 |
| ONS-L01 | OneShot | parent关闭取消阻塞中的foreground child，Tool返回失败且不遗留Agent/Session | `TestForegroundOneShotStopsWhenParentCloses` | 通过 |
| CON-C01 | Continuable | 两个并发`send_message`只恢复一个cold epoch，消息按Inbox执行后共同settle | `TestConcurrentMessagesShareOneColdContinuableEpoch` | 通过 |
| CON-L01 | Continuable | parent关闭取消阻塞child，持久化终态并释放全部live Agent/Session | `TestContinuableChildStopsWhenParentCloses` | 通过 |

已有纵向证据继续覆盖：

- OneShot正常foreground输出与释放：`TestForegroundOneShotRunsChildAndReleasesIt`；
- OneShot向parent报告：`TestOneShotChildCanReportThroughItsParentAgent`；
- Continuable首次执行、持久化和变cold：`TestContinuableToolPersistsCompletedChildForLaterResume`；
- cold resume与parent settlement：`TestSendMessageResumesColdContinuableChild`；
- report、interrupt和Runtime/Plugin shutdown：`TestReportExtensionDeliversChildSelectedContentToParent`、`TestInterruptAgentCancelsCurrentTurnAndKeepsQueuedFollowup`、`TestReportExtensionReleasesResidentInstallationDuringRuntimeShutdown`、`TestSubagentPluginUnloadRequestsResidentChildClosure`。

## 3. 本轮结论

新增OneShot与Continuable用例未发现各自Mode owner的新缺陷。默认装配测试发现的delegation seed职责错误同时影响三个模式，根因、修复和证据统一记录在[Bound测试跟踪](./bound-test-tracking.zh-CN.md)的BND-D04。

## 4. 验证记录

以下结果来自2026-08-28的当前工作树：

| 验证 | 命令 | 状态 |
| --- | --- | --- |
| 新增用例重复运行 | `go test ./subagent -run '<新增用例>' -count=20 -timeout=120s` | 通过 |
| 新增用例race重复运行 | `go test -race ./subagent -run '<新增用例>' -count=5 -timeout=120s` | 通过 |
| Subagent全套重复运行 | `go test ./subagent -count=10 -timeout=180s` | 通过 |
| Subagent全套race重复运行 | `go test -race ./subagent -count=3 -timeout=240s` | 通过 |
| 全量测试 | `go test ./...` | 通过 |
| 全量race测试 | `go test -race ./...` | 通过 |
| 静态检查 | `go vet ./...` | 通过 |
| 全量构建 | `go build ./...` | 通过 |
| 格式与空白 | `go fmt ./...`、`git diff --check` | 通过 |
| 文档链接 | 本轮变更Markdown的相对文件链接检查 | 通过 |

本文只记录实际执行结果；模拟LLM transport的集成测试不等同于真实provider验收。
