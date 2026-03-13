# switch-monitor (Go)

A single-binary switch port monitor for **Netgear GS108Ev3** and **Mercury SG108 Pro** managed switches.  
Polls port link status and speed, prints a summary table, and sends email alerts when a concerned port goes down or its link speed drops below the configured threshold.

This is a Go rewrite of the Python implementation with zero runtime dependencies — just copy the binary.

---

## Features

- HTTP scraping of switch web interfaces (no third-party libraries required at runtime)
- Supports Netgear GS108Ev3 (password-only, MD5 hashed auth) and Mercury SG108 Pro (username/password)
- Configurable concerned ports, port aliases, and minimum speed threshold
- Transition-based alerting (emails only on state changes, not every poll)
- SMTP email with port-465 SSL and port-587 STARTTLS support
- Per-switch ASCII status tables in console output and email body
- Structured log file (`slog`) + optional JSONL history per port check
- `--once` flag for cron/systemd-timer use

---

## Quick start

### Build

```bash
# For the local machine (x86-64)
go build -o switch-monitor ./cmd/switch-monitor/

# For NanoPi R2S / other ARM64 Linux (cross-compile from any machine)
GOOS=linux GOARCH=arm64 go build -o switch-monitor-arm64 ./cmd/switch-monitor/
```

Go 1.22 or newer is required.

### Configure

```bash
cp config.example.yaml config.yaml
# Edit config.yaml with your switch IPs, passwords, and email settings
```

### Run

```bash
# Continuous polling (interval from config)
./switch-monitor --config config.yaml

# Single check (for cron)
./switch-monitor --config config.yaml --once
```

---

## Configuration

All settings are in a single YAML file.  See `config.example.yaml` for a fully-annotated example.

| Key | Default | Description |
|-----|---------|-------------|
| `switches[].name` | — | Display name |
| `switches[].type` | — | `netgear_gs108ev3` or `mercury_sg108pro` |
| `switches[].admin_url` | — | `http://192.168.x.x` |
| `switches[].password` | — | Admin password |
| `switches[].username` | — | Mercury only; usually `admin` |
| `switches[].concerned_ports` | — | List of port numbers to monitor |
| `switches[].port_aliases` | — | Optional map: `{1: "Server", 2: "NAS"}` |
| `min_speed_mbps` | `1000` | Alert if link speed drops below this |
| `alert_email` | — | Recipient address |
| `check_interval_seconds` | `60` | Polling interval |
| `smtp.enabled` | `true` | Enable SMTP email alerts |
| `smtp.smtp_host` | — | e.g. `smtp.qq.com` |
| `smtp.smtp_port` | — | `465` (SSL) or `587` (STARTTLS) |
| `smtp.smtp_use_tls` | `true` | Enable STARTTLS for non-465 ports |
| `smtp.from_email` | — | Sender address (overridden by `smtp_user` if set) |
| `smtp.smtp_user` | — | SMTP login username |
| `smtp.smtp_password` | — | SMTP login password |
| `telegram.enabled` | `false` | Enable Telegram bot alerts |
| `telegram.token` | — | Telegram Bot API token |
| `telegram.chat_id` | — | Telegram user or group chat ID |
| `telegram.proxy` | — | Optional HTTP proxy (e.g. `http://127.0.0.1:1080`) |
| `log_dir` | `logs` | Directory for log and history files |
| `log_file` | `switch_monitor.log` | Log filename |
| `history_file` | `switch_monitor_history.jsonl` | JSONL history file (set empty to disable) |
| `log_level` | `INFO` | `DEBUG`, `INFO`, `WARN`, or `ERROR` |

---

## Systemd service (optional)

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
