# 冻结归档全量重放准备

此命令只读取当前授权数据库归档，为正常运行控制创建一个新的 pending 观察轮次；不采集、不调用模型、不清空或归档 Workflow/记忆/历史，不启动服务，不自动激活队列。所有 SQL 在 dbbridge，命令不隐式迁移数据库。

```bash
# 使用既有进程环境中的 DATABASE_URL，不回显凭据。
go run ./cmd/archive-replay --key backend-flash-full-20261115 --output ../.build/backend-flash-full-20261115/replay
# 审阅 inventory.private.json，再显式执行；旧 queued 会完整备份并退回 pending。
go run ./cmd/archive-replay --key backend-flash-full-20261115 --output ../.build/backend-flash-full-20261115/replay --execute --park-queued
```

默认 dry-run 使用 REPEATABLE READ READ ONLY，没有数据库写入；输出目录0700、清单0600。清单包含全部当前 active/enabled 连接且用户未禁用的 platform_records，以及当前授权 DWS 群内消息和已归档附件元数据。附件正文不会进入事件；平台原快照字段（包括 source_content）保留，仅追加 archive_replay 标记，实际 capture 继续使用既有敏感字段清洗。operation=created 表示重放首次观察，occurred_at 保留原记录 observed_at，不能解读为刚发布。正文及课/群范围不按最近150条概览截断。

execute 先锁运行控制批次和运行审计表，拒绝任何 running 批次/Agent/控制Run；有旧 queued 且未指定 --park-queued 时拒绝。旧 queued 不删除、不重新封批，其内容、执行阶段、已有结果和事件ID保留在不可覆盖的 prepared.private.json，备份 fsync 成功后才允许事务提交。旧队列只退回 pending 并记录 ARCHIVE_REPLAY_PARKED_QUEUED 原因；终态和既有已执行业务记录不更改。

新轮次键由显式 key 确定，封批和seed只处理该 round，使用原课程/群分组及大小限制；blocked超大对象保留，不偷偷丢失。相同 key 重复调用读取首轮冻结清单与现有批次状态，不追加事件、不再停其他队列。提交回执未知时必须复用同 key 查询，不能换 key 重试。

创建结果仍为 pending（超限为 blocked）。调用方应在核对 freeze_count、来源计数、batch scope、旧queued备份后，只激活这个 round；命令不会吸纳历史 pending/queued 或重置原记录、连接checkpoint、Workflow、记忆。控制台通过正常 control_rounds/batches/items 可见，不另建私有执行队列。

测试使用专用 TEST_DATABASE_URL 随机schema，覆盖只读计数、授权群隔离/课程分组、round限定、不丢source_content与观察时间、park备份、幂等、备份失败整事务回滚、running拒绝；不使用正式执行或上游模型。
