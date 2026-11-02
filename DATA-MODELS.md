# 共享对象与端口定义 v0.1

本文保留完整目标的对象规范，不代表全部已有实现。当前用户/DWS 存储对象和 Repository 的实际签名在 `internal/model/model.go`，CLI 工作区加密在 `internal/credential`；尚未生成通用平台/Agent/学业对象。当前运行接口见 README 与 internal/dws/README。字段名采用预期 JSON 名称；内部专用对象不直接作为 HTTP 响应。`?` 表示可选，列表无值时使用空数组。详细目标行为以 CONTRACTS.md 为准。

## 1. 身份、能力与认证

| 对象 | 字段与含义 |
|---|---|
| Actor（内部） | user_id：应用认证后的用户，由可信中间件生成 |
| Scope（内部） | user_id、connection_id：已验证归属的调用范围 |
| Descriptor | source：jwxt/dws/chaoxing/ucampus/mooc；display_name；auth_methods；resources；can_search；can_fetch；can_download |
| Connection | id；source；state：pending/active/expired/disabled；display_name：安全别名；last_synced_at? |
| AuthInput（敏感临时输入） | method：password/qr_code/sso/device_code；account?；password?；code?。不得进入日志、模型、普通数据库或任务队列 |
| AuthChallenge | id；state：pending/requires_input/authorized/expired/failed；action?：scan_qr/open_url/enter_code；display_url?；qr_code?；expires_at；poll_after_ms?。只向当前用户暴露白名单交互内容 |
| SessionStatus | state：连接状态；checked_at |

ResourceKind：course、schedule、message、assignment、exam、notice、material。能力只能声明实际支持项。

## 2. 源记录与同步

| 对象 | 字段与含义 |
|---|---|
| ExternalRef | kind；id：原生ID；parent_id?：群/班级等复合身份组成部分 |
| Attachment | id；name；media_type?；size_bytes?；source_version?；sha256?：已验证字节摘要 |
| SourceTime | instant?：RFC3339；date?：YYYY-MM-DD；timezone?；raw_text?。instant/date 互斥；无法精确解析时只保留原文 |
| SchedulePattern | teaching_weeks：教学周列表；weekday：周一=1；period_start；period_end；semester_start_date?；timezone? |
| SourceFields | course_name?；term?；teachers；location?；starts_at?/ends_at?/due_at?：SourceTime；status?：平台原文状态；task_type?：源任务类型（如 quiz/homework/exam）；phase?：源阶段（如提交/互评/成绩公布，保留原文）；recurrence?：SchedulePattern |
| Record | ref；course_ref?；title?；content?；author?；fields：SourceFields；occurred_at?；updated_at?；observed_at；source_version?；content_hash；deleted；attachments；payload_ref? |
| Partition | id：连接/资源内稳定ID，账户级流为空字符串；label：安全描述 |
| PartitionRequest（内部） | resource；cursor；limit |
| PartitionPage（内部） | items：Partition[]；next_cursor；has_more |
| SyncRequest（内部） | resource；partition_id；cursor；limit |
| SyncPage（内部） | records：Record[]；next_cursor；has_more |
| SearchRequest（内部） | resource；query；course_ref?；cursor；limit |
| AttachmentRequest（内部） | record：ExternalRef；attachment_id；expected_source_version?；expected_sha256?。版本或摘要至少一项可验证，否则拒绝读取 |
| File（内部流） | body：可关闭字节流；name；media_type；size_bytes?。不是可序列化 JSON，不暴露上游签名 URL |

Record 只表达来源观察，绝不能直接当成已确认学业事件。中心层注入连接/用户身份；平台不可覆盖。PayloadRef 是已去除凭据的受控源内容引用；游标由平台/加密 checkpoint 管理，不给模型或客户端。

## 3. 学业对象

| 对象 | 字段与含义 |
|---|---|
| EvidenceRef | record_id；version：不可变版本；excerpt?：引用片段，不作为权限或独立证据 |
| Evidence | ref：EvidenceRef；connection_id；source；record：对应版本的 Record |
| Course | id；name；term?；teachers；evidence |
| TimeClaim | value?/end?：instant；date?/end_date?：日期；timezone；precision；source_text?；evidence。具体互斥与范围规则见 CONTRACTS.md |
| EventDraft | kind；title；description；course_id?；candidate_course_ids；due?：TimeClaim；evidence；verification；open_questions；supersedes_id? |
| AcademicEvent | EventDraft 全部字段 + id；version：整数乐观锁；updated_at |
| UserTask | id；event_id；title；status：open/completed/dismissed |
| Finding | id；event_ids；summary；verification；evidence |
| Dashboard | tasks；events；findings，各最多20项 |

事件 kind：assignment、exam、notice、schedule_change、preparation。
验证状态 verification：pending、confirmed、rejected、conflicted；Agent 只输出 pending/conflicted。
时间 precision：instant、day、instant_range、date_range、unknown；日期不得用午夜 instant 代替。

## 4. 公共分页、任务与错误

| 对象 | 字段与含义 |
|---|---|
| PageRequest | cursor?；limit?：默认20、最大100 |
| Page<T> | items：T[]；next_cursor?：中心签名游标，非上游游标 |
| Job | id；kind：sync/investigation/chat/purge；status：queued/running/succeeded/failed/cancelled；error? |
| Error | code；message：安全错误；retryable；retry_after_ms? |
| Run | job；result?：成功提交后的 AgentResult；conversation_id? |

错误码与 HTTP 映射见 CONTRACTS.md。错误信息不得包含平台响应体、凭据、聊天原文或命令输出。

## 5. Agent 端口

### 输入输出

- AgentRequest（内部）：user_id、run_id、query、trigger_evidence、conversation_id、budget。
- Budget：max_rounds、max_tool_calls、deadline。由系统配置，不由模型提升。
- AgentResult：answer、evidence、drafts、notification_advice、open_questions、stop_reason。
- NotificationAdvice：draft_index、importance（low/normal/high）、reason。仅为建议。
- stop_reason：completed、needs_confirmation、budget_exceeded。

| 操作 | 输入 | 输出 |
|---|---|---|
| Investigate | AgentRequest | AgentResult |
| Chat | AgentRequest | AgentResult |

### 用户/run 绑定的工具端口

| 操作 | 输入 | 输出 |
|---|---|---|
| FindCourses | query、PageRequest | Page<Course> |
| SearchEvidence | query、course_id?、connection_id?、kind?、PageRequest | Page<Evidence> |
| GetEvidence | EvidenceRef | Evidence |
| ReadSource | connection_id、operation：search/fetch、resource、query?、ref?、limit | Page<Evidence>，必须先保存证据 |

模型不提供 user_id、凭据、任意 URL、SQL、shell 命令。工具网关负责授权与预算，模型请求不等于有权限执行。

## 6. Handle 用例端口

以下操作均隐含接收 context 与已认证 Actor，返回业务对象或结构化错误；外部传入的所有 ID 都重新验证所有权。HTTP 对应关系见 CONTRACTS.md。

| 用例 | 输入（不含 Actor） | 输出 |
|---|---|---|
| ListSources | 无 | Descriptor[] |
| ListConnections | 无 | Connection[] |
| CreateConnection | source、display_name、幂等键 | Connection |
| BeginAuth | connection_id、AuthInput | AuthChallenge |
| ContinueAuth | connection_id、challenge_id、AuthInput | AuthChallenge |
| CheckSession | connection_id | SessionStatus |
| Disconnect | connection_id | 无，幂等禁用并删除凭据 |
| PurgeConnectionData | connection_id、幂等键 | Job |
| RequestSync | connection_id、resources、幂等键 | Job |
| GetDashboard | 无 | Dashboard |
| ListCourses | PageRequest | Page<Course> |
| SetCourseMapping | connection_id、external_course：ExternalRef、course_id、expected_version | 无；expected_version=0 为仅创建 |
| ListEvents | course_id?、verification?、kind?、PageRequest | Page<AcademicEvent> |
| GetEvent | event_id | AcademicEvent |
| ConfirmEvent | event_id、expected_version、decision：confirm/reject、reason? | AcademicEvent |
| GetEvidence | EvidenceRef | Evidence |
| OpenAttachment | EvidenceRef、attachment_id | File，必须匹配历史版本 |
| ListTasks | PageRequest | Page<UserTask> |
| SetTaskStatus | task_id、status | UserTask |
| ListFindings | PageRequest | Page<Finding> |
| Investigate | query、evidence、幂等键 | Run |
| Chat | conversation_id?、message、幂等键 | Run |
| GetRun | run_id | Run |
| GetJob | job_id | Job |
| GetNotificationPreferences | 无 | NotificationPreferences |
| SetNotificationPreferences | NotificationPreferences | 无 |
| RegisterDevice | device_id、channel、token | 无，不回显 token |
| DeleteDevice | device_id | 无 |

NotificationPreferences：enabled、timezone、quiet_start?/quiet_end?（HH:mm，必须同时提供或同时缺省）。Device Token 作为投递凭据保护。

## 7. 凭据端口（仅内部）

- Factory.ForConnection：接收可信 Scope 与 Source，验证归属后返回绑定到该连接的 Store。
- Store.Get / Put / Delete：仅操作连接内的本地键；Put 可带 expires_at。
- Store.DeleteAll：删除当前连接全部凭据/认证挑战。
- Store 内容统一加密，使用端仅短暂持有明文；不将 Store 或 Factory 注入 Agent。
- CLI 原生会话采用中心管理的加密凭据工作区后端，覆盖授权租约、恢复、刷新状态持久化、释放和销毁，不强制把官方存储重写成键值 Store。该工作区端口细节待实现前确定；它与键值 Store 遵循同一连接所有权和静态加密要求。钉钉具体约束见 internal/dws/README.md。

上述通用 Factory/Store 端口仍为后续实现要求；当前 DWS 纵切面已通过 credential.Vault + dbbridge scoped workspace/lease 实现加密原生会话包，不重写官方 Token 存储格式。后续平台接入时统一通用凭据端口，不能将未来端口草案视为已有源码。
