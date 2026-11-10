# 教学周日程

范围：一张 `schedules` 表、用户隔离 CRUD、内部 Bridge、完整手动教务课表快照的事务刷新。无额外定时任务，不改 Agent Framework 或信息轮询。

## 数据

新增迁移 v8。字段为 `id/user_id/academic_year/term/teaching_week/start_time/end_time/title/details/source/event_status/mod/series_key/origin_key/active/created_at/updated_at`。学期复用教务的学年起始年份 + term(1/2)，教学周1–60。开始/结束为分钟精度 timestamptz，API RFC3339，存储 UTC；校历按 Asia/Shanghai 展开。

复合索引 `(user_id,academic_year,term,teaching_week,start_time,id)`；另有用户时间范围索引及课表来源唯一键 `(user_id,academic_year,term,origin_key)`。`time_status` 在每次读取时计算，不落库：start 前 NOT_STARTED，start ≤ now < end 为 IN_PROGRESS，end 起 FINISHED。event_status 独立持久化，默认 INCOMPLETE，可改 COMPLETED。

`source` 仅 TIMETABLE / USER / AGENT。HTTP 创建固定 USER，内部 Bridge 创建固定 AGENT；TIMETABLE 只能由同步写入。API 不接受客户端指定 user_id、mod 或来源身份。用户身份来自应用会话，Bridge OwnerID 来自可信调用者。所有 SQL 在 dbbridge。

## HTTP

挂载在现有会话认证和 Origin/CSRF 中间件下。以下写请求需要现有 `academic_csrf` Cookie 对应的 `X-CSRF-Token` 和同源 Origin；不需要提交学校账号密码。

```http
POST /api/v1/schedules
Content-Type: application/json

{"academic_year":2026,"term":1,"teaching_week":8,"start_time":"2026-10-28T14:00:00+08:00","end_time":"2026-10-28T15:35:00+08:00","title":"复习数学","details":"准备习题"}
```

| 方法 | 路径 | 用途 |
|---|---|---|
| GET | `/api/v1/schedules?academic_year=2026&term=1&teaching_week=8` | 按学期、教学周查询 |
| GET | `/api/v1/schedules?from=2026-10-26T00:00:00%2B08:00&to=2026-11-02T00:00:00%2B08:00` | 时间范围查询，返回与 [from,to) 重叠的日程；可附学期过滤 |
| GET | `/api/v1/schedules/{id}` | 单条详情及修改历史，包括已失效的课表安排 |
| PATCH | `/api/v1/schedules/{id}` | 修改安排或完成状态 |
| DELETE | `/api/v1/schedules/{id}` | 删除一条日程；课表日程保留删除标记及历史，重复同步不复活相同来源身份 |

列表仅返回 active 数据，按 start_time,id 排序，默认100条，limit最大1000，offset分页，返回 items/offset/next_offset。遍历至空页可取全周任意多条日程；时间范围最长366天。手动日程的教学周与日期由调用者明确指定，不猜其校历归属。

仅更新完成状态无需修改模式：

```json
{"event_status":"COMPLETED"}
```

永久修改：

```json
{"mode":"PERMANENT","start_time":"2026-10-29T14:00:00+08:00","end_time":"2026-10-29T15:35:00+08:00"}
```

临时修改：

```json
{"mode":"TEMPORARY","start_time":"2026-10-30T14:00:00+08:00","end_time":"2026-10-30T15:35:00+08:00"}
```

## Mod 及同步冲突规则

每条课表日程是某教学周的一次具体安排。`mod` JSONB 示例结构：

```json
{
  "base":{"teaching_week":8,"start_time":"...","end_time":"...","title":"数学","details":"..."},
  "permanent":{"fields":["start_time","end_time"],"after":{"teaching_week":8,"start_time":"...","end_time":"...","title":"数学","details":"..."}},
  "temporary":{"fields":["start_time","end_time"],"after":{"teaching_week":8,"start_time":"...","end_time":"...","title":"数学","details":"..."}},
  "history":[{"mode":"PERMANENT","before":{},"after":{},"effective_week":8,"original_id":"原选择日程ID","at":"..."}]
}
```

- base 是最新教务基础安排，history 保存每次本地修改及上游变更前后完整关键字段。SYNC / SYNC_REMOVED 记录上游变化或取消。每条最多256项历史，超限返回409并保留数据，不截断历史。
- PERMANENT 对同学期同 series_key 的选定周及后续已有周生效；后续同步首次出现的新周也继承此前永久调整。只覆盖明确修改的字段，不冻结其他上游字段。此前教学周不改。
- TEMPORARY 仅写当前 occurrence，不复制到其他周。第8周调整后，第9周自然使用其自身基础/永久安排，不需要清理任务。时间调整必须仍在该教学周内。
- 查询使用 `base → permanent → temporary` 的结果；临时修改在所在周优先。event_status 仅更新选定日程，不随永久调整扩散，也不会被重复同步重置。
- USER/AGENT 是独立普通事项，修改只作用于该条，保留 before/after 历史，不扩展周期规则。
- 单一课程安排以课程/教学班身份匹配，星期、时间、教室变化更新同一 ID。多安排课程缺少经过验证的上游安排 ID，保守使用星期/节次区分；无课程/教学班ID时退回课程名+教师。身份不再匹配时，旧行 inactive，新安排独立生成，不将本地修改猜测性套到别的课程。同步结果 `retired_modifications` 明确列出本次失效且有本地修改的旧日程ID，详情可追溯。
- 最新完整快照中消失的课表安排标为 inactive；有效空快照可取消该学期全部课表来源安排。USER/AGENT 不参与刷新。相同来源身份重新出现时可重新激活，显式本地删除标记优先。

## 手动课表同步

后台 `/admin` 现有教务连接中填写学年、学期、**第1教学周周一日期**和**图形化时间表编辑器**，再点“获取课表”。也可调用原有接口：

```http
POST /api/v1/platforms/connections/{connection_id}/sync
Content-Type: application/json

{
  "resource":"schedule",
  "academic_year":2026,
  "term":1,
  "calendar":{
    "week_one_monday":"2026-09-07",
    "periods":[
      {"period":1,"start":"08:00","end":"08:45"},
      {"period":2,"start":"08:50","end":"09:35"}
    ]
  }
}
```

**示例日期和节次仅为虚构验收配置，必须换成学校该学期真实校历和全部所需节次作息**。配置按请求传入，不建立第二张校历表，不默认套用作息。遗漏课表所需节次、未知周次表达、异常记录、分页失败均报错，不提交部分日程。支持周次区间/列表和单双周，非连续节次展开为多个安排。无具体时间的实践原文只归档，返回 skipped_practice 数量。

响应含 academic_year/term/records/generated/skipped_practice/retired_modifications。generated 是完整快照展开的基础日程数，包括被本地删除标记隐藏的来源安排；首次同步可直接按教学周GET验证。课表归档仍可用原 preview 查看，日程以 `/schedules` 为准。

调用链：

```text
platform.HTTP POST /sync (resource=schedule + calendar)
  → platform.Service.SyncTimetable
  → jwxt.AcademicTermReader.SyncTerm (从空游标收齐完整快照)
  → schedule.Expand (校历、教学周、星期、节次 → 具体发生时间)
  → model.TimetableCommitter.CommitTimetable
  → dbbridge.platformRepo.commit
      用户锁 + 连接租约校验
      → 更新/移除本连接本学期课表归档
      → refreshTimetable (仅该用户该学期TIMETABLE，重放本地修改)
      → 会话和checkpoint
      → 单一事务COMMIT
```

所有分页读取/解析/展开成功后才开始业务写事务。最多5000条来源记录、20000条展开日程；不完整或超限拒绝，不静默截断。读取失败不改旧数据，写入失败整笔回滚。保留教务手动模式，不增加轮询或自动登录。

## 内部 Bridge

`Service` 实现现有 `bridge.Port`，app 创建同一服务挂载HTTP并作为组合Port注入；非 schedule 操作交给已有 Demo。可由其他后端模块注入该Port使用，不开放任意 HTTP Bridge 入口。

- `schedule.create`：Payload 与 HTTP创建相同，来源固定 AGENT。
- `schedule.query`：Payload 对应 `model.ScheduleQuery`。
- `schedule.get` / `schedule.delete`：`{"id":"..."}`。
- `schedule.update`：`{"id":"...","mode":"TEMPORARY", ...}` 或仅 event_status。

OwnerID 只从可信 Request.OwnerID 绑定，payload 中 user_id/source/mod 均拒绝。本次未注册专用模型工具或改动 Agent Runtime；后端内部可直接调用。

## 测试和限制

`go test ./internal/schedule ./internal/platform` 不访问真实上游。dbbridge 的 PostgreSQL 验收仅在显式 TEST_DATABASE_URL 的独立测试库中执行，每个测试还建立随机schema并清理。覆盖多周、多事项、查询/CRUD、HTTP自动接线、重复去重、变更/取消/空快照、USER/AGENT保留、永久与临时修改、新未来周继承、状态、用户/学期隔离、分页读取失败、解析失败、数据库写入异常回滚和身份歧义冲突报告。

本次验证使用合成课表和真实隔离 PostgreSQL；未提交真实学校账号或请求真实教务接口。未新增校历持久化、跨周移动、通用周期规则、冲突检测、提醒/请假/Workflow重算或Agent自动调度，均不属于本次基础模块范围。

## 后台日程与时间表编辑器

登录 `/admin`，点击导航“日程面板”。选择学年、学期、教学周即可按周一至周日查看日程卡片，支持上一周/下一周、新增个人日程、编辑、完成/未完成、删除和修改历史。超过1000条可继续加载。日期时间明确使用北京时间，临时修改默认只改所选安排，永久修改需确认；只提交用户实际改动的字段，避免冻结未修改的上游信息。时间状态每30秒按当前时间更新展示，不请求上游。

教务连接中展开“课表时间表编辑器”，填写第1教学周周一日期，设置每天节数（1–30）、首节开始时间、每节课时长（1–240分钟）及课间休息（0–360分钟），点击“生成 / 重新填充节次”。下方时间轴及可编辑表格支持逐节修改开始/结束、课时和课后休息；“从本节顺延”保留本节开始时间，按各节自身课时和课后休息重排后续节次。勾选“修改后自动顺延”则逐节编辑时自动更新后续安排，可用较长休息表示午休或晚休。越过当天或无效输入拒绝整次填充，保留旧表；同步前再验证首周日期、时间先后与重叠。默认数值仅填写建议，需明确生成并按真实作息确认。

“获取课表”自动将表格转换为原有calendar参数，无需编写JSON，同步成功后刷新日程面板的对应学期。草稿仅在当前页面内按用户、连接及学期隔离；刷新网页、退出登录或切换用户会清空，不新增数据库配置表。

离线真实浏览器回归：`PLAYWRIGHT_MODULE=<playwright-core路径> node scripts/schedules_ui.cjs`。Chromium路径可由CHROMIUM_PATH指定。所有API均为合成fixture，无真实上游或数据库写入；覆盖日程管理、历史、时间转换、失败表单保留、周切换、草稿隔离、节次生成/顺延、同步参数及账号重置。

## 单双周

单双周按所选学期的**教学周编号**计算，不使用自然周或ISO周。课表按来源weeks_raw逐段展开：例如3-17周(单)只生成3、5、7、9、11、13、15、17周，3周,7-9周(单),13-17周(单)只生成3、7、9、13、15、17周；双周同理。卡片展示原始教学周规则和单双周安排标记，工具栏显示当前教学周的单/双周。

课表日程mod额外保留weeks_raw、teaching_weeks和week_pattern（ODD/EVEN/ALL）作为来源规则。ALL表示来源同时含奇偶周，具体周次仍以teaching_weeks为准，不意味着覆盖学期每一周。同一课程同一时段的单周、双周安排可以共用原有series_key和不同周的origin_key；永久修改、后续新增周的修改继承都额外按week_pattern隔离。临时修改仍仅作用于指定教学周。无需新增数据库表或迁移。
