# 通信机制与异步任务实现

状态：实现提案；API 基线 v4.5.3。[整体方案](./05-implementation-overview.md) · [业务协议](./02-capability-protocols.md)。

AgentLoop 直接使用 GoAkt 框架逻辑身份及以下异步 API，不新增系统请求入口。
组件响应返回配置/结果及版本，不返回业务对象；调用方可使用框架 GrainIdentity 寻址。详见[10](./10-runtime-and-initialization.md)。

## 1. 调用选择

Grain 与 PipeTo 是两个独立维度。Grain 决定稳定身份、状态 owner 与激活方式；PipeTo 决定如何
执行异步任务并回送结果。普通 Actor 和 Grain 都可以把外部请求放入 PipeTo 类任务，不能把它限制为文件 I/O。

异步请求有两种实现方式：

| 方式 | 执行位置与推进 | 适用与约束 |
| --- | --- | --- |
| Request + Then | 框架建立请求关联，响应在调用方消息处理上下文接续 | 直接跨 actor/grain 协作；业务最终回复需相应 Response/延迟回复机制 |
| PipeTo / PipeToSelf 包装请求 | task 执行 HTTP/SDK 请求，或在 task 内调用等待响应的 Ask 类接口；结果回 mailbox | 对调用方 actor 异步，task 自己可能阻塞；必须管理超时、取消、退出与资源预算 |

本方案保留已选的跨 owner Request 作为默认路径，PipeTo 用于普通异步调用和任务适配。这是协议选择，
不是 PipeTo 无法请求其他 actor。若要统一改为 task 内 Ask 再 PipeTo 回传，应作为通信方案变更单独评审：
它不阻塞调用方 mailbox，但仍包含任务级等待，且不能自动解决被调用方在完成 I/O 后如何回复的问题。
用户已明确 actor 不采用同步方案；上述 task 内 Ask 仅解释 PipeTo 的技术能力，不作为本提案的实现路径。


| 场景 | 调用方式 | 完成含义 |
| --- | --- | --- |
| Grain → Grain，需要结果 | RequestGrain + Then | 业务 owner 返回约定阶段结果 |
| Grain → 普通 Actor，需要结果 | RequestActor + Then | 目标明确回复；不是普通消息处理返回 |
| 普通 Actor → Grain，需要结果 | ReceiveContext.RequestGrain + Then | Grain 的 Response 或 GrainReply 完成请求 |
| 普通 Actor 单向消息 | Tell | 发送返回不能证明业务完成 |
| Grain 单向投递 | 经受管理发送任务或既有异步入口投递 | 不在 OnReceive/Then 内调用可能等待确认的 TellGrain |
| Grain 内部耗时工作 | PipeToSelf，返回关联完成消息 | task 返回后进入 mailbox，再由 owner 结算 |
| 事实通知 | TopicActor/PubSub 经消费者桥接 | 消费者可追赶；不能据此认定事务完成 |

禁止在 OnReceive/Receive/Then 中 Ask、SendSync、等待 channel、等待 task join 或等待另一个 owner 停止。
v4.5.3 的 ActorSystem.TellGrain 本地路径等待处理确认；名字中的 Tell 不等于可在回调里直接调用。
这个约束来自源码与超时堆栈，不能用“所有 Tell 都 fire-and-forget”的规则替代。

## 2. 延迟回复与后续状态推进

被调用 Grain 在原 OnReceive 内调用 DeferResponse，把 GrainReply 交给本次 operation 记录。
后续 I/O 完成消息或下游 Then 校验 operation 后更新状态，再通过句柄回复。句柄不传给外部工具 SDK。
不保存 GrainContext，不把 Grain 的字段交给 task 读写。

Then 本身运行在调用方消息处理上下文，可直接做短状态更新与延迟回复。业务模块只表达“请求、结果处理、
取消、查询”；需要新的消息处理上下文继续请求时，由发起方的局部适配负责接续，并管理任务终止。

实验已验证“Then 将一次结果交给 PipeToSelf，再回到新 OnReceive”的路径，但它只是 发起方局部适配候选，
不作为每个 Actor 的编程模板。采用前必须验证任务登记、超时、停止清理和完成投递失败；
任何接续任务都不能无限等待丢失的回调。该适配不创建第二套业务请求状态，不改变响应归属。

## 3. 消息与错误契约

消息类型由接收 owner 定义。传输携带业务 ID、实例 incarnation、operation/update ID 和必要 revision；
不是所有消息都塞进一个包含大量可选字段的通用 envelope。框架 correlation 只用于一次 Request，
业务 ID 用于超时后查询、幂等和过期拒绝。跨 Go 对象反向关联仅保留地址/ID，不循环嵌套对象。

| 结果 | 调用方动作 |
| --- | --- |
| 业务成功 / 业务拒绝 | 依据 typed result 推进或结算；保留 canonical 工具错误码 |
| 传输失败、超时、目标死亡 | 转入未知/失败状态，查询原 operation；不自动重放副作用 |
| 取消接纳 | 标记取消中；仍等待实际退出与最终结算 |
| 最终取消完成 | 确认任务已退出及资源收尾，才释放执行预算 |
| 过期/重复完成 | 拒绝推进新执行；已发送 Session 提交仍须查询结算，不能全部丢弃 |

进程内业务响应使用 owner 定义的结构体并携带 `Err error`，通过 GrainReply.Response 返回，保留错误类型及 errors.Is/As 包装链。
Then 的 error 参数表示框架请求失败，业务响应 Err 表示业务操作失败。工具既有协议结果码仍在外部边界按原契约映射，
不以字符串或新增枚举代替 Go 业务错误。GrainReply.Err 对普通 error 只保留文字，不保证其类型与包装链。
已完成对照验证，见[局部接续设计](./14-continuation-design.md)。此结论限定进程内消息，远程编码另行设计。

## 4. 工具 I/O 与取消

每个 operation 建立独立 context、取消函数和任务记录。PipeToSelf task 只捕获不可变参数与 context，
执行普通 SDK/文件/进程 I/O，返回携带 operation 身份的完成消息。文件读取的取消事实不等于 ReadFile
已中止；子进程需要 adapter 实际终止进程并等待退出。效果处执行权限检查。

首期将可预期业务错误编码进 typed completion。Grain PipeToSelf 原生错误会投递 StatusFailure，
它与普通 Actor PipeTo 的错误路径不同；不要依赖没有 operation 关联的通用失败去结算并发工具。
不用 PipeTo 超时冒充 task 已退出，不把超时后的迟到结果当成新操作。业务关闭先排空；异常取消明确传播。

## 5. PubSub 与背压

提议由普通 Actor 桥接持有 TopicActor 订阅，消费消息后异步转交对应 Grain。桥接只管理订阅、有限缓冲、
revision 合并及投递状态，不拥有工具目录或 Agent 状态。独立 Grain 直接订阅的可用性尚未验证，首期不依赖它。
初始化采用订阅确认、权威快照、revision 对齐；断线、溢出、缺版本后重新取快照。
Session 事实需要按日志序号补读，不能只保留最新事件。ToolsChanged 可以合并为最高版本通知。

接纳队列、活动 Request、任务、通知缓冲分别限额；用户流量不得占满全部控制/完成处理能力。
满载在入口拒绝或延迟接纳；已接纳 operation 的完成投递失败要显式上报并保留可查询记录。
优先级 mailbox 不能保证满队列仍能入队。具体容量、控制消息隔离实现由压力实验确定后实施，不宣称目前已保证不丢。

## 6. 框架停用适配

业务文档只使用“关闭中、关闭失败、关闭完成”，不直接暴露 GoAkt 停用消息。
GoAkt v4.5.3 的 `PoisonPill` 是框架消息：普通投递触发 Grain 的停用处理。
实验中 `Request(PoisonPill)` 超时，`TellGrain(PoisonPill)` 触发停用；后者可能等待处理确认，
必须由 生命周期 owner 的受管理任务调用，不能在 Actor 消息循环内等待。

旧实验使用 `Quiesced` 表示业务已排空；它不是本方案对外关闭结果，也不证明实例已经停止。
局部适配必须把框架调用结果交给生命周期 owner，由其结合资源释放和身份占用决定是否关闭完成。
最终停用确认与重复停用竞争仍需新增测试，不能仅凭这些框架名称声称行为已经实现。
