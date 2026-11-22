# 受控归档批次执行

本命令只处理已准备、封批并由调用方显式激活的一个round。它不迁移数据库、不Activate、不Recover旧任务，不启动HTTP、platform或DWS采集，不修改连接或正式模型settings。要求现有数据库已完成迁移，正式Agent和provider设置已安装。

```bash
go run ./cmd/control-run --execute --round replay_<32hex> --output-dir ../.build/control-run-<new-directory> --trace
```

凭据仅从DATABASE_URL和CREDENTIAL_KEY进程环境装配。模型默认、超时、重试、并发沿用正式加密settings；没有命令级覆盖。真实日程服务通过schedule.New(db.Schedules(),bridge.Demo{})装配，Worker仍受自身工具权限、用户授权和Schema约束；没有主动通知投递端口。

启动前读全量SQL聚合，拒绝其他round queued、任何running、未封事件或指定round pending。必须先由调用方激活目标轮次。执行仓库嵌入原dbbridge controls保留items/artifacts可选接口，Claim按round限制，即使之后出现别轮queued也不会误领；15秒巡检发现队列范围变化会停止本命令。Heartbeat只续当前round当前实例租约，不回收其它round陈旧状态，Seal不全局封批。

runcontrol.New安装原Observer，因此control_runs、items和artifacts在同一正式数据库保存，既有2611 API_ONLY管理页可以查看。API_ONLY不执行采集或队列。独立进程中的运行取消不共享内存active map，管理页不能保证取消本命令正在执行的batch；用管理作业SIGTERM/SIGINT停止命令，context取消会等待当前服务退出并保留真实提交审计。

每15秒打印状态计数，无私人正文/密钥；output-dir必须新建，目录0700。preflight.private.json和progress.private.json为0600，进度报告原子替换。--trace可记录按真实Agent Run ID分组的模型请求/响应和usage（含私人归档内容、无配置密钥），不得公开。完成条件为指定round所有批次terminal/blocked，且无queued/running/pending，退出不代表所有业务任务成功；需看succeeded/partial/failed/blocked及产物。
