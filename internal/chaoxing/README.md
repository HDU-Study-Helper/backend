# 学习通只读适配器

所有实现仅位于 `internal/chaoxing/**`，使用 `internal/contract/platform.go` 当前可编译接口：`New(contract.Dependencies) (contract.Adapter, error)`、`Describe`、`BeginAuth`、`PollAuth`、`Sync`。根目录旧 CONTRACTS 中较大的目标接口不是本次实现签名。

## 能力与验证

| 能力 | 当前实现 | 验证状态和限制 |
|---|---|---|
| 二维码登录 | 官方登录页隐藏 uuid/enc → `/createqr` 下载 PNG/JPEG → POST `/getauthstatus/v2`；pending/scanned/authorized/expired | 匿名官方 GET 登录页/createqr 与 Go BeginAuth 实测通过；未扫码状态实测为 `status:false,type:"3",mes:string`，兼容 type 数字及十进制字符串。真实扫码/账号未验证；不支持二次认证 |
| course | `mooc1-api/mycourse/backclazzdata?view=json&mcode=&rss=1` 的 result/channelList/content/course/data | JSON 协议有现有 repo 实现，本次 Mock 通过；该路径未列为 research/05 的真实验证端点。最多500课程，按 course/class 去重 |
| assignment | 逐课 stucoursemiddle 刷新 workEnc/enc/openc，GET `/mooc-ans/mooc2/work/list`，pageNum | 端点在既有 research/05 真实实测，本实现仅 Mock；仅列表名称与源状态，不读题目或答案。必须有文本 `n/total` 总数，或明确“暂无作业/没有作业”；缺少证据报 ErrParse，不能将第一页冒充全量 |
| progress | 逐课中间页 enc/openc → `mooc2-ans/mycourse/studentcourse` | 已有调研实测支持，本实现 Mock 通过；仅“已完成任务点: done/total”汇总，非视频播放时长/精确课程完成百分比，不写进度 |
| notice | POST `notice.chaoxing.com/pc/notice/getNoticeList`，`notices.list/lastGetId/lastPage` | 调研实测、本实现 Mock 两页通过；title/content 全文保留为纯文本，仅白名单时间、已读、来源类型字段；无课程归属证据时保持账号级 |
| exam | GET `/exam-ans/exam/test/examcode/examlist` 账号考试安排表 | 调研实测9列 dataTr、本实现 Mock通过；只读时间原文、时长、状态和已公布分数，不点击开始考试、不读取试题 |
| discussion | 逐课程中间页 `li[dataname=tl] a[data-url]` → 官方讨论页 → POST `course/topic/{bbsid}/getTopicList`；正文 HTML 转纯文本 | 本次已授权实际课程通过 `ProbeCourseDiscussion` 读取 **11 条帖子**，probe 与 Sync 共享导航/列表 helper；仅该课的列表请求已验证，其他课程与真实续页未验证。必须提交 `tags=classId0000001,classIdCLASS,courseIdCOURSE`，不可省略；普通同步不读回复、文件或调用写接口 |
| material | GET `mooc2-ans/coursedata/stu-datalist`；解析 `ul.dataBody_td` 文件/文件夹，按目录分页并逐层展开 | 合成 fixture 与两份本地历史 HTML 响应结构校验通过；新实现未请求真实账号，真实续页及子目录遍历尚未验证。仅目录元数据，不请求 `downloadData`、预览页或文件内容 |
| discussion_reply_user（按需可选方法） | `DiscussionReplyUsers(ctx,scope,session,courseID,classID,topicID)` → 官方 GET `/pc/invitation/getReplyList?bbsid=&uuid=&order=1&lastValue=` | 仅顶层回复者观察，**不含回复正文、附件、图片**；不加入自动 Sync/Describe.Resources。接口 URL 依据 `research/projects/20-chaoxing-cli.md`；本模块仅 Mock 通过，匿名详情页重定向登录，尚未取得官方详情 JS 或验证真实回复响应，需父层授权实测 |

未支持：课程内失败的 exam-list、作业/考试题目及答案、提交、签到、刷任务点、附件下载/云盘、账号密码/短信/学校SSO、双因子登录、实时增量 changefeed。未知 Resource 返回 `contract.ErrUnsupported`。没有SQL、HTTP路由、后台 goroutine 或轮询循环；由中心层独立按能力调度、加密存储并写库。

## 课程资料目录

`material` 已加入 `Describe.Resources`，由现有 platform Worker 每轮读取一页并提交数据库；也可 POST `/api/v1/platforms/connections/{id}/sync`，body `{"resource":"material"}`，立即读取一页。课程定向同步参数当前仍仅支持 discussion，material 从账号课程列表逐课读取。

- 每次最多读取课程列表和一个目录页，不在一次调用中遍历全部文件夹。课程快照、待展开目录、页码和已见 ID 存在加密 checkpoint 中；扫描完成清空游标，下轮重新扫描。
- 依据历史官方 HTML 的 `totalPages/totalPage` 与 `pages/currentPageNo` 判断续页，不按条数猜分页。缺少分页、同页字段矛盾、目录总页数中途变化、重复条目/目录循环或坏行均返回 parse_failed，本页不提交。`当前页共 N 个` 仅校验本页条数。允许有明确目录容器及终页证据的空目录。
- `Kind=material`，ExternalID 为原生 dataId，ParentID 为 `[course_id,class_id]` 课程复合键；父文件夹放在 `fields.parent_data_id`（根目录为 `"0"`）。这样移动/改名仍按同一课程中的 dataId 更新当前行。
- Title 为文件/文件夹名，Content 为空；Fields 仅保留 `course_id/class_id/data_id/parent_data_id/path/is_folder/file_type` 及有值时的 `size_raw/creator/time_raw`。`path` 是包含当前条目名的路径数组。文件夹与文件都入库，不读取正文或二进制；不保存签名、CPI、对象地址、下载 URL 或任意原始属性。目录导航 enc 仅进入加密游标。
- 上限：每页500项、每目录1000页、每课程10000条观察、目录深度32层、游标1MiB；超限明确失败，不标记为全量完成。缺失对象不自动删除，归档表示最新已观察状态。

## 会话、二维码和错误

每次调用复制注入的 `http.Client`，替换为私有带 publicsuffix 校验的 cookiejar，超时最多30秒，不修改原client、Jar、Timeout、CheckRedirect。Transport继续复用以便连接池和测试注入。请求只访问HTTPS `*.chaoxing.com`，拒绝域外重定向；每页响应最多4MiB。

`Session` 是带版本和完整 user+connection Scope 的 JSON opaque bytes，含 cookie 的来源/域/path/expiry、安全属性和有限的二维码状态；cookie不会跨请求存入全局对象。MaxAge转为绝对expiry，恢复不会续命。建码时cookie与uuid/enc一起保存，轮询必须传上一次返回的 Session。授权成功兼容顶层与 `data` 嵌套的 `status/type`，status 仅接受 JSON bool，type 接受整数或规范十进制字符串；必须在同一官方 origin 的有效 cookiejar 内同时存在非空 `_uid/UID` 和认证 cookie（`cx_p_token/p_auth_token/vc3/_d` 之一），不凭 status 单独授权。成功直接持久化会话，不额外请求 `/base`，避免可选资料页面的网络/重定向故障丢失已成功种下的 Cookie。User-Agent、Accept-Language、二维码和轮询 Accept 对齐成功研究脚本。官方登录页内联 JS 使用3秒间隔、50次后停止页面轮询；本地轮询预算采用 research 成功脚本的300秒（研究成功含人工扫码约3.5分钟），不声称延长官方二维码有效期，服务端失效状态仍优先；手机取消(type=6)映射 expired，拒绝(type=7)返回 auth_required；未知状态/结构报 parse_failed。

**BeginAuth 返回 `QRImage` 与 `QRMediaType`，QRURL为空。** 中心必须通过当前登录用户专属的图片响应或data URI展示这份官方图片；不能把不含挑战会话cookie的 `/createqr` API URL发给浏览器作为等效二维码。AuthResult.Session/QRImage有JSON排除标记，中心需明确处理图片且不得暴露 Session。敏感会话与游标必须由父层加密；不应给模型、业务记录或日志。二维码成功后清除uuid/enc。

HTTP 429 → ErrRateLimited；401/403/登录重定向/登录HTML → ErrAuthRequired；签名权限页、“参数传递不正确”、未知结构、重复分页、总量变化 → ErrParse；上游非200/网络失败 → ErrUpstream。取消与截止时间返回context错误。错误不附URL、响应体、账号或Cookie。

## 身份、分页与完整性

- course ExternalID是JSON数组字符串 `[courseid,clazzid]`，可逆且不会分隔符碰撞；assignment的ExternalID为workId，ParentID为同一课程复合键；progress ExternalID=`task-points`、ParentID为课程复合键。
- notice优先使用uuid，其次idCode/id；账号级ParentID空，不伪造课程关联。exam ParentID=`account-exam-schedule`，优先原生examId/testid；无ID时使用标题+时间原文的SHA256观察键，Fields.identity_source明确标记schedule_text_hash。后者同名同时间安排可能碰撞，时间更改可能产生新观察，不能作为删除/跨版本身份保证。
- course上游JSON列表一次受限读取，适配器本地每页50项，后续cursor携带课程快照。assignment/progress首调用发现最多500课程；随后每调用只处理当前一门课的一页，最多课程列表+中间页+资源页三个请求。每个输出资源页最多500记录。没有隐式跨课程全量循环。
- assignment每页刷新签名，cursor携带当前课序号/pageNum/已见work IDs/总量；检测重复ID、重复页、空续页、变化总数，失败不返回可提交Page。总数语法沿用调研repo实现，真实平台当前页面是否仍提供该文本尚未实测。
- discussion 首次发现课程快照，每次仅当前课程一页，首调用最多4个请求（课程列表、中间页、讨论页、列表），后续最多3个；接口 pageSize=20，响应硬上限500条，page上限1000。cursor增加 DiscussionLastValue/DiscussionLastAuxValue，与原有 Scope/Resource/Index/Page 一同封装；lastValue→last_reply_time、lastAuxValue→lastAuxValue，poff.lastPage 必须为 bool 或数值0/1。空页且有more仍必须返回推进的游标；缺失分页、重复分页标记、status:false、坏JSON或任一坏记录均失败，不返回可提交Page。课程结束清空本课分页状态，Index推进，跨课程分次调用，全部结束后从头重扫。
- 可选 `DiscussionCourses(ctx,scope,session)` 发现账号当前课程，仅输出 `CoursePartition{CourseID,ClassID}`；`SyncDiscussionCourse(ctx,scope,session,courseID,classID,cursor)` 首次重新读取课程列表核验归属并取CPI，后续要求游标仅含同一门课（拒绝全账户/其他课程游标），复用同一Sync。中心必须按course/class保存独立checkpoint：某课无入口/解析失败不会推进该课，其他课可独立读取；旧账户级Sync遇失败课仍报错，不伪装空成功或跳课。普通Sync cursor与单课程cursor都属于可信加密checkpoint，不接受外部伪造。
- discussion 导航只接受中间页真实的官方HTTPS URL；保留原query全部键值与fragment，再补中间页enc/oldenc、DOM `#t`（input value或文本）、learnSilverStartTime/EndTime 等。讨论页仅解析必要的hidden bbsid或circle.bbsid，不下载脚本；bbsid缺少时使用中间页/原query的源值。列表POST携带同一私有jar和真实讨论页Referer。ExternalID=topic uuid，ParentID=课程复合键；Fields保留course_id/class_id、author/reply_count/praise_count/topic_type，以及源createrName、createTime/updateTime/lastReplyTime等有限白名单；未知时间不编造。字段不复制票据、URL或附件对象。
- 按需回复者读取每次先恢复当前 Scope 的授权会话并重新读取课程列表，核验 course/class、取得 CPI，然后复用 `openDiscussion` 保留同一私有 jar 和实际导航 Referer。只在目标课程 `topicList` 中观察到准确 topic uuid 后请求回复列表；归属扫描最多20页、400条话题，未找到或预算耗尽拒绝，不因 bbsid 相同推断班级权限。列表归属校验复用已有解析器，其临时读取的话题正文不进入回复者输出。
- 回复者方法一次汇总最多25页、500条观察；以 `poff.lastValue` 续页、`poff.lastPage` bool/0/1 终止，不按条数推断终点。缺少明确分页、重复游标（含跨多页循环）、重复回复 ID、解析/上游错误、取消或预算不足均整次失败，返回零 Page，不返回可归档部分成功；成功返回更新的私有 Session，HasMore=false、NextCursor空。响应协议目前是待真实验证的严格假设，若上游不同需按官方 JS/实际形状修正，不能静默降级。
- 回复者 `Kind=discussion_reply_user`、ExternalID=回复 `id`、ParentID=topic uuid、Title=姓名；Content 始终空，Fields 仅 `course_id/class_id/topic_id/user_id/user_name/anonymous/coverage` 白名单。身份读取 `puid`，显示名依次取 `createrName/replyName`；只有明确 `anonymous` bool/0/1（兼容字符串0/1）时输出该标志。匿名为真时丢弃 puid 与 createrName，仅取 replyName 别名或“匿名用户”，不反查身份。所有记录 `coverage=top_level_only`，忽略 subreply，不声称覆盖嵌套参与者；同一用户多次回复保留多条观察，调用方可按明确身份去重。不复制 content/images/files/phone/studentNo/secretURLs 或任意原始响应对象。
- notice严格以lastGetId→lastValue续页，以lastPage bool/0/1终止；缺标志/重复游标显式失败。exam安排表单页最多500记录；无数据需要可识别table或明确暂无考试。
- NextCursor是version+scope+resource+来源上下文的base64 JSON（不是加密），只供可信中心加密checkpoint，不能接收外部任意cursor。HasMore=true保证非空并推进。完成时NextCursor为空，下轮从头扫描，由中心幂等去重；不推断删除。记录Fields不携带cpi、签名或详情URL。

## 验收

默认只使用 `httptest` 合成脱敏fixture（包含匿名官方响应的真实字段结构），Transport把官方URL映射到本地测试服务器，没有真实网络或账号访问，也没有读取research/live。排查时以临时内存匿名探测确认：登录页 uuid/enc 有 id，name-only fallback 亦受回归覆盖；createqr 虽声明 image/jpeg，正文 PNG magic 正常；BeginAuth 成功，parse_failed 可在首次 PollAuth 复现，原因为 type 返回字符串而旧解码器只接受整数。二维码凭据及原响应未落盘或输出，临时网络探测测试已移除。HTML用核心负责人统一增加的 `golang.org/x/net/html`，仅任务点数值/分页数值采用小型明确语法regex。

```sh
gofmt -w internal/chaoxing/*.go
go test ./internal/chaoxing
go vet ./internal/chaoxing
go test -race ./internal/chaoxing
```

覆盖官方图片/建码cookie恢复、扫码/确认/取消/过期/双因子拒绝、两页课程与作业、两页通知、任务点与考试、敏感字段不出记录、cookie期限与域覆盖、多user+connection会话及cursor隔离、缺失分页证据、登录页、签名失败、解析失败、429、取消和unsupported。Mock通过不等于真实账号已验证；真实上游授权/分页/账号数据仍待用户自行授权后的中心集成验收。
