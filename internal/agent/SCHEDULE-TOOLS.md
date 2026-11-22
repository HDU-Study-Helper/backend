# 日程工具

宿主通过 `Dependencies.Schedules` 注入现有日程 `bridge.Port`，注册以下工具；临时后台及正式 App 共享同一日程 Service/Repository。无内部 HTTP、额外数据库或后台计时器。

| 工具 | 权限 | 输入/行为 |
|---|---|---|
| schedule_query | schedule.read | academic_year/term/teaching_week，或 RFC3339 from/to；可结合过滤。时间范围最长366天；limit默认100、最大1000；offset最大100000 |
| schedule_get | schedule.read | id；读取详情、有效状态及修改历史，可能返回已失效记录 |
| schedule_create | schedule.write | academic_year/term/teaching_week/start_time/end_time/title，details可选；来源AGENT |
| schedule_update | schedule.write | id/mode，至少提供start_time/end_time/title/details/event_status中的一个；省略字段保留，details空串清除，null不接受 |
| schedule_delete | schedule.delete | id；课表保留删除标记和历史，手动/Agent日程永久删除 |

Schema 拒绝额外字段；OwnerID只能取可信Runtime上下文。每次执行检查工具与权限，沿原Bridge进入日程Repository，再做当前用户隔离。查询结果再检查用户归属/active/数量；时间区间为重叠匹配[from,to)，按start_time,id排序。返回 `items/offset/next_offset`，遍历至空页；若下个偏移超过100000，额外返回 `page_limit_reason=offset_limit`，保持原next_offset数字但不得继续使用，应缩小时间范围。空结果正常返回数组，失败不会伪装成空结果。

完整输入/输出JSON Schema通过后台工具目录 `/api/v1/agents/tools` 提供。这5个工具属于后端宿主扩展，不包含在纯框架Builtin目录导出中；普通后端注册表最多26个工具（21个通用工具加5个日程工具）；具体Agent仍经EnabledTools和Permissions筛选。Learner暂存另有两个会话工具，临时注册表只接线schedule_query/get和既有Search读取依赖，写日程工具不注册，并用只读Bridge再次拒绝写操作。Filter只授权query/get。创建实例不自动授权其他Agent。

五个日程工具所有参数具有字段级说明；limit/offset/details的默认注解仅描述创建或查询行为，更新不自动填默认值。title最大1000 UTF-8字节、details最大16384字节，输入Schema保留字符上限作为宽松预检，handler在调用Port前执行精确字节检查，原domain校验不变。输出完整声明Schedule、mod、patch和history；历史mode包含TEMPORARY/PERMANENT/SYNC/DELETE/SYNC_REMOVED。

写入复用原日程契约：时间精确到分钟，单次时长不超过7天；学期和教学周由调用方明确提供，不自动推断。TEMPORARY只改选定周；课表PERMANENT可影响同系列当前及后续周，完成状态仅作用于选定项。没有expected_version或新增幂等键，不要盲重试写入。没有新增通知发送或自然语言日期解析。

验证：`go test ./...`、`go vet ./...`（backend与agent/framework）。`TestAgentScheduleToolsPersisted` 使用独立PostgreSQL测试库及隔离schema，经Mock模型→真实Runtime→工具Schema与权限→进程内Bridge→真实Repository，覆盖5项CRUD、读回、时间范围/教学周/分页、额外身份参数拒绝、读写权限及跨用户隔离。未在测试中调用真实外部模型或教学平台。

在线批量检查：项目根目录执行 `python3 backend/scripts/schedule_tools_smoke.py`，使用环境中的后台登录信息检查工具目录及Filter只读配置；加 `--live` 会消耗已配置模型额度，执行一次只读任务，核验运行记录中确实有成功的schedule_query及合法JSON输出。安全报告写入 `.build/schedule-tools-smoke.json`，不保存日程内容或密钥。

当前本地demo已通过该真实模型检查：Filter成功调用两次schedule_query（16ms/2ms），最终输出合法JSON，状态NO_MATCH；整体运行约105秒，包含模型及其他查询耗时。没有执行日程写入或下游分发。此测试验证真实查询调用，不代表已存在适用Workflow或业务执行引擎。
