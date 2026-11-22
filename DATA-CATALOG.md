# 数据库结构与可采集数据字典

本文汇总当前后端已实现的数据结构、平台字段、关联方式和读取入口，供后端、前端及后续 Agent 开发使用。当前迁移至v15；原始采集表基线为v1–v4；与后续目标草案 `DATA-MODELS.md` 不同，本文描述当前代码。修改迁移、适配器输出或查询接口时应同步更新本文。

依据：`internal/dbbridge/{migrate,platform,preview}.go`、`internal/model/`、`internal/contract/platform.go`、各平台适配器及 README。本文不包含真实账号、Cookie、Token、二维码票据或私人业务数据；示例均为虚构。

## 1. 数据层级与实现边界

```text
用户 users
 ├─ 应用登录 sessions
 ├─ 钉钉连接 dws_connections
 │   ├─ 授权群 dws_groups
 │   ├─ 租约 dws_leases
 │   ├─ 同步任务 dws_jobs
 │   ├─ 同步水位 dws_checkpoints
 │   └─ 消息当前记录 dws_messages → 附件归档 dws_attachments
 └─ 学习平台连接 platform_connections
     ├─ 同步游标 platform_checkpoints
     ├─ 抓取状态 platform_sync_status
     └─ 业务当前记录 platform_records
```

当前没有独立的统一课程、作业、考试、通知、待办或 Agent 结果表。三大学习平台的数据按资源类别写入 `platform_records.content`，钉钉消息写入 `dws_messages.content`。这些是来源当前观察，不是经过跨源合并或用户确认的学业事实。每个逻辑对象只保留一行最新content及hash：相同内容不写入，变化时更新原行并保留稳定中心ID；A→B→A也会更新为A，不因曾经观察到A而跳过。

个人数据以 `user_id + connection_id` 隔离。连接内再保留原生课程、班级、群、任务等身份。所有业务 SQL 集中在 dbbridge；HTTP/平台模块不直接访问数据库。当前隔离由 Repository 参数、SQL 谓词、复合外键及租约校验实现，未启用 PostgreSQL RLS。

## 2. 数据库表结构

### 2.1 记法

- PostgreSQL `text` 为字符串，原生 ID 不转整数；`bigint` 为64位整数。
- `timestamptz` 为带时区时间；业务中使用 UTC 表达瞬间。
- `bytea` 为二进制；是否加密须看字段说明，不能认为所有 bytea 都是密文。
- `jsonb` 为结构化 JSON。
- 下列字段除特别标注“可空”外均为 `NOT NULL`。
- 所有下列外键都采用 `ON DELETE CASCADE`。断开连接与物理删除不同：断开保留已有当前记录。

### 2.2 用户与应用会话

```text
users
  id               text          PK
  username         text          UNIQUE
  display_name     text
  password_hash    text          bcrypt 哈希
  role             text          CHECK admin / user
  disabled         boolean       DEFAULT false
  created_at       timestamptz   DEFAULT now()
  updated_at       timestamptz   DEFAULT now()

sessions
  hash             bytea         PK；必须32字节；随机会话Token的SHA-256
  user_id          text          FK users.id
  expires_at       timestamptz
  created_at       timestamptz   DEFAULT now()
```

额外索引：`sessions(user_id)`。不保存应用会话 Token 明文。用户角色、密码、禁用状态改变时撤销会话；不能移除最后一名可用管理员。

### 2.3 钉钉连接、授权与调度

```text
dws_connections
  user_id          text          FK users.id
  id               text
  profile          text          官方 corpId:userId；首次授权前可为空
  corp_id          text          DEFAULT ''
  upstream_user_id text          DEFAULT ''
  mode             text          CHECK personal；school仅预留、当前不支持
  enabled          boolean       DEFAULT true
  state            text          DEFAULT pending
  workspace        bytea         加密官方CLI工作区；DEFAULT空；最多21MiB
  created_at       timestamptz   DEFAULT now()
  updated_at       timestamptz   DEFAULT now()
  PK(user_id,id)

dws_groups
  user_id          text
  connection_id    text
  id               text          原生群ID
  name             text
  PK(user_id,connection_id,id)
  FK(user_id,connection_id) → dws_connections(user_id,id)

dws_leases
  user_id          text
  connection_id    text
  group_id         text          可为空；实际同连接排他
  token            bigint        fencing token
  expires_at       timestamptz
  PK(user_id,connection_id)
  FK(user_id,connection_id) → dws_connections(user_id,id)

dws_checkpoints
  user_id          text
  connection_id    text
  group_id         text
  cursor           bytea         敏感游标密文；DEFAULT空
  cli_token        bytea         敏感CLI续读状态密文；DEFAULT空
  poll_watermark   timestamptz    已完成窗口的终点
  updated_at       timestamptz   DEFAULT now()
  PK(user_id,connection_id,group_id)
  FK(user_id,connection_id,group_id) → dws_groups(user_id,connection_id,id)

dws_jobs
  user_id          text
  connection_id    text
  id               text
  group_id         text
  state            text          CHECK queued/running/succeeded/failed/cancelled
  error_code       text          DEFAULT ''；安全错误码
  created_at       timestamptz   DEFAULT now()
  updated_at       timestamptz   DEFAULT now()
  PK(user_id,connection_id,id)
  FK(user_id,connection_id) → dws_connections(user_id,id)
```

`dws_fencing_seq` 提供租约 token。授权身份、工作区和 active 状态通过受租约保护的原子操作绑定；已有身份重新授权不能换成另一个账号。

### 2.4 钉钉当前记录与附件归档

```text
dws_messages
  id               bigint        PK；序列自增
  user_id          text
  connection_id    text
  group_id         text
  external_id      text          官方消息ID
  source_version   text          实际源版本/更新时间；未知可为空字符串
  content_hash     text          内容SHA-256
  content          bytea         JSON序列化正文，非凭据密文；最多1MiB
  observed_at      timestamptz    当前内容首次入库或最近变化时的观察时间
  FK(user_id,connection_id) → dws_connections(user_id,id)
  UNIQUE(user_id,connection_id,group_id,external_id)
  UNIQUE(user_id,connection_id,id)

dws_attachments
  user_id          text
  connection_id    text
  message_id       bigint
  id               text
  name             text
  media_type       text
  sha256           text          归档字节摘要
  data             bytea         附件原始字节，非凭据密文；最多8MiB
  PK(user_id,connection_id,message_id,id)
  FK(user_id,connection_id,message_id) → dws_messages(user_id,connection_id,id)
```

额外索引：`dws_messages(user_id,connection_id,group_id,id)`。附件只保存首次观察到的当前字节，不保证它就是历史消息发布时的原版。消息读取 API 将 bytea 解码为 JSON 对象，不要求前端解 base64。

### 2.5 学习通、MOOC、U校园共享存储

```text
platform_connections
  user_id          text          FK users.id
  id               text
  source           text          CHECK chaoxing / mooc / ucampus / jwxt（v7）
  state            text          DEFAULT pending
  enabled          boolean       DEFAULT true
  session          bytea         加密Cookie/Token/平台状态；DEFAULT空；最多2MiB
  auth_expires_at  timestamptz    DEFAULT epoch；二维码挑战截止，不等于所有Cookie的TTL
  error_code       text          DEFAULT ''
  lease_token      bigint        DEFAULT 0
  lease_until      timestamptz    DEFAULT epoch
  updated_at       timestamptz   DEFAULT now()；不是抓取成功时间
  PK(user_id,id)

platform_checkpoints
  user_id          text
  connection_id    text
  kind             text          course/assignment/exam/progress/notice/discussion
  cursor           bytea         加密的适配器续读状态；DEFAULT空；最多2MiB
  updated_at       timestamptz   DEFAULT now()；成功提交此资源一页的时间
  PK(user_id,connection_id,kind)
  FK(user_id,connection_id) → platform_connections(user_id,id)

platform_records
  id               bigint        PK；序列自增
  user_id          text
  connection_id    text
  source           text
  kind             text
  external_id      text          平台原生或明确标记的复合身份
  parent_id        text          父课程/期次/任务等；未知使用空字符串
  hash             text          规范JSON内容SHA-256
  content          jsonb         Record对象，见第3节
  observed_at      timestamptz    首次入库或最近内容变化时的观察时间
  FK(user_id,connection_id) → platform_connections(user_id,id)
  UNIQUE(user_id,connection_id,kind,external_id,parent_id)
```

额外索引：`platform_records(user_id,connection_id,kind,id)`。`platform_fencing_seq` 提供租约 token。消息/记录、会话更新与游标在同事务提交。相同内容重扫不写入；内容变化时原行更新content、hash和`observed_at`，中心ID不变。`observed_at`不是每次成功采集时间，最近抓取成功应查同步状态/游标时间。数据库列仍名为`hash`，对外JSON字段继续为`content_hash`。

重新授权不会复用已 active 的连接来混入另一个平台账号历史。调用方不能自行传 user_id 读取他人数据。

### 2.6 抓取状态与迁移

```text
platform_sync_status
  user_id          text
  connection_id    text
  kind             text
  lease_token      bigint
  last_attempt_at  timestamptz
  last_finished_at timestamptz    可空
  last_success_at  timestamptz    可空；最近一页成功
  last_error_code  text          DEFAULT ''
  last_duration_ms bigint        可空；最近尝试耗时
  last_page_count  integer       可空；名字虽含page，实际为上一成功页返回条数
  has_more         boolean       可空；上一成功页是否有后续
  in_progress      boolean       DEFAULT false
  PK(user_id,connection_id,kind)
  FK(user_id,connection_id) → platform_connections(user_id,id)

dbbridge_migrations
  version          integer       PK
  applied_at       timestamptz   DEFAULT now()
```

迁移 v1：用户和 DWS；v2：三大学习平台连接/游标/记录；v3：抓取状态；v4：业务记录改为按逻辑对象唯一的最新状态。v4将旧多版本按observed_at降序、id降序选最新一行，删除旧行并建立上述不含hash/source_version的唯一键。DWS旧消息附件合并到保留的最新消息行，同附件ID优先保留最新记录已有附件，再补充旧行附件；不提供业务多版本历史。后续schema变更应新增迁移，不修改已应用版本。

历史抓取可能没有尝试/耗时信息；预览可从 checkpoint 恢复历史最近成功时间。页面“抓取中”还会核验连接有效、租约 token 和租约到期时间，防止崩溃残留状态误显示为运行中。空页成功也更新成功时间；成功一页不等于全量完成。

## 3. 学习平台记录 JSON 格式

`platform_records.content` 对应 `contract.Record`。来源 `source`、所属用户/连接和观察时间在外层归档记录中。

```json
{
  "id": 101,
  "source": "chaoxing",
  "kind": "assignment",
  "external_id": "work-demo",
  "parent_id": "[\"course-demo\",\"class-demo\"]",
  "content_hash": "<SHA-256小写十六进制>",
  "content": {
    "kind": "assignment",
    "external_id": "work-demo",
    "parent_id": "[\"course-demo\",\"class-demo\"]",
    "title": "示例作业",
    "fields": {
      "course_id": "course-demo",
      "class_id": "class-demo",
      "status": "未交"
    },
    "observed_at": "0001-01-01T00:00:00Z"
  },
  "observed_at": "2026-01-01T00:00:00Z"
}
```

注意：当前中央层计算内容哈希前，会将 `content.observed_at` 清为 Go 零时间，避免观察时间变化导致无意义更新；**真实观察时间以外层 `observed_at` 为准**。这是当前序列化格式，不把零时间解释成来源发布时间。

`internal/platform/service_test.go` 的 `TestSyncObservationTimestampDoesNotChangeRecord` 覆盖学习通、MOOC、U校园：仅采集时间（含纳秒）变化时身份、内容和哈希不变；来源业务时间更正仍改变哈希，身份保持不变。同名课程、通知或考试可能具有不同来源 ID，不能仅按标题合并。

Record 字段：

| 字段 | 类型 | 含义 |
|---|---|---|
| kind | string | course / schedule / assignment / exam / progress / notice / discussion / discussion_reply_user / material |
| external_id | string | 资源在父范围内的原生/复合ID |
| parent_id | string，可省略 | 原生父范围，未知不推断 |
| title | string，可省略 | 来源标题 |
| content | string，可省略 | 已适配的来源正文；不保证每类资源都有详情正文 |
| fields | object | 平台白名单字段，命名按平台保真，见下表 |
| source_version | string，可省略 | 上游确有版本时才提供 |
| observed_at | RFC3339时间 | 适配器观察时间；入库内容中当前被归零，查外层真实值 |

所有平台字段均可能因源端未返回而缺省。未提供的截止时间、成绩或完成率不补零、不推测。时间后缀 `_raw` 的值保留来源文案，不能直接当精确时间使用。

## 4. 各平台可获取的信息

### 4.1 学习通（chaoxing）

| kind | 当前输出 | fields |
|---|---|---|
| course | 课程标题、教师、课程/教学班身份 | course_id、class_id、teacher |
| assignment | 作业标题、原始提交状态；不读取答案或提交 | course_id、class_id、status |
| material | 课程资料文件/文件夹目录；title 为名称，content 为空，不下载文件 | course_id、class_id、data_id、parent_data_id（根目录为 `"0"`）、path（含当前条目名的路径数组）、is_folder、file_type；有值时 size_raw、creator、time_raw |
| progress | 课程任务点完成数/总数 | course_id、class_id、completed、total、metric=`task_points` |
| exam | 账号级考试安排标题、原始时间/状态/成绩文案 | time_raw、duration_raw、availability_status、submission_status、score_raw、terminal_raw、identity_source |
| notice | 通知标题、正文（HTML转换为文本） | 源端提供时保留 sendTime、insertTime、isread、sourceType |
| discussion | 课程讨论话题标题、正文、作者、回复/点赞计数 | course_id、class_id、author、reply_count、praise_count、topic_type，及存在时的 createTime/updateTime/lastReplyTime 等源时间；不含回复正文或上传文件 |

回复用户按需资源 `discussion_reply_user`：external_id 为回复ID，parent_id 为话题UUID，title 为显示名，content 为空；fields 仅 course_id、class_id、topic_id、user_id、user_name、anonymous（明确时）、coverage=top_level_only。平台用户列表按用户ID/显示名汇总回复数；匿名不保留真实ID。不采集回复正文、上传图片/文件、学号/联系方式，不覆盖楼中楼；每个回复对象保存最新观察，但名单仍是曾观察到的参与者；缺失回复不自动移除，不代表当前未删除的精确名单。GET/POST接口及使用见根README。

身份与归类：

- 课程 `external_id` 为 JSON 数组字符串 `[course_id,class_id]`。
- 资料 `external_id` 为 dataId，`parent_id` 为课程复合键；父文件夹由 fields.parent_data_id 表达。沿用当前状态去重更新，未见对象不自动删除；签名/下载链接不进入业务记录。
- 作业 `external_id` 为 workId；`parent_id` 为上述课程复合键。
- 进度 `external_id=task-points`；`parent_id` 为课程复合键。
- 讨论 `external_id` 为原生话题 UUID，`parent_id` 为课程复合键，明确归入 course_id+class_id；不是 assignment，也不因正文有“上传习题”就自动创建作业。读取官方课程讨论列表时必须带当前教学班tags，不能省略后把权限错误当空列表。
- 考试 `parent_id=account-exam-schedule`。优先原生考试ID；缺失时以标题+时间内容哈希生成 `schedule:...`，并标记 `identity_source=schedule_text_hash`。时间更正可能产生新的逻辑观察，不应误认为可靠原生ID。
- 通知优先 uuid/idCode/id；当前无明确课程字段，因此不按课程名猜测归属。
- **课程归类必须同时使用 course_id + class_id**，同课程不同班不能混合。预览按这两个明确字段分组；缺少任一标识的考试/通知放在“未关联课程”。

能力限制：作业当前没有结构化完整截止时间/附件/题干覆盖；进度只是任务点汇总；考试只读账号安排，不进入试卷。课程列表本地每页50，逐课作业分页要求源页面有明确总量依据。研究中有真实扫码和读取成功案例；当前 Go 登录已按成功流程修正，匿名二维码/待扫码轮询验证通过，但不能将研究样本等同于所有账号端到端验证。

### 4.2 中国大学 MOOC（mooc，含SPOC）

| kind | 当前输出 | fields |
|---|---|---|
| course | 课程标题、开课期次、课程类型、学校 | courseId、termId、courseType、school |
| assignment | 安全目录中作业/测验任务标题与源元数据 | courseId、termId、unitId、contentId、courseType、task_type=quiz/homework，及下列可选字段 |
| exam | 安全目录中考试任务标题与源元数据 | 同上，task_type=exam |
| progress | 明确个人状态、源端进度/分数字段；或最近访问单元 | 同上；lastLearnUnitId存在时单独观察，coverage=`last visited unit only` |
| notice | 当前未支持 | 返回 unsupported，不产生伪造空结果 |

任务元数据白名单（仅源端返回标量时保留）：

```text
contentType, type, submitStatus, finishStatus, statusText, statusName,
studentScore, userScore, finalMark, usedTryCount,
submitEndTime, deadline, endTime, examEndTime, testEndTime,
homeworkEndTime, jobDeadline, closeTime, startTime, examStartTime,
evaluateStart, evaluateEnd, evaluationEndTime, evaluateScoreReleaseTime,
enableEvaluation, scorePubStatus, peerReviewStatus, evaluationStatusText,
finished, learned, learnStatus, progress
```

任务还带 `status_interpretation="source values only; completion unknown unless explicit"`，互评阶段与提交状态不合并。没有明确完成字段就不计算完成率，lastLearnUnitId不等于完成单元。

ID 为 base64url 编码 JSON 数组：课程 `[courseId,termId]`；任务 `[courseId,termId,unitId,contentId,nodeId]`。任务 parent_id 为所属课程期次键。不要只用 courseId 合并不同期次。

不调用可能开卷/开始计时的 getOpenQuizPaperDto/getOpenHomeworkPaperDto，不获取答案、不提交。SPOC源期次映射有已知限制，公告未支持。本 Go 模块以官方公开代码/调研与脱敏Mock验证；完整真实扫码和实际账号数据仍需验收。

### 4.3 U校园 AI（ucampus）

| kind | 当前输出 | fields |
|---|---|---|
| course | 门户首页课程标题、班级、课程实例与资源身份、原始起止文案 | course_id、class_id、instance_id、course_resource_id、class_name、start_time_raw、end_time_raw |
| assignment | 教材 leaf 任务身份和源策略；不是独立教师作业 | course_id、class_id、instance_id、course_resource_id、unit_id、task_id、task_type=`textbook_task` |
| progress | 教材 leaf 的原始状态和策略 | 同上，以及 duration、tab_type、state、strategies |
| exam / notice | 当前未支持 | 返回 unsupported |

教材任务/进度白名单：

```text
state:      pass, pass2, perm
strategies: start_time, end_time, required, min_score_pct, statistic_mode_out
```

assignment/progress使用同一受控leaf投影，字段不保证都是独立作业或课程总完成率。`source_version` 仅在上游 rt.Version 存在时写入。ID 保留 course/class/instance/resource/unit/task 组合，不能只按 task_id 去重。

**额外会话要求**：二维码会话能用于门户课程；教材任务/进度需要合法官方内容端 annotator 会话。当前不会生成或伪造签名Token，缺少合法会话返回 unsupported。运维可通过 ucampus-import 导入并经官方账户接口校验。

验证范围：匿名二维码创建与WebSocket握手真实通过；用户反馈U校园可用。不能据此声称教材任务/进度、正式考试或通知已完整验证。

### 4.3.1 杭电教务（jwxt，手动采集）

本实现依据调研协议，仅离线 fixture 验证，未实际访问教务。登录账号和完整 Cookie 会话由中心按 user+connection 加密保存于 platform_connections.session；密码不存，业务记录无 Cookie/票据/完整学生资料。教务不参与 Targets 定时采集；无自动重登录或探活。

| kind | 当前输出 | fields |
|---|---|---|
| course | 指定学年/学期的已选课程 | academic_year、term、entry_type=course、course_id、class_id，及有值的 class_name、credits_raw、course_category/category、teacher_raw/teacher_name、location_raw、schedule_raw 等白名单 |
| schedule | 普通课表安排与实践安排 | 普通项 academic_year、term、entry_type=class_session、weekday（1–7）、periods_raw、weeks_raw、identity_source=schedule_text_hash，以及有值的 course_id/class_id、teacher、location、campus、credits_raw 等；实践 entry_type=practice、identity_source=practice_text_hash，title为原文 |

两类 ParentID 为 `[学年起始年份,学期]`；course 的 ExternalID 为 `[course_id,class_id]`，schedule 为安排原文哈希。来源哈希在时间/地点变化时可能变化；中心完整课表同步会清理本连接该学期已消失的课表归档并刷新TIMETABLE日程。不得将节次原文直接当作钟点。Adapter每页100项，最多5000项、响应4MiB、游标1MiB；course逐页保存加密checkpoint，schedule在单次手动请求收齐完整快照再事务提交。未知分页包装/坏记录失败，不返回可提交部分结果。

POST `/api/v1/platforms/jwxt/login` body `{username,password}`；POST 连接 `/sync` body `{resource:"course",academic_year:2026,term:1}`；课表 resource=schedule 还必须提供 calendar（首周周一日期及节次起止时间）；GET 连接 `/records?resource=...` 或统一 preview 查询已归档。学年起始年份2000–2100，学期1/2；抓取状态按资源显示最近一次手动采集，checkpoint独立按学期保存。完整字段和已知限制见 [教务说明](internal/jwxt/README.md)。

### 4.4 钉钉（dws）

消息 content 是以下 Observation JSON，保持官方字段名：

```text
messageId, conversationId, sender?, senderId?, senderType?,
messageType, text（可为null）, createTime, updateTime?,
replyId?, threadId?, quotedMessage?, forwarded?,
resourceRefs[], labels[], verification
```

- createTime/updateTime保留来源形式，可能为毫秒、RFC3339或学习平台样式的时间字符串；解析时无时区日期时间按Asia/Shanghai处理。
- quotedMessage/forwarded仅保留白名单来源文本和身份字段，排除传输票据URL等。
- resourceRefs包含 attachmentId、type、resourceId、messageId、openConversationId、name?、downloadable。只允许确认属于已有消息的资源下载；内部download参数不作为普通输出保留。
- labels为规则匹配 assignment/exam/notice/file/ordinary，可多标签；verification始终unconfirmed，不是模型确认。
- 上下文为同群前后各3条中心ID邻居，不代表语义推断关联；引用字段保留但不保证完整线程遍历。
- 附件按需归档，返回名称、媒体类型、SHA-256及image/document/archive/other分类；只声明首次当前观察，不能证明任意历史原版。没有PDF/Office抽取或OCR。

仅同步显式授权群，不因拥有学校账号而跨用户共享私人消息。真实群历史、双账号刷新和真实附件覆盖需单独验收。

## 5. 会话、刷新与敏感字段

- `sessions` 是应用自身登录；`platform_connections.session` 是学习平台会话；`dws_connections.workspace` 是官方CLI原生凭据工作区，三者不同。
- 平台 Session 可能包含私有 Cookie、登录临时状态；U校园还可有JWT、OpenID、annotator。均由统一 Vault 使用 CREDENTIAL_KEY 加密，并以应用用户+连接作为绑定信息。
- 成功HTTP响应下发的Set-Cookie由私有cookiejar接收，适配器返回新的Session，中央层在授权/同步成功后持久化。这是被动保存更新，不等于实现了主动刷新、失效后自动重登或所有JWT轮换协议。
- 同步失败不推进游标。当前没有统一的失败请求凭据更新恢复协议，不能承诺所有中途刷新情况都能恢复。
- 普通记录、抓取统计、预览API和后续Agent均不应获得Cookie、Token、CLI工作区、加密会话原文或二维码内部票据。

## 6. 同步周期与完整性

- 学习平台Worker仅选择enabled、active且应用用户未禁用的连接。启动立即扫描，整轮处理结束后等待60秒；串行处理各连接、各资源，每资源一页。网络耗时会叠加，不是严格一分钟内全量完成。
- 无持久增量API的适配器在一轮分页结束后从头扫描，由hash去重；空Records但HasMore=true合法，必须继续后续周期。长课程列表/逐课分页可能需要多个周期。
- DWS默认每轮结束等待1分钟，可配置DWS_POLL_INTERVAL；首次回看24小时，后续重叠5分钟。不能保证永久捕获所有旧消息编辑/撤回。
- 页面扫码状态轮询与数据Worker分开，学习通按返回poll_after_ms约3秒、MOOC约2秒、U校园可能长轮询35秒。
- 预览每10秒自动刷新是可选纯数据库读取，不改变采集周期。

## 7. 数据预览及读取入口

所有HTTP入口需当前应用会话，写操作还需Origin+CSRF。应用用户身份从服务端认证上下文取，不由模型/请求body自由选择。

| 入口 | 用途 |
|---|---|
| GET /api/v1/platforms/connections | 当前用户学习平台连接 |
| GET /api/v1/platforms/connections/{id}/records?resource=assignment&after=0&limit=20 | 按资源读取当前记录；resource空可跨资源 |
| POST /api/v1/platforms/connections/{id}/sync | body `{resource:"course"}`；立即读并提交一页 |
| GET /api/v1/preview/overview | 当前用户各连接/资源归档数量与抓取状态 |
| GET /api/v1/preview/records?source=chaoxing&connection_id=...&resource=&after=0&limit=20 | 统一归档预览，含DWS消息 |
| GET /api/v1/preview/courses?source=chaoxing&connection_id=... | 学习通course+class课程分组与未关联记录数 |
| GET /api/v1/preview/records?...&course_key=... | 按返回opaque key筛课程；resource空查看同课所有资源 |
| GET /api/v1/dws/connections/{id}/messages?group_id=...&after=0&limit=20 | 钉钉群消息当前记录 |
| GET /api/v1/dws/connections/{id}/messages/{message}/context?group_id=... | 同群邻近上下文 |

学习通 course_key 是服务器生成的base64url JSON组合键，仅用作过滤，不是授权凭据；每次仍检查用户和连接归属。`__unassociated__` 表示无明确课程组合身份。当前其他平台还没有同样的课程下拉视图，但记录中保留其复合身份。

预览分页返回 `{items:[],next_after:...}`；next_after=0表示当前查询无后续。返回各对象当前记录；`record_count`（记录数）和`entity_count`（逻辑对象数）为兼容保留，可能相等。分页仍按稳定中心ID使用`after=id`，更新内容不改变ID，列表顺序可能不变，已越过的ID也不会因更新重新出现；这不是change feed，查看变化应刷新或回到首批。对外`content_hash`、完整JSON与字段树格式保持兼容。

同进程可通过 model.PlatformRepository / PreviewRepository / DWSRepository 注入只读业务查询。当前 Agent 及其工具网关未实现，bridge只有demo.echo；数据库可读不表示Agent已接入。未来应向Agent提供绑定当前用户的受控查询/证据工具，不直接交SQL或会话。

## 8. 业务变更trigger端口

`internal/trigger`是独立的进程内Port。采集业务记录新增或内容更新在事务成功提交后，尽力（best effort）调用Handler；覆盖已接入的课程、作业、考试、进度、通知、讨论、回复用户及DWS附件，排除DWS `message`。用户、应用会话、连接授权状态、credentials/workspace、checkpoint、租约、jobs和抓取状态等管理数据不触发。

默认`app.New`使用`Noop`；应用可通过`app.NewWithTrigger(ctx,cfg,handler)`注入Handler，数据库接线使用启动期`dbbridge.DB.SetTriggerHandler(handler)`。自行Handler可按`Created`、`Updated`、`Deleted`协议操作订阅；`Deleted`仅协议预留，当前不判断上游删除、不发出删除事件，缺失对象不会自动删除。

payload仅包含用户/连接/来源/资源/记录等安全引用、操作、发生时间及`ChangedFields`字段名，不包含历史正文、Cookie、Token或其他credentials。它没有数据库trigger、outbox、持久队列、重试或实际Agent；提交后调用失败不回滚已保存业务数据，也不提供可靠投递保证。详见[trigger/README.md](internal/trigger/README.md)。

## 9. 验证与后续维护

已执行全仓测试、vet、编译、相关模块race，以及独立PostgreSQL事务/租约/去重/课程分组/预览隔离测试。匿名二维码生成/等待轮询和U校园WebSocket握手有真实网络验证；具体成功账号及各资源完整覆盖需逐平台验收。学习通旧研究的真实成功案例不应自动当作现Go实现所有接口已验证。

本文件是当前实现的数据字典；完整未来契约仍见 CONTRACTS.md / DATA-MODELS.md。后续新增字段、刷新协议、课程映射或Agent工具时，应同时更新此文件及对应模块README，并说明兼容性。

## Agent Framework 表（迁移 v5）

通用框架使用以下表：`agent_instances`（实例配置/owner/status）、`memories`（V9：用户 PUB、Agent PRI、Workflow PRI；旧 agent_memories 作为迁移存档保留）及 `memory_relations`（同用户直接关联）、`agent_workflows`（owner 私有定义及单父级）、`agent_workflow_versions`（完整版本快照）、`agent_runs`（父子关系/状态/预算使用/安全错误）。完整 DDL 位于 [agent_schema.go](internal/dbbridge/agent_schema.go)，字段与迁移说明见 [框架文档](../agent/framework/README.md)。

框架 Data Query 仅开放 `platform_records` 的白名单投影，受 owner、enabled 及 active 连接约束；新建的 Agent 私有表、完整 `content` / `fields` 对象和凭据均不进入通用数据集目录。基础字段保留 id/connection_id/source/kind/external_id/parent_id/title/text；`text` 优先读取记录正文 `content`，兼容旧 `text`。

业务字段均以 `string` 返回，缺失或不合类型为空字符串，源数字与布尔值仅转文本，不转换时区、epoch单位或业务含义：

| 投影字段 | 来源与语义 |
|---|---|
| course_id / class_id / term_id | fields的snake_case优先，兼容MOOC courseId/classId/termId；不猜跨源课程关系 |
| course_name | 显式course_name/courseName；只有course资源可回退该记录title，任务标题不充当课程名 |
| task_type | 原始task_type |
| status / source_status | 同一兼容别名：status → statusText → statusName → submission_status → submitStatus → finishStatus → learnStatus，保留第一个明确源值，不统一解释完成状态 |
| submission_status / availability_status / finish_status / learn_status | 原始submission_status或submitStatus、availability_status、finishStatus、learnStatus，分别保留，不能相互推断 |
| progress / completed / total | 原始progress/任务点完成数/总数，不生成百分比 |
| deadline | deadline → submitEndTime → homeworkEndTime → examEndTime → testEndTime → jobDeadline；不以通用endTime、closeTime、U校园可用期结束冒充截止 |
| start_time / end_time | 原始start_time/startTime/examStartTime/start_time_raw或end_time/endTime/closeTime/end_time_raw；U校园回退strategies.start_time/end_time，仅代表来源的起止/可用期 |
| published_at | 仅明确published_at/publish_time/publishTime/releaseTime；不使用观察、创建或开放时间补齐 |
| source_time / time_raw | source_time依次读time_raw/createTime/sendTime/insertTime，含义由source/kind决定；time_raw原样，考试时间区间与资料时间文案不解析为日程 |
| file_type / size_raw / is_folder / path | 资料目录的明确源值；path仅接受字符串数组并返回JSON文本，嵌套对象不暴露 |

全部共32个字段，可按需选择；筛选值仍为字符串。即使已取得截止日期或原始epoch，也不能据此猜测日程起止时间、学期或教学周。字段是增量兼容扩展，无数据库迁移；跨来源的状态代码和时间格式没有统一化。独立PostgreSQL回归位于 `internal/dbbridge/agent_data_fields_test.go`，默认未设置专用 `TEST_DATABASE_URL` 时明确跳过集成用例。

### Agent 管理配置（迁移 v6）

`agent_framework_settings`：id（固定 1）、encrypted_settings（credential.Vault AES-GCM 密文，最多 1 MiB）、version（正整数并发控制）、updated_at。模型与 Search 的完整配置均加密，API Key 不存入普通 JSON 或通用数据集；仅管理后台服务可解密。`agent_runs` 原有 record JSONB 继续作为管理页的运行历史，按 owner/agent 查询最近记录。

## 教学周日程（v8）

新增唯一一张 schedules：id(text PK)、user_id(FK users)、academic_year(int)、term(int)、teaching_week(int)、start_time/end_time(timestamptz分钟精度)、title/details(text)、source(TIMETABLE/USER/AGENT)、event_status(INCOMPLETE/COMPLETED)、mod(JSONB)、series_key/origin_key(text)、active(bool)、created_at/updated_at。time_status不持久化，查询按当前时间计算。复合索引user_id+academic_year+term+teaching_week；时间范围索引user_id+start_time+end_time；TIMETABLE来源身份在用户/学期内唯一。

课表手动同步使用完整快照事务刷新TIMETABLE；取消安排保留inactive行与mod历史，列表只返回active，按ID可追溯。USER/AGENT不受替换。完整字段规则、API、日程身份及修改冲突政策见internal/schedule/README.md。

Memory V0.2 的完整结构、迁移和权限矩阵见 [Memory V0.2](../agent/framework/MEMORY-V0.2.md)。该系统不合并采集记录或日程数据。

课表派生日程的mod还保存weeks_raw、teaching_weeks、week_pattern（ODD/EVEN/ALL）作为来源周次规则。永久修改及未来周继承同时按series_key和单双周模式隔离；界面显示原始教学周规则，单双周按教学周编号判定。

## 结构化学业检索（v12–v13，2026-11-08）

前述 platform_records 为兼容原始归档路径；学习通/MOOC 另增加可重建的结构化层，不改变原生身份、账号授权和原始证据。

- `courses`：按用户、连接、来源、原生课程ID隔离的课程目录。
- `academic_offerings`：明确开课实例；学习通为课程＋教学班，MOOC 为课程＋期次；保留 opaque course_ref、名称、教师、学校、SPOC源期次关系和覆盖状态；v13补充课程简介与白名单课程要求/教师团队字段；仅保存来源实际提供的内容，并保留简介来源课程和覆盖状态。不同教学班、期次及同名课程不自动合并。
- `academic_items`：章节、课时、资料、作业、考试、讨论、通知与课程观察，显式 offering_id 外键；无法确认课程的账号级通知/考试可为空。包含正文、作者、目录路径、原生层级ID、标准状态和各类独立时间字段。
- `academic_item_personal_states`：尝试、提交、评分、互评、任务点、最后访问等源状态，与事项内容分开；不以尝试数或最后访问推断完成。
- `academic_item_evidence`：事项到原始记录的外键映射；同一任务的 assignment/progress 可合并为一条事项并保留多个证据ID。历史MOOC unit6被归考试的观察在结构化层纠正为讨论，原证据保留。

所有关联使用 user_id＋connection_id 复合外键，检索核验连接 active/enabled 与用户有效性。索引覆盖课程/类型、状态、发布时间、截止时间和文本；时间范围在 timestamptz 上比较，不再对毫秒数字或格式不一的字符串排序。

时间对象保存 value/date/raw/precision/source_field/state。完整日期时间可规范为UTC；仅日期保存 date，不捏造午夜；缺年份或相对倒计时保留部分精度且不参与瞬间范围查询。发布时间、更新时间、开始、结束、截止分开，资料列表时间未核实语义时仅保留元数据。

Agent 新增 `academic_offerings`、`academic_items` 数据集，使用既有 data_query，最多32个字段/查询；旧 platform_records 数据集继续作为原始证据。新目录与结构化筛选接口为 `/api/v1/academic/{offerings,items,tree}`，检查页 `/admin/academic`；tree?format=text 返回目录文本。完整树超过20000条明确失败，不能静默截断。

采集每页原始记录、结构化更新、检查点同事务提交；`go run ./cmd/academic-maintain rebuild` 可从原始归档重建，`export` 导出真实目录JSON/文本；`go run ./cmd/academic-refresh` 只补采学习通/MOOC，不启动Agent或DWS。生产操作前保留一致性备份。详见 [存储说明](internal/dbbridge/academic_storage.md) 与 [检查API](internal/academic/README.md)。

## 课程采集黑名单（v14）

`collection_policies` 保存每个用户的版本号和更新时间；`collection_exclusions` 保存按用户、连接、来源及原生 course_ref 精确匹配的停采规则，以及课程名称、教学班/期次、offering_id 展示快照。规则不依赖可重建目录的外键，课程目录暂时缺失或连接停用时仍可显示和移除。新选择必须来自当前用户有效连接的已有开课实例。

后台 `/admin/academic#collection-policy` 可勾选已有课程、筛选、保存或清空选择；API `GET/PUT /api/v1/academic/collection-policy` 使用版本控制和CSRF保护。PUT整体替换，参数为 `{version,offering_ids}`，最多2000项；冲突返回409。

采集每页前重新加载策略，学习通在课程请求前跳过、清理该课分页；MOOC按所属路由期次跳过，包括引用源期次和待取正文队列。数据库提交事务再检查最新黑名单，防止保存策略时已经发出的请求写入原始记录、投影和变化事件。历史归档保留，解除后按后续正常扫描继续。共享课程发现列表及没有明确课程标识的账号级通知/考试仍读取；不凭同名或来源期次推断黑名单归属。

## Learner暂存提交账本（v16）

`learner_stage_commits`按`owner_user_id,session_id`唯一，保存完整提交payload的request_hash、提交receipt与created_at。定义、目标PRI记忆和内部关系在同一短事务应用，提交前比较选择时的Workflow/记忆/关系基线并重新检查具体工具权限。相同session及相同payload重放返回原receipt，差异payload冲突。未提交草稿只在Run内存中，不写此账本，也不能据其声称跨Run自动恢复或request_id去重。模型输出READY只表示请求提交，父调用接收宿主生成的APPLIED/NO_CHANGE及commit_receipt。

## 按课程生成Trigger批次（v15）

Trigger模型选择协议为`indices_v1`：每次请求的changes使用从1开始的index，模型返回`relevant_indices`。编号只在当前请求/页有效，后端映射回该页冻结数组的真实change_id后，仍以`relevant_change_ids`保存control_batches.trigger_output；control_events、原始changes及下游证据引用保持真实ID。Agent Run原始输出可见短编号，回查时需使用同次输入；不能拿编号当全局事件ID。旧持久结果不迁移。

`control_batches.batch_scope` 为可空JSONB，课程范围为 `{kind,connection_id,offering_id?,course_ref?,course_name?}`，DWS群范围为 `{kind:"group",connection_id,group_id}`。已有已封批历史保留原scope和内容，不回填或重分组。学习通、MOOC在每轮刷新结束后按用户＋平台＋连接＋确认的所属课程引用聚合，不按资源、同名或SPOC内容源期次合并。无可靠课程归属的信息按连接单列。DWS在同轮同用户下按连接＋群聚合，单群超过100条／44000字节时仅在该群内拆块。变化payload新增可选group_id：消息取同步检查点的群，附件取归属消息的群；旧未封批消息可从快照conversationId读取，旧附件按用户＋连接＋中心消息ID读取归属。无法确认或群标识冲突的事件单独封为blocked/DWS_GROUP_UNRESOLVED，不跨群合并。以上复用现有JSONB，无新增数据库迁移。U校园及其他来源仍按旧平台批次处理。

同课程的一轮全部变化只封成一批；不以100条或全局5000事件查询上限拆散。课程原始事件总量超过700KiB则保留全部证据并封成blocked/COURSE_INPUT_TOO_LARGE，不截断。Trigger课程输入可达768KiB、上下文至少1MiB。未归属和旧平台批次仍采用100条／44000字节边界。封批事务及advisory lock保证重复调用和并发调用不重复分配事件；课程归属使用冻结的after/before和当前结构化证据核对，冲突保守归入未关联。后台显示课程范围，Filter仍逐项处理Trigger选中的变化。
