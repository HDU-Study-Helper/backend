# 中国大学 MOOC / SPOC 只读适配器

范围为 `internal/mooc/**`，可选诊断入口 `cmd/mooc-inspect/**`。`New(contract.Dependencies)` 返回 contract.Adapter；没有 SQL、HTTP route、后台循环或模型调用。父层负责加密凭据、可信 Scope、调度、入库及内容哈希幂等。已有认证、重试、空子集合兼容与会话隔离逻辑保留。

## 当前能力和证据

|资源|行为|覆盖边界|
|---|---|---|
|course|MOOC/SPOC分页、逐期目录元数据、官方学习页教师展示名、经courseDto.id验证的官方课程信息区全文、SPOC源期次发现|当前会话验证MOOC课程介绍2620字节及教师展示名；SPOC介绍用明确源courseId和已发现学校路由，缺失映射时unsupported；不是全部公开/历史课程|
|chapter / lesson|完整可见目录章、节，标题、顺序、原生ID和路径|只沿明确目录字段，不猜未知结构|
|material|视频、文档、图文及未知单元目录元数据；type4读取图文正文，type3读取PDF页数/比例元数据|仅verified getLessonUnitLearnVo的3/4，不下载PDF或媒体，不暴露签名URL；其他类型正文unsupported|
|discussion|普通unit.contentType6根帖要求正文、目录原生个人状态和viewStatus对应的平台任务点状态|自动同步不请求PostBean详情、回复列表、参与者身份或回复正文；viewStatus缺省/未知时任务状态未知，不推断回复提交结果|
|assignment|quizs/quizzes/homeworks 集合和有明确任务证据的单元，任务描述若已随目录提供则提取可读文本|不打开试卷、不读题目/答案、不提交|
|exam|exams / 单一 exam 对象，或 unit.test.type=6；下一独立Sync读courseBean.getNewExamList|旧版线上期末考试列表原站已验证；新版仅验证空数组，非空明确unsupported而不丢弃先前旧版记录，不猜题目结构|
|progress|lastLearnUnitId、原生term totalLearnedTime、章/节/单元显式字段；下一独立Sync读getMemberLearningRate的lessonLearned/lessonsCount|原站已验证MOOC为6/46、SPOC为2/57；其分母不是目录lesson节点数，不合成完成率；已实测lessonLearned=null/lessonsCount=12时保留明确null并标partial_source_fields_observed，不造零；memberId不采集，最后访问不等于完成|
|notice|官方DWR CourseBean.getAllAnnouncementByTerm(termId,1)，标题/正文/源发布时间及修改时间|已验证源期次返回1条公告；MOOC/SPOC路由与源期次独立读取；不含教师论坛/global通知|

真实只读诊断使用 dbbridge 公共端口及 credential 解密已有会话，仅在内存恢复会话，不重新授权、不提交记录/游标/会话。诊断仅输出白名单元数据/形状，绝不输出 Cookie、签名URL、题目或答案。没有运行第三方混合读写代码，没有访问 research/mooc/live/private 会话。历史研究见 `research/mooc/REPORT.md`、`live/TEST_REPORT.md`；当前字段证据来自官方目录、官方学习页脚本数据和官方公开 JS。实现独立编写。本轮官方来源：[学习内容模块](https://mc.stu.126.net/pub/s/tp_learn_index_desktop_modules_learn_content_77de50b84f45c2d14d1999790ee28ebd.js)、[讨论模块](https://mc.stu.126.net/pub/s/tp_learn_index_desktop_modules_learn_forum_e1700063cdadd9e39b7413f0100c6b34.js)、[任务概览模块](https://mc.stu.126.net/pub/s/tp_learn_index_desktop_modules_learn_eb4621a644ff77602df69a4bb13e5c55.js)。只读RPC签名在这些代码中确认，真实响应再次验证；PostBean.incrPostBrowse/clearUnreadCount独立写调用不执行。

本轮验证：普通MOOC三门目录分别 6章27节101单元、5章25节81单元、4章4节52单元。SPOC route 1488617444 目录为空，但官方 `window.moocTermDto.id=1488350464`，读取该源期次得到6章27节101单元；route1488617443对应源1488348465，保留路由自身2章4单元及源4章52单元。第三SPOC route1487718452有11章47节75单元，无独立moocTermDto。只代表当前可见目录，不包含未发布或不可访问内容。

## 分类和字段

任务与普通单元使用不同枚举，必须结合 source_collection：

- quizs/quizzes → assignment(task_type=quiz)，homeworks → assignment(homework)，exams/exam → exam。
- unit.test.type=2/3/6 分别为 quiz/homework/exam；`test`仅是本节点元数据，不递归为第二个任务。
- 官方普通unit枚举1视频、2视频字幕、3PDF、4图文、5QUESTION、6讨论、7直播、15交互课件。无test的2为material(video_subtitles)；test.type缺省的旧type2仅在test提供显式提交/分数/尝试次数等考核字段时为quiz。空test或未知test.type输出material、unit_type=assessment_unknown、classification_status=unsupported_assessment_subtype，不按普通unit类型调用正文端点。5同样保留未知考核状态；不读取题目、不猜考试。
- 未识别普通单元仍输出material、unit_type=unknown，保留原contentType及明确未知状态，不静默丢掉。

自动同步的子记录 ParentID 保持原路由course.ExternalID。历史回复者记录 ParentID 为根帖ExternalID，并通过course_ref/courseRef保留原路由课程归属；当前自动同步不再生成回复者记录。层次在 Fields：courseId/termId/routeCourseId/routeTermId、sourceTermId、activeTermId、source_scope(route或active_source)、sourceCourseId、course_name、chapterId/lessonId/unitId/contentId/nodeId、path/path_titles。catalog_node_type=unit/chapter/lesson/任务类型，source_collection及assessment_evidence记录分类依据。不同源期次相同ID不会碰撞。同名MOOC与SPOC绝不按课程名或courseId合并。

Fields保留白名单源标量：releaseTime/gmtCreate/gmtModified/position、totalScore/bonusScore/testTime/trytime/tryTime、截止/互评/成绩发布时间、原生提交状态、分数、可见性、时长、completePercent等。嵌套 test_metadata 单独保留白名单以避免不同层同名字段丢失。时间保留源数值/字符串、原有单位及哨兵值，不造时区。任务新增assessment_time_fields={node:{…},test:{…}}逐层保留时间，assessment_time_semantics/assessment_time_coverage说明含义与缺口。releaseTime是发布/开放信息，不能自动当考试开始；deadline是提交截止，evaluateStart/evaluateEnd是互评窗口，evaluateScoreReleaseTime/scoreReleaseTime是成绩发布。原startTime/endTime仅原样保留，无值不拿其他字段补；官方UI有fallback不代表源语义相同。新增userSubmitStatus/userEffectStatus等白名单，仅源实际提供才输出，不从互评阶段或分数推提交完成。SourceVersion仍留空；gmtModified是普通源字段，不能证明整个DTO版本。

Content来自安全目录description、公告content、type4.htmlContent、type6.post.content。HTML转可读文本并丢弃链接及脚本；不复制原始DTO。type3为body_status=metadata_only，保留textPages/textPageWhRatio；其他未核实正文显式unsupported。每条记录coverage=partial及原因；课程含notice/material_body/discussion_body/assessment_body覆盖声明、catalog_status和源期次发现状态。讨论根帖仍为Kind=discussion、discussion_record_type=root_post，正文保留讨论要求，ParentID与ExternalID及原路由/源期次归属保持不变。自动同步仅通过一次CourseBean.getLessonUnitLearnVo保留根帖内容及安全标量，立即输出根帖；不再读取PostBean.getPostDetailById或getPaginationReplys。reply_collection_policy=disabled_current_user_task_state_only、reply_coverage=not_requested_current_user_task_state_only、reply_body_coverage=not_collected_by_policy明确此策略。目录viewStatus按原站数值真值规则投影task_point_completed与task_point_status：原站已验证0为灰色、5为绿色；不把枚举误当成仅0/1。缺失、null或非数值未知形状保持unknown。它表示服务器返回的目录任务点状态；前端打开讨论发出的finished请求不等于服务器接受，也不执行该写入。认证目录unit.replyCount与根帖post.countReply分别是个人单元计数和全帖总计数，不能混用：仅明确的目录非负计数产生current_user_reply_count，正数标participated、显式0标not_participated，null/缺省维持unknown，不从未采名单或全帖总数推断本人未回复。原站本次24个讨论单元中11个viewStatus=5/replyCount=1，另13个viewStatus=0/replyCount=null；与已完成／未完成目录样式对照。公告保留noticeId/publishTime/gmtCreate/gmtModified/status/type/weight。

进度新增native_progress_fields、progress_status(source_fields_observed/not_provided)、progress_coverage、progress_value_semantics。章/节/单元保留自身字段，不用子节点平均值替代上游进度；讨论单元viewStatus单独投影平台任务点标记，其他原始进度不泛化为完成状态，completePercent不自动乘100，learnTime不猜单位。新增term_progress只承载源term显式字段；lastLearnUnitId记录仍仅最后访问。自动同步保留目录userSubmitStatus等个人状态，不发起讨论详情读取。

课程同一稳定ExternalID最终顶层字段对应最近读取的期次；catalog_sources数组最多保留路由及active源两个独立快照，包含sourceTermId/sourceCourseId/source_scope、catalog_status/catalog_chapter_count、白名单时间/可见性/进度标量、介绍/要求/目标/教师及其coverage。源期次缺省字段不会继承路由期次值；官方学习页教师展示名可跨源保留，但catalog_lector_display_names不冒充源期次教师。快照不递归携带目录、完整响应或个人资料。官方课程信息区正文读取成功时body_status=official_course_information_sections。

## 资源数据矩阵（本次代码审查）

|资源|输入 → 输出|缺失及原因|
|---|---|---|
|course|课程列表course/term/school + 各term DTO + 官方学习/课程页 → 稳定路由课程、源归属、catalog_sources、介绍、要求、目标、教师、原生时间/可见性|源未提供的时间/进度不补；介绍缺明确sourceCourseId→school映射时unsupported；未把完整courseDto/教师资料复制到Fields|
|chapter / lesson|chapters/lessons显式层次 → 原生ID、path/path_titles、顺序、releaseTime/可见性、节点自身native_progress_fields|无原生进度时not_provided；不平均子节点；不沿未知对象推层次|
|material|units枚举及白名单元数据 → 类型、课程/期次/章/节、目录描述；type4.htmlContent→可读正文，type3→PDF页数/比例|视频/字幕/直播/交互课件及未知类型正文unsupported；PDF/媒体字节和签名URL未采集；未知test类型不触发正文读|
|discussion root|普通type6目录 + 安全LearnVo post → 要求正文、postId、全帖计数、目录个人状态及独立个人回复计数|不读Post详情或回复页；null计数保持未知；任务点不冒充评分或内容合格|
|discussion_reply_user|历史记录和内部旧分页实现保留兼容；自动同步不再生成|自动回复列表及正文采集禁用；没有新增手动参与者工具或独立采集队列|
|assignment / exam|quizs/quizzes/homeworks/exams/exam或明确test.type → task_type、分类依据、目录/嵌套test白名单、逐层assessment_time_fields、个人提交状态|无开始/结束不从releaseTime/deadline/互评窗口猜；不开卷、不取题目答案；未知test类型保留assessment_unknown|
|progress|term及章/节/单元/任务显式字段 → 原生零/false、native_progress_fields、lastLearnUnitId独立记录|无原生字段不输出完成率；最后访问不等于完成；时间单位/percent比例不归一化|
|notice|各路由/源term官方公告列表 → noticeId、标题正文、publishTime/gmtCreate/gmtModified及状态|不覆盖全站/教师论坛通知；没有源版本证据则SourceVersion空|

跨期次元数据覆盖/遗漏、不递归响应、未知考核不读正文、目录零值提交状态由脱敏fixture覆盖。2026-11-12通过新扫码会话串行浏览原站MOOC及SPOC目录、单个讨论、作业测验和考试列表，拦截进度保存、回复分页和答题请求；目录真实响应另做离线回放。未恢复批量采集、提交学业记录或运行模型。详见INTERFACE-VERIFICATION-20261112.md。测试核对双期次根帖归属、正文与任务状态保留、旧回复游标第7页无需网络即输出根帖、后续待处理记录与下一课程不丢失，以及课程排除策略。共享投影应继续保留userSubmitStatus/userEffectStatus与totalLearnedTime零值。

## 调度和游标

Exam与Progress读取目录后先保存SupplementalPending，下一独立Sync分别读取新版考试列表与学习统计，再推进active源期次或下一课程；失败保留该阶段，不重放已输出的旧考试。新版考试空数组以及已实测SPOC路由的code=0/result=null可确认（原站同显示暂无新版考试）；缺失result或类型不符仍parse_failed，非空返回unsupported而不猜结构，也不伪造空考试记录。学习统计只保留sourceTerm、lessonLearned/lessonsCount及独立来源，不保存memberId或合成百分比。

每次Sync通常最多一次逻辑上游读取（目录瞬时网络故障最多两次重试；下文明确的旧会话空列表形状最多追加一次官方首页会话恢复和一次目录重读）。讨论在目录后只读取一个安全单元根帖正文并立即输出，不建立回复分页状态。已有合法Discussion游标（包括分页进行中）在重新检查课程排除策略后，无网络读取直接输出保存的根帖及当前策略标记，然后清除Discussion；后续Pending与Courses保持原次序。被排除课程的根帖与同课程待处理队列不输出，下一课程仍保留。旧游标页数、ID集合和Bytes字段仍按原边界验证，不据此继续自动读取。单响应仍受4MiB及DWR累计结构预算约束。发现一页课程、GET一个SPOC官方页面、读取一个期次目录/公告列表、或读取一个安全单元正文。材料/讨论目录把需要正文的记录放入有界敏感cursor队列，每次只读取一个，全部处理后继续下一期次。所有资源单页最多500条，多余目录/公告记录同样进入cursor队列，后续每次输出最多500条且不重复请求源。课程流也逐期读取以保留真实课程元数据；额外步骤读取官方学习页的learn-teacher-names与/course/信息区，原生courseDto.id必须匹配。MOOC列表形成有界sourceCourseId→school索引；SPOC源课介绍仅用该明确映射，不用本地SPOC学校猜源课路由。description_coverage/teacher_coverage/description_source_course_id保留覆盖与出处，目录已给描述不会被缺省页面覆盖。SPOC先验证官方页面的courseDto/termDto归属，再从activeTermId或moocTermDto.id发现源期次；不执行JavaScript。路由与源目录独立读取，Referer始终保留原路由。缺少明确源映射时标unsupported，未知字段/矛盾映射明确失败。

空记录且HasMore=true合法，父层必须继续。游标base64url JSON绑定version/source/resource/Scope，最多8课程，课程分页每范围最多100页，页号/相邻页重复/未知目录形状明确失败。每响应4MiB，目录深度24、10000节点、2000输出；课程名512字符、游标1MiB、待正文队列2000条，SPOC脚本数据对象256KiB预算。根chapters必须数组，子集合null表示源缺省；空目录不代表完整覆盖。

父层通过`contract.WithExcludedCourses`传入当前连接的课程黑名单；匹配原生course.ExternalID，即routeCourseId/routeTermId的base64url键。每页在当前课程发现、介绍、目录、公告请求及待正文/溢出记录输出前重新检查，路由与active源期次均按原路由归属跳过。共享课程发现页仍读取，SourceSchools可来自该页；保存的课程数组和索引不因过滤重新排列。跳过只推进一个游标槽或清空待输出队列，不在调用内循环请求下一门课程；移除黑名单后下次完整扫描可重新读取，适配器不删除已有记录。离线注入Transport测试覆盖中途新增、待正文、active源、同名不同期次、Scope隔离及移除恢复；未为此执行真实上游请求。

ID无padding base64url(JSON字符串数组)：

- course.ExternalID=`[routeCourseId,routeTermId]`。
- 子记录ExternalID=`[routeCourseId,routeTermId,sourceTermId,unitId,contentId,nodeId]`。
- lastLearn记录ExternalID=`[routeCourseId,routeTermId,sourceTermId,"lastLearnUnitId"]`。
- notice.ExternalID=`[routeCourseId,routeTermId,sourceTermId,"notice",noticeId]`。
- discussion回复ExternalID=`[routeCourseId,routeTermId,sourceTermId,"discussion",postId,"reply",replyId]`，不与根帖合并。
- term进度ExternalID=`[routeCourseId,routeTermId,sourceTermId,"term_progress"]`。

增加sourceTermId改变了旧任务/progress键；父层须处理旧错误exam记录、旧任务重复及历史归类迁移，不能将旧exam计数继续作为真实考试数。没有删除或增量证据，未出现不等于删除。

## 认证、安全和测试

扫码仍使用官方code.do/poll.do，二维码只允许官方图片域并升级HTTPS；2确认后必须换得STUDY_INFO与NTESSTUDYSI才授权。会话为敏感opaque []byte，仅父credential加密存储。每请求恢复独立Cookie jar，返回Page.Session供父持久化。HTTPS同源重定向最多6次，20s请求超时。401/403或已确认认证码返回ErrAuthRequired，429不重试；目录读对transport及408/502/503/504退避500ms/1s重试。仅PostBean.getPostDetailById/getPaginationReplys对transport、响应body读取失败及408/502/503/504退避200ms重试一次，每次20s独立请求期限，两次请求加退避最多约40.2s且受父context约束；父取消/截止立即停止，父仍有效时单次期限失败可重试。每次从当前Cookie jar重建body、Cookie及CSRF token，单响应4MiB限制不变。其他DWR读不重试；认证、429、HTTP500、解析及DWR remote exception不重试。该讨论重试仅通过离线注入Transport测试验证，未执行源网络、数据库或模型调用。课程列表有一个已实测的会话恢复例外：旧STUDY_INFO可能返回code=0、result.result=null及pageIndex匹配/totleCount=0/totlePageCount=1，不能据此认定账号无课。仅此精确形状触发一次GET /home.htm，沿已有HTTPS同源重定向通过官方changeCookie.htm更新内存Cookie，再重读一次课程列表；只接受正常数组，持续null或其他未知结构仍明确parse_failed，不推进游标。成功Page.Session携带更新后的Cookie，父层负责加密保存。该握手只用已有登录Cookie，不重新扫码、不猜凭据、不读取或提交学习进度。2026-11-10真实只读验证：从原已存会话及空course游标出发，首步复现null→官方Cookie更新→3门MOOC正常数组，随后逐页传递Page.Session；24次Sync读完3门MOOC及3门SPOC课程流（含源期次目录和课程信息），最终HasMore=false。诊断未向中心库提交会话、记录或checkpoint；不据此宣称其他资源已完成重新入库。

可选 `contract.SessionRefresher` 已实现：`RefreshSession(ctx, Scope, Session)` 恢复该 Scope 的私有 Cookie jar，只发起一次官方 `GET /home.htm`（允许上述有界 HTTPS 同源重定向），再以已有课程发现的合法参数 `type=30,p=1,psize=8,courseType=1` 调用 `learnerCourseRpcBean.getMyLearnedCoursePanelList` 核验实际登录。它不依赖本地 Authorized 标记，正常空数组同样可以核验成功；精确的旧会话 null/零总数形状在此显式刷新入口返回 `ErrAuthRequired`，未知 null、缺失列表和畸形课程仍返回 `ErrParse`。401/403/认证业务码、限流、瞬时错误和解析错误各自保留错误类别。只有验证成功才返回更新的敏感 Session；Cookie 的 MaxAge 在首次接收时固化为绝对 Expires，恢复不会延长有效期，删除指令也随会话持久化。父层负责在租约保护下加密保存，且仅由父层对 Sync 的 `ErrAuthRequired` 执行一次刷新和原游标重试；适配器不叠加认证错误重试。上述旧 Sync 精确 null 形状恢复仍保持原有调用数及持续 null 的 parse_failed 语义。新增刷新方法、Scope 隔离、重定向、错误分类和 Cookie 生命周期仅通过脱敏 Transport 单元测试验证，未执行真实账号刷新或中心库提交。

禁止开卷RPC、申请作答、报名、保存/提交、进度写入或对未知/评测单元调用LearnVo。仅DWR CourseBean.getLessonUnitLearnVo(contentId,type,0,unitId)的3/4/6类型白名单允许，且不执行官方viewer随后独立调用的saveLearnInfo。DWR请求严格text/plain换行键值，响应仅解析data-only变量、属性、数组、标量、Date数值和单一remoteHandleCallback；不eval、不运行任意脚本。构建前累计容器/属性/数组槽（含不可达与稀疏数组）10000预算；延迟引用解析有环检测、深度24/10000节点、20000语句/字面量预算，分词时也提前限制嵌套与分段数量，保留合法emoji与转义富文本。默认测试仅注入Transport及脱敏fixture，不访问网络。覆盖真实unit/assessment枚举分离、单一exam、目录层次和完整字段、SPOC双期次与原Referer/owner、歧义映射拒绝、相同源节点跨scope独立、旧认证/边界/取消/并发隔离用例。

```bash
gofmt -w internal/mooc/*.go cmd/mooc-inspect/main.go
go test ./internal/mooc
go test -race ./internal/mooc
go vet ./internal/mooc
```

诊断入口只接受唯一active MOOC连接；Acquire/Release用于加载已有密文，不写源记录或checkpoint，输出不适合作为完整采集完成凭据。`MOOC_INSPECT_SYNC=1`通过Sync从空cursor读取最多12步，当前自动路径只输出讨论根帖，不读取Post详情或回复页。旧诊断`MOOC_INSPECT_COMPLETION=1`仍包含显式详情及第一页回复读取；它不属于自动同步，也不是已提供的用户按需工具，本次未执行。历史2026-11-09首分页15条回复/总309条/21页的只读验证仅说明当时解析能力，不代表当前策略、全帖或全账户已采集。当前要求停止网络采集期间不得运行这些诊断。旧内部advanceDiscussion及其分页、归属、预算、匿名和敏感字段过滤测试保留，用于历史实现兼容，不再由Sync调用。当前默认测试仅使用注入Transport及脱敏fixture，覆盖自动根帖单次正文读、双期次归属、旧游标无网络退役、空正文/未知状态保留、课程排除、进度零值和逐层时间独立。
