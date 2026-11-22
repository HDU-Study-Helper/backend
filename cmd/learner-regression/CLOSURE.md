# 单课程分级闭环验证

沿用learner-regression的只读源快照与独立schema，不启动HTTP、采集或原控制队列。所有模型、Workflow、记忆和Run仅在隔离目标中使用；模型设置密文由既有Vault装配，不输出密钥。

## 阶段

1. 默认只读枚举，使用`--offering <准确开课实例ID>`选定一个课程。
2. `--execute --prepare-only --offering ...`克隆隔离schema但不调用模型。源库历史queued状态不表示实际执行进程；如已确认冻结且需接受这些旧状态，显式使用`--allow-active-snapshot`，manifest保留状态计数。
3. `--execute --resume-dir <冻结目录> --closure-stage tools`：真实Runtime和PostgreSQL、确定性模型，检查精确课程查询及当前Workflow PRI memory create/get/update/delete；不消耗模型额度。
4. `--execute --resume-dir ... --closure-stage learner --course-timeout 12m`：新Learner暂存协议的单Agent初始化，保留请求/响应、提交结果及课程定义与记忆。已有同实体Workflow时拒绝盲重建。
5. `--execute --resume-dir ... --closure-stage full --course-timeout 15m`：真实Trigger、Filter、Learner、Worker调用，以及一项只读后续任务；保留各阶段返回与真实子Run。使用隔离的归档课程创建观察，不伪造新课程事实，不改原冻结批次。这是逻辑调用闭环，不覆盖原control队列封批、分页和重放。
6. `--closure-stage followup`：仅对已有课程Workflow执行后续任务，失败时保留原提交，不重建。`--closure-stage revise`：Worker核查账号级通知、课程要求及错误固化的单次限制，必要时通过真实Learner修订；定义改版后按新版本发起followup，父Run不假称刷新scope。
7. `--closure-stage repair`：修正已发现的记忆事实或来源映射错误，不重新初始化；先回读已提交状态，避免在先前响应502后重复执行。最终结果及完整目标记忆的显式platform_records引用会按授权记录核对原生ID，不静默改错。
8. `--closure-stage format --health-request <保留的response.private.json>`：将真实非法JSON作为同一Runtime首个冻结响应，其后只调用选定模型修复最终文本。没有工具或业务写权限；检查会话、实体、版本和完成意图保持不变，记录一次格式拒绝与实际修复用量。不会提交历史完成标记。
9. `--closure-stage model`：只发最小JSON请求，区分供应商可用性与多轮Agent效率，不读课程、不调用工具。可附`--health-request <保留请求>`检查相同规模上下文及工具Schema的响应；历史消息按数据传入，返回工具调用也不执行。

诊断执行在隔离目标中默认将模型请求超时设为90秒、供应商重试设为0；可显式使用`--diagnostic-model-timeout 180`延长单次等待，以区分慢响应和执行错误（允许10–600秒）。模型名称和其余Agent限制沿用现有配置，源库模型设置不变。该诊断策略与旧基线不同，耗时比较必须注明，不能把差异全部归因于提示词。

真实模型前必须先通过工具及暂存确定性/数据库测试。单Agent和全链初始化宜分别新建隔离快照，避免单Agent先创建的Workflow改变全链初始化起点。

当前阶段不提供日程/通知服务给Agent，避免业务动作影响验收；Worker保留记忆写工具。隔离四角色同时移除web_search工具与权限，关联发现只查询冻结授权归档。全链判断不仅看模型Run成功，还检查同课程active Workflow、有来源PRI、实际Worker子调用及后续任务COMPLETED产物。最后仍需人工/代理审阅知识是否覆盖关键课程要求、来源与未知，不把Schema合规当作完全理解课程。

## 安全与保留

resume需要完整冻结manifest，仅接受生成的精确schema，search_path不回退public；不会恢复源采集或重置源Workflow。每次closure写独立attempt目录，0600原始模型请求/响应及private报告保留。命令可重复执行会新增隔离审计，但不会自动清理之前失败或运行结果未知的对象。

工具预算修复、模型轮数/Token/耗时及失败原因记在`.build/agent-closure-change-log.md`，收益只有实测时才标为已验证。
