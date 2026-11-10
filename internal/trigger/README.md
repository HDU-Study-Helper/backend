# trigger：业务变更到 Agent 的预留接口

本模块定义进程内 `Handler.Handle(ctx, Change) error`，为未来 Agent 消费业务变化提供单独边界。本次没有数据库原生触发器、模型调用、独立Worker、事件表或消息总线。默认处理器 `Noop` 不执行任何操作。

## 事件范围

- 学习通、MOOC、U校园来源记录：course、schedule（教务课表）、assignment、exam、progress、notice、discussion、discussion_reply_user、material（仅资料目录）。
- DWS 附件业务变化。
- **DWS message 暂不触发**，等待消息权重/处理策略确定。
- 用户管理、登录会话、Cookie、Token、CLI工作区、游标、租约、同步任务和抓取状态不作为模型业务事件发布。

记录真实插入产生 `created`，同对象业务内容更新产生 `updated`。相同内容重扫不写业务行，不触发。`deleted` 在协议预留，当前不会因为某次同步没看到对象就删除数据或发删除事件；尚无统一上游删除检测。

## 接线与事务语义

`app.NewWithTrigger(ctx, cfg, handler)` 是未来Agent的组装入口；普通 `app.New` 注入 `trigger.Noop{}`。底层由 `dbbridge.DB.SetTriggerHandler` 接收处理器，只在数据库事务成功提交后调用。事务失败或回滚不调用；处理器失败不将已提交数据伪装成数据库失败。

调用为同步、尽力通知，带短context截止时间；处理器必须尊重取消并迅速返回，不能在其中直接进行长时间模型推理。Go context不会强制终止忽略context的函数。没有可靠队列、重试或重放，进程在提交后崩溃可能丢失通知。将来正式接Agent时，由接入层决定是否另加持久化任务或事务outbox，本次不提前建设。

## Change 字段

| 字段 | 用途 |
|---|---|
| UserID / ConnectionID | 服务端确定的用户与连接范围 |
| Source / Resource | 来源和资源类别 |
| RecordID | 已落库最新状态记录的ID |
| ExternalID / ParentID | 原生对象和父对象身份 |
| Operation | created / updated / deleted |
| ChangedFields | 发生变化的顶层业务JSON键；如 fields、title |
| OccurredAt | 变更通知时间 |

事件不携带Cookie、Token、会话、历史正文或任意SQL。未来Agent应使用绑定该用户的只读业务接口按记录ID取当前内容，并重新校验归属。当前尚未实现Agent读取工具。

`FieldChanges` 忽略 observed_at，避免采集时间本身产生模型事件；保留JSON大整数精度。事件只描述当次存储变更，不保证后续读取时对象没有再次更新。

验证：`go test ./internal/trigger`；提交后触发、回滚不触发及更新去重的集成验证位于dbbridge测试中。
