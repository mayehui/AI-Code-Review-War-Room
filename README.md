# AI Code Review War Room

多模型 MR / PR 审核群机器人。服务接收 GitHub / GitLab webhook，拉取 diff，并发调用多个 AI reviewer，经过可选的二轮互评和最终聚合后，把结构化结果发布到群机器人或标准输出。

## 当前能力

- GitHub Pull Request webhook：`opened`、`reopened`、`synchronize`、`ready_for_review`
- GitLab Merge Request webhook：`open`、`reopen`、`update`
- 多 reviewer 并发审核
- 可配置 debate round，让模型看到其他 reviewer 的上一轮结果后重新判断
- 可选 judge reviewer 做最终裁决
- 支持 OpenAI-compatible Chat Completions，例如 Kimi、MiniMax、DeepSeek、OpenRouter 等
- 支持 OpenAI Responses API，例如 Codex 系列模型
- 支持 Anthropic Messages API
- 支持 command reviewer，用于桥接 Codex CLI、Claude Code 或内部 Agent
- 支持 stdout、飞书、企业微信、Slack/通用 webhook 发布
- 内存 job 状态查询

## 快速启动

无 API Key 本地试跑：

```bash
CGO_ENABLED=0 go run ./cmd/warroom -config configs/config.mock.yaml
```

真实模型配置：

```bash
export MOONSHOT_API_KEY=xxx
export MINIMAX_API_KEY=xxx
export OPENAI_API_KEY=xxx
export WARROOM_WEBHOOK_SECRET=your-webhook-secret

CGO_ENABLED=0 go run ./cmd/warroom -config configs/config.example.yaml
```

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

飞书：

```yaml
publishers:
  - id: feishu
    type: webhook
    style: feishu
    webhook_url: https://open.feishu.cn/open-apis/bot/v2/hook/xxx
```

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
CGO_ENABLED=0 go test ./...
```
