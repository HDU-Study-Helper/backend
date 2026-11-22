# 学习通只读适配器

所有实现仅位于 `internal/chaoxing/**`，使用 `internal/contract/platform.go` 当前可编译接口：`New(contract.Dependencies) (contract.Adapter, error)`、`Describe`、`BeginAuth`、`PollAuth`、`Sync`，并实现可选 `contract.SessionRefresher.RefreshSession(ctx, scope, session)`。根目录旧 CONTRACTS 中较大的目标接口不是本次实现签名。

## 能力与验证

| 能力 | 当前实现 | 验证状态和限制 |
|---|---|---|
| 二维码登录 | 官方登录页隐藏 uuid/enc → `/createqr` 下载 PNG/JPEG → POST `/getauthstatus/v2`；pending/scanned/authorized/expired | 匿名官方 GET 登录页/createqr 与 Go BeginAuth 实测通过；未扫码状态实测为 `status:false,type:"3",mes:string`，兼容 type 数字及十进制字符串。真实扫码/账号未验证；不支持二次认证 |
| course | `mooc1-api/mycourse/backclazzdata?view=json&mcode=&rss=1` 的 result/channelList/content/course/data | JSON 协议有现有 repo 实现，本次 Mock 通过；该路径未列为 research/05 的真实验证端点。最多500课程，按 course/class 去重 |
| assignment | 逐课 stucoursemiddle 刷新 workEnc/enc/openc，GET `/mooc-ans/mooc2/work/list`，pageNum | 端点在既有 research/05 真实实测，本实现仅 Mock；列表名称与源状态，并从已验证同课程/班级/作业 ID 的官方 task/view 安全 GET 读取白名单头信息；不读题目或答案。必须有文本 `n/total` 总数，或明确“暂无作业/没有作业”；缺少证据报 ErrParse，不能将第一页冒充全量 |
| chapter / lesson | 同一 `studentcourse` 响应中的 `chapter_item/catalog_name/catalog_sbar` 目录 | 原生 `cur<knowledgeID>` 节点按有子节点/叶子分别输出 chapter/lesson，标题组缺 ID 时只进入 path；脱敏 fixture 验证，当前实现真实核验状态见下文 |
| progress | 逐课中间页 enc/openc → `mooc2-ans/mycourse/studentcourse` | 保留“已完成任务点: done/total”汇总并输出原生节点任务点；仅显式 `knowledgeJobCount/catalog_points_yi/icon_yiwanc` 为总数、待完成数和完成依据；缺标记为 unknown，不写进度；done>total 保留原值并标 source_inconsistent |
| notice | POST `notice.chaoxing.com/pc/notice/getNoticeList`，`notices.list/lastGetId/lastPage` | 调研实测、本实现 Mock 两页通过；title/content 全文保留为纯文本，仅白名单时间、已读、来源类型字段；无课程归属证据时保持账号级 |
| exam | GET `/exam-ans/exam/test/examcode/examlist` 账号考试安排表 | 调研实测9列 dataTr、本实现 Mock通过；只读时间原文、时长、状态和已公布分数，不点击开始考试、不读取试题 |
| discussion | 逐课程中间页 `li[dataname=tl] a[data-url]` → 官方讨论页 → POST `course/topic/{bbsid}/getTopicList`；正文 HTML 转纯文本 | 本次已授权实际课程通过 `ProbeCourseDiscussion` 读取 **11 条帖子**，probe 与 Sync 共享导航/列表 helper；仅该课的列表请求已验证，其他课程与真实续页未验证。必须提交 `tags=classId0000001,classIdCLASS,courseIdCOURSE`，不可省略；普通同步不读回复、文件或调用写接口 |
| material | GET `mooc2-ans/coursedata/stu-datalist`；解析 `ul.dataBody_td` 文件/文件夹，按目录分页并逐层展开 | 合成 fixture 与两份本地历史 HTML 响应结构校验通过；新实现未请求真实账号，真实续页及子目录遍历尚未验证。仅目录元数据，不请求 `downloadData`、预览页或文件内容 |
| discussion_reply_user（按需可选方法） | `DiscussionReplyUsers(ctx,scope,session,courseID,classID,topicID)` → 官方 GET `/pc/invitation/getReplyList?bbsid=&uuid=&order=1&lastValue=` | 仅顶层回复者观察，**不含回复正文、附件、图片**；不加入自动 Sync/Describe.Resources。已授权数学课程指定话题真实读取68条顶层回复，匹配当前用户1条；不代表覆盖嵌套回复或正式提交状态 |

未支持：课程内失败的 exam-list、作业/考试题目及答案、提交、签到、刷任务点、附件下载/云盘、账号密码/短信/学校SSO、双因子登录、实时增量 changefeed。未知 Resource 返回 `contract.ErrUnsupported`。没有SQL、HTTP路由、后台 goroutine 或轮询循环；由中心层独立按能力调度、加密存储并写库。

## 课程资料目录

`material` 已加入 `Describe.Resources`，由现有 platform Worker 每轮读取一页并提交数据库；也可 POST `/api/v1/platforms/connections/{id}/sync`，body `{"resource":"material"}`，立即读取一页。课程定向同步参数当前仍仅支持 discussion，material 从账号课程列表逐课读取。

- 每次最多读取课程列表和一个目录页，不在一次调用中遍历全部文件夹。课程快照、待展开目录、页码和已见 ID 存在加密 checkpoint 中；扫描完成清空游标，下轮重新扫描。
- 依据历史官方 HTML 的 `totalPages/totalPage` 与 `pages/currentPageNo` 判断续页，不按条数猜分页。缺少分页、同页字段矛盾、目录总页数中途变化、重复条目/目录循环或坏行均返回 parse_failed，本页不提交。`当前页共 N 个` 仅校验本页条数。允许有明确目录容器及终页证据的空目录。
- `Kind=material`，ExternalID 为原生 dataId，ParentID 为 `[course_id,class_id]` 课程复合键；父文件夹放在 `fields.parent_data_id`（根目录为 `"0"`）。这样移动/改名仍按同一课程中的 dataId 更新当前行。
- Title 为文件/文件夹名，Content 为空；Fields 仅保留 `course_id/class_id/data_id/parent_data_id/path/is_folder/file_type` 及有值时的 `size_raw/creator/time_raw`。`path` 是包含当前条目名的路径数组。文件夹与文件都入库，不读取正文或二进制；不保存签名、CPI、对象地址、下载 URL 或任意原始属性。目录导航 enc 仅进入加密游标。
- 上限：每页500项、每目录1000页、每课程10000条观察、目录深度32层、游标1MiB；超限明确失败，不标记为全量完成。缺失对象不自动删除，归档表示最新已观察状态。

## 课程采集排除

可信宿主通过 `contract.WithExcludedCourses` 注入当前 user+connection Scope 的原生课程引用（JSON `[course_id,class_id]`）。普通 Sync 在输出 course 及请求 assignment/progress/material/discussion 的任何课程入口前检查精确引用，包括旧 checkpoint 已保存但当前账号列表不存在的课程；不按名称匹配，不受其他 Scope 的排除项影响。账号课程发现请求仍允许执行。游标 Courses 快照保持原序，使用 Index 在内存中跳过排除课程（最多500项），同时清空该课作业 ID/总数、讨论标记、资料目录队列/已见 ID/页数及其他分页状态，不为被排除课程发送请求。完成扫描后游标清空；移除排除项后，下轮完整扫描可再次读取该课程。

可选 `DiscussionCourses` 不返回被排除课程；`SyncDiscussionCourse` 在验证参数、Scope、授权会话及已有单课游标后返回空终页，`DiscussionReplyUsers` 在验证参数及会话后返回空终页，均不打开该课程讨论入口。notice/exam 当前仅有账号级来源证据，继续读取并保留；不能从标题推断课程后排除。本策略由脱敏注入 Transport 测试验证，未请求真实账号。

## 会话、二维码和错误

每次调用复制注入的 `http.Client`，替换为私有带 publicsuffix 校验的 cookiejar，超时最多30秒，不修改原client、Jar、Timeout、CheckRedirect。Transport继续复用以便连接池和测试注入。请求只访问HTTPS `*.chaoxing.com`，拒绝域外重定向；每页响应最多4MiB。

`Session` 是带版本和完整 user+connection Scope 的 JSON opaque bytes，含 cookie 的来源/域/path/expiry、安全属性和有限的二维码状态；cookie不会跨请求存入全局对象。MaxAge转为绝对expiry，恢复不会续命。建码时cookie与uuid/enc一起保存，轮询必须传上一次返回的 Session。授权成功兼容顶层与 `data` 嵌套的 `status/type`，status 仅接受 JSON bool，type 接受整数或规范十进制字符串；必须在同一官方 origin 的有效 cookiejar 内同时存在非空 `_uid/UID` 和认证 cookie（`cx_p_token/p_auth_token/vc3/_d` 之一），不凭 status 单独授权。成功直接持久化会话，不额外请求 `/base`，避免可选资料页面的网络/重定向故障丢失已成功种下的 Cookie。User-Agent、Accept-Language、二维码和轮询 Accept 对齐成功研究脚本。官方登录页内联 JS 使用3秒间隔、50次后停止页面轮询；本地轮询预算采用 research 成功脚本的300秒（研究成功含人工扫码约3.5分钟），不声称延长官方二维码有效期，服务端失效状态仍优先；手机取消(type=6)映射 expired，拒绝(type=7)返回 auth_required；未知状态/结构报 parse_failed。

**BeginAuth 返回 `QRImage` 与 `QRMediaType`，QRURL为空。** 中心必须通过当前登录用户专属的图片响应或data URI展示这份官方图片；不能把不含挑战会话cookie的 `/createqr` API URL发给浏览器作为等效二维码。AuthResult.Session/QRImage有JSON排除标记，中心需明确处理图片且不得暴露 Session。敏感会话与游标必须由父层加密；不应给模型、业务记录或日志。二维码成功后清除uuid/enc。

HTTP 429 → ErrRateLimited；401/403/登录重定向/登录HTML → ErrAuthRequired；签名权限页、“参数传递不正确”、未知结构、重复分页、总量变化 → ErrParse；上游非200/网络失败 → ErrUpstream。取消与截止时间返回context错误。错误不附URL、响应体、账号或Cookie。

## 已有会话 Cookie 更新

`RefreshSession` 只接受当前完整 Scope 且 `Authorized=true` 的已有会话。每次显式调用均执行 GET `https://i.chaoxing.com` 和已用于扫码入口的固定 HTTPS Passport `/login?fid=-1&refer=https%3A%2F%2Fi.chaoxing.com`，由官方响应在私有 cookiejar 中设置、覆盖或删除 Cookie，然后以原课程接口的 `parseCourses` 强校验实际授权访问。课程列表允许明确 `result=1/channelList=[]` 的空账号；200 首页、登录表单、任意原始 JSON 或仅有 Cookie 均不足以判定成功。课程核验禁止继续跟随重定向；登录页及明确认证失败返回 `ErrAuthRequired`，未知数据结构仍返回 `ErrParse`，网络、取消、限流、验证码错误保留原语义。

刷新专用客户端仅允许已知 GET 页面之间最多3次重定向：精确 `i.chaoxing.com` 的 `/`、`/base`（可带单一数字 `t`），以及精确 `passport2.chaoxing.com/login`（仅单一 `fid=-1`、指向上述个人主页的 `refer`）。拒绝任意其他超星子域、路径、HTTP、userinfo、非443端口、额外查询键及嵌套任意跳转目标；不执行页面 JS、表单、扫码接口或账号密码登录。刷新前后 `_uid/UID` 如存在必须一致，跨 origin 冲突或账号变化返回 `ErrAuthRequired`，不将其他账号保存到原连接。仍过滤持久化 `k8s`；Set-Cookie 的 Max-Age 优先于 Expires，在接收时转为绝对期限，恢复时不会续命，删除 Cookie 不复活。

机制是已有根登录允许时的官方页面访问和服务端 Cookie 交换，没有发现可证明永久续期的学生端 refresh-token 协议。2026-11-11 匿名只读检查确认官方登录入口返回登录表单，主页重定向至403说明页；匿名静态登录脚本也重定向至403页，因此不据此添加未知 `uservalidate` 等路径。同日宿主通过已有授权连接实际调用平台服务刷新，Chaoxing 耗时2323ms，课程验证通过，结果经宿主加密保存，连接保持 active，checkpoint 不变，无源业务写入及 Cookie 暴露（脱敏回执位于工作区 `.build/cookie-refresh-live.json`）。此检查证明当前已授权会话可完成该流程，尚未证明已失效的课程会话可从残留根 Cookie 恢复，也未证明服务端延长了登录期限。过期根登录、依赖未实现 JS/额外握手的情况仍可能需要用户重新授权。参考官方入口：[个人主页](https://i.chaoxing.com)、[登录页](https://passport2.chaoxing.com/login?fid=-1&refer=https%3A%2F%2Fi.chaoxing.com)，已有仓库研究的主页 `/base` 与课程协议。成功输出只供宿主加密保存，适配器不修改资源游标或数据库。

## 身份、分页与完整性

- course ExternalID是JSON数组字符串 `[courseid,clazzid]`，可逆且不会分隔符碰撞；assignment的ExternalID为workId，ParentID为同一课程复合键；progress ExternalID=`task-points`、ParentID为课程复合键。
- notice优先使用uuid，其次idCode/id；账号级ParentID空，不伪造课程关联。exam ParentID=`account-exam-schedule`，优先原生examId/testid；无ID时使用标题+时间原文的SHA256观察键，Fields.identity_source明确标记schedule_text_hash。后者同名同时间安排可能碰撞，时间更改可能产生新观察，不能作为删除/跨版本身份保证。
- course上游JSON列表一次受限读取，适配器本地每页50项，后续cursor携带课程快照。assignment/progress首调用发现最多500课程；每调用只处理当前一门课的一页，作业最多课程列表+中间页+列表页+5个头（每头最多2次同源安全重定向），教学资源最多课程列表+中间页+目录页三个请求，后续本地分页无需上游I/O。每个输出资源页最多500记录。没有隐式跨课程全量循环。
- assignment每页刷新签名，cursor携带当前课序号/pageNum/已见work IDs/总量；检测重复ID、重复页、空续页、变化总数，失败不返回可提交Page。总数语法沿用调研repo实现，真实平台当前页面是否仍提供该文本尚未实测。
- discussion 首次发现课程快照，每次仅当前课程一页，首调用最多4个请求（课程列表、中间页、讨论页、列表），后续最多3个；接口 pageSize=20，响应硬上限500条，page上限1000。cursor增加 DiscussionLastValue/DiscussionLastAuxValue，与原有 Scope/Resource/Index/Page 一同封装；lastValue→last_reply_time、lastAuxValue→lastAuxValue，poff.lastPage 必须为 bool 或数值0/1。空页且有more仍必须返回推进的游标；缺失分页、重复分页标记、status:false、坏JSON或任一坏记录均失败，不返回可提交Page。课程结束清空本课分页状态，Index推进，跨课程分次调用，全部结束后从头重扫。
- 可选 `DiscussionCourses(ctx,scope,session)` 发现账号当前课程，仅输出 `CoursePartition{CourseID,ClassID}`；`SyncDiscussionCourse(ctx,scope,session,courseID,classID,cursor)` 首次重新读取课程列表核验归属并取CPI，后续要求游标仅含同一门课（拒绝全账户/其他课程游标），复用同一Sync。中心必须按course/class保存独立checkpoint：某课无入口/解析失败不会推进该课，其他课可独立读取；旧账户级Sync遇失败课仍报错，不伪装空成功或跳课。普通Sync cursor与单课程cursor都属于可信加密checkpoint，不接受外部伪造。
- discussion 导航只接受中间页真实的官方HTTPS URL；保留原query全部键值与fragment，再补中间页enc/oldenc、DOM `#t`（input value或文本）、learnSilverStartTime/EndTime 等。讨论页仅解析必要的hidden bbsid或circle.bbsid，不下载脚本；bbsid缺少时使用中间页/原query的源值。列表POST携带同一私有jar和真实讨论页Referer。ExternalID=topic uuid，ParentID=课程复合键；Fields保留course_id/class_id、author/reply_count/praise_count/topic_type，以及源createrName、createTime/updateTime/lastReplyTime等有限白名单；未知时间不编造。字段不复制票据、URL或附件对象。
- 按需回复者读取每次先恢复当前 Scope 的授权会话并重新读取课程列表，核验 course/class、取得 CPI，然后复用 `openDiscussion` 保留同一私有 jar 和实际导航 Referer。只在目标课程 `topicList` 中观察到准确 topic uuid 后请求回复列表；归属扫描最多20页、400条话题，未找到或预算耗尽拒绝，不因 bbsid 相同推断班级权限。列表归属校验复用已有解析器，其临时读取的话题正文不进入回复者输出。
- 回复者方法一次汇总最多25页、500条观察；以 `poff.lastValue` 续页、`poff.lastPage` bool/0/1 终止，不按条数推断终点。缺少明确分页、重复游标（含跨多页循环）、重复回复 ID、解析/上游错误、取消或预算不足均整次失败，返回零 Page，不返回可归档部分成功；成功返回更新的私有 Session，HasMore=false、NextCursor空。分页协议已由本次指定数学话题真实读取68条验证；其他课程或未来形状仍需明确核验，未知结构不能静默降级。
- 回复者 `Kind=discussion_reply_user`、ExternalID=回复 `id`、ParentID=topic uuid、Title=姓名；Content 始终空，Fields 仅 `course_id/class_id/topic_id/user_id/user_name/anonymous/coverage` 白名单。身份读取 `puid`，显示名依次取 `createrName/replyName`；只有明确 `anonymous` bool/0/1（兼容字符串0/1）时输出该标志。匿名为真时丢弃 puid 与 createrName，仅取 replyName 别名或“匿名用户”，不反查身份。所有记录 `coverage=top_level_only`，忽略 subreply，不声称覆盖嵌套参与者；同一用户多次回复保留多条观察，调用方可按明确身份去重。不复制 content/images/files/phone/studentNo/secretURLs 或任意原始响应对象。
- notice严格以lastGetId→lastValue续页，以lastPage bool/0/1终止；缺标志/重复游标显式失败。exam安排表单页最多500记录；无数据需要可识别table或明确暂无考试。
- NextCursor是version+scope+resource+来源上下文的base64 JSON（不是加密），只供可信中心加密checkpoint，不能接收外部任意cursor。HasMore=true保证非空并推进。完成时NextCursor为空，下轮从头扫描，由中心幂等去重；不推断删除。记录Fields不携带cpi、签名或详情URL。

## 2026-11-08 进度源值兼容

本次现有授权连接的只读诊断确认，一门课程官方页明确展示“已完成任务点21/16”。旧解析器将done>total当成parse_failed，导致该课程之后的进度分页停止。现在完整保留completed=21、total=16、metric=task_points，并添加source_inconsistent=true及inconsistency_reason=completed_exceeds_total；不截断、不换算百分比、不捏造0。当前失败checkpoint单页真实验证已返回1条记录且has_more=true，测试覆盖后续课程继续读取。其余缺失、非数字或未识别结构仍报解析错误。

## 元数据保真（当前授权连接只读核验）

在现有加密会话内，通过 dbbridge + credential 进程解密后只读访问既有课程/作业列表/讨论列表/通知列表/资料目录；不提交记录、Session 或 checkpoint，不访问作业详情、答案、考试入口或文件下载。`cmd/chaoxing-inspect` 仅输出固定白名单日期字段、字段计数和受限 HTML 文本/属性名，拒绝清单之外的上游路径，URL 查询参数、Cookie、签名和附件对象不进入输出。

- 实际 23 门课程的 25 条作业列表中，已检查行仅提供名称、状态与部分 `div.time.notOver` 的“剩余N小时N分钟”，没有绝对截止日期或正文说明；日期属性检查也未发现隐藏的绝对时间。现在保留 `deadline_raw/time_raw` 及 `*_source=assignment_list:time`、`*_precision=relative`、`*_reason=relative_countdown_only`，不将采集时间加上倒计时冒充精确截止时间。若列表明确显示“发布时间/截止时间/作业说明”等标签，则保留对应原文与字段依据；此分支为脱敏 fixture 覆盖，当前账号未观察到。任务详情未请求时为 `body_status=not_collected`、`body_reason=task_detail_not_requested`，列表未给出的发布时间/截止时间为 `*_precision=unknown`、`*_reason=not_provided_by_source`。不从作业标题里的日期/章节推断元数据。
- 实际读取 44 条讨论，全部使用数值 epoch 毫秒 `create_time/update_time`，旧白名单会丢弃这两个字段。现在保留原键并提供 `published_at_raw/source/precision`、`updated_at_raw/source/precision`；兼容 `createTime/updateTime/gmtCreate/gmtModified` 等有限标量日期字段。回复时间独立保留为 `last_reply_at_*`，不能代替帖子发布时间。真实 `createrName` 保留为 `author` 及 `author_source`。
- 通知只读抽样 60 条，均有 `sendTime/insertTime/createrName`，一条额外提供 `updateTime`。`published_at_raw` 明确取 `sendTime`，`published_at_semantics=notice_send_time`；`insertTime` 仅为 `inserted_at_*`，不当作更新时间。明确 `updateTime` 才进入 `updated_at_*`。通知仍为账号级，无证据不填课程。
- 资料目录额外保留 `author`（源 `creator`）、`coverage=material_directory`、`body_status=not_collected` 与 `body_reason=file_content_not_requested`。实际小样本时间单元仅显示 `11-02 17:13`，没有 title/datetime/data-time 的完整日期；`time_raw` 不改写，`time_precision=unknown/time_reason=missing_year`，缺失单元为 `not_provided_by_source`，不猜年份。资料时间的业务含义仍为 `time_semantics=unspecified_list_time`，不自动映射为发布时间。已有目录 `path/parent_data_id` 保持原义，不将文件夹名推断为章节。
- 所有明确课程归属的资源添加 `course_name`；课程 ExternalID、子资源 ParentID 始终保持原 JSON `[course_id,class_id]` 身份。每种资源添加具体 `coverage`。考试只添加 `account_exam_schedule` 范围及 `body_reason=exam_not_opened`，保留时间区间原文并交中央层处理，不点击考试、不强制课程关联。
- 时间转换由中央层负责；本模块仅输出原文、来源、源精度与未知原因，保留所有旧字段兼容。倒计时会随观察变化，不能仅凭其变化认定上游修改了截止日期。`body_status` 描述已取得正文的范围，空字符串不代表已取得空白详情。

## 作业头、教学目录和个人节点进度

- 作业列表返回的 `li[data]` 仅当 origin 为 `https://mooc1.chaoxing.com`、路径精确为 `/mooc-ans/mooc2/work/task` 且单一 `courseId/classId/workId` 与当前记录一致时读取；重定向最多2次，仅允许同 origin、同身份的 `/mooc-ans/mooc2/work/view`。拒绝任何 dowork、开始作答、表单提交、JS、域外或错身份路由；不会从标题猜 ID 或构造答题入口。被拒绝重定向保留列表观察，标记 `header_status=unavailable/header_reason=unsafe_redirect_blocked`；网络、认证、取消仍使本页失败。
- 每次最多处理5条作业头；未完成项及来源 URL 只进入宿主加密游标，所有记录字段均无 URL/CPI/签名。已读头信息合并到原作业记录，不生成新身份。仅解析首个任务内容边界之前的可见 `detailsHead/infoHead` 等已知头容器，保留 `answer_time_raw`、`question_count/full_score` 和 `start_at/end_at/deadline_*` 字段依据，不把题目、答案、脚本、隐藏值写入记录。未开放作业的可见头部只给开始时间时，仅提取开始时间。缺少可识别头信息时标记 unavailable。
- 授权预检已确认目标“2026 第二章第三次作业20261102”的实际官方 view 页展示“题量10、满分100、作答时间11-04 09:30至11-08 23:59”。源日期缺年，输出 `*_precision=unknown/*_reason=missing_year`；标题中的2026不参与补年。
- `chapter/lesson` 的 ParentID 始终为课程 JSON `[course_id,class_id]`，ExternalID 为原生知识节点 ID。Fields 包含 `node_id,parent_node_id,path,path_ids,order,number`（有源值时）和 `coverage=teaching_chapter_tree`；目录标题无 ID 时不伪造章节记录，其名称仍在 path，path_ids 只包含有原生 ID 的祖先及当前节点。嵌套原生节点作为 chapter，叶子作为 lesson。
- `progress` 保留 ExternalID=`task-points` 汇总，增加 ExternalID=`node:<nativeID>` 的个人节点观察，coverage=`personal_node_task_points`。`knowledgeJobCount` 是唯一允许读取的隐藏数值元数据，映射 `task_point_total`；可见 `catalog_points_yi` 映射 `task_point_remaining`，只有零才明确 completed，正数为 incomplete；显式 `icon_yiwanc` 也支持 completed，缺标记保持 unknown。`task_point_completed=total-remaining` 仅在两个明确计数一致时提供并注明计算来源，不推断视频时长或精确百分比。
- 教学页最多4MiB/100000 DOM节点/128层、10000个原生节点；输出每次100条，多余脱敏记录存在加密游标，续页不再发请求。总游标最多1MiB，超限返回 parse_failed；每次处理待续作业或节点前重新检查课程排除。章节记录不是任务卡片内容，不打开视频、卡片、答题页。
- 按需 `DiscussionReplyUsers` 新增回复 `create_time/createTime/update_time/updateTime` 的白名单时间依据，以及 `participation_status=reply_observed`、`participation_semantics=top_level_reply_not_submission_receipt`。仅同私有 jar 下无冲突的 `_uid/UID` 可与公开 puid 比较，输出 `is_current_user/current_user_match=verified_cookie_uid`；匿名或身份未知保持 unknown。回复观察不能作为正式提交回执；列表未出现当前用户不能推断“未提交”。匿名仍丢弃真实姓名及 puid。

## 2026-11-09 授权只读核验

本次通过既有 dbbridge/credential 端口恢复授权会话，诊断过程中不保存记录、Session 或 checkpoint：

- 数学课程目标作业由列表原 URL 安全 GET 至官方 view 后，适配器实际输出 `question_count=10/full_score=100/answer_time_raw=11-04 09:30 至 11-08 23:59/header_status=provided`，三个起止/截止时间字段均为 missing_year，没有从标题补年。实际页面外层 `#fanyaMarking.fanyaMarking.TiMu` 包住头与题目，兼容仅豁免此精确外层，并跳过头内部 `h2.mark_title` 标题；其余题目/答案边界仍停止。
- 同一数学课程 progress 返回188条观察（1条汇总+187个原生节点），分页100+88；145个节点同时有明确 total/remaining，170个有 remaining，187个都有显式个人状态依据。该页顶层目录标题没有 ID，187个原生节点均为叶子，所以 chapter 为0条、lesson 有187个原生节点；顶层目录仍保留在每条 path 中。没有伪造章节 ID 或把资料文件夹当章节。
- 指定“上传习题2.4”话题真实返回68条顶层回复；68条都有 `create_time`，1条额外提供 `updateTime`。使用同私有 jar 的 UID 对 puid 明确匹配到当前用户1条。仅输出回复观察与时间/覆盖字段，缺失更新时间保持未知，嵌套回复和正式提交回执未覆盖。
- 诊断命令支持 `INSPECT_COURSE_ID/INSPECT_CLASS_ID` 仅检查指定已授权课程，以及 `INSPECT_RESOURCE=discussion_reply_user` + `INSPECT_TOPIC_ID` 定向回复核验；默认只输出计数、字段覆盖和明确指定标题的作业元数据。`INSPECT_HEADER_STRUCTURE=1` 才输出有界头结构，全部省略 URL 查询值和原始任务内容。

## 验收

默认只使用 `httptest` 合成脱敏fixture（包含匿名官方响应的真实字段结构），Transport把官方URL映射到本地测试服务器，没有真实网络或账号访问，也没有读取research/live。排查时以临时内存匿名探测确认：登录页 uuid/enc 有 id，name-only fallback 亦受回归覆盖；createqr 虽声明 image/jpeg，正文 PNG magic 正常；BeginAuth 成功，parse_failed 可在首次 PollAuth 复现，原因为 type 返回字符串而旧解码器只接受整数。二维码凭据及原响应未落盘或输出，临时网络探测测试已移除。HTML用核心负责人统一增加的 `golang.org/x/net/html`，仅任务点数值/分页数值采用小型明确语法regex。

```sh
gofmt -w internal/chaoxing/*.go
go test ./internal/chaoxing
go vet ./internal/chaoxing
go test -race ./internal/chaoxing
```

覆盖官方图片/建码cookie恢复、扫码/确认/取消/过期/双因子拒绝、两页课程与作业、两页通知、任务点与考试、敏感字段不出记录、cookie期限与域覆盖、多user+connection会话及cursor隔离、缺失分页证据、登录页、签名失败、解析失败、429、取消和unsupported。Mock通过不等于真实账号已验证；真实上游授权/分页/账号数据仍待用户自行授权后的中心集成验收。

## 2026-11-04 反爬诊断与修复

真实浏览器从官方个人主页进入课程列表、再打开官方课程链接，可正常读取课程页。对同一课程进行低频单因素对照：原持久化 Cookie → 202 验证页；仅改 Referer → 仍202；保留原请求头、仅去掉持久化 `k8s` → 200 且有 workEnc；恢复旧 Cookie → 再次202。当前证据确认旧 `k8s` 回放是本次故障的触发因素，不足以确定平台通用频率阈值或证明其他账号永远不需要验证。旧入口本身仍可正常跳转，无需切换域名。

参考在线项目 [xxt-assistant issue #1](https://github.com/led629263224-cmyk/xxt-assistant/issues/1) 的同类诊断，并核对 [chaoxing-cli](https://github.com/menghuanshiguang/chaoxing-cli) 与 [chaoxing-homework-checker](https://github.com/Zhanghuaimin-233/chaoxing-homework-checker) 的请求流程。仅实现本账号实测确认的 `k8s` 修复，不依据外部 issue 批量清除其他路由/登录 Cookie，也不伪造票据。

- restore 忽略历史会话里的 `k8s`，save 不再持久化它；服务端当前流程新发的 Cookie 仍正常由私有 jar 接收、在本次调用内使用。登录凭据、用户/连接隔离、绝对过期时间和分页检查点不变。
- 同一 Adapter 的请求（包括重定向）起始时间至少相隔700毫秒，使用可取消等待，无后台 goroutine。节流器只共享时间状态，不共享账号 Cookie；这是保守限速，不声称为官方安全阈值。
- 明确识别最终 URL 的 antispiderShowVerify，以及响应开头的专用验证页标志，返回 `verification_required`；普通课程正文提及验证码不触发。平台服务保留该安全错误码（HTTP429），定时轮询或讨论逐课扫描遇验证/限流就停止该连接本轮后续请求，其他连接继续。不自动重试验证请求，不把验证页当作空数据，也不将仍有效登录态标为过期。
- 脱敏回归覆盖旧票忽略、实时 Cookie 保留、持久化过滤、HTTP202/200验证识别、请求间隔与取消、验证后停止逐资源/逐课程扫描、数据库状态保留。

真实修复后一次批量诊断：作业与进度同步成功，23个课程讨论请求均不再遇到验证页；讨论总体仍出现独立 parse_failed，不能将反爬消失等同于全部数据解析成功。完整无正文/凭据的 HTTP 状态计数与对照见工作区 `.build/chaoxing-cookie-ab.json`、`.build/chaoxing-fixed-probe.jsonl`。

## 2026-11-04 通知与讨论分页兼容

反爬修复后剩余两个 parse_failed 均已定位到实际 JSON 分页协议与本地强校验的差异：

- 通知带有效续页游标请求到末尾，返回精确的 `{"status":true}`，不再含 notices；重新从首页读取则正常返回20、20、17条（均 lastPage=0）。现在仅在**非空续页游标 + 仅有 status:true 的信封**下将其识别为空终页，成功提交后清空本地 checkpoint，下轮重新扫描首页。首页缺 notices、显式 null list/notice、附带未知错误字段等仍报错；不因无数据删除既有归档。
- 23门课程讨论中，空课程返回 status:true、poff.lastPage=1，省略 datas。现在仅在显式终页时允许省略 datas；续页缺 datas、显式 null 或畸形集合仍报错。
- 有帖子课程的 poff.lastValue 有效，但可能完全省略 lastAuxValue。后者现在按可选辅助游标处理，缺省为空，后续请求仍携带必需 lastValue；缺 lastValue、显式 null/畸形辅助值、重复且不推进的游标仍报错。

新增脱敏回归测试覆盖以上真实协议形态及反例。诊断工具支持 `PROBE_SOURCE=chaoxing PROBE_RESOURCES=notice,discussion go run ./cmd/refresh-probe` 批量验证；仅输出响应结构/计数和结果，不输出通知或帖子正文。`PROBE_NOTICE_FRESH=1` 从首页在内存中读最多6页通知，不更改数据库检查点；用于对照续页响应，不表示一次完整账号归档验收。

修复后真实批量复测：通知当前续页正常结束；23门课程讨论均成功（19个省略 datas 的空末页，4个有帖子页），包含之前缺辅助游标的课程。之前反爬诊断段所述“讨论仍 parse_failed”是分页兼容修复前的历史结果。全包 race 测试与 vet 通过（本次只改解析与诊断，未改数据库）；复测仅表示本轮实际读取成功，不宣称所有历史页一次性读完。

最终运行中验证：学习通最新整轮7种资源均成功（7/7、failed=0），新增22条、同内容83条；`python3 scripts/chaoxing_regression.py --require-all-resources` 的12项检查通过，包含连接保留及7种资源成功。也实际读取了有多页课程的第二页（5条、明确终页），脱敏双页测试确认缺省辅助游标仍正确传递 lastValue。运行中的本地 demo 已更新，恢复原10分钟定时设置。全局任务仍可能因U校园缺教材授权标为部分失败，不能与学习通结果混淆。诊断与最新快照 `.build/chaoxing-parser-report.json`。
