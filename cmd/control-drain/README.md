# 部署前只暂停未领取批次

```bash
go run ./cmd/control-drain --execute --round replay_<32hex> --output-dir ../.build/control-drain-<new-directory>
```

只装配dbbridge.Open，不迁移、不调用模型、不采集、不终止任务。DATABASE_URL来自既有进程环境。--execute和准确round必填，output-dir必须新建，权限0700。

短事务持有control_batches EXCLUSIVE表锁，精确选定目标round的queued批次。将完整原批次（ID/status/stage/attempt/error/changes/trigger/filter等）保存为0600 before.private.json并fsync文件和目录；备份失败回滚且队列不变。备份完成后只将选中的queued改为pending，error_code为DEPLOYMENT_DRAIN，其他字段不变。running和其他round不动。commit后另写result.private.json回执；若commit应答未知或回执写失败，先看数据库及备份，不盲重跑/恢复。

现有control-run会继续处理在途running，但无新queued可领；待running归零后由主调用方停止旧managed进程、部署新版，再通过正常管理API激活pending。此命令不自动等待、kill、恢复或Activate。所有业务结果和已提交写入保持原样。
