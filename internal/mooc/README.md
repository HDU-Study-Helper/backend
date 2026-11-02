# 中国大学 MOOC / SPOC 只读适配器

仅实现 `internal/mooc/**`。入口 `New(contract.Dependencies) (contract.Adapter, error)`，实现当前 `internal/contract/platform.go` 的 Describe / BeginAuth / PollAuth / Sync。无 SQL、HTTP route、后台循环、其他平台依赖或新增外部依赖。父层负责凭据加密、Scope 可信构造、Worker、入库、内容哈希幂等和 API。

## 实现和验证状态

| 能力 | 本模块实现 | 验证边界 |
|---|---|---|
| 官方扫码二维码获取与轮询 | GET code.do，真实官方 image URL；0/4 pending、1 scanned、2 token 换 Cookie、3 expired | Mock 通过；本轮只匿名探测 code.do 响应形状，不扫码、不读取已有真实会话。已有研究浏览器真实登录成功证据，不能等同本 Go 链路已真实验收 |
| MOOC/SPOC 已选课 | courseType=1/2，form type=30/p/psize=8，result.result/list 与 pagination/query | Mock 两页、同课程不同 term 通过；研究曾验证两个范围首页，本模块多页尚未真实验收 |
| assignment | 逐期 getLastLearnedMocTermDto 的 quizs/quizzes/homeworks 和明确任务单元；保留源截止、提交、互评字段 | Mock 通过；研究仅有限普通 MOOC quiz 目录和空 SPOC 目录证据，非完整账号作业验收 |
| exam | 相同安全目录内 exams 或明确 contentType=6 单元的任务元数据 | Mock 通过；真实考试目录覆盖未验证，不取试卷、题目或答案，不开卷 |
| progress | lastLearnUnitId、目录任务/单元显式个人状态、个人分数、尝试数等原值 | Mock 通过；仅观测字段，lastLearnUnitId 表示最后访问而非已完成。没有推导完成率、提交成功或“已完成” |
| notice | `ErrUnsupported`，Describe 不声明 | 尚无核实端点；不以本地提醒替代平台公告 |

本模块未进行真实账号登录或采集，没有读取 `research/mooc/live/private/**`、真实 Cookie 或原始私密日志。协议依据 `research/mooc/REPORT.md`、`live/TEST_REPORT.md`、`live/LOGIN_STATUS.md`、已审查的 MIT Keqi/MOOC_reminder 协议字段与当前官方公开 core JS；实现独立编写，未复制 UNLICENSED MCP 实现。

## 认证与会话

BeginAuth 返回真实官方 `QRURL`（`https://mooc-image.nosdn.127.net/...`）。公开 code.do 当前返回该域的 HTTP 图片链接，适配器限定精确域名后升级 HTTPS，不生成占位图、不返回 token API URL。code.do 参数 width=182/height=182；poll.do 参数 pollKey。官方公开前端枚举确认 0 未扫码、1 扫码未确认、2 已确认、3 失效、4 其他 App 扫码。2 只在官方 `mocMobChangeCookie.htm` 换得 STUDY_INFO 与 NTESSTUDYSI 后返回 authorized；scanned 不等于 authorized。返回 PollAfter=2s，父调度器执行轮询。上游未提供过期时间，ExpiresAt 使用本地保守 2 分钟有效窗口；上游 codeStatus=3 可提前失效。网络/结构错误明确返回，不伪装 expired。

Session 是敏感 opaque `[]byte`，包含私有 Cookie jar、待授权 pollKey、Scope、状态；只允许父凭据层加密持久化。AuthResult 的 Session 不参与 JSON 序列化，父 HTTP 层只对白名单 QR/状态字段响应。每次调用复制注入 HTTPClient，保留 Transport，不使用注入 Client 的 Jar，不修改原 Client/全局状态。每次从当前 Session 恢复独立 jar；Page.Session 总是回传 Cookie 刷新，父层应与对应 checkpoint 一起持久化，禁止日志或普通 metadata。

仅允许 HTTPS 官方 www.icourse163.org 认证重定向，最多 6 次；跨域重定向明确失败。真实成功流程若出现其他官方域重定向，需要独立验证后增加精确白名单，当前不会放宽到任意网易域。会话、游标均校验非空 user+connection。Session 是内部可信加密状态，不是可供客户端构造或导入的 Cookie API。

## Sync 调度、游标与原生 ID

所有资源首次 cursor 为空。每次 Sync 最多执行一次上游 RPC：

1. 发现一页 MOOC 课程。course 直接输出；其他资源将最多 8 个期次封入 cursor，该次允许 Records=[]、HasMore=true。
2. assignment/exam/progress 每次读取一个期次目录，成功后推进到下一期次；这一页任一解析失败会整页报错，不推进。
3. 继续课程下一页，再切换 SPOC courseType=2；全部完成时 HasMore=false、NextCursor 为空。下一次周期从头全量扫描。

父 Worker 必须继续合法空页。没有原生增量 API、删除证据或持久增量水位；由父层内容哈希幂等保存源观察，未出现不能视为删除。分页使用 totlePageCount/totalPageCount，缺失时按 8 条推断是否还有一页，少于 8 条终止；每范围最多 100 页，越界/重复相邻课程页明确 ErrParse，不静默截断。每目录最多 4 MiB 响应、深度24、10000个节点、2000条输出，超过预算失败。

cursor 是 base64url JSON，含 version/source=mooc/resource/user+connection、courseType、页号、当前最多8期次、期次索引与前页摘要；严格验证资源与 Scope。它是敏感内部 checkpoint，父 API 不应接受客户端上游 cursor。不同资源各自扫描，无共享内存课程缓存；父层可按速率和预算调度。

ID 采用无 padding 的 base64url(JSON字符串数组)，可逆且没有分隔符歧义：

- course.ExternalID = `[courseId, termId]`，同课程不同 term 永不合并。
- task/progress.ParentID = 同一期次的 course.ExternalID。
- task/progress.ExternalID = `[courseId, termId, unitId, contentId, nodeId]`。
- 最后访问单元记录 ExternalID = `[courseId, termId, "lastLearnUnitId"]`。

Fields 同时保留各项原生字符串 ID、courseType，缺失 unitId/contentId 保留空值，绝不拿 aid 替代；unitId 继承最近目录 unit 或显式 unitId，contentId 优先直接值，再取嵌套 test 内容 ID。源 course term 与 DTO 明确 id/termId 不同会报解析错误；尚不实现 SPOC source activeTermId 自动映射，避免混淆源期次和路由期次。

只沿已核实 chapters/lessons/units/quizs/quizzes/homeworks/exams 目录字段遍历。`test` 只读取白名单元数据，不将它作为第二个任务重复生成。旧文档 contentType=3 可能表示文档，因此普通 unit 的 3 只有携带 test 元数据才作为 homework。不按标题关键词猜考试。不以分数、互评阶段或 usedTryCount 推断完成状态。提交截止和 evaluateEnd 分别保留，互不覆盖。返回源时间原始数值/字符串，不捏造时区或未知日期。未观察到可靠原生版本字段，SourceVersion 留空。

成功 code 但 result 缺失/null、目录没有明确 chapters 数组、错误期次、畸形页、非 JSON 都报 ErrParse；明确 chapters=[] 可返回空的有限目录观察，特别是 SPOC 不代表源课程没有作业。

## 安全限制与验收

每请求检查 context、20s 超时、4MiB 体积上限；401/403/已观察认证错误码映射 ErrAuthRequired，429 映射 ErrRateLimited，其余上游错误不包含 URL/响应体/凭据。Record 只输出白名单标量元数据，不包含原始 DTO、Cookie、CSRF、Token、签名 URL、题干、已有答案。禁止 getOpenQuizPaperDto/getOpenHomeworkPaperDto、报名、LearnVo 批量请求、进度上报、保存、提交或任何代做行为。没有隐式重试、休眠或循环轮询。

默认测试完全使用本地脱敏 fixture 和注入 Transport，不访问真实平台。已覆盖 QR pending/scanned/authorized/expired、未知状态、token 换 Cookie、Scope 拒绝、两页课程、MOOC/SPOC、同 course 多 term、作业/考试/进度白名单、缺失 result/目录、空目录、重复分页、跨资源游标、取消/超时、限流、体积上限、跨域重定向和并发双用户 Cookie 隔离。

验收仅运行本包：

```bash
gofmt -w internal/mooc/*.go
go test ./internal/mooc
go test -race ./internal/mooc
go vet ./internal/mooc
```
