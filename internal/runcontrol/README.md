# 计划任务与运行控制

Trigger 编号协议更新：后端输入/校验与 Agent 提示词模板需要同步部署，数据库中的已安装 Trigger 实例也须显式更新提示词。旧实例若仍返回长ID会被新模型输出校验拒绝；不会自动降级接收旧协议而掩盖部署不一致。本次仅修改源码和模板，未运行测试、模型、采集或配置安装，未重启服务。

`app` 管理采集时钟与本服务生命周期。采集提交时，`dbbridge` 在同一业务事务内保存脱敏前后快照。v15起，学习通与中国大学MOOC刷新完成后按轮次、用户、来源连接、所属课程开课实例分批；同课跨资源变化一起交给Trigger。学习通课程＋教学班、MOOC课程＋所属期次为身份，SPOC引用源期次不替换所属期次。同名、不同连接、不同教学班/期次不合并。无可靠课程归属的信息按连接单列为unassociated，不猜课程。DWS在同一轮、同一用户内按connection_id＋group_id封批，不同连接或群不混合；消息与附件变化保存可信group_id，旧未封批消息从conversationId读取，旧附件按当前用户＋连接＋中心消息ID查回群。无法确认或标识冲突的事件逐条保存在blocked/DWS_GROUP_UNRESOLVED批次，不混入其他群。U校园及其他来源保持原平台分批规则。

每个已归属课程在同一轮只生成一个批次，不再因100条上限或全局5000条截取而拆散；课程原始事件总量最多8MiB，超过时完整保存在blocked批次并标记COURSE_INPUT_TOO_LARGE，不截断。Trigger按最多100条／128KiB分页检查所有事件。模型输入标记`selection_protocol=indices_v1`，每页changes按输入顺序标记`index:1..N`，不再重复发送变化change_id/evidence_ids；模型只返回`{decision,reason,relevant_indices:[1,2,...]}`。后端严格检查整数编号、范围、唯一性及输入顺序，按当前页原始数组映射回真实变化ID后再汇总和持久化；页编号从1重新开始，不能跨页引用。每次输入另带`selection_count=N`及`selection_bounds:{min:1,max:N}`，明确当前可选编号；`page.index/page.count`仅表示分页位置/页数，不是选择上限。空页N=0、无可选编号。语义校验失败仍最多使用既有的一次纠正Run，`validation_error`包含本页实际条数、编号范围及具体错误位置/值，错误文本有界且不回显完整非法输出；再次返回越界编号仍失败，不截断或自动接受。该修复未改变同Run通用JSON格式修复机制，也未新增下游执行。已有持久化`relevant_change_ids`继续读取，下游不接收短编号；存储上限不等于单次模型输入预算，单条仍受实际上下文限制。未归属组、DWS单群及其他平台分批仍每批最多100条、约44KB；单群超限只在该群内拆块，超大单条保留为blocked。课程`batch.scope`冻结课程名称、course_ref、offering_id及connection_id；DWS使用`{kind:"group",connection_id,group_id}`，Trigger输入标记`processing_unit=dws_group`，范围同时传入Filter。已封存历史批次不重分组、不回填scope，内容、状态、初筛和产物均保持原样。此次DWS群封批改动仅做静态审阅，未运行测试或真实采集，未重启现有服务。

首次运行前默认只采集。管理员在任务页点击「第一次运行」（GET/POST `/api/v1/control/first-run`）后，后台冻结选课、课表、平台课程入口和最近 168 小时有来源时间的授权动态。按教务选课拆成独立 Learner Run，在同一后台进程内按 Learner 的并发配置处理不同课程。只在原提示词前拼接简短首轮说明；使用 Learner 原工具和实例配置。标准 `data_search`、`data_query` 和日程读取共用服务端七天条件；本学期课表、选课和课程目录作为基础背景，不按课名预筛动态记录，课表不作为记忆来源。复用常规暂存提交；只有找到值得保存的有据事实才建档。每门课显示实际运行及已提交的记忆和 Workflow。无可靠发布时间的条目按来源/类型计数在首轮批次结果中，不用观察时间或截止时间替代。首轮成功后只自动领取点击之后的变化，首轮期间的变化也保留；失败可重试同一快照。既有手动批次操作仍保留。四个受控处理槽可并行执行批次，模型HTTP实际并发由独立Provider门限控制。Trigger先整批筛选；相关批次保存校验后的Trigger结果并以`stage=items,status=queued`让出处理槽，队列优先领取尚处于`stage=trigger`的批次，再继续逐项阶段。逐项阶段重新领取会更新attempt并复用Trigger，不再次请求初筛模型。让出时保留待处理事项，取消/失败仍按真实终态记录。每个change都有稳定持久化item_id，IGNORE也保存原因。TRIGGER与UNCERTAIN选中的变化在明确同一课程、来源及连接且尚未执行时组成有界课程处理组：最多50条、完整编码输入不超过128KiB、证据引用最多100条；完整原文、原生ID及每条源审计项均保留。每组进入一次Filter，可按具体业务拆项，减少同课重复调查；不规定一门课只能有一个Workflow。其他来源、归属不明、单条超限或已有执行历史保持原逐条路径。源审计项关联组的真实运行和结果，不复制路由或伪造完成；组COMPLETED仍要求实际Worker业务COMPLETED。组和业务子项的数量与源记录数分别解读。Filter可把一条原文拆为多个parent_item_id子事项，并多次调用Learner/Worker；同Workflow不同事项允许多次执行，仅item/run重复被拒绝。取消原12分钟批次上限，长任务继承宿主context并持续心跳。Trigger 模型输出严格校验本页编号，映射后的持久结果继续核验真实变化 ID；Filter 的 Worker 执行引用核对实际子 Run。执行成功与业务 COMPLETED 分别记录。

Filter 输入的 `content` 是结构化变化数组，每项保留 `change_id`（变化ID）、`record_id`（中心归档ID）、`external_id`（平台原生ID）和 `connection_id`。顶层 `evidence_refs` 继续引用变化ID，`evidence_ref_kind=change_id` 明确其命名空间；`evidence_links` 保留四种ID的映射，并提供按顺序对应的 `change_ids/record_ids/external_ids/connection_ids`。`external_id` 从 after 提取，撤回/删除等缺少 after 时回退 before，缺失则为空，不猜测。读取实际归档应使用现有 `data_query`，对 `platform_records.id` 执行 `eq evidence_links[].record_id`；不能把变化ID当中心记录ID。查询仍按用户与有效连接隔离，`text` 优先返回源记录 `content` 正文，旧记录回退 `text` 字段。

来源补充快照可附带同账号作业通知候选：只有已核验记录与结构化作业/考试精确对应、绝对截止为空且标题足够具体时，查询同owner、connection、source的notice，标题包含完整作业标题，不叠加账号通知常缺少的course/class字段。最多3条、整包8KiB，保留原文与“候选待核对”标记；候选不自动确认课程归属、截止或个人状态。整体快照超48KiB时先去掉可选候选，保持原事实快照；查询失败不阻断原处理。课程组仍只对当前宿主核验对象做有界补充，不承诺组内每个任务都已关联，其他缺口由授权查询处理。

Learner通过宿主暂存会话编辑目标Workflow与PRI记忆，只有有效完成标记、完整必要工具结果及权限/版本/引用校验通过才正式原子提交。宿主返回APPLIED/NO_CHANGE和commit_receipt，Filter消费的是提交后的结果，不把模型READY当已落库；实现与限制见[learnerstage](../learnerstage/README.md)。暂存Run失败、取消或格式异常不提交，最终Run审计由宿主统一保存。Learner自主决定调查范围、信息取舍和画像组织。`profile_manifest`是可选元数据，省略或null均兼容；给定时仅要求JSON对象，其内部版本、策略、查询、搜索覆盖和维度均开放，不作为创建或交接的业务完整性门槛。不强制六数据集搜索完成、六维覆盖、known来源非空、profile_index或特定主题类别/数量。结构合法不表示调查或事实已经完整。交接仍回读实际Workflow并核对owner、active、entity及version；若选择原子初始化私有记忆，现有种子title/content/source_refs参数、owner ACL、单事务保存及提交后receipt保持不变，不由画像类别推断写入成功。

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
