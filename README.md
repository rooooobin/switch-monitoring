# switch-monitor (Go)

A single-binary switch port monitor for **Netgear GS108Ev3** and **Mercury SG108 Pro** managed switches.  
Polls port link status and speed, prints a summary table, and sends alerts when a concerned port goes down or its link speed drops below the configured threshold.

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
- Aligned plain-text status table in console output and alert messages
- **Hot config reload** — edits to `config.yaml` are picked up automatically between poll cycles; no restart needed
- Structured log file (`slog`) + optional JSONL history per port check
- `--once` flag for cron/systemd-timer use
- `--no-email` flag to suppress all alerts (useful for testing)

---

## Quick start

### Build

```bash
# For the local machine (x86-64)
go build -o switch-monitor ./cmd/switch-monitor/

# For NanoPi R2S or other ARM64 Linux (cross-compile from any machine)
GOOS=linux GOARCH=arm64 go build -o switch-monitor-arm64 ./cmd/switch-monitor/
```

Go 1.22 or newer is required.

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
| `telegram.recipients[].token` | — | Telegram Bot API token |
| `telegram.recipients[].chat_id` | — | Target user or group chat ID |
| `telegram.recipients[].proxy` | — | Optional HTTP proxy, e.g. `http://127.0.0.1:7890` |

A single legacy `token` / `chat_id` / `proxy` directly under `telegram:` is still accepted and treated as a one-item list.

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
