# 回环接口

桌面应用在 `127.0.0.1:37913` 提供只监听回环地址的控制接口，供 `opendl-cli` 与浏览器原生主机使用。所有请求需要请求头 `X-OpenDL-Token`，令牌为安装级随机值，保存在配置目录的 `api-token`（`%AppData%/opendl` 或 `~/.config/opendl`），文件权限仅属主可读。接口不接受跨源页面调用；令牌等同于本机操作权限，不要复制到其他机器或贴进工单。

## 端点

| 方法 | 路径 | 请求体 | 成功响应 |
| --- | --- | --- | --- |
| GET | /api/ping | 无 | `{"ok": true, "capabilities": [...]}` |
| POST | /api/download | 下载请求 JSON | `{"id": "<任务编号>"}` |
| GET | /api/tasks | 无 | 任务快照数组 |
| POST | /api/cancel | `{"id": "..."}` | `{"ok": true}` |
| POST | /api/resume | `{"id": "..."}` | `{"ok": true/false}` |
| POST | /api/finish-live | `{"id": "..."}` | `{"ok": true/false}`；请求结束直播并发布 |
| POST | /api/pause | `{"id": "..."}` | `{"ok": true}` |
| POST | /api/start-all | 无 | `{"count": N}` |
| POST | /api/pause-all | 无 | `{"count": N}` |

下载请求 JSON 与任务的 `Request` 字段一致：`url`（必填，HTTP/HTTPS/FTP/FTPS）、`name`、`directory`、`queue`（空主队列、`night`、自定义 ID）、`pageUrl`、`headers`（对象）、`sha256`、`syncMinutes`、`mediaMode`（`hls-ts`/`dash-fmp4`）、`mediaQuality`、`audioLanguage`、`clientRequestId`（幂等去重编号）、`requireSignature`、`signature`、`signatureKey`。所有可选字段在服务端逐项规范化，非法值返回 400 与原因文本。

## 错误与语义

- 401：令牌缺失或不匹配（恒定时间比较）。
- 400：请求格式、地址、请求头、摘要、同步周期、媒体选项、队列或提交编号非法，响应为原因文本。
- 409：`clientRequestId` 与既有提交的内容指纹不一致（幂等冲突）。
- 503 `{"code": "journal_write_failed", "accepted": false}`：任务记录落盘失败，未接受。
- 503 `{"code": "update_in_progress", "accepted": false}`：应用冻结任务调度准备更新，稍后重试。
- `/api/resume` 的 `ok: false`：任务不可继续（编辑/校验中、夜间等待、需要重新登录等）。
- `/api/finish-live` 的 `ok: true` 仅表示已接受结束请求；任务会完成当前批次并异步混流、校验和发布，最终结果应通过 `/api/tasks` 查看。任务快照的 `LiveRecording` 表示可发起结束请求，`FinishingLive` 表示正在收尾。任务未在录制或请求已提交时返回 `ok: false`。`ping` 的 `finish-live` 能力表示接口可用。

批量控制的语义与界面一致：`start-all` 继续全部可恢复任务（不含夜间等待），`pause-all` 停止运行与排队任务并保留可恢复状态；两者返回实际处理数量。

## 示例

```sh
TOKEN=$(cat ~/.config/opendl/api-token)
curl -s -H "X-OpenDL-Token: $TOKEN" http://127.0.0.1:37913/api/ping
curl -s -X POST -H "X-OpenDL-Token: $TOKEN" -H "Content-Type: application/json" \
  -d '{"url":"https://example.com/f.zip","queue":"night"}' \
  http://127.0.0.1:37913/api/download
```

命令行等价物：`opendl-cli ping`、`opendl-cli add https://example.com/f.zip --queue night`、`opendl-cli finish-live 任务编号`。接口版本随应用发布，新字段只增不改；`capabilities` 列出当前实例已声明的能力：`scheduled-queue`（计划队列）、`idempotent-submission`（幂等提交）、`media-hls`/`media-dash`（媒体模式）、`media-quality`/`media-audio-language`（选轨）、`finish-live`（直播收尾发布）、`task-control`（继续/暂停与批量控制）。旧组件据此明确拒绝不支持的选项；原生主机另声明 `site-rules`（规则下发），能力集合按各自职责划分。
