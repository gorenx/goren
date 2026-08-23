# Web 自有测试问题记录

状态：Resolved Findings

本文记录 Web 自有测试实际暴露的问题、根因、结构性修复和证据。它不是新的行为规范；Web 能力边界仍由[Web Agent 主会话设计](../../zh-CN/21-web-agent-main-flow.md)拥有，包内运行方式见[Web 包说明](../README.zh-CN.md)。对应代码提交为 `9cb505e`。

## 1. 重放事件被重复应用到流式草稿

现象：Mux 将同一 `seq` 的 `assistant/chunk` 重放两次时，Session event window 只保留一条事件，但页面草稿从 `hello` 变为 `hellohello`。

根因：`ConversationStore` 分别维护 event window 和 stream draft。前者按 `seq` 去重，后者却对每个到达帧执行增量拼接；两份可变状态没有共享同一个事实边界。

解决：由 [`mergeEvents`](../src/session-events.ts) 生成按 `seq` 唯一且有序的 Session event window，再由 [`projectStream`](../src/session-stream.ts) 从该窗口重建草稿。实时重放和 history baseline 因此使用同一个投影输入，不再各自修改草稿。

证据：[`conversation-store.test.ts`](../src/conversation-store.test.ts) 的 replay 用例和 [`session-stream.test.ts`](../src/session-stream.test.ts) 的纯投影用例。

## 2. History 响应覆盖同时到达的实时事件

现象：选择 Session 后，`session.history` 尚未返回时可以收到 Mux `session/event`；旧实现随后用 history 响应整体替换该 Session 的浏览器 event window，导致刚收到的实时 chunk 消失。

根因：异步 history request 建立了读取起点，却没有在提交响应时与当前实时窗口合并。`selectionVersion` 只阻止旧 Session selection 写回，不能处理同一 selection 内的 baseline/live 竞态。

解决：history 返回后，将响应事件与 `await` 期间已进入 Store 的当前事件按 `seq` 合并，再一次性提交 event window 和 stream projection。该处理保留 Session event 的不可变 sequence identity，没有引入第二套时间戳或补偿路径。

证据：[`conversation-store.test.ts`](../src/conversation-store.test.ts) 的 history/live 并发用例先阻塞 history、注入实时 chunk，再释放响应并验证 chunk 仍可见。

## 3. 测试源码污染生产 Tailwind 产物

现象：只增加 `*.test.tsx` 后，生产 CSS 多出 `.container`、`.static` 等未被页面使用的 utility，CSS 内容和哈希发生变化。

根因：Tailwind v4 自动扫描 `src`，把测试中的变量名和字符串也当作候选 class；测试代码错误进入生产样式的 source boundary。

解决：[`styles.css`](../src/styles.css) 显式排除 `*.test.ts`、`*.test.tsx` 和 `src/test`。重新构建后 CSS 恢复原内容哈希 `app-Cs8NWEXP.css`，只有实际 Store 实现变更刷新 JavaScript asset。

证据：连续生产构建以及 [`Site` 组件测试](../site_test.go)验证内嵌哈希资源和 cache policy。

## 4. 新增 Web 自有验证层

此前 `web` 只有 TypeScript build 和仓库级 JSDOM UI contract，没有包内可独立运行的测试命令。现在 `pnpm run test` 使用 Vitest/JSDOM，测试按 owner 分散在六个文件中：

- `HarnessAPI` unary correlation 和 `/api/respond` envelope；
- Session event 合并与 stream projection；
- `ConversationStore` 启动、重放、history/live 竞态、历史恢复和 Question answer；
- Composer 受控输入、同步重复提交门控和 IME；
- QuestionCard 必答校验与结构化答案。

本轮执行：

```text
(cd web && pnpm install --frozen-lockfile)
(cd web && pnpm run test)     # 6 files, 18 tests
(cd web && pnpm run typecheck)
(cd web && pnpm run build)
go test ./web ./tests/architecture
go test ./...
go test -race ./...
go vet ./...
go build ./...
git diff --check
```

以上命令通过。服务端 subagent 的独立证据见[服务端测试问题记录](../../subagent/docs/server-test-findings.zh-CN.md)。

## 5. 尚未覆盖

- 当前环境没有可连接的真实浏览器实例，因此没有完成视觉、布局和原生键盘行为验收；JSDOM 不替代该层。
- 按当前测试范围，本轮没有运行 Web 到 Go 服务端的 UI contract，也没有据此宣称 subagent 的 Web 端到端通过。
- [权威 Web 设计](../../zh-CN/21-web-agent-main-flow.md)写的是 `turn/end` 删除 draft，而当前实现及[包说明](../README.zh-CN.md)保留无 committed assistant message 的中断草稿。本轮保持既有运行行为，没有在测试修复中替用户决定这项可见语义；确认后应统一设计、实现和用例。
