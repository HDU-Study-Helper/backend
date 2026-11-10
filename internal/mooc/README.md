# 中国大学 MOOC / SPOC 只读适配器

范围为 `internal/mooc/**`，可选诊断入口 `cmd/mooc-inspect/**`。`New(contract.Dependencies)` 返回 contract.Adapter；没有 SQL、HTTP route、后台循环或模型调用。父层负责加密凭据、可信 Scope、调度、入库及内容哈希幂等。已有认证、重试、空子集合兼容与会话隔离逻辑保留。

## 当前能力和证据

|资源|行为|覆盖边界|
|---|---|---|
|course|MOOC/SPOC分页、逐期目录元数据、官方学习页教师展示名、经courseDto.id验证的官方课程信息区全文、SPOC源期次发现|当前会话验证MOOC课程介绍2620字节及教师展示名；SPOC介绍用明确源courseId和已发现学校路由，缺失映射时unsupported；不是全部公开/历史课程|
|chapter / lesson|完整可见目录章、节，标题、顺序、原生ID和路径|只沿明确目录字段，不猜未知结构|
|material|视频、文档、图文及未知单元目录元数据；type4读取图文正文，type3读取PDF页数/比例元数据|仅verified getLessonUnitLearnVo的3/4，不下载PDF或媒体，不暴露签名URL；其他类型正文unsupported|
|discussion|普通unit.contentType6根帖、PostBean.getPostDetailById当前用户关注/投票状态及getPaginationReplys分页回复|真实验证首帖15条回复/总309条/21页；每帖最多20页或300条及512KiB累计原文；不取嵌套评论或参与者资料，不冒充考试|
|assignment|quizs/quizzes/homeworks 集合和有明确任务证据的单元，任务描述若已随目录提供则提取可读文本|不打开试卷、不读题目/答案、不提交|
|exam|exams / 单一 exam 对象，或 unit.test.type=6|实际 exams.contentType=4 已验证；不能拿 plain unit.contentType6 推断考试|
|progress|lastLearnUnitId、原生term totalLearnedTime、章/节/单元及任务的显式个人字段（含零值）、原始completePercent/learnTime/viewStatus|章无字段明确not_provided；节viewStatus真实可见，含义不猜；最后访问不等于完成，不合成章/节/课程完成率|
|notice|官方DWR CourseBean.getAllAnnouncementByTerm(termId,1)，标题/正文/源发布时间及修改时间|已验证源期次返回1条公告；MOOC/SPOC路由与源期次独立读取；不含教师论坛/global通知|

真实只读诊断使用 dbbridge 公共端口及 credential 解密已有会话，仅在内存恢复会话，不重新授权、不提交记录/游标/会话。诊断仅输出白名单元数据/形状，绝不输出 Cookie、签名URL、题目或答案。没有运行第三方混合读写代码，没有访问 research/mooc/live/private 会话。历史研究见 `research/mooc/REPORT.md`、`live/TEST_REPORT.md`；当前字段证据来自官方目录、官方学习页脚本数据和官方公开 JS。实现独立编写。本轮官方来源：[学习内容模块](https://mc.stu.126.net/pub/s/tp_learn_index_desktop_modules_learn_content_77de50b84f45c2d14d1999790ee28ebd.js)、[讨论模块](https://mc.stu.126.net/pub/s/tp_learn_index_desktop_modules_learn_forum_e1700063cdadd9e39b7413f0100c6b34.js)、[任务概览模块](https://mc.stu.126.net/pub/s/tp_learn_index_desktop_modules_learn_eb4621a644ff77602df69a4bb13e5c55.js)。只读RPC签名在这些代码中确认，真实响应再次验证；PostBean.incrPostBrowse/clearUnreadCount独立写调用不执行。

本轮验证：普通MOOC三门目录分别 6章27节101单元、5章25节81单元、4章4节52单元。SPOC route 1488617444 目录为空，但官方 `window.moocTermDto.id=1488350464`，读取该源期次得到6章27节101单元；route1488617443对应源1488348465，保留路由自身2章4单元及源4章52单元。第三SPOC route1487718452有11章47节75单元，无独立moocTermDto。只代表当前可见目录，不包含未发布或不可访问内容。

## 分类和字段

任务与普通单元使用不同枚举，必须结合 source_collection：

- quizs/quizzes → assignment(task_type=quiz)，homeworks → assignment(homework)，exams/exam → exam。
- unit.test.type=2/3/6 分别为 quiz/homework/exam；`test`仅是本节点元数据，不递归为第二个任务。
- 官方普通unit枚举1视频、2视频字幕、3PDF、4图文、5QUESTION、6讨论、7直播、15交互课件。无test的2为material(video_subtitles)，有test及任务证据才为quiz。5输出material、unit_type=assessment_unknown、classification_status=unsupported_assessment_subtype；不读取题目、不猜考试。
- 未识别普通单元仍输出material、unit_type=unknown，保留原contentType及明确未知状态，不静默丢掉。

所有子记录 ParentID 保持原路由course.ExternalID，层次在 Fields：courseId/termId/routeCourseId/routeTermId、sourceTermId、activeTermId、source_scope(route或active_source)、sourceCourseId、course_name、chapterId/lessonId/unitId/contentId/nodeId、path/path_titles。catalog_node_type=unit/chapter/lesson/任务类型，source_collection及assessment_evidence记录分类依据。不同源期次相同ID不会碰撞。同名MOOC与SPOC绝不按课程名或courseId合并。

Fields保留白名单源标量：releaseTime/gmtCreate/gmtModified/position、totalScore/bonusScore/testTime/trytime/tryTime、截止/互评/成绩发布时间、原生提交状态、分数、可见性、时长、completePercent等。嵌套 test_metadata 单独保留白名单以避免不同层同名字段丢失。时间保留源数值/字符串、原有单位及哨兵值，不造时区。任务新增assessment_time_fields={node:{…},test:{…}}逐层保留时间，assessment_time_semantics/assessment_time_coverage说明含义与缺口。releaseTime是发布/开放信息，不能自动当考试开始；deadline是提交截止，evaluateStart/evaluateEnd是互评窗口，evaluateScoreReleaseTime/scoreReleaseTime是成绩发布。原startTime/endTime仅原样保留，无值不拿其他字段补；官方UI有fallback不代表源语义相同。新增userSubmitStatus/userEffectStatus等白名单，仅源实际提供才输出，不从互评阶段或分数推提交完成。SourceVersion仍留空；gmtModified是普通源字段，不能证明整个DTO版本。

Content来自安全目录description、公告content、type4.htmlContent、type6.post.content。HTML转可读文本并丢弃链接及脚本；不复制原始DTO。type3为body_status=metadata_only，保留textPages/textPageWhRatio；其他未核实正文显式unsupported。每条记录coverage=partial及原因；课程含notice/material_body/discussion_body/assessment_body覆盖声明、catalog_status和源期次发现状态。讨论根帖discussion_record_type=root_post，回复=reply，root_discussion_id连接原根帖记录，ParentID仍为路由课程；postId/replyId为原生字符串，正文去HTML/链接。current_user_state仅hasFollowed/hasVoteUp/hasVoteDown原生布尔。官方页面webUser.id仅留敏感cursor内存用于与posterId/replyerId比较，业务字段仅输出is_current_user；身份未知则缺省该布尔，current_user_identity_coverage=unknown，绝不复制poster/replyer的姓名、学号或身份ID。current_user_participation_fields保留目录replyCount/voteCount等原生状态，未扫描到本人回复不构成未参与。reply_coverage/reply_truncated/reply_pages_read/reply_count_observed/reply_total_count/reply_total_pages/reply_snapshot_changed区分页读完、预算截断和源变化，comment_coverage明确嵌套评论unsupported。根帖在回复读取结束后输出覆盖结果。公告保留noticeId/publishTime/gmtCreate/gmtModified/status/type/weight。

进度新增native_progress_fields、progress_status(source_fields_observed/not_provided)、progress_coverage、progress_value_semantics。章/节/单元保留自身字段，不用子节点平均值替代上游进度；原始viewStatus不解释成完成状态，completePercent不自动乘100，learnTime不猜单位。新增term_progress只承载源term显式字段；lastLearnUnitId记录仍仅最后访问。

## 调度和游标

每次Sync最多一次逻辑上游读取（目录瞬时网络故障最多两次重试）：讨论在目录/正文后分别读取详情、每次一页回复；固定时间升序，校验pageNo/postId/重复replyId，页码不符、空页仍有后续或未知形状明确失败。每帖20页/300回复/512KiB累计原文，超过预算保留已有记录并标截断；页总数变化显式标snapshot_changed。回复正文不放入cursor，只保留去重ID及累计预算；单响应仍受4MiB及DWR累计结构预算约束。发现一页课程、GET一个SPOC官方页面、读取一个期次目录/公告列表、或读取一个安全单元正文。材料/讨论目录把需要正文的记录放入有界敏感cursor队列，每次只读取一个，全部处理后继续下一期次。所有资源单页最多500条，多余目录/公告记录同样进入cursor队列，后续每次输出最多500条且不重复请求源。课程流也逐期读取以保留真实课程元数据；额外步骤读取官方学习页的learn-teacher-names与/course/信息区，原生courseDto.id必须匹配。MOOC列表形成有界sourceCourseId→school索引；SPOC源课介绍仅用该明确映射，不用本地SPOC学校猜源课路由。description_coverage/teacher_coverage/description_source_course_id保留覆盖与出处，目录已给描述不会被缺省页面覆盖。SPOC先验证官方页面的courseDto/termDto归属，再从activeTermId或moocTermDto.id发现源期次；不执行JavaScript。路由与源目录独立读取，Referer始终保留原路由。缺少明确源映射时标unsupported，未知字段/矛盾映射明确失败。

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

扫码仍使用官方code.do/poll.do，二维码只允许官方图片域并升级HTTPS；2确认后必须换得STUDY_INFO与NTESSTUDYSI才授权。会话为敏感opaque []byte，仅父credential加密存储。每请求恢复独立Cookie jar，返回Page.Session供父持久化。HTTPS同源重定向最多6次，20s请求超时。401/403或已确认认证码返回ErrAuthRequired，429不重试；仅目录读对transport及408/502/503/504退避500ms/1s重试，解析和认证错误不重试。

禁止开卷RPC、申请作答、报名、保存/提交、进度写入或对未知/评测单元调用LearnVo。仅DWR CourseBean.getLessonUnitLearnVo(contentId,type,0,unitId)的3/4/6类型白名单允许，且不执行官方viewer随后独立调用的saveLearnInfo。DWR请求严格text/plain换行键值，响应仅解析data-only变量、属性、数组、标量、Date数值和单一remoteHandleCallback；不eval、不运行任意脚本。构建前累计容器/属性/数组槽（含不可达与稀疏数组）10000预算；延迟引用解析有环检测、深度24/10000节点、20000语句/字面量预算，分词时也提前限制嵌套与分段数量，保留合法emoji与转义富文本。默认测试仅注入Transport及脱敏fixture，不访问网络。覆盖真实unit/assessment枚举分离、单一exam、目录层次和完整字段、SPOC双期次与原Referer/owner、歧义映射拒绝、相同源节点跨scope独立、旧认证/边界/取消/并发隔离用例。

```bash
gofmt -w internal/mooc/*.go cmd/mooc-inspect/main.go
go test ./internal/mooc
go test -race ./internal/mooc
go vet ./internal/mooc
```

诊断：在仅当前进程source .env后`go run ./cmd/mooc-inspect`，只接受唯一active MOOC连接；不改变中心业务数据。Acquire/Release仅用于合法加载已有密文，不写源记录或checkpoint。诊断输出不适合作为完整采集完成凭据。`MOOC_INSPECT_SYNC=1`通过真实Sync入口从空cursor读取最多12步、遇首个回复页即停止，仅输出记录类型、待处理计数及安全类型字段，不落库；当前会话6步读到15条reply，确认目录→正文→详情→回复状态机可用。续读旧checkpoint可能只处理原扫描末尾课程，少量调用不证明全账户重新读取；完整新轮需从当前轮终点后的空cursor开始，已保存记录不删除。`MOOC_INSPECT_COMPLETION=1`读取一个课程、一个讨论首帖、详情和第一页回复，输出结构及白名单布尔/计数，不输出回复正文或身份。2026-11-09当前会话验证详情与15条回复解析成功，21页/309回复仅首分页已真实读取；完整20页预算及截断用离线fixture覆盖，没有据此声称真实全帖已读完。全6门及SPOC双期次目录另有本轮真实只读形状验证。默认测试覆盖回复两期次路由保留、读操作白名单、私有资料丢弃、未知身份不变false、错误页/归属/重复/空循环拒绝、页与累计文本预算、进度零值及逐层时间独立。
