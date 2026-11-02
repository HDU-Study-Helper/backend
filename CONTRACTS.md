# 第一版接口规范 v0.1

本文件是完整目标接口语义规范，共享数据对象与端口输入输出见 `DATA-MODELS.md`。当前 v0.1 实际交付为 README 的用户管理、DWS 专用路由、bridge Demo 和 dbbridge Repository；DWS 专用 HTTP 契约见 `internal/dws/README.md`。本文件的通用平台/Agent/Handle 签名与学业路由仍为后续草案，不代表已经实现。v0.1 用户认证为本地用户名密码 + PostgreSQL hash-only Cookie Session，所有写请求要求同源 Origin 与会话 CSRF；管理员权限在可信服务端判定。

## 1. 总体约定

- Go 包名使用小写：`handle`、`agent`、`jwxt`、`dws`、`chaoxing`、`ucampus`、`mooc`，它们在 `internal/` 下同级。
- Handle = 应用编排层。HTTP Handler 只能调用 `handle.Service`；平台负责人对外暴露 `contract.Adapter`，不注册 HTTP 路由。
- Go 方法接收 context，尊重取消/截止时间；成功返回数据、失败返回 error。不使用 panic 表示平台错误。
- 中心 ID 是不可猜测字符串；原生 ID 保留字符串，不转整数，不拼接可执行命令。
- 时间采用 RFC3339 带时区；存储 instant 统一 UTC，显示时区默认候选 Asia/Shanghai；未知时间必须缺省。日期精度用 YYYY-MM-DD，不捏造截止到午夜。
- `attachments/items/evidence` 等集合响应使用 `[]`，不输出 null；可选对象用省略表示未知。
- 所有请求参数、长度、页数、外部调用数均由业务层限制。普通列表 limit 默认 20，最大 100；平台单页同步默认 100，最大 500，可按平台更严格限制。
- 私有数据始终按 user + connection 隔离。Actor/Scope 由可信代码生成，不允许客户端/模型传 user_id 选择账号。外部对象、附件、挑战和会话也必须属于该连接。

## 2. 平台 Adapter

### 2.1 交付形式

各平台在自己的目录实现下列统一接口，完成后加编译断言 `var _ contract.Adapter = (*Adapter)(nil)`：

```go
type Adapter interface {
    Describe() Descriptor
    BeginAuth(context.Context, Scope, AuthInput) (AuthChallenge, error)
    ContinueAuth(context.Context, Scope, string, AuthInput) (AuthChallenge, error)
    CheckSession(context.Context, Scope) (SessionStatus, error)
    Disconnect(context.Context, Scope) error
    ListPartitions(context.Context, Scope, PartitionRequest) (PartitionPage, error)
    Sync(context.Context, Scope, SyncRequest) (SyncPage, error)
    Search(context.Context, Scope, SearchRequest) (SyncPage, error)
    Fetch(context.Context, Scope, ExternalRef) (Record, error)
}
```

构造函数约定 `New(deps Dependencies) (contract.Adapter, error)`；Dependencies 由平台包定义，注入受限凭据 Factory、超时 HTTP Client 或隔离 CLI runner。构造函数只校验依赖，不隐式登录、不启动后台循环。具体实现类型可以不导出。

| 方法 | 输入/输出语义 |
|---|---|
| Describe | 声明实际已实现、测试通过的认证方式和资源。不能因为产品预期就宣称支持 |
| BeginAuth | 在已创建的 pending connection 上开始认证，成功会话写加密 Store，返回安全挑战状态 |
| ContinueAuth | 校验 challenge 属于 Scope、未过期，按需轮询/提交验证码；authorized 后不再暴露平台 Token |
| CheckSession | 只检查/安全刷新当前平台会话，expired 不是内部异常；网络失败返回结构化错误，不假装 expired |
| Disconnect | 幂等删除本地会话/挑战，不依赖远程注销成功；中心层先禁用连接和在途提交权限 |
| ListPartitions | 按 resource 有界分页发现当前可读分区，返回稳定 Partition.ID；账户级资源返回一项 ID 为空的分区，不支持资源返回 unsupported |
| Sync | 指定 resource + partition 拉取一页已脱敏 Record，不写中心状态和 checkpoint |
| Search | 仅执行有界只读搜索，支持时声明 CanSearch；否则 unsupported |
| Fetch | 以原生复合标识读详情，支持时声明 CanFetch；记录不存在返回 not_found |
| FileReader.OpenAttachment | 可选接口，输入 AttachmentRequest 中的期望源版本/摘要；只能返回可验证匹配的授权附件，无法验证返回 conflict；句柄由调用方关闭，禁止返回带票据 URL |

历史证据附件优先读取与 EvidenceRef 绑定的不可变归档。实时源读取必须提供并验证 ExpectedSourceVersion 或 ExpectedSHA256；两者都无则失败，不能把当前字节冒充历史附件。摘要验证需要在向客户端输出字节前完成，可先写有大小限制的临时文件。附件内容尚未采集且平台不能证明版本时，只展示元数据与不可用状态。

Search/Fetch 无法原生支持时可明确 unsupported，不允许伪造成功空数组。同步是各平台最小交付必须实现的读取路径。CanSearch/CanFetch 为粗粒度发现，指定资源不支持时仍返回 unsupported。

### 2.2 身份和原始记录

连接 source 由中心注册表确定，不信任平台返回或 HTTP body 任意覆盖。每条逻辑记录键：

`(user_id, connection_id, resource_kind, external_parent_id, external_id)`

学习通 course/class 组合、钉钉 conversation/message 组合不得丢失。`CourseRef` 是平台内课程标识，不能直接当中心 CourseID；没有明确课程归属时省略。群与课程关系由中心映射或待确认推断解决。

每个 Record 是源观察，至少包含 Ref、ObservedAt、ContentHash。业务时间使用 OccurredAt/UpdatedAt，不能用采集时间冒充发布时间。Content/Fields 是允许进入业务层的源内容；精简响应不能丢失与学业事项有关的原文。完整原始响应如需保存，先删除 Cookie、Token、签名参数，再以受控 PayloadRef 引用；凭据本身不进入 Raw Event Memory。

`SourceFields` 容纳课程名、学期、教师、地点、开始/结束/截止时间、平台原始状态和课表周次。平台未提供的信息留空，不能由 Adapter 用 LLM 补齐。章节任务点在确有能力时映射为 assignment，并在源文本保留其类型；本版不获取考试答案、不自动做题或提交。

内容哈希算法固定 SHA-256，小写十六进制。输入为对 Ref/CourseRef/Title/Content/Author/Fields/OccurredAt/UpdatedAt/Deleted/Attachments 的确定性 JSON 编码，排除 ObservedAt、PayloadRef、短期 URL；附件按 ID 排序，其他无序列表规范排序。平台原生版本号保留 SourceVersion。中心对同一键、相同哈希且相同 SourceVersion 幂等；业务时间、内容或非空原生版本变化都保存新不可变版本，不能吞掉仅时间更正。精确的确定性编码辅助函数由核心负责人统一实现，平台不得各写一套。

删除必须是平台明确提供的 tombstone；一页/一次同步没看到对象，不代表对象已删除。

### 2.3 分页、分区与增量

- Worker 先调用 ListPartitions，分页持久化发现结果并为每个分区入队；发现游标与数据同步 checkpoint 分开。发现失败必须显式记录，不能宣称全账户同步完成。空分区列表不构成删除证据，群/课程授权范围在平台连接内部配置，本版不新增选择群 HTTP 协议。发现到的范围必须限制为用户授权范围。
- SyncRequest.Resource 明确资源；PartitionID 是课程/班级/群等原生分区。账户级流可以为空；平台的 README 必须说明各资源分区格式。
- 首次 Cursor 为空；平台自己封装分页参数、水位和快照状态，不强行共用页码。Cursor 可含敏感 upstream 状态，只能在平台/加密 checkpoint 内使用。
- SyncPage.NextCursor 是本页成功提交后应保存的续读状态；HasMore=true 时必须非空且推进；HasMore=false 时也可返回下一轮增量水位。
- 达到当前同步终点后，如果平台不支持持久增量，可返回空 NextCursor，下轮从头扫描并依赖幂等去重，必须记录这一限制。
- 空 Records + HasMore=true 合法，但必须推进 cursor；检测 cursor 循环即报 parse_failed，避免无限读取。
- 一页失败返回 error，不返回可推进的部分成功。多课程/多资源拆成独立分区作业，保存各自成功与错误；不得把局部失败伪装为整次同步成功。
- Search 用同一页结构，但 NextCursor 只作本次搜索续页，不写同步 checkpoint。模型工具无直接 cursor 访问，网关负责限页。
- API 不允许用户传平台 cursor；Worker 从存储读取并在记录/outbox/checkpoint 同一事务提交后推进。

### 2.4 平台预期范围（不是已验证能力）

| 子目录 | 第一阶段优先能力 | 后续候选能力 | 注意事项 |
|---|---|---|---|
| jwxt | 授权/SSO、课程、课表 | 考试安排 | 保留教学周/节次；学期起始日不明时不推算具体日期 |
| dws | Go 包装固定版本官方 DWS CLI 二进制，接入个人授权与指定群消息读取 | 消息增量、历史搜索、文件元数据按二进制实际能力验证 | 不重写协议/认证；群标识用 message.ParentID；隔离各连接配置、凭据、缓存与子进程 |
| chaoxing | 授权、课程、逐课程作业 | 通知、考试、资料详情/附件 | 课程/班级组合和不同模块签名留在客户端内部；接口以真实测试为准 |
| ucampus | 授权、英语课程、学习任务 | 任务详情、通知 | 原始任务状态与截止时间保真；不扩展代做/提交 |
| mooc | 授权、MOOC/SPOC 已选课程与期次、作业/测验/考试任务元数据 | 已验证只读资料；公告接口需独立验证 | 保留 courseId/termId/unitId/contentId；不调用未确认副作用的开卷接口；互评阶段不等于提交状态 |

中国大学 MOOC 作为独立来源纳入本版规范，标识固定为 `mooc`，不并入学习通。该来源由本次用户要求补入，早期 PROJECT.md 的四来源表尚未同步；现有调研见 `../research/mooc/REPORT.md`，不代表后端已完成接入。

MOOC 原生课程与开课期次必须分别保留；复合标识采用可逆编码，具体规则由 mooc 模块文档明确。测验映射 assignment 时在源字段保留 task_type=quiz，作业为 homework，考试为 exam；互评采用独立的 source phase 字段，不能覆盖原始提交状态。

每个平台 README 用“已验证 / Mock 通过 / 未支持”标注能力，不将上述表格直接抄成 Describe 的支持列表。

## 3. Agent 接口

`agent.Service.Investigate` 和 `Chat` 接收可信 Request，输出 Result；两种模式共享证据、用户状态、工具接口。逻辑上调查与分析可以分步，首版不要求独立多模型。

允许的模型工具：

| 工具 | 用途 |
|---|---|
| FindCourses | 查用户的统一课程 |
| SearchEvidence | 按课程/来源/关键词检索已存证据 |
| GetEvidence | 读取指定不可变证据版本 |
| ReadSource | 对用户已授权连接进行有界 search/fetch，结果先入库再返回证据 |

工具绑定用户与 run；模型不能传入 UserID、任意 URL、Cookie、SQL、命令或工具权限。工厂注入的 Tools 实现校验每次调用，不能只在启动时检查。

初版执行预算建议：最多 6 轮、12 次工具调用、90 秒；这是默认配置建议，最终值由负责人配置。每次工具调用同时受剩余时间和平台限流约束。预算不足返回已获证据和 stop_reason=budget_exceeded，不编造补全。cancel/服务错误返回 error。记录安全的工具名、耗时、证据 ID 和终止原因，不记录敏感请求。

Result 中每个 EventDraft 必须满足：

1. Kind 为 assignment/exam/notice/schedule_change/preparation，标题非空，有至少一条当前用户的证据。
2. EvidenceRef.RecordID + Version 指向已保存版本；重要时间字段有单独 Evidence，不能只在回答末尾挂一组无关来源。
3. TimeClaim 的 precision=instant 只用 Value，day 只用 Date；instant_range 用 Value+End，date_range 用 Date+EndDate，终点包含边界且不得早于起点。日期为 YYYY-MM-DD，禁止与 instant 字段混用。不确定时间仅保留 SourceText、Evidence 和 precision=unknown，不填结构化时间；不确定课程放 CandidateCourseIDs/OpenQuestions，CourseID 可空。
4. 模型只能输出 pending/conflicted；confidence 不能替代确认。confirmed/rejected 仅由受控验证或用户操作生成。
5. SupersedesID 只作为更新建议；中心层校验归属、版本与证据后决定是否合并，Agent 无数据库写权限。
6. NotificationAdvice 只给对应 DraftIndex、重要性和理由；不等于允许立即推送。

Chat 与 Investigate 通过 durable Job 执行，客户端轮询 Run；首版暂不冻结 SSE/流式协议。对话历史按 conversation_id 和用户隔离。没有证据的普通解释可返回空 Evidence，但不得声称查到了未查询的学业事实。

## 4. Handle 应用接口

完整用例清单与输入输出见 `DATA-MODELS.md` 的 Handle 端口表。Handle 必须完成：身份校验 → 连接/资源授权 → 参数验证 → 幂等/事务/队列 → 调用 Adapter 或 Agent → 结构化返回。HTTP 层和平台模块不得各自复制这套流程。

- 应用身份：HTTP 中间件构造 Actor；本版没有指定学生注册/登录提供商，不用教学平台账号直接代替应用会话。
- 连接：创建 pending → 认证挑战 → active；过期由 session 检测/明确认证失败转 expired。disabled 不再同步。
- 同步：用户可以选择已声明资源；未选时使用该连接实际支持资源。返回 Job，不同步等待全量拉取。
- 学业：Dashboard、课程、事件、发现、任务和证据查询；按 immutable EvidenceRef 展示历史证据。
- 确认：confirm/reject + expected_version；未解决冲突不得直接 confirm，需先明确课程映射/证据或产生新事件版本。首版不允许任意 PATCH 模型字段。
- 任务：open/completed/dismissed，仅影响应用任务；pending/conflicted 事件只能显示为待确认任务，不能看起来已核实。
- 调查与对话：持久化 run/job；最终结果及派生写入提交后才 succeeded。
- 通知：用户偏好、设备注册与移除；通道未冻结，channel 必须受服务端白名单限制。
- 断开与删除：断开删除凭据并停止采集；历史数据删除是单独的显式 purge job，见架构文档。

## 5. HTTP v1 草案

统一前缀 `/api/v1`，以下除 health 外都需应用身份。应用登录路由待身份方案确定后另行补充，不能默认公开调试用户。

响应约定：同步资源成功 200，创建资源 201；入队 202，删除/无返回更新 204。错误格式 `{"error":{"code":"...","message":"安全说明","retryable":false},"request_id":"..."}`。成功直接返回对应 DTO；列表 `{items:[],next_cursor?:"..."}`；异步返回 Job，Agent 类异步返回 Run。

| 方法 | 路径 | Handle / 行为 |
|---|---|---|
| GET | /health | 存活检查，仅返回服务状态 |
| GET | /sources | ListSources |
| GET | /connections | ListConnections |
| POST | /connections | CreateConnection；source, display_name；201 |
| POST | /connections/{id}/auth | BeginAuth；AuthInput |
| POST | /connections/{id}/auth/{challenge_id} | ContinueAuth；AuthInput，可空字段轮询 |
| POST | /connections/{id}/session/check | CheckSession |
| DELETE | /connections/{id} | Disconnect；保留禁用连接及已存历史，204 |
| POST | /connections/{id}/purge | PurgeConnectionData；必须先断开，202 |
| POST | /connections/{id}/sync | RequestSync；body={resources:[...]}，202 |
| GET | /jobs/{id} | GetJob；同步/清理进度 |
| GET | /dashboard | GetDashboard；近期任务、事件、发现，各最多20项 |
| GET | /courses | ListCourses；cursor, limit |
| PUT | /course-mappings | SetCourseMapping；CourseMapping，204 |
| GET | /events | ListEvents；course_id, verification, kind, cursor, limit |
| GET | /events/{id} | GetEvent |
| POST | /events/{id}/confirmation | ConfirmEvent；expected_version, decision, reason |
| GET | /evidence/{record_id}/versions/{version} | GetEvidence |
| GET | /evidence/{record_id}/versions/{version}/attachments/{attachment_id} | OpenAttachment；授权流式响应 |
| GET | /tasks | ListTasks；cursor, limit |
| PATCH | /tasks/{id} | SetTaskStatus；body={status:"completed"} |
| GET | /findings | ListFindings；cursor, limit |
| POST | /agent/investigations | Investigate；query, evidence，202 |
| POST | /agent/chat | Chat；conversation_id?, message，202 |
| GET | /agent/runs/{id} | GetRun |
| GET | /notification-preferences | GetNotificationPreferences |
| PUT | /notification-preferences | SetNotificationPreferences；204 |
| PUT | /devices/{id} | RegisterDevice；channel, token，204；响应不回显 token |
| DELETE | /devices/{id} | DeleteDevice；204 |

POST /connections、sync、purge、investigations、chat 必须带 `Idempotency-Key`；按用户+操作+body hash 验证，同 key 同 payload 重放返回原资源/任务，同 key 不同 payload 返回409。认证挑战请求不用此队列幂等规则，按 challenge 自身过期和状态机管理；BeginAuth 同一连接只保留一个有效挑战。

HTTP 分页 cursor 是服务签名的不透明游标，绑定用户、过滤条件和排序；不能复用 upstream cursor。URL 中 id 对应对象不属于当前用户时统一404。

| 错误码 | HTTP | 处理约定 |
|---|---|---|
| invalid_argument | 400 | 参数错误，不重试 |
| unauthenticated | 401 | 应用会话缺失/失效 |
| not_found | 404 | 对象不存在或不属于当前用户 |
| conflict | 409 | 版本、状态、幂等键冲突 |
| auth_required / auth_expired / challenge_required | 409 | 教学平台需要重新授权，与应用401区分 |
| rate_limited | 429 | 有条件返回 Retry-After，对应 RetryAfterMS |
| unsupported | 422 | 平台/资源未支持，不能假装空结果 |
| upstream_unavailable | 502 | 临时平台失败，按 Retryable 判定 |
| parse_failed | 502 | 结构变化，默认不盲目重试 |
| budget_exceeded | 422 | 业务预算拒绝；已启动 Agent 预算耗尽优先返回有 stop_reason 的 Result |
| internal | 500 | 仅安全说明与 request_id，内部原因不外泄 |

## 6. 平台验收清单

- 只在自己的目录开发；共享类型变更另开负责人审核 PR。
- 编译断言与 `go test ./...` 通过，依赖只用到允许的共享端口。
- 使用脱敏 fixture 覆盖：正常读取、空数据、至少两页、同内容重试、更新、登录失效、限流、取消/超时、解析失败和 unsupported。
- 能区分上游缺失字段与真实零值；保留原生复合 ID、时间原文和明确来源。
- 同步失败不推进 cursor；不做跨源合并，不创建 confirmed 事件，不直接推通知。
- README 列明认证方式、支持资源/分区、分页/水位、平台限制、测试命令与真实验证状态。
- 仓库、错误信息、日志、fixture 均不包含真实账号密码、Cookie、Token、二维码票据或私人聊天内容。

## 7. 尚未冻结的事项

当前已实现本地密码登录 + Cookie Session，以及 users/DWS 的 PostgreSQL schema；生产身份对接、推送通道、真实平台能力、模型供应商/工具调用协议、学业 schema 与检索仍待后续验证。Go module 暂用本地路径 `academic/backend`，待 GitHub Organization 与正式仓库名确定后由核心负责人统一调整。不得在平台实现中自行决定以上共享事项。
