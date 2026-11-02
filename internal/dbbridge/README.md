# db-bridge v0.1

本包是后端唯一 PostgreSQL 实现。`model.UserRepository` / `model.DWSRepository` 定义业务端口；users、dws、HTTP 层不能直接导入 pgx 或执行 SQL。

启动顺序：`Open(ctx, DATABASE_URL)` → `Migrate(ctx)` → `Users()/DWS()` 注入服务 → 进程结束 `Close()`。Open 验证连接但不修改 schema，连接超时5秒，最多10连接。Migrate 使用事务 advisory lock 串行执行版本账本和 DDL；后续 schema 变更须增加新版本，不能更改已应用版本来升级。

用户包含用户名、显示名、bcrypt 哈希、admin/user、禁用和时间。Repository 保护最后可用管理员；密码/角色/禁用变更原子撤销会话。session 仅保存SHA256，签发可要求 ExpectedPasswordHash，防止密码重置竞态。禁用用户同时失效其 DWS 租约并取消未完成任务。

DWS 每次操作必须传可信 `DWSScope{UserID,ConnectionID}`；所有查询按该范围限定。`ListPollTargets` 仅用于受控 Worker，不能开放给客户端。学校统一账号 mode 预留但目前 unsupported。首次官方授权可在全空身份且无凭据的 pending 连接上原子绑定身份、密文及 active；已绑定身份不可换号。CreatePendingConnection 在用户行锁下复用空 pending，CompleteLink 同事务验证用户状态、连接及租约 fencing。

连接级租约在不同进程间排他，token 来自单调序列。`AcquireLease(scope,"",ttl)` 支持认证及同连接多群串行；CommitSync 再检查授权群。断开、替换群范围、用户禁用均使旧 token 失效，恢复权限也不会恢复旧能力。旧任务失败使用 `FailLeasedJob`；非fenced `FailJob` 只可结束尚未执行的queued入队失败。

迁移v4后，平台记录和DWS消息都只保留同对象最新状态：platform唯一键为user/connection/kind/external_id/parent_id，DWS消息唯一键为user/connection/group/external_id。内容相同不写业务行，变化原位更新并保留稳定ID。CommitSync同事务保存最新消息、工作区密文、完成水位和job succeeded；任何验证/写入失败全部回滚。历史迁移按observed_at/id保留最新现存记录并迁移附件引用；不保存旧版本。当前完整时间窗提交，暂不持久化每页 CLI continuation。消息批最多10000且总16MiB，每消息1MiB；工作区密文最多21MiB；附件8MiB。

业务变更通过独立trigger.Handler端口在提交后通知，默认Noop；SetTriggerHandler可供未来Agent注入。平台业务记录和DWS附件新增/变更可触发，DWS消息及所有运行状态不触发；重复内容/回滚不触发。通知为best effort，无事件队列或模型调用，handler错误不改变已提交数据库结果。

测试仅在显式 TEST_DATABASE_URL 时连接单独测试库，自动创建/清理随机schema。默认测试不读取 DATABASE_URL、不连真实上游。已在独立PostgreSQL运行迁移幂等、用户保护/会话撤销、跨owner、事务回滚、消息去重、scope fencing、撤群再恢复、禁用再启用和旧任务失败拒绝测试。

## Agent Framework 存储（v5）

`DB.Agents()` 实现框架 Repository；`DB.AgentData()` 提供固定 `platform_records` 字段白名单的只读投影。所有身份查询按 owner 范围，私有记忆进一步按 agent 范围。v5 新建 agent_instances、agent_memories、agent_workflows、agent_workflow_versions、agent_runs；沿用原迁移账本与连接池，没有第二套数据库基础设施。Workflow 定义与版本快照同事务提交，parent 同 owner 且创建后不可变。工具不会访问会话、凭据或任意表。完整结构和验证见 [框架文档](../../../agent/framework/README.md)。
