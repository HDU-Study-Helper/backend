# 学业归档检查

`academic` 提供当前用户的数据库归档检查、课程采集黑名单接口与 `/admin/academic` 页面。构造函数仍为 `New(model.AcademicRepository)`，黑名单通过可选的 `model.CollectionPolicyRepository` 类型断言使用；可信 owner 来自 `httpkit.CurrentUser`。实际 SQL、归档投影、20,000 行容量检查和策略持久化由存储层实现。App 构造 `academic.New(db.Academic())`，API 挂在现有 users.Middleware 下，服务内部再次检查当前用户。不接收客户端 user_id，不触发同步、重建或上游请求。

页面按“来源 → 连接 → 课程教学班/期次 → 分类 → 源端章节/课时路径 → 条目”展示，保留同名课程的独立开课身份。未关联通知等条目单列，不能根据名称猜归属。点击课程或条目查看正文、全部业务时间及其来源字段/精度/状态、个人状态和缺失说明；原生标识及完整记录折叠。覆盖证据未知、未采集和未支持明确展示。当前存储投影仅接入学习通与 MOOC，其余来源没有伪造空目录。

## HTTP

所有 API 前缀 `/api/v1/academic`，需要现有用户会话。页面本身是公共静态壳，不含用户数据，未登录时提供 `/admin` 登录链接。

- `GET /collection-policy`：返回当前用户完整 `model.CollectionPolicy`（初始 `version:0, excluded:[]`），不接受查询参数。底层未实现可选接口时返回422。
- `PUT /collection-policy`：整体替换黑名单，JSON `{version:int, offering_ids:[]string}`，成功200返回最新策略。必填非负整数版本及最多2000个不重复、非空且最多128字节的课程标识；空数组清空。此接口使用256 KiB专用请求上限，严格拒绝未知、重复、大小写不符、缺失或空值字段，以及重复课程标识、额外JSON和全部查询参数。写操作由挂载点 `users.Middleware` 校验同源 Origin、会话与 `academic_csrf` / `X-CSRF-Token`。版本冲突返回409 `conflict`，与目录容量422区分；课程不属于用户或新选课程连接不可用返回404。已保存且连接已停用或当前目录未列出的规则由存储层允许保留，取消勾选可移除；不凭课程名识别规则。
- `GET /offerings`：`source`、`connection_id`、`course_ref`、`offset`、`limit`。
- `GET /items`：以上条件，以及 `offering_id`、`kind`、`status`、`search`、`published_after`、`published_before`、`deadline_after`、`deadline_before`。
- `GET /tree`：可选 `source`、`connection_id`；返回完整有界 `model.AcademicTree`，附加 `directory`（目录节点引用原始 offerings/items 数组的下标）。不能通过普通列表分页截断目录。
- `GET /tree?format=text`：UTF-8 纯文本目录。导出函数 `RenderTree(model.AcademicTree) string` 提供全部字段详情；`RenderCompactTree(model.AcademicTree) string` 保留所有课程与条目，每条记录一行，仅附有值的日期与已知状态，更适合目录验收。`Directory` 也可复用为结构化目录，各节点 `count` 为其子树条目总数。

列表默认 limit=20，最大100，offset 非负。时间条件必须是完整 RFC3339 并带时区，范围起点不得晚于终点；比较语义由 Repository 实现，仅比较源端提供的可比较时间。未知参数、重复参数和客户端 user_id 返回400。不存在或不属于当前用户的连接返回404；目录容量冲突返回422 `tree_too_large`，提示缩小范围或使用分页接口；其他失败返回安全错误，不输出部分目录或数据库错误细节。

## 验证与限制

`go test ./internal/academic ./internal/app`、`go vet ./internal/academic ./internal/app` 和 `node --test internal/academic/academic_ui.test.cjs`。脱敏 fake Repository 测试覆盖未登录拒绝、可信 owner 传递、客户端 user_id/无效筛选拒绝、所有筛选字段传递、严格时间校验、错误不泄漏部分数据、JSON 转义及纯文本 Content-Type、完整目录与同名跨连接身份隔离；黑名单另覆盖真实 users 中间件的会话/同源/CSRF、方法与查询校验、严格JSON、2000项容量、完整替换和版本冲突。Node内置测试解析页面脚本并验证筛选/目录切换保留隐藏规则、读取失败禁用保存、过时响应被忽略、保存完整集合与CSRF、409保留草稿并要求显式重载。页面动态内容仅使用 textContent，正文不按 HTML 执行。

这些测试不证明真实上游完整采集；数据库归属约束、真实课程目录和完整覆盖范围由 dbbridge/平台集成测试及父任务验收。页面不新增定时刷新和应用产品导航。
