# 计划任务与运行控制

`app` 管理采集时钟与本服务生命周期。采集提交时，`dbbridge` 在同一业务事务内保存脱敏前后快照。v15起，学习通与中国大学MOOC刷新完成后按轮次、用户、来源连接、所属课程开课实例分批；同课跨资源变化一起交给Trigger。学习通课程＋教学班、MOOC课程＋所属期次为身份，SPOC引用源期次不替换所属期次。同名、不同连接、不同教学班/期次不合并。无可靠课程归属的信息按连接单列为unassociated，不猜课程；DWS、U校园及其他来源保持原平台分批规则。

每个已归属课程在同一轮只生成一个批次，不再因100条上限或全局5000条截取而拆散；课程原始事件总量最多8MiB，超过时完整保存在blocked批次并标记COURSE_INPUT_TOO_LARGE，不截断。Trigger按最多100条／128KiB分页检查所有事件，严格验证并按原顺序汇总真实变化ID；存储上限不等于单次模型输入预算，单条仍受实际上下文限制。未归属组及原平台分批仍每批最多100条、约44KB；超大单条保留为blocked。`batch.scope`冻结课程名称、course_ref、offering_id及connection_id，并随Trigger与Filter输入传递；历史批次scope为空，内容、状态、初筛和产物均保持原样。

当前固定为手动模式：刷新只生成 pending 批次。管理员点击后才 pending → queued，四个受控处理槽可并行执行批次，模型HTTP实际并发由独立Provider门限控制。Trigger先整批筛选；相关批次保存校验后的Trigger结果并以`stage=items,status=queued`让出处理槽，队列优先领取尚处于`stage=trigger`的批次，再继续逐项阶段。逐项阶段重新领取会更新attempt并复用Trigger，不再次请求初筛模型。让出时保留待处理事项，取消/失败仍按真实终态记录。每个change都有稳定持久化item_id，IGNORE也保存原因。TRIGGER与UNCERTAIN选中的变化在明确同一课程、来源及连接且尚未执行时组成有界课程处理组：最多50条、完整编码输入不超过128KiB、证据引用最多100条；完整原文、原生ID及每条源审计项均保留。每组进入一次Filter，可按具体业务拆项，减少同课重复调查；不规定一门课只能有一个Workflow。其他来源、归属不明、单条超限或已有执行历史保持原逐条路径。源审计项关联组的真实运行和结果，不复制路由或伪造完成；组COMPLETED仍要求实际Worker业务COMPLETED。组和业务子项的数量与源记录数分别解读。Filter可把一条原文拆为多个parent_item_id子事项，并多次调用Learner/Worker；同Workflow不同事项允许多次执行，仅item/run重复被拒绝。取消原12分钟批次上限，长任务继承宿主context并持续心跳。Trigger 输出严格校验真实变化 ID；Filter 的 Worker 执行引用核对实际子 Run。执行成功与业务 COMPLETED 分别记录。

Filter 输入的 `content` 是结构化变化数组，每项保留 `change_id`（变化ID）、`record_id`（中心归档ID）、`external_id`（平台原生ID）和 `connection_id`。顶层 `evidence_refs` 继续引用变化ID，`evidence_ref_kind=change_id` 明确其命名空间；`evidence_links` 保留四种ID的映射，并提供按顺序对应的 `change_ids/record_ids/external_ids/connection_ids`。`external_id` 从 after 提取，撤回/删除等缺少 after 时回退 before，缺失则为空，不猜测。读取实际归档应使用现有 `data_query`，对 `platform_records.id` 执行 `eq evidence_links[].record_id`；不能把变化ID当中心记录ID。查询仍按用户与有效连接隔离，`text` 优先返回源记录 `content` 正文，旧记录回退 `text` 字段。

Learner自主决定调查范围、信息取舍和画像组织。`profile_manifest`是可选元数据，省略或null均兼容；给定时仅要求JSON对象，其内部版本、策略、查询、搜索覆盖和维度均开放，不作为创建或交接的业务完整性门槛。不强制六数据集搜索完成、六维覆盖、known来源非空、profile_index或特定主题类别/数量。结构合法不表示调查或事实已经完整。交接仍回读实际Workflow并核对owner、active、entity及version；若选择原子初始化私有记忆，现有种子title/content/source_refs参数、owner ACL、单事务保存及提交后receipt保持不变，不由画像类别推断写入成功。

Filter 同一轮多个 `agent_dispatch` 可最多四路并行；其他工具保持原顺序。同一用户同一 Workflow 在所有 Runtime 入口使用 PostgreSQL 会话 advisory lock 串行，等待锁不占主连接池。子调用继承同一工作流时复用当前执行链持有的锁。获取后再次核验状态与版本。排队不承诺无限等待，仍受调用链截止时间限制。

状态与已提交队列保存在 PostgreSQL，重启后 queued 继续；失去心跳 45 秒的 running/refreshing 标记 interrupted，已保存 Diff 不丢弃。失败重试仅允许没有开始 Workflow 的批次（复用已验证 Trigger 输出）；已开始 Workflow 的结果必须先核查，界面不会盲目重试。

HTTP `/api/v1/control` 由现有登录、同源和 CSRF 中间件保护，所有端点仅管理员：

- `GET /`：最近 20 轮、150 批次、100 Run 摘要与全量状态计数。
- `GET /batches/{id}`：完整 Diff、Trigger/Filter 输出及关联 Run。
- `GET /runs/{id}`：完整 Run 详情与输出（含item_id关联）。
- `GET /items?round_id=&batch_id=&status=&after=&limit=&offset=`：事项分页，limit默认100最大500，返回items/counts/next_after；counts统计叶事项，父容器不重复计数。
- `GET /items/{id}`：item、关联runs及artifacts；batch详情也附items/artifacts。
- `GET /artifacts?round_id=&item_id=&batch_id=&type=&after=&limit=&offset=`：可视化产物分页。WORKFLOW从实际Learner结果回读；MEMORY从实际Worker归属下GetMemory核实，NOTICE只是应用内内容，不表示已发送。
- `POST /start`：`{round_id?,source?,batch_id?}`，必须指定轮次或批次；幂等返回 accepted 数量。
- `POST /batches/{id}/retry`：安全重试；不符合条件返回 409。
- `POST /batches/{id}/cancel`：取消排队或本进程正在处理的批次；不会撤销已提交业务操作。

管理页 `/admin/tasks` 每 2 秒观察状态，隐藏时暂停刷新，提供计时器设置、平台统计、手动启动、取消、安全重试和 Diff/输出详情。计时器设置仍仅当前进程有效；处理队列与结果持久化。采集沿用现有平台分页预算，仅记录已采集到的新增/更新与 DWS 附件变化；不从“此次未读到”推断删除。

验证：`go test ./...`、`go vet ./...`；指定专用 `TEST_DATABASE_URL` 后 dbbridge 测试覆盖真实 PostgreSQL 分批、幂等、并发领取、重启中断、锁串行及模拟模型端到端管线。framework 运行 `go test -race ./...`。模拟模型测试不等同真实上游/模型业务效果验证。

本地维护与验证：在 backend 目录设置现有部署环境及 `OPTIMIZE_OWNER_ID` 后，`go run scripts/optimize_demo.go sync` 会先备份当前实例/流程到 `.build/optimization-backup.json`，同步角色提示词并通过 Repository 新增 Workflow 版本。只用于已审查的 Demo 配置，备份存在时拒绝重复覆盖。设置 `OPTIMIZE_BATCH_ID` 后，`go run scripts/optimize_demo.go replay` 可重放指定已存在批次；Worker 在此验证入口移除日程写入/删除权限，模型单次请求超时在验证进程设为90秒，Learner 仍可创建或更新定义，工具和运行记录持久化。它不重写原批次结果，输出保存在 `.build/optimization-replay*.json`。`tune` 维护模式将当前用户Filter父子共享模型预算设为32次、相关模型服务超时设为90秒；本次真实回归已证实原16次预算不足以完成首次创建流程后的最终汇总。

批量入口：`backend/scripts/test_run_control.sh`（可设专用 `TEST_DATABASE_URL`）；本地只读接口冒烟 `python3 backend/scripts/run_control_regression.py` 从环境读取管理员登录，检查认证/CSRF、查询、非法参数与未知批次，不触发真实模型。
