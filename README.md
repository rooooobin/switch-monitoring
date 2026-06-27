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
- **iKuai integration** — list/enable/disable DNAT rules (iKuai 3.x and 4.x API compatible)
- **Mihomo (Clash Meta) integration** — list/switch/test proxy latency from Telegram or CLI
- **Xiaodu smart speaker integration** — DLNA playback control, DuerOS TTS/voice commands, alert TTS, online probe, BDUSS health check
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
switch-monitor --config config.yaml mihomo list-proxy
switch-monitor --config config.yaml mihomo set-proxy <name>
switch-monitor --config config.yaml xiaodu status
switch-monitor --config config.yaml xiaodu tts "你好"
switch-monitor --config config.yaml xiaodu probe
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
| `/list_dnat` | List iKuai DNAT rules |
| `/enable_dnat <id>` / `/disable_dnat <id>` | Toggle an iKuai DNAT rule |
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

### iKuai Router

Manage port-forward (DNAT) rules on an iKuai router. Compatible with iKuai 3.x and 4.x `/Action/call` APIs.

| Key | Default | Description |
|-----|---------|-------------|
| `ikuai.enabled` | `false` | Enable iKuai integration |
| `ikuai.url` | — | Router base URL, e.g. `https://192.168.1.1` |
| `ikuai.username` | — | Web admin username |
| `ikuai.password` | — | Web admin password |

CLI: `ikuai list-dnat`, `ikuai enable-dnat <id>`, `ikuai disable-dnat <id>`

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

CLI: `xiaodu status`, `xiaodu volume <n>`, `xiaodu mute`, `xiaodu unmute`, `xiaodu play <url>`, `xiaodu stop`, `xiaodu pause`, `xiaodu seek <pos>`, `xiaodu tts <text>`, `xiaodu say <text>`, `xiaodu probe`, `xiaodu bduss-check`

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
