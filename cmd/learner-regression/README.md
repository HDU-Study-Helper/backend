# Learner 全课程隔离回归

本入口专门验证 Learner 的首次课程 Workflow/私有画像初始化。默认只读枚举当前有效学习通、MOOC `academic_offerings` 的全部课程，不依赖管理页最近 150 批次的概览。它不重新采集平台数据，也不把本次模型运行等同 Trigger → Filter → Worker 全链验收。

## 执行

从 `backend` 目录运行；环境文件只加载到进程，不回显。先审阅源码及 dry-run manifest，再执行。

```bash
set -a
. ./.env
set +a
export GOCACHE=/home/skysight/aigc-project/.build/gocache
export GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local

go run ./cmd/learner-regression
# 可选：只选择指定用户或已停止轮次明确关联的课程。
go run ./cmd/learner-regression --owner '<应用用户ID>' --round '<历史轮次ID>'
# 显式调用真实模型；每次新建保留的隔离 schema。
go run ./cmd/learner-regression --execute --course-timeout 30m --parallel 2
# 明确恢复同一隔离快照；不重新读取或克隆生产数据。
go run ./cmd/learner-regression --execute --resume-dir '../.build/learner-regression-<保留目录>' --parallel 4
```

默认不调用模型，不创建 schema，不修改任何数据库行；会在 `.build/learner-regression-*` 创建 0700 私有目录和 0600 清单。`DATABASE_URL` 必需，`--execute` 还需要现有 `CREDENTIAL_KEY`。提供 `--source-schema` 时只接受单一普通 schema 名称。默认存在 queued/running 批次、refreshing 轮次或正在采集资源时拒绝冻结。显式 `--allow-active-snapshot` 可在这些状态存在时冻结同一 REPEATABLE READ 快照中的已提交业务数据，manifest记录活动状态条数；不复制执行状态，不取消或改写源库任务，不能据此声称平台采集已完成。

## 隔离与备份

生产连接使用 `default_transaction_read_only=on` 和 `REPEATABLE READ READ ONLY`。课程列表及业务数据来自同一快照。只有 `--execute` 会创建随机 `learner_regression_<UTC时间>_<随机数>` schema，目标连接 search_path 仅包含该 schema，不回退 public。迁移只在目标执行。schema 永久保留，包括准备中断或执行失败；没有自动清库、生产状态恢复或自动重试。

按外键顺序复制用户、Agent 实例、加密 provider settings、平台/DWS 连接和授权群、原始业务记录、结构化课程/事项/证据、个人状态、日程与 DWS 消息附件。原 ID 保持不变。provider settings 密文原样复制，只由既有 agentadmin/vault 装配解密，不输出密钥。`.build` 中额外保留实例配置与加密 provider settings 的私有备份。平台 session/workspace 密文可能随连接复制，但不会被解密或调用；因此隔离 schema 和全部产物仍包含私人数据，需按原数据库权限保护。

不复制原 Workflow、记忆、Agent Run、control 队列和登录 session，从而真正验证首次初始化。目标复制完成后，仅更新所选用户 Learner 提示词；不改其他角色提示词、模型参数或预算。Learner工具被限制为数据只读、Workflow list/get/create、记忆只读；原子画像创建所需 memory.write 权限保留。使用原始 `db.Agents()`，保留 `WorkflowProfileRepository`/`WorkflowLocker` 等可选接口。未注册日程工具、未装配通知、未开放派发或 Web 工具、未创建平台/采集服务；无作业提交、通知发送或外部平台写入路径。仅真实配置的模型 API 会在 execute 时被调用。

## 范围和验证

无 `--round` 时枚举全部 active chaoxing/mooc 结构化开课实例。指定轮次时按 `control_batches.batch_scope` 的 connection + offering_id/course_ref 精确选课，不按课程名猜配。历史空 scope 或未关联的记录不会伪造成课程输入；未关联记录仍包含在复制的业务数据中。manifest 的课程清单是完整冻结输入，`--parallel` 默认2、允许1–4；课程可有限并行，实际仍受原 Agent max_concurrent 和 Provider 并发门限约束，不改模型或预算。结果始终按原manifest课程顺序保存，完成顺序不改变输入。新建课程 Workflow/记忆在同一隔离 schema 中保留。

每课保留原始模型请求/响应、输入、Run 记录、错误、持久化校验结果。`requests.jsonl` 包含每次请求大小、历史工具结果字节、耗时、usage 和安全错误；`progress.json` 汇总已完成/核验/失败数量。`results.private.json` 含私人业务结果，只在私有目录保存。模型 run 成功不自动算业务验收：必须返回 APPLIED，回读 active Workflow 的版本和精确课程实体，并读取绑定 Workflow PRI 的有来源 profile_index 和主题记忆；任一不满足记失败，最终非零退出。后端原子创建还执行当前 manifest/种子校验。该校验不证明每个事实语义正确，仍需审查来源与画像覆盖。

可用 `python3 cmd/learner-regression/analyze.py /绝对路径/回归目录` 生成不含课程正文的 `audit.json` 与 `audit.md`：汇总模型/工具次数、查询范围、字段信息增量和峰值请求快照。无持久化 Run 审计的中断案例回退到请求历史并标记 partial，不把缺失的终页结果当作已观察证据。仅连接查询单列；字节数不能等同 token 或 Runtime ContextBytes。

另用 `PYTHONDONTWRITEBYTECODE=1 python3 cmd/learner-regression/quality.py /绝对路径/回归目录` 审查实际成功创建的种子：执行定义字段、读取计数、来源引用、动态分类及观察时间分别标记需复核项，生成 `quality.json` 与脱敏 `quality.md`。规则无发现不等于语义正确；审查不会修改历史画像或把持久化成功改成模型重新执行。

### 恢复与尝试历史

`--resume-dir` 必须与明确的 `--execute` 一起使用，不接受更改owner/round/allow-active-snapshot。仅从保留manifest读取严格格式 `learner_regression_YYYYMMDD_HHMMSS_<16位小写hex>` 目标，用重新解析的精确DSN连接隔离schema；`current_schema()`不匹配或schema不存在即拒绝，不迁移、不克隆、不依赖生产活动状态。冻结课程摘要与独立课程清单必须一致，每条课程还与隔离库原课程逐项核对。旧目录首次恢复在核对后建立冻结清单，保留旧manifest与results副本。

恢复只同步目标Learner最新提示词，不改provider/model/limits。已verified课程跳过；未verified或缺失课程先list/get全部已有Workflow核对实体，任何同实体Workflow（包括draft/archived）都保守标记 `review_required`，不再调用模型创建，交由人工或agent核验后处理。隔离库仍有running Run则拒绝恢复，不自动篡改状态。新执行器用目录锁防止重复启动；旧编译执行器不认识此锁，恢复前必须明确停止它。

每次执行或恢复新增独立 `attempt-*` 目录，保存尝试元数据、输入和本次结果；旧尝试记录不覆盖。累计results/progress由单一协调器写入，使用同目录0600临时文件、fsync后原子rename，按manifest序输出。取消后用独立15秒清理context读取Run审计并持久化结果，manifest标记interrupted。收到强制kill/机器崩溃时manifest可能保留executing，恢复仍会检查目标实际running状态及同实体既有写入。

### 全链扩展

如果要验收 Trigger/Filter/Worker，另建显式全链模式：从同一只读快照用直接 SQL 枚举选定停止轮次全部 control_events/batches，复制为目标新队列，禁止复用 overview 的 150 条限制；冻结后不吸纳新 pending。只在隔离目标运行 runcontrol，限制业务写工具并保留原 Repository 可选接口，再分别统计路由真实子 Run、Workflow PRI、Worker 业务状态及产物。不能使用旧 reset/resume 脚本清空生产数据或改写历史失败来获得成功。

## 本地纯测试

```bash
env -u TEST_DATABASE_URL GOCACHE=/home/skysight/aigc-project/.build/gocache GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local go test ./cmd/learner-regression
go vet ./cmd/learner-regression
```

不设置 `GOPATH=.build/gopath`：当前工作区模块缓存缺少 x/text 的相关版本，默认 `/home/skysight/go/pkg/mod` 已有依赖。测试只验证隔离范围、外键复制顺序、opaque 课程身份和工具限制，不访问数据库、模型或外部平台。
