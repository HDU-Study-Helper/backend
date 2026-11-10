# 杭电教务只读适配器

按现有 SSO / 正方调研协议实现，**本 Go 实现仅经过本地合成 fixture 测试，未使用真实账号、未请求真实教务端点，未进行真实上游或真实浏览器验收；中心日程接线另有隔离PostgreSQL验收**。历史研究的登录/课表成功记录不等于当前实现已通过真实验证。

## 能力和接入

`New(contract.Dependencies) (contract.Adapter, error)`，同时实现可选 `contract.PasswordLogin` 与 `contract.AcademicTermReader`。由 app 注册，platform 负责应用用户权限、加密会话及事务入库；本包没有 SQL、HTTP 路由或后台循环，无新增外部依赖。

| 能力 | 实现 | 验证/限制 |
|---|---|---|
| 账号密码登录 | SSO GET 提取 `login-page-flowkey/login-croypto`；Base64 密钥 → AES-ECB/PKCS7 → Base64 密码；一次 POST 后跟随已知 HTTPS 教务跳转，检查学生菜单标题和有效 JSESSIONID | 离线 fixture；验证码/多因素认证未支持；不重试、不回退正方本地 RSA 登录 |
| 凭据保存 | 返回带 version、user+connection scope、登录账号及完整 Cookie 属性的 opaque Session；中心使用 AES-256-GCM 加密写入 platform_connections.session | 密码和一次性票据不保存，不进入业务记录、HTTP 响应或日志；服务器重启可恢复 Cookie，会话失效需要用户重新登录 |
| schedule | POST `/jwglxt/kbcx/xskbcx_cxXsKb.html?gnmkdm=N2151`，`xnm/xqm`；kbList 普通安排与 sjkList 实践原文 | 离线 fixture；仅保留上游明确的教学周/星期/节次，不猜学校作息或具体日期 |
| course（已选课程） | POST `/jwglxt/xsxk/zzxkyzb_cxZzxkYzbChoosedDisplay.html?gnmkdm=N253512`，`xkxnm/xkxqm`；严格接受 JSON 数组 | 端点与参数参考 research/hdu-sso/repos/zfn_api 的 get_selected_courses；杭电该端点未实测；不自动尝试其他查询/选退课接口 |
| 定时采集 | `Describe.ManualSyncOnly=true`；Repository Targets 排除 jwxt，Worker 也跳过手动平台 | 默认关闭；本版只开放手动获取，没有启用定时采集开关或自动重登录 |

QRLogin=false，BeginAuth/PollAuth 返回 unsupported。普通 Adapter.Sync 对 course/schedule 返回 invalid_argument，要求显式使用 SyncTerm；其他资源 unsupported。不实现成绩、考试、选课/退课或文件下载。

## 后台与 API

配置已有 `CREDENTIAL_KEY` 后，后台 `/admin` 的学习平台区域展示“杭电教务登录”：输入统一认证账号密码，提交后清空密码输入框。登录只建立并保存会话，不自动抓课表或课程。已有 active 连接展示学年起始年份、学期选择、首周周一日期和图形化时间表编辑器（节数、课时、休息及顺延），以及“获取课表”“获取已选课程”。默认值仅作为可编辑的日历建议，学年学期最终由用户选择；手动请求保存后可在数据预览中选“杭电教务（手动采集）”。预览自动刷新只读取数据库。

所有 API 挂在应用会话认证下，POST 沿用 Origin + CSRF：

```text
POST /api/v1/platforms/jwxt/login
{"username":"<统一认证账号>","password":"<密码>"}
→ 201 当前应用用户的 PlatformConnection，不含凭据

POST /api/v1/platforms/connections/{id}/sync
课表：{"resource":"schedule","academic_year":2026,"term":1,"calendar":{"week_one_monday":"<该学期首周周一YYYY-MM-DD>","periods":[{"period":1,"start":"HH:mm","end":"HH:mm"}]}}
→ 200 完整快照日程刷新统计；calendar必填，含全部所需节次
已选课程：{"resource":"course","academic_year":2026,"term":1}
→ 200 {"status":"page_committed"}

GET /api/v1/platforms/connections/{id}/records?resource=schedule&limit=20
GET /api/v1/preview/records?source=jwxt&connection_id=...&resource=course
DELETE /api/v1/platforms/connections/{id}
```

academic_year 是学年起始年份（2000–2100），term 只接受1/2，对应源端3/12。缺失/非法学期返回400；不接受和学习通 course_id/class_id 定向参数混用。每次登录为不同 active 会话创建不同连接，防止换号混入已有记录；失败的 pending 连接可能保留错误状态，可在后台断开。断开删除会话及游标，保留已归档业务数据。

## 数据、分页和身份

- `schedule`、`course` 统一存到 platform_records，ParentID 为 JSON `[学年起始年份,学期]`。checkpoint 按 `resource:year:term` 隔离，抓取状态仍按资源汇总最近一次手动尝试。
- 已选课程 ExternalID 为 `[course_id,class_id]`，两个 ID 均要求源端明确；course_id 优先 kch_id 再 kch，class_id 优先 jxb_id 再 do_jxb_id。标题来自 kcmc。
- 课表没有经过验证的稳定安排 ID，采用课程/教学班/标题/星期/节次/周次/教室/教师的文本哈希，明确 `identity_source=schedule_text_hash`。来源身份仍为文本哈希；中心手动课表同步会以最新完整快照刷新本连接本学期归档及日程，移除已消失的来源记录。实践项按 qtkcgs 原文生成 practice_text_hash，没有可核实身份则不伪造课程关联。
- Fields 白名单：academic_year、term、entry_type、course_id/class_id（有值时）、class_name、credits_raw、category、course_category、nature、teacher、teacher_raw、teacher_name、location、location_raw、campus、assessment_raw、total_hours_raw、weekly_hours_raw、schedule_raw。普通课表额外 weekday（1–7）、periods_raw、weeks_raw、identity_source；实践只含学年、学期、entry_type=practice、identity_source。不保存 xsxx、学号等整份个人资料、原始响应对象或操作票据。
- 上游课表对象/已选课程数组在一次受限请求中读取，最多4MiB、5000记录；Adapter本地每页100项；course仍逐页提交并保存加密checkpoint，课表则由platform一次手动请求收齐所有页后统一事务刷新，始终从最新完整快照开始。没有根据猜测的上游分页大小截断；未知 items 包装、缺少 kbList、null、坏记录、重复身份均整页失败。显式空数组是有效完整快照；课表中心流程据此取消该学期TIMETABLE日程并保留其修改历史，USER/AGENT不受影响。
- 游标绑定 scope/resource/year/term，base64 JSON 自身不是加密，必须只来自中心加密 checkpoint；上限1MiB，超限不返回部分可提交结果。

## 会话与传输边界

每次恢复私有 publicsuffix CookieJar，保留域/path/expiry/secure 等，不修改注入客户端，不共享全局会话。源 Cookie 的 MaxAge 一次转绝对过期时间，恢复不延长寿命。AuthResult.ExpiresAt 是本地授权记录时间，不代表源端承诺8小时有效；以实际请求返回为准，不后台探活。

仅请求 HTTPS 的 sso.hdu.edu.cn 与 newjw.hdu.edu.cn。保留 CAS 注册的 HTTP service 参数原值，只升级历史研究观察到的同域 `/sso/driot4login`（含 ;jsessionid）、`/jwglxt/ticketlogin`、`/jwglxt/xtgl/index_initMenu.html` 回调传输为 HTTPS；拒绝其他 HTTP、域外、用户信息和非默认端口。密码 POST 遇307/308不重放，跳转最多8次；业务读取不跟随登录跳转。

错误只返回安全分类：401/403/登录页面或读取重定向为 auth_required，429 为 rate_limited，未知结构为 parse_failed，网络/其他上游错误为 upstream_unavailable；取消/超时保留 context 错误。不附响应正文、URL或账号。SSO会话失效后仅显式重新登录，不暗中提交密码。

## 离线验收

```sh
gofmt -w internal/jwxt internal/platform/password*.go
env -u TEST_DATABASE_URL go test ./internal/jwxt ./internal/platform
env -u TEST_DATABASE_URL go test -race ./internal/jwxt ./internal/platform
go vet ./...
```

httptest 的 Transport 将所有官方 HTTPS URL 映射到本机临时服务器，不访问真实平台、不读取真实账号或研究会话。覆盖 AES 表单、单次登录、回调升级、拒绝跨域/POST重放、菜单/Cookie判断、加密存储接线、用户/连接隔离、学期/游标隔离、两类数据及本地分页、未知结构/过期/取消、手动HTTP，以及 Worker 跳过教务。未做真实学校接口或真实账号测试；中心日程路径已用独立测试库验证事务与回滚。课表具体配置、修改规则和验收见 [schedule/README](../schedule/README.md)。
