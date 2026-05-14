# AI Code Review War Room

多模型 MR / PR 审核群机器人。服务接收 GitHub / GitLab webhook，拉取 diff，并发调用多个 AI reviewer，在飞书群里按 reviewer 机器人实时展示讨论过程，达成共识后发布结构化审核结果；未收敛时发布分歧报告。

## 当前能力

- GitHub Pull Request webhook：`opened`、`reopened`、`synchronize`、`ready_for_review`
- GitLab Merge Request webhook：`open`、`reopen`、`update`
- 多 reviewer 并发审核
- 可配置 debate round，让模型看到其他 reviewer 的上一轮结果后重新判断
- 可选 judge reviewer 做最终裁决
- 支持共识模式：每轮后由 judge 判断是否收敛，收敛后再发最终结果
- 支持飞书多机器人事件发布：每个 reviewer 可绑定独立群机器人
- 支持 OpenAI-compatible Chat Completions，例如 Kimi、MiniMax、DeepSeek、OpenRouter 等
- 支持 OpenAI Responses API，例如 Codex 系列模型
- 支持 Anthropic Messages API
- 支持 command reviewer，用于桥接 Codex CLI、Claude Code 或内部 Agent
- 支持 stdout、飞书、企业微信、Slack/通用 webhook 发布
- 内存 job 状态查询

## 快速启动

无 API Key 本地试跑：

```bash
make run-mock
```

真实模型配置：

```bash
export MOONSHOT_API_KEY=xxx
export MINIMAX_API_KEY=xxx
export OPENAI_API_KEY=xxx
export WARROOM_WEBHOOK_SECRET=your-webhook-secret

make run
```

`make run` 默认读取 `configs/config.yaml`。该文件用于本地私有配置，已在 `.gitignore` 中忽略，不要提交真实 webhook 地址或签名密钥。

> 当前机器上使用默认 `CGO_ENABLED=1` 运行 Go 测试/二进制时可能触发 macOS `dyld missing LC_UUID`，使用 `CGO_ENABLED=0` 可以规避。

## HTTP 接口

健康检查：

```bash
curl http://localhost:8080/healthz
```

手动提交一次审核：

```bash
curl -X POST 'http://localhost:8080/reviews/manual?sync=true' \
  -H 'content-type: application/json' \
  -d '{
    "repository": "demo/repo",
    "title": "Fix payment retry",
    "url": "https://example.com/mr/1",
    "source_ref": "feature/retry",
    "target_ref": "main",
    "diff": "diff --git a/main.go b/main.go\n@@\n+func main() {}\n"
  }'
```

异步提交后查询：

```bash
curl -X POST http://localhost:8080/reviews/manual \
  -H 'content-type: application/json' \
  -d '{"repository":"demo/repo","title":"demo","diff":"diff --git a/a.go b/a.go\n+bad"}'

curl http://localhost:8080/jobs/<job_id>
```

Webhook：

```text
POST /webhooks/github
POST /webhooks/gitlab
```

GitHub 使用 `X-Hub-Signature-256` 和 `WARROOM_WEBHOOK_SECRET` 校验。GitLab 使用 `X-Gitlab-Token` 校验。

## 配置模型

共识模式：

```yaml
review:
  consensus_enabled: true
  max_consensus_rounds: 3
  judge_reviewer_id: openai-codex
```

开启后，`judge_reviewer_id` 必填，且需要至少一个非 judge reviewer。每轮 reviewer 讨论完成后，judge 会返回是否已达成共识；达成共识时使用 judge 的 `final_findings` 作为最终结果，未在上限内收敛时生成 `completed_with_disagreements` 报告。

OpenAI-compatible 模型：

```yaml
reviewers:
  - id: kimi
    name: Kimi Reviewer
    type: chat_model
    provider: kimi
    api_style: openai_chat_completions
    base_url: https://api.moonshot.cn/v1
    model: kimi-k2.6
    api_key_env: MOONSHOT_API_KEY
```

OpenAI Responses API：

```yaml
  - id: openai-codex
    name: Codex Reviewer
    type: chat_model
    provider: openai
    api_style: openai_responses
    base_url: https://api.openai.com/v1
    model: gpt-5.1-codex
    api_key_env: OPENAI_API_KEY
```

Command reviewer：

```yaml
  - id: local-agent
    name: Local Agent Reviewer
    type: command
    command: ["./scripts/reviewer-agent"]
    timeout: 180s
```

Command reviewer 会把完整 review prompt 写入 stdin，并期望 stdout 返回如下 JSON：

```json
{
  "summary": "short summary",
  "findings": [
    {
      "severity": "high",
      "type": "bug",
      "file": "src/payment.go",
      "line": 42,
      "title": "retry may double charge",
      "evidence": "the retry path creates a new charge without idempotency key",
      "suggestion": "reuse the original idempotency key",
      "confidence": 0.85
    }
  ]
}
```

## 发布到群

推荐在飞书群里创建这些自定义机器人：

| 中文名称 | 英文标识 | 用途 |
| --- | --- | --- |
| 评审战情室总结 | `feishu-summary` | 发布开始消息和最终结果 |
| Codex 审核员 | `feishu-codex-reviewer` | 发布 Codex reviewer 的每轮观点 |
| Codex 裁判 | `feishu-judge` | 发布 judge 的共识判定 |
| Kimi 审核员 | `feishu-kimi` | 接入 Kimi 后发布 Kimi reviewer 的观点 |
| MiniMax 审核员 | `feishu-minimax` | 接入 MiniMax 后发布 MiniMax reviewer 的观点 |
| Claude 审核员 | `feishu-claude` | 接入 Claude 后发布 Claude reviewer 的观点 |

只有一个 Codex 账号时，先创建前三个：`评审战情室总结`、`Codex 审核员`、`Codex 裁判`。

飞书多机器人共识流：

```yaml
publishers:
  - id: feishu-summary
    type: webhook
    style: feishu
    events:
      - job_started
      - final_report
    webhook_url: https://open.feishu.cn/open-apis/bot/v2/hook/xxx
    sign_secret: optional-sign-secret

  - id: feishu-kimi
    type: webhook
    style: feishu
    events:
      - reviewer_result
    reviewer_id: kimi
    webhook_url: https://open.feishu.cn/open-apis/bot/v2/hook/xxx

  - id: feishu-judge
    type: webhook
    style: feishu
    events:
      - judge_result
    reviewer_id: openai-codex
    webhook_url: https://open.feishu.cn/open-apis/bot/v2/hook/xxx
```

`events` 未配置时默认只发布 `final_report`，兼容旧配置。飞书 `sign_secret` 可选，开启飞书自定义机器人签名校验时填写。

企业微信：

```yaml
publishers:
  - id: wecom
    type: webhook
    style: wecom
    webhook_url: https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=xxx
```

Slack：

```yaml
publishers:
  - id: slack
    type: webhook
    style: slack
    webhook_url: https://hooks.slack.com/services/xxx
```

## 开发验证

```bash
make test
```
