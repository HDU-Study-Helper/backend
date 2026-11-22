# App 对话入口（后端；前端占位）

用户主动发消息才运行 Chat，不订阅采集流。可在同一后端的 `/admin/chat` 使用当前登录账号测试真实对话，回答 ChB 追问，并通过会话链接刷新恢复。打开页面本身不建会话、不调模型；APP 客户端未改动。以下接口均在 `/api/v1/chat` 下，使用应用会话及同源 Origin/CSRF；无需、也不接受用户 ID、Agent ID、角色、Workflow ID 或恢复用的历史消息。

| 方法/路径 | 含义 |
|---|---|
| `POST /threads` | 在当前账号创建会话；返回 `thread_id`。不调用模型。 |
| `GET /threads/{thread}` | 最近的对话记录和服务器保存的当前待答问题。 |
| `POST /threads/{thread}/messages` | `{ "request_id":"客户端生成的唯一请求标识", "message":"我这周有哪些作业？" }`。新任务取消同会话旧的待答 ChB 接续，已提交工作不撤销。 |
| `POST /threads/{thread}/answers` | `{ "request_id":"新的请求标识", "question_id":"服务器返回的标识", "message":"周四" }`。接续原问题；不新建一个普通任务。 |
| `GET /silent-questions` | 仅列本账号 ChA 待答问题（包括题面、选项、问题标识、来源键）。没有 Chat 中转。 |
| `POST /silent-questions/{id}/answers` | `{ "source_key":"问题返回的来源键", "answer":"是" }`。直接交回提出问题的 Agent。校验账号、渠道和原任务；不进入 Chat 消息历史。 |
| `GET /todos`、`GET /notices` | 列出当前账号内共享待办、已记录通知。不是外部推送回执，也没有 App 计划任务管理接口。 |

消息与答复串行处理同一会话；相同 request_id 和内容读回同一结果，运行中重复提交不重入模型。相同键换内容冲突。完成输出为 `{text, cards, interaction}`。`interaction` 只能有一个权威问题，ID/选项以持久化对象为准；并行出现多个问题可用待答列表。卡片仅是 JSON 展示占位，不应作为可执行 HTML 插入 DOM。新消息携带近期会话上下文，正式工作续接走宿主保存的任务记录，不让客户端提交模型历史，也不把私人子 Agent 轨迹返回前端。

## 角色和产物边界

- Chat 在本用户范围读写公开/自身私有记忆、查公开待办、搜索公开网页、读取流程定义、派发 Worker 或 Learner。自己不绑定 Workflow；公开数据不跨用户。所有课程的持续提醒需求应引用真实用户消息，保存个人公开偏好并立即派适用 Worker 核对已有任务，不单靠未来新通知。
- Worker 的真实写入：公开 TASK、立即/定时 NOTICE、SCHEDULE、PLANNED_JOB；如已存在则按证据更新。Question 放 `interaction`，ChB 给用户的文字放 `text`，不由模型编造送达状态。主动/静默由模型依据用户意愿选，和 ChA/ChB 无关；ChA 问题落静默通知，ChB 直接回对话。
- Learner 的已核实修改和待答问题可以同次提交；恢复重新核验目标和版本。新运行使用已有回执、最新授权和服务器保存的逻辑上下文，不重放已执行的工具。用户不回答时不占连接、不设十分钟等待。
- 定时执行工具范围、每周重复、结果/失败回执与恢复边界见 [计划任务](../actions/README.md)。前端仍是演示占位，没有接入设备投递或计划任务编辑。
