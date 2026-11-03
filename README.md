# switch-monitor (Go)

A single-binary switch port monitor for **Netgear GS108Ev3** and **Mercury SG108 Pro** managed switches.  
Polls port link status and speed, prints a summary table, and sends alerts when a concerned port goes down or its link speed drops below the configured threshold.

Optional integrations: **iKuai** router DNAT control, **Mihomo** (Clash Meta) proxy switching, and **Xiaodu** smart speaker control with alert TTS.

---

## Features

- HTTP scraping of switch web interfaces — no runtime dependencies, just copy the binary
- Supports **Netgear GS108Ev3** (password-only, MD5 hashed auth) and **Mercury SG108 Pro** (username/password)
- Configurable concerned ports, port aliases, and minimum speed threshold
- Transition-based alerting (alerts only on state changes, not every poll)
- Double-confirmation backoff (temporarily shortens polling interval to verify link downgrades before alerting)
- **Multiple email recipients** — send to any number of addresses
- **Multiple Telegram bots/chats** — each with its own token, chat ID, and optional proxy
- SMTP email with port-465 SSL and port-587 STARTTLS support
- Telegram Bot API notifications with optional HTTP proxy support
- **Telegram command support** — trigger checks and control integrations from your bot
- **iKuai integration** — list/add/edit/delete/enable/disable DNAT port-mapping rules (iKuai 3.x and 4.x API compatible)
- **Mihomo (Clash Meta) integration** — list/switch/test proxy latency from Telegram or CLI
- **LLM usage** — DeepSeek balance and 百炼 (DashScope) spend/tokens via Telegram or CLI
- **Xiaodu smart speaker integration** — DLNA playback control, DuerOS TTS/voice commands, alert TTS, online probe, BDUSS health check, cloud IP auto-discovery
- **Xiaodu AI skill** — a DuerOS custom-skill webhook that turns the speaker into a voice front-end for any OpenAI-compatible model, with multi-turn memory
- Optional Google Calendar / Microsoft Outlook repair events for confirmed issues
- Aligned plain-text status table in console output and alert messages
- **Hot config reload** — edits to `config.yaml` are picked up automatically between poll cycles; no restart needed
- Structured log file (`slog`) + optional JSONL history per port check
- `--once` flag for cron/systemd-timer use
- `--no-email` flag to suppress all alerts (useful for testing)
- `--version` prints build version and git commit

---

## Quick start

### Build

```bash
VERSION=$(git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT=$(git rev-parse HEAD)
LDFLAGS="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}"

# Local machine
go build -ldflags "${LDFLAGS}" -o switch-monitor ./cmd/switch-monitor/

# NanoPi R2S or other ARM64 Linux (cross-compile)
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags "${LDFLAGS}" -o switch-monitor-arm64 ./cmd/switch-monitor/
```

Go 1.25 or newer is required (see `go.mod`).

Verify the embedded version:

```bash
./switch-monitor-arm64 --version
```

### Configure

```bash
cp config.example.yaml config.yaml
# Edit config.yaml with your switch IPs, passwords, and alert settings
```

### Run

```bash
# Continuous polling (interval from config, config changes reloaded automatically)
./switch-monitor --config config.yaml

# Single check and exit (for cron)
./switch-monitor --config config.yaml --once

# Suppress all alerts (useful for testing)
./switch-monitor --config config.yaml --no-email
```

### Local subcommands (without running the monitor loop)

Run after global flags such as `--config`:

```bash
switch-monitor --config config.yaml ikuai list-dnat
switch-monitor --config config.yaml ikuai enable-dnat <id>
switch-monitor --config config.yaml ikuai add-dnat <name> <wan_port> <lan_addr> <lan_port> [tcp|udp|tcp+udp] [comment...]
switch-monitor --config config.yaml ikuai edit-dnat <id> wan=8080 comment="new comment"
switch-monitor --config config.yaml ikuai del-dnat <id> [id...]
switch-monitor --config config.yaml mihomo list-proxy
switch-monitor --config config.yaml mihomo set-proxy <name>
switch-monitor --config config.yaml xiaodu status
switch-monitor --config config.yaml xiaodu tts "你好"
switch-monitor --config config.yaml xiaodu probe
switch-monitor --config config.yaml xiaodu discover
switch-monitor --config config.yaml xiaodu skill-test "用一句话介绍你自己"
switch-monitor --config config.yaml xiaodu skill-chat
switch-monitor --config config.yaml xiaodu skill-serve
switch-monitor --config config.yaml xiaodu codebuddy-models
```

See `switch-monitor help` for the full list.

---

## Configuration

All settings live in a single YAML file. See `config.example.yaml` for a fully-annotated example.  
The file is watched for changes between every poll cycle — no restart required to apply edits.

### Switches

| Key | Description |
|-----|-------------|
| `switches[].name` | Display name |
| `switches[].type` | `netgear_gs108ev3` or `mercury_sg108pro` |
| `switches[].admin_url` | e.g. `http://192.168.1.1` |
| `switches[].password` | Admin password |
| `switches[].username` | Mercury only; usually `admin` |
| `switches[].concerned_ports` | List of port numbers to monitor |
| `switches[].port_aliases` | Optional map `{1: "WAN", 2: "NAS"}` shown in tables and alerts |

### Alerting

| Key | Default | Description |
|-----|---------|-------------|
| `alert_emails` | — | List of email recipients (or single `alert_email` for backwards compatibility) |
| `min_speed_mbps` | `1000` | Alert if link speed drops below this value |
| `check_interval_seconds` | `60` | Polling interval in seconds |
| `recheck_interval_seconds` | `5` | Shortened polling interval used to double-confirm pending alerts |

### SMTP

| Key | Default | Description |
|-----|---------|-------------|
| `smtp.enabled` | `false` | Enable email alerts |
| `smtp.smtp_host` | — | e.g. `smtp.qq.com` |
| `smtp.smtp_port` | — | `465` (SSL) or `587` (STARTTLS) |
| `smtp.smtp_use_tls` | `false` | Enable STARTTLS (for port 587) |
| `smtp.from_email` | — | Sender address (overridden by `smtp_user` if set) |
| `smtp.smtp_user` | — | SMTP login username |
| `smtp.smtp_password` | — | SMTP login password |

### Telegram

| Key | Default | Description |
|-----|---------|-------------|
| `telegram.enabled` | `false` | Enable Telegram alerts |
| `telegram.listen_commands` | `false` | Enable polling for `/check` commands |
| `telegram.recipients[].token` | — | Telegram Bot API token |
| `telegram.recipients[].chat_id` | — | Target user or group chat ID |
| `telegram.recipients[].proxy` | — | Optional HTTP proxy, e.g. `http://127.0.0.1:7890` |

A single legacy `token` / `chat_id` / `proxy` directly under `telegram:` is still accepted and treated as a one-item list.

#### Interacting with the Bot

If `telegram.listen_commands` is set to `true`, `switch-monitor` listens for messages sent to the configured bot.  
Commands are only accepted from `chat_id`s listed in `telegram.recipients`.

| Command | Description |
|---------|-------------|
| `/check` | Force an immediate poll of all switches |
| `/list_dnat` | List iKuai port-mapping (DNAT) rules |
| `/enable_dnat <id>` / `/disable_dnat <id>` | Toggle an iKuai DNAT rule (multiple ids allowed) |
| `/add_dnat <name> <wan_port> <lan_addr> <lan_port> [proto] [comment...]` | Create a port-mapping rule; `proto` defaults to `tcp` |
| `/edit_dnat <id> [name=] [wan=] [lan=] [lanport=] [proto=] [comment=]` | Change only the fields you pass |
| `/del_dnat <id> [id...]` | Ask to delete rule(s); confirm with `/del_dnat_confirm <id> ...` |
| `/dnat_help` | Show iKuai command syntax |
| `/list_proxy` | List Mihomo selector outbounds |
| `/set_proxy <name>` | Switch Mihomo selector to `<name>` |
| `/delay_proxy <name>` | Test proxy latency (ms) |
| `/xiaodu_status` | Xiaodu speaker status (volume, playback, DuerOS config) |
| `/xiaodu_volume <0-100>` | Set Xiaodu volume |
| `/xiaodu_mute` / `/xiaodu_unmute` | Mute or unmute |
| `/xiaodu_play <url>` | Play audio URL via DLNA |
| `/xiaodu_stop` / `/xiaodu_pause` | Stop or pause playback |
| `/xiaodu_tts <text>` | Speak text (DuerOS TTS with local fallback) |
| `/xiaodu_say <text>` | Send voice command (e.g. "现在几点了") |
| `/xiaodu_probe` | Check Xiaodu DLNA reachability |
| `/xiaodu_bduss_check` | Validate DuerOS BDUSS credentials |
| `/xiaodu_discover` | Resolve the speaker's current LAN IP from the cloud |
| `/xiaodu_skill_status` | DuerOS AI skill webhook status (listen, model, sessions) |
| `/llm_usage` or `/ai_usage` | DeepSeek balance + 百炼 spend/token usage (live API) |
| `/deepseek_balance` | DeepSeek prepaid balance only |
| `/bailian_usage` | Alibaba 百炼 (DashScope) spend and tokens only |

The bot also registers these commands automatically on startup (`setMyCommands`), so typing `/`
in the chat shows the menu. To set it once by hand with BotFather, use `/setcommands` and paste:

```text
check - Force an immediate poll of all switches
list_dnat - List iKuai port mapping rules
add_dnat - Add iKuai port mapping (use: /add_dnat <name> <wan_port> <lan_addr> <lan_port> [tcp|udp|tcp+udp] [comment])
edit_dnat - Edit iKuai port mapping (use: /edit_dnat <id> wan=8080 comment=new comment)
del_dnat - Delete iKuai port mapping (use: /del_dnat <id> [id...])
del_dnat_confirm - Confirm deletion requested by /del_dnat
dnat_help - Show iKuai port mapping command usage
enable_dnat - Enable an iKuai port mapping rule (use: /enable_dnat <id>)
disable_dnat - Disable an iKuai port mapping rule (use: /disable_dnat <id>)
list_proxy - List Mihomo selector outbounds
set_proxy - Switch Mihomo selector (use: /set_proxy <name>)
delay_proxy - Test proxy latency in ms (use: /delay_proxy <name>)
xiaodu_status - Xiaodu speaker status (volume, playback, DuerOS config)
xiaodu_volume - Set Xiaodu volume (use: /xiaodu_volume <0-100>)
xiaodu_mute - Mute Xiaodu speaker
xiaodu_unmute - Unmute Xiaodu speaker
xiaodu_play - Play audio URL via DLNA (use: /xiaodu_play <url>)
xiaodu_stop - Stop playback
xiaodu_pause - Pause playback
xiaodu_tts - Speak text via DuerOS TTS (use: /xiaodu_tts <text>)
xiaodu_say - Send voice command (e.g. "现在几点了")
xiaodu_probe - Check Xiaodu DLNA reachability
xiaodu_bduss_check - Validate DuerOS BDUSS credentials
xiaodu_discover - Resolve the Xiaodu speaker's current LAN IP from the cloud
xiaodu_skill_status - Show DuerOS AI skill webhook status
llm_usage - Show DeepSeek + 百炼 usage
deepseek_balance - Show DeepSeek balance only
bailian_usage - Show Bailian usage only
```

The menu is defined once in `internal/runner/bot_commands.go`, so the listing above and the API
registration cannot drift apart.

### LLM usage (DeepSeek & 百炼)

Query prepaid balance and spend via provider APIs (requires `telegram.listen_commands: true` for bot commands).

| Key | Default | Description |
|-----|---------|-------------|
| `llm_usage.enabled` | `false` | Enable LLM usage queries |
| `llm_usage.proxy` | — | Optional HTTP proxy for DeepSeek/DashScope API calls |
| `llm_usage.deepseek.enabled` | `false` | Query DeepSeek `GET /user/balance` |
| `llm_usage.deepseek.api_key` | — | DeepSeek API key |
| `llm_usage.bailian.enabled` | `false` | Query DashScope `GET /api/v1/quotas` |
| `llm_usage.bailian.api_key` | — | DashScope (百炼) API key |
| `llm_usage.bailian.api_base` | China default | Override for intl region endpoints |

CLI: `llm usage`, `llm deepseek`, `llm bailian`

DeepSeek does not publish a token-spend history API; the integration reports **remaining balance**.

For 阿里云百炼, switch-monitor queries **Token Plan Personal / Coding Plan** usage windows (**used / remaining / reset time**), not DashScope `/quotas` model rate limits.

| Key | Description |
|-----|-------------|
| `llm_usage.bailian.plan` | `token_plan_personal` (default/auto) or `coding_plan` |
| `llm_usage.bailian.api_key` | Plan API key (`sk-sp-…` / `sk-ws-…`); optional if cookie/`bl` is set |
| `llm_usage.bailian.console_cookie` | Cookie header from Bailian console (required unless using `bl`) |
| `llm_usage.bailian.console_cookie_file` | Path to a file containing that cookie header |
| `llm_usage.bailian.prefer_bl_cli` | Prefer `bl usage` / `bl console call` when Bailian CLI has console login |

Console session is required because Alibaba does not expose used/remaining/reset on the DashScope API Key alone. One-time setup on the host:

```bash
npm install -g bailian-cli
bl auth login --console   # browser login
# then prefer_bl_cli: true
```

Or paste a fresh `Cookie:` from DevTools on the Token Plan page into `console_cookie` / `console_cookie_file`.

### iKuai Router

Manage port-forwarding / port-mapping (DNAT) rules on an iKuai router.
It works with iKuai 3.x and 4.x through the same `/Action/call` API (`func_name: dnat`).
On iKuai OS 4.x the rules live under **策略中心 → NAT → 端口映射**.

| Key | Default | Description |
|-----|---------|-------------|
| `ikuai.enabled` | `false` | Enable iKuai integration |
| `ikuai.url` | — | Router base URL, e.g. `https://192.168.1.1` |
| `ikuai.username` | — | Web admin username |
| `ikuai.password` | — | Web admin password |

CLI: `ikuai list-dnat`, `ikuai enable-dnat <id>`, `ikuai disable-dnat <id>`,
`ikuai add-dnat <name> <wan_port> <lan_addr> <lan_port> [proto] [comment...]`,
`ikuai edit-dnat <id> [name=] [wan=] [lan=] [lanport=] [proto=] [comment=]`, `ikuai del-dnat <id> [id...]`

Port fields accept a single port (`8080`), an inclusive range (`8000-8010`) or a comma-separated list
(`8080,8081`); WAN and LAN specs must cover the same number of ports or the router rejects them.

iKuai's own limits (verified against 4.0.x):

- Rule name ≤ 15 characters, must start with a letter/digit/CJK and may only contain letters, digits,
  CJK, `-` and `_`. Spaces and dots are rejected; the Telegram handler replaces them (spaces become `-`)
  so a chat message does not fail on the name.
- Comment ≤ 64 characters, may contain spaces (the adapter applies iKuai's percent-encoding) but must not
  start with `-` or `_`.
- Protocol must be `tcp`, `udp` or `tcp+udp`; `interface` defaults to `all`.
- Invalid input is validated locally, so a typo produces a readable message instead of
  `3001 请求参数不合法`. Router-side errors (e.g. `端口冲突`) are passed through unchanged.

### Mihomo (Clash Meta)

You can configure multiple Mihomo instances (or the same instance multiple times with different selectors) to change proxies across your network simultaneously.

| Key | Default | Description |
|-----|---------|-------------|
| `mihomo.enabled` | `false` | Enable Mihomo API integration |
| `mihomo.instances[].name` | `mihomo` | Friendly name for the instance/group |
| `mihomo.instances[].api_base` | `http://127.0.0.1:9090` | The external-controller URL |
| `mihomo.instances[].secret` | — | The secret for the API, if configured |
| `mihomo.instances[].selector` | `GLOBAL` | The name of the Proxy Group/Selector you want to control |
| `mihomo.latency_test_url` | gstatic 204 | Probe URL for `/delay_proxy` |
| `mihomo.latency_timeout_ms` | `5000` | Delay test timeout (ms) |

*A legacy single-instance config under `mihomo` directly is also supported.*

CLI: `mihomo list-proxy`, `mihomo set-proxy <name>`, `mihomo delay-proxy <name>`

### Xiaodu Smart Speaker

Control a Xiaodu speaker on the LAN via DLNA; optional DuerOS cloud credentials enable native TTS and voice commands.

| Key | Default | Description |
|-----|---------|-------------|
| `xiaodu.enabled` | `false` | Enable Xiaodu integration |
| `xiaodu.ip` | — | Speaker IP address |
| `xiaodu.port` | `49494` | DLNA port |
| `xiaodu.auto_discover_ip` | `true` | Resolve the speaker's LAN IP from the cloud device list instead of trusting `xiaodu.ip`. The address is DHCP-assigned and moves between reboots, which otherwise breaks DLNA and TTS silently. Requires `bduss`; the device is matched by `cuid`, and any lookup failure falls back to `xiaodu.ip`. |
| `xiaodu.client_id` | — | DuerOS client ID (from xiaodu.baidu.com device list) |
| `xiaodu.cuid` | — | Device CUID |
| `xiaodu.bduss` | — | Baidu BDUSS cookie |
| `xiaodu.scene_id` | — | Scene ID for native TTS (broadcastTTS) |

**Alert TTS** — speak a short summary when confirmed port issues are alerted (after email/Telegram send succeeds):

| Key | Default | Description |
|-----|---------|-------------|
| `xiaodu.alert_tts.mode` | `off` | `off` (disabled), `always`, or `window` (time-limited) |
| `xiaodu.alert_tts.start_time` | `08:00` | Window start (local time) when `mode: window` |
| `xiaodu.alert_tts.end_time` | `22:00` | Window end when `mode: window` |
| `xiaodu.alert_tts.timezone` | `Asia/Shanghai` | Timezone for the alert window |
| `xiaodu.alert_tts.min_issues` | `1` | Minimum confirmed issues before TTS |

**Online probe** — periodic DLNA reachability check:

| Key | Default | Description |
|-----|---------|-------------|
| `xiaodu.probe.enabled` | `false` | Enable periodic probe |
| `xiaodu.probe.interval_seconds` | `600` | Probe interval |
| `xiaodu.probe.notify_telegram` | `true` | Notify on online/offline state change |

**BDUSS check** — periodic DuerOS credential validation:

| Key | Default | Description |
|-----|---------|-------------|
| `xiaodu.bduss_check.enabled` | `false` | Enable periodic BDUSS check |
| `xiaodu.bduss_check.interval_seconds` | `86400` | Check interval |
| `xiaodu.bduss_check.notify_telegram` | `true` | Notify when BDUSS becomes invalid or valid again |

CLI: `xiaodu status`, `xiaodu discover`, `xiaodu volume <n>`, `xiaodu mute`, `xiaodu unmute`, `xiaodu play <url>`, `xiaodu stop`, `xiaodu pause`, `xiaodu seek <pos>`, `xiaodu tts <text>`, `xiaodu say <text>`, `xiaodu probe`, `xiaodu bduss-check`, `xiaodu skill-serve`, `xiaodu skill-test <text>`, `xiaodu skill-chat`, `xiaodu codebuddy-models`

### Xiaodu AI Skill (DuerOS custom skill → your LLM)

Turns the speaker into a voice front-end for your own model. DuerOS pushes each
utterance made inside the skill session to a webhook; the reply goes back as
`PlainText` and the speaker's native TTS reads it aloud.

This is the input half that the speaker has no public API for. Polling a
conversation log — the trick Xiaomi speakers allow — is not possible on Xiaodu:
Baidu exposes no such endpoint, and there is no way to mute the built-in answer,
so a polling bridge would answer twice. A skill session has neither problem.

**One-time setup on <https://dueros.baidu.com/dbp>** (log in with the *same*
Baidu account the speaker is bound to):

1. Create a **自定义技能** (custom skill) and pick a 调用名 (invocation name).
2. Under 意图, reference the system intent **缺省意图**
   (`ai.dueros.common.default_intent`). Without it, NLU rejects free-form
   questions and your webhook never sees them. Optionally add an intent with a
   `query` slot; both forms are handled.
3. Under 配置服务 choose **Web Service** and set the URL to
   `https://<your-public-host>/dueros` (must be HTTPS with a valid certificate).
4. In 模拟测试 enable **真机测试**, then say **“小度小度，打开调试技能模式”** to the
   speaker. Unpublished skills run on your own device in this mode — no review,
   no 开发者认证 needed (certification is only required to publish to the store).
5. Say **“小度小度，打开<调用名>”**, then just talk.

**Verify locally before exposing anything.** `skill-test` drives the real handler
in-process, and `skill-chat` does the same over stdin so multi-turn memory can be
checked; neither needs a public endpoint, a DBP skill, or a reachable speaker:

```bash
switch-monitor --config config.yaml xiaodu skill-test "用一句话介绍你自己"
switch-monitor --config config.yaml xiaodu skill-chat
switch-monitor --config config.yaml xiaodu skill-serve   # foreground webhook
switch-monitor --config config.yaml xiaodu codebuddy-models
```

| Key | Default | Description |
|-----|---------|-------------|
| `xiaodu.skill.enabled` | `false` | Run the webhook |
| `xiaodu.skill.provider` | `openai` | `openai` (any compatible endpoint) or `codebuddy` (Tencent subscription) |
| `xiaodu.skill.listen` | `:9528` | Bind address |
| `xiaodu.skill.path` | `/dueros` | Webhook path; must match the platform config |
| `xiaodu.skill.verify_signature` | `true` | Verify DuerOS's RSA-SHA1 request signature |
| `xiaodu.skill.allowed_skill_ids` | `[]` | Optional `applicationId` allowlist |
| `xiaodu.skill.llm_base_url` | — | OpenAI-compatible API root (ignored for `codebuddy`) |
| `xiaodu.skill.llm_api_key` | — | API key / bearer token (also used by `codebuddy`) |
| `xiaodu.skill.codebuddy_token_file` | — | Explicit path to a bearer-token file (`codebuddy` only) |
| `xiaodu.skill.codebuddy_refresh_token_file` | — | Optional separate refresh-token file (`codebuddy` only) |
| `xiaodu.skill.llm_model` | — | Model id |
| `xiaodu.skill.llm_proxy` | — | Optional HTTP proxy |
| `xiaodu.skill.llm_timeout_seconds` | `60` | Request timeout |
| `xiaodu.skill.system_prompt` | voice-oriented Chinese prompt | System message |
| `xiaodu.skill.max_tokens` | provider default | Completion cap |
| `xiaodu.skill.temperature` | provider default | Sampling temperature |
| `xiaodu.skill.max_reply_chars` | `300` | Spoken answer is truncated to this many runes |
| `xiaodu.skill.history_messages` | `12` | Turns replayed per session |
| `xiaodu.skill.session_ttl_seconds` | `900` | Idle sessions are dropped after this |
| `xiaodu.skill.greeting` | `我在，想问什么？` | Reply to LaunchRequest |
| `xiaodu.skill.farewell` | `好的，再见。` | Reply that ends a session |
| `xiaodu.skill.exit_keywords` | `退出,结束,再见,拜拜,不用了,没事了,bye,exit` | Exact matches (punctuation ignored) end the session |
| `xiaodu.skill.notify_telegram` | `false` | Mirror each voice exchange into the chat |

Notes:

- **Keep `verify_signature` on** for any internet-reachable endpoint. Without it,
  anyone who finds the URL can drive your speaker and spend your LLM quota. The
  check follows Baidu's 技能认证 spec: the certificate URL is pinned to
  `https://duer.bdstatic.com/saiya/flow/` on port 443, the certificate's SAN must
  be `dueros-api.baidu.com`, the signature is RSA-SHA1 over the raw body, and the
  request timestamp must be within 180 s.
- Replies are flattened for speech before being spoken: markdown, links, bullets
  and code fences are stripped, then truncated on a rune boundary. The **full**
  reply is what goes into history, so a follow-up can still refer to content the
  speaker never read aloud.
- History is in-memory per DuerOS session and intentionally not persisted;
  sessions last minutes, so a restart costs nothing but a fresh conversation.
- The skill config is read when the monitor loop starts. Changing `listen` or
  `path` needs a restart (other config sections hot-reload as usual).
- Only one skill can be in 调试技能模式 at a time per personal developer account.

#### CodeBuddy provider (subscription-backed)

Set `xiaodu.skill.provider: codebuddy` to answer through a Tencent CodeBuddy
subscription. The model list reports cost 0 for these models, so voice traffic
is effectively free within the plan.

**Deployment contract:** this binary runs as a standalone systemd service on an
ARM box (r2s). There is no Pi installation, no browser login flow and no
per-user credential store there. Therefore all credentials come from
`config.yaml` — nothing is inferred from `$HOME`, nothing is read from another
tool's files, and **nothing is ever written back to disk**. Use absolute paths
for token files so the unit's `WorkingDirectory` cannot affect resolution.

Choose one credential source (checked in this order):

1. **API key / static bearer token** (simplest for a daemon):

   ```yaml
   xiaodu:
     skill:
       enabled: true
       provider: codebuddy
       llm_api_key: "ck_..."          # API key, or a static access token
       llm_model: deepseek-v4.1-flash  # default if omitted
   ```

2. **Token file** — plain text, or a JSON object with `access` / `refresh` /
   `expires` (epoch ms). The optional refresh file wins over an embedded one:

   ```yaml
   xiaodu:
     skill:
       enabled: true
       provider: codebuddy
       codebuddy_token_file: /home/rhuang/.config/switch-monitor/codebuddy.token
       codebuddy_refresh_token_file: /home/rhuang/.config/switch-monitor/codebuddy.refresh
   ```

`llm_base_url` is optional for CodeBuddy (defaults to
`https://copilot.tencent.com`) and can be overridden for an international
endpoint. Protect token files with `chmod 600` and keep them outside the repo;
the service user only needs read access.

Pick a model and verify the whole chain locally:

```bash
switch-monitor --config config.yaml xiaodu codebuddy-models
switch-monitor --config config.yaml xiaodu skill-test "用一句话介绍你自己"
switch-monitor --config config.yaml xiaodu skill-chat
```

Implementation notes (why this is not just another `llm_base_url`):

- The gateway **rejects non-streaming requests** (`code 11101`), so the client
  requests SSE and aggregates `delta.content` into a complete sentence before
  the speaker reads it aloud. `reasoning_content` (chain-of-thought) is
  deliberately discarded — it must never be spoken.
- The craft-agent model list comes from `GET /v3/config`; the gateway returns
  `agents[].models` as id strings and model metadata separately.
- CodeBuddy occasionally wraps an upstream hiccup as HTTP 400
  `{"code":11133}`. Those are retried with 1s → 4s → 10s → 25s backoff;
  every other 400 is surfaced verbatim.
- Requests carry the VSCode-shaped `X-*` headers the gateway expects.
- If a refresh token is configured, the access token is renewed in memory 5
  minutes before expiry and once more on a mid-flight 401/403. The rotated
  token is **not persisted**: update the configured key/token file yourself
  when it expires. A missing credential produces an error naming the exact
  config keys to set.

### Calendar (optional)

| Key | Default | Description |
|-----|---------|-------------|
| `calendar.enabled` | `false` | Create/update a daytime repair event on confirmed issues |
| `calendar.provider` | — | `google` or `microsoft` |
| `calendar.timezone` | — | IANA timezone, e.g. `America/Los_Angeles` |

See `config.example.yaml` for OAuth fields. Keep `config.yaml` private.

### Logging

| Key | Default | Description |
|-----|---------|-------------|
| `log_dir` | `logs` | Directory for log and history files |
| `log_file` | `switch_monitor.log` | Log filename |
| `history_file` | `switch_monitor_history.jsonl` | JSONL history (set empty to disable) |
| `log_level` | `INFO` | `DEBUG`, `INFO`, `WARN`, or `ERROR` |

---

## Alert message format

Alerts include an aligned status table per switch followed by an issue summary:

```
[Switch Monitor] Summary: 2 issue(s)

Issues: 2

=== mercury-sg108 ===
Port              | Status | Mbps | Tx        | Rx
------------------+--------+------+-----------+-----------
1 · optical modem | UP     | 1000 | 1,234,567 | 9,876,543
2 · IPTV          | DOWN   | -    | -         | -
5 · Mom           | UP     | 100  | 50,000    | 60,000

Issue Details:
  - mercury-sg108 port 2 (IPTV): DOWN
  - mercury-sg108 port 5 (Mom): LOW SPEED (100 Mbps)
```

---

## Systemd service

Create `/etc/systemd/system/switch-monitor.service`:

```ini
[Unit]
Description=Switch Port Monitor
After=network.target

[Service]
ExecStart=/usr/local/bin/switch-monitor --config /etc/switch-monitor/config.yaml
Restart=on-failure

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now switch-monitor
```

Config changes (e.g. adding a Telegram recipient or adjusting the poll interval) are applied automatically without restarting the service.
