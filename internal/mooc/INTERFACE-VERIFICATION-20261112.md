# MOOC 原站接口核验 — 2026-11-12

本次扫码登录成功后，通过真实浏览器串行进入课程目录、单条讨论、测验作业列表、考试列表。未启动批量采集或Agent执行；未枚举ID，不翻讨论回复页。路由拦截器只放行明确只读操作，拦截学习记录保存、回复、试卷和答题操作。原始响应含个人资料，只保存于本地私有证据，不纳入版本控制。

## 已核验接口

|入口|请求|真实证据与解释|实现|
|---|---|---|---|
|扫码登录|code.do → poll.do → mocMobChangeCookie.htm|扫码授权后获得会话，并实际进入登录态课程页面|已有适配器；新会话已由Vault加密保存，不改采集checkpoint|
|我的课程|learnerCourseRpcBean.getMyLearnedCoursePanelList|用于登录验证和明确课程/期次发现，不按名称合并MOOC与SPOC|保留原分页、scope、黑名单|
|课程目录|POST courseBean.getLastLearnedMocTermDto，termId|MOOC本次7章36节119单元，5测验5作业24讨论；SPOC独立路由核验|保留来源期次、层级、原生枚举与零值|
|讨论任务点|同一目录的unit.viewStatus|截图对应已完成讨论为5/绿色，未完成为0/灰色；原站代码按数值真值渲染learned|修正0/1误读，task_point_status、completed、source、semantics向Agent透出|
|本人讨论参与|同一认证目录unit.replyCount|已完成样本为1；未完成样本null。与全帖countReply分开，null不改0|明确个人计数才投影；没有本人计数不通过名单反推未参与|
|讨论要求正文|DWR CourseBean.getLessonUnitLearnVo(contentId,6,0,unitId)|单个未完成讨论返回根帖题目/要求；目录replyCount=null，而post.countReply=73|只留要求和安全标量，禁止复制完整poster/member资料|
|测验作业列表|同一目录＋原站learnTestList导航|5测验5作业；UI截止时间2026/12/29 20:00与源deadline对应|保留test白名单及node/test时间分层；userScore=null不造0，不用原始枚举猜提交|
|旧版考试|目录exams/exam|C语言SPOC考试页显示“线上期末考试，总分100分”|仍单独保留旧版来源|
|新版考试|POST courseBean.getNewExamList，form termId|同一浏览器正常考试导航触发，code=0/result=[]；另已实测SPOC路由返回明确result=null，源期次[]，UI均暂无新版考试|新增独立阶段；空数组与该明确空值已支持，非空仍明确unsupported，不伪造解析|
|课程学习统计|POST mocCourseV2RpcBean.getMemberLearningRate，form termId|MOOC lessonLearned=6/lessonsCount=46；SPOC2/57|独立progress记录；不存memberId，不把统计分母与目录节点数混用，不算百分比|

## 已停止的错误路径

自动讨论流不再调用PostBean.getPaginationReplys或getPostDetailById。原有合法回复分页游标退役时只输出已保存根帖，保留后续课程和待处理单元，不读取下一页。历史已采名单未删除，本次不继续采集，也不据此判定新任务状态。旧游标中的详情响应曾可能覆盖目录viewStatus/replyCount；退役时只从独立current_user_participation_fields目录快照恢复这两个字段（含null），缺快照保留未知，并清除旧派生个人计数及根帖作者身份标记。

前端打开讨论调用saveMocContentLearn({finished:true})仅是客户端请求，不能证明服务器会认可；用户实际验证需要回复。此写入始终被拦截，未以此测试或修改学习状态。

## 边界与未验证

- 绿色表示原站返回的任务点标记，不能扩展成教师评分通过或回复内容合格。
- 新版考试只有空列表的真实证据，非空解析未宣称已验证；遇到非空明确失败，不静默漏掉。
- 不打开试卷、不申请作答、不读题目答案、不提交、不写学习进度。
- 课程统计的课节分母与目录lesson数量不同，保持各自口径，不计算伪完成率。
- 真实浏览器和响应核验覆盖代表性MOOC及SPOC；本次未全量重采或提交学业记录。
- 自动同步只按请求的课程资源、路由/源期次和已保存检查点推进；一调用一个逻辑读取，不并行扫课。重新启用采集仍需用户明确指令。

## 验收

包括当前真实0/5讨论状态、个人计数null/0/1与全帖总数隔离、旧游标退役不读网络、根帖单次正文读取、MOOC/SPOC期次隔离、旧考试先落结果后新版阶段失败不重放、学习统计独立记录、结构化投影与敏感字段拒绝。测试默认脱敏离线；原站核验仅按正常页面顺序定向完成。
