# agent-plugins

ai-agent 的官方插件集合，覆盖 Pipe（消息处理管道）、Dialog（IM平台）和 LLM 三大类型。

每个插件是独立 Go 模块，通过空导入激活：

```go
import _ "github.com/nuln/agent-plugins/pipes/dedup"
```

---

## Pipe 插件（消息处理管道）

Pipe 按 priority 升序执行，任意 Pipe 返回 `true` 即停止后续处理。

| 插件 | Priority | 模块路径 | 说明 |
|------|----------|----------|------|
| [management](#management) | 30 | `.../pipes/management` | HTTP 管理 API |
| [heartbeat](#heartbeat) | 40 | `.../pipes/heartbeat` | 定时心跳注入 |
| [webhook](#webhook) | 50 | `.../pipes/webhook` | HTTP Webhook 接收 |
| [telemetry](#telemetry) | 100 | `.../pipes/telemetry` | 消息日志记录 |
| [dedup](#dedup) | 200 | `.../pipes/dedup` | 消息去重 |
| [userroles](#userroles) | 250 | `.../pipes/userroles` | 角色限流（手动注册） |
| [ratelimiter](#ratelimiter) | 300 | `.../pipes/ratelimiter` | 全局滑动窗口限流 |
| [langdetector](#langdetector) | 400 | `.../pipes/langdetector` | 语言检测 |
| [redactor](#redactor) | 500 | `.../pipes/redactor` | API Key 等敏感信息脱敏 |
| [contextinjector](#contextinjector) | 600 | `.../pipes/contextinjector` | 注入时间戳等上下文 |
| [guardrail](#guardrail) | 700 | `.../pipes/guardrail` | 危险操作拦截 |
| [router](#router) | 900 | `.../pipes/router` | `/llm <name>` 切换 LLM |
| [command](#command) | 1000 | `.../pipes/command` | 自定义斜杠命令 |

---

### management

HTTP 管理 API 服务，监听在独立端口，提供 status 和 agents 接口。不影响消息链路（passthrough）。

**环境变量：**

| 变量 | 默认值 | 说明 |
|------|--------|------|
| `MANAGEMENT_PORT` | `9820` | HTTP 监听端口 |
| `MANAGEMENT_TOKEN` | — | Bearer Token 认证（不填则无认证） |
| `MANAGEMENT_CORS_ORIGIN` | — | 允许的 CORS Origin，逗号分隔；填 `*` 则全放行；不填则不发 CORS 头 |

**接口：**
```
GET /api/v1/status   → {status, uptime_sec, memory_mb, goroutines}
GET /api/v1/agents   → {agents: [name, ...]}
```

---

### heartbeat

定期向指定 Session 注入 Prompt，触发 LLM 主动响应。不影响消息链路（passthrough）。

**环境变量：**

| 变量 | 默认值 | 说明 |
|------|--------|------|
| `HEARTBEAT_SESSION_KEY` | — | **必填**，目标 Session Key |
| `HEARTBEAT_PROMPT` | `Heartbeat check-in: ...` | 注入的 Prompt 文本 |
| `HEARTBEAT_INTERVAL_MINS` | `30` | 注入间隔（分钟） |

---

### webhook

HTTP Webhook 端点，接收外部事件并注入到 Session。不影响消息链路（passthrough）。

**环境变量：**

| 变量 | 默认值 | 说明 |
|------|--------|------|
| `WEBHOOK_PORT` | `9111` | HTTP 监听端口 |
| `WEBHOOK_TOKEN` | — | 认证 Token（不填则无认证） |
| `WEBHOOK_PATH` | `/hook` | HTTP 路径 |

**请求格式（POST）：**
```json
{
  "session_key": "user:12345:main",
  "prompt": "执行每日报告",
  "event": "scheduled_task"
}
```

**认证方式（任选其一）：**
- `Authorization: Bearer <token>`
- `X-Webhook-Token: <token>`
- `?token=<token>`

---

### telemetry

用 `slog` 记录每条消息的元数据（ID、用户、内容长度、时间）。无需配置，无法拦截消息。

---

### dedup

消息去重，防止 IM 平台重投递或重启时重处理旧消息。

- 基于 `Message.MessageID` 去重（TTL 60秒，后台定期清理）
- 基于 `Message.CreateTime` 过滤进程启动前的消息

无需配置。

---

### userroles

基于角色的细粒度限流和命令限制，**无自动注册**，需在应用代码中配置：

```go
import "github.com/nuln/agent-plugins/pipes/userroles"

mgr := userroles.NewUserRoleManager()
mgr.Configure(
    // 默认角色
    userroles.RoleInput{
        Name:      "default",
        RateLimit: userroles.RateLimitCfg{MaxMessages: 5, Window: time.Minute},
    },
    // 命名角色
    []userroles.RoleInput{
        {
            Name:    "vip",
            UserIDs: []string{"uid_alice"},
            RateLimit: userroles.RateLimitCfg{MaxMessages: 60, Window: time.Minute},
        },
        {
            Name:             "restricted",
            UserIDs:          []string{"uid_bob"},
            DisabledCommands: []string{"reset", "exec"},
            RateLimit:        userroles.RateLimitCfg{MaxMessages: 2, Window: time.Minute},
        },
    },
)
agent.RegisterPipe("userroles", 250, func(pctx agent.PipeContext) agent.Pipe {
    return userroles.NewPipe(pctx, mgr)
})
```

---

### ratelimiter

全局滑动窗口限流，按 UserID 统计。超出限制时回复用户提示。

**环境变量：**

| 变量 | 默认值 | 说明 |
|------|--------|------|
| `RATELIMIT_MAX_MESSAGES` | `20` | 窗口内最大消息数 |
| `RATELIMIT_WINDOW_SECS` | `60` | 滑动窗口时长（秒） |

---

### langdetector

检测用户消息语言（占位实现，可替换为真实检测库）。无需配置，不拦截消息。

---

### redactor

自动脱敏消息中的 API Key（匹配 `sk-*`、`ghp_*`、`gho_*` 等格式），替换为 `[REDACTED]`。无需配置，不拦截消息。

---

### contextinjector

在消息内容前注入当前时间戳 `[Time: 2006-01-02 15:04:05]`。无需配置，不拦截消息。

---

### guardrail

检测危险操作意图（含 "delete all"、"wipe" 等关键词），非 admin 用户需二次确认（回复 YES）。

**环境变量：**

| 变量 | 默认值 | 说明 |
|------|--------|------|
| `GUARDRAIL_ADMINS` | `admin` | admin UserID 列表，逗号分隔 |

---

### router

处理 `/llm <name>` 命令，在运行时切换当前 Session 使用的 LLM。无需配置。

---

### command

自定义斜杠命令框架，支持 `{{1}}`、`{{args}}` 等模板占位符。需要代码方式注册命令：

```go
registry := commandpkg.NewCommandRegistry(safety)
registry.Add(&commandpkg.CustomCommand{
    Name:   "summary",
    Prompt: "请总结以下内容：{{args}}",
})
```

**环境变量（与 guardrail 共享）：**

| 变量 | 默认值 | 说明 |
|------|--------|------|
| `GUARDRAIL_ADMINS` | `admin` | 可执行敏感命令的 admin ID |

---

## Dialog 插件（IM 平台）

### lark（飞书/Lark）

**必填：**
- `LARK_APP_ID` / opts `app_id`
- `LARK_APP_SECRET` / opts `app_secret`

**可选（opts）：**
| Key | 默认 | 说明 |
|-----|------|------|
| `reaction_emoji` | `OnIt` | 收到消息时的 Emoji 反应，填 `none` 禁用 |
| `allow_from` | — | 允许的用户/群组 ID，逗号分隔 |
| `group_reply_all` | `false` | 是否回复群组中的所有消息 |
| `share_session_in_channel` | `false` | 群组内共享会话 |
| `reply_in_thread` | `false` | 以 Thread 形式回复 |
| `enable_feishu_card` | `true` | 使用交互式卡片消息 |

---

### telegram

**必填：**
- `TELEGRAM_TOKEN` / opts `token`

**可选：**
| 变量/Key | 说明 |
|----------|------|
| `TELEGRAM_ALLOW_USER_IDS` / `allow_from` | 允许的 Telegram User ID，逗号分隔 |
| `proxy` | HTTP 代理地址（如 `http://127.0.0.1:7890`） |
| `proxy_username` / `proxy_password` | 代理认证 |
| `group_reply_all` | 回复群内所有消息 |
| `share_session_in_channel` | 频道内共享会话 |

---

## LLM 插件

### claudecode（Claude Code CLI）

依赖 `claude` CLI 已安装并在 PATH 中。

**认证：**
- `ANTHROPIC_API_KEY` / opts `api_key`
- `ANTHROPIC_BASE_URL`（默认 `https://api.anthropic.com`）

**可选 opts：**
| Key | 说明 |
|-----|------|
| `work_dir` | Claude 工作目录（默认 `.`） |
| `model` | 模型名（如 `sonnet`、`opus`、`haiku`） |
| `mode` | 权限模式：`default` \| `acceptEdits` \| `plan` \| `bypassPermissions` |
| `router_url` | Claude Code Router 地址 |
| `router_api_key` | Router API Key |

---

### codex（OpenAI Codex CLI）

依赖 `codex` CLI：`npm install -g @openai/codex`

**认证：**
- `OPENAI_API_KEY` / opts `api_key`
- `OPENAI_BASE_URL`（可选，用于代理）

**可选 opts：**
| Key | 说明 |
|-----|------|
| `work_dir` | 工作目录 |
| `model` | 模型名 |
| `reasoning_effort` | `low` \| `medium` \| `high` \| `xhigh` |
| `mode` | `suggest` \| `auto-edit` \| `full-auto` \| `yolo` |
| `CODEX_HOME` | Codex 配置目录 |

---

### gemini（Google Gemini CLI）

依赖 `gemini` CLI 已安装，或通过 `GEMINI_CMD` 指定路径。

**可选 opts：**
| Key | 说明 |
|-----|------|
| `cmd` / `GEMINI_CMD` | gemini CLI 路径 |
| `work_dir` | 工作目录 |
| `model` | 模型名 |
| `mode` | 执行模式（默认 `plan`） |

---

## 通用配置工具

每个插件都在 `init()` 中注册了配置规格（`RegisterPluginConfigSpec`），可用于：

```go
// 列出所有插件的配置规格
specs := agent.ListPluginConfigSpecs()

// 生成 .env 配置模板
fmt.Print(agent.GenerateEnvTemplate())

// 只生成指定插件的模板
fmt.Print(agent.GenerateEnvTemplate("webhook", "lark", "claudecode"))

// 启动时校验必填配置
if errs := agent.ValidateAllPluginConfigs(nil); len(errs) > 0 {
    for _, e := range errs {
        log.Printf("missing config: %v", e)
    }
}
```
