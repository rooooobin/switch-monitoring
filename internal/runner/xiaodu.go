package runner

import (
	"context"
	"fmt"
	"html"
	"log/slog"
	"strconv"
	"strings"

	"switch-monitor/internal/adapter"
	"switch-monitor/internal/config"
	"switch-monitor/internal/telegram"
)

func isXiaoduCommand(text string) bool {
	return strings.HasPrefix(text, "/xiaodu_")
}

func newXiaoduClient(cfg *config.XiaoduConfig) *adapter.XiaoduClient {
	return adapter.NewXiaoduClient(cfg.IP, cfg.PortOrDefault(), adapter.XiaoduDuerOSConfig{
		ClientID: cfg.ClientID,
		CUID:     cfg.CUID,
		BDUSS:    cfg.BDUSS,
		SceneID:  cfg.SceneID,
	})
}

func (r *Runner) handleXiaoduTelegram(ctx context.Context, client *telegram.Client, chatIDStr, text string, chatID int64) {
	if r.cfg.Xiaodu == nil || !r.cfg.Xiaodu.Enabled {
		_ = client.SendMessage(ctx, chatIDStr, "⚠️ Xiaodu integration is not enabled in config.")
		return
	}
	if strings.TrimSpace(r.cfg.Xiaodu.IP) == "" {
		_ = client.SendMessage(ctx, chatIDStr, "⚠️ Xiaodu ip is not configured.")
		return
	}

	c := newXiaoduClient(r.cfg.Xiaodu)

	switch {
	case text == "/xiaodu_status" || strings.HasPrefix(text, "/xiaodu_status "):
		st, err := c.GetStatus(ctx)
		if err != nil {
			_ = client.SendMessage(ctx, chatIDStr, "❌ Xiaodu status failed: "+err.Error())
			return
		}
		msg := "🔊 <b>Xiaodu Status</b>\n<pre>" + html.EscapeString(FormatXiaoduStatus(st)) + "</pre>"
		_ = client.SendMessageHTML(ctx, chatIDStr, msg)

	case strings.HasPrefix(text, "/xiaodu_volume"):
		arg := strings.TrimSpace(strings.TrimPrefix(text, "/xiaodu_volume"))
		if arg == "" {
			_ = client.SendMessage(ctx, chatIDStr, "⚠️ Usage: /xiaodu_volume <0-100>")
			return
		}
		v, err := strconv.Atoi(arg)
		if err != nil {
			_ = client.SendMessage(ctx, chatIDStr, "❌ Invalid volume: "+arg)
			return
		}
		got, err := c.SetVolume(ctx, v)
		if err != nil {
			_ = client.SendMessage(ctx, chatIDStr, "❌ Set volume failed: "+err.Error())
			return
		}
		slog.Info("Xiaodu volume set via Telegram", "chat_id", chatID, "volume", got)
		_ = client.SendMessage(ctx, chatIDStr, fmt.Sprintf("✅ Volume set to %d.", got))

	case text == "/xiaodu_mute":
		if err := c.SetMute(ctx, true); err != nil {
			_ = client.SendMessage(ctx, chatIDStr, "❌ Mute failed: "+err.Error())
			return
		}
		_ = client.SendMessage(ctx, chatIDStr, "✅ Muted.")

	case text == "/xiaodu_unmute":
		if err := c.SetMute(ctx, false); err != nil {
			_ = client.SendMessage(ctx, chatIDStr, "❌ Unmute failed: "+err.Error())
			return
		}
		_ = client.SendMessage(ctx, chatIDStr, "✅ Unmuted.")

	case strings.HasPrefix(text, "/xiaodu_play"):
		arg := strings.TrimSpace(strings.TrimPrefix(text, "/xiaodu_play"))
		if arg == "" {
			_ = client.SendMessage(ctx, chatIDStr, "⚠️ Usage: /xiaodu_play <url>")
			return
		}
		if err := c.PlayURL(ctx, arg, "Audio"); err != nil {
			_ = client.SendMessage(ctx, chatIDStr, "❌ Play failed: "+err.Error())
			return
		}
		_ = client.SendMessage(ctx, chatIDStr, "✅ Playing: "+arg)

	case text == "/xiaodu_stop":
		if err := c.Stop(ctx); err != nil {
			_ = client.SendMessage(ctx, chatIDStr, "❌ Stop failed: "+err.Error())
			return
		}
		_ = client.SendMessage(ctx, chatIDStr, "✅ Stopped.")

	case text == "/xiaodu_pause":
		if err := c.Pause(ctx); err != nil {
			_ = client.SendMessage(ctx, chatIDStr, "❌ Pause failed: "+err.Error())
			return
		}
		_ = client.SendMessage(ctx, chatIDStr, "✅ Paused.")

	case strings.HasPrefix(text, "/xiaodu_tts"):
		arg := strings.TrimSpace(strings.TrimPrefix(text, "/xiaodu_tts"))
		if arg == "" {
			_ = client.SendMessage(ctx, chatIDStr, "⚠️ Usage: /xiaodu_tts <text>")
			return
		}
		used, err := c.TTS(ctx, arg)
		if err != nil {
			_ = client.SendMessage(ctx, chatIDStr, "❌ TTS failed: "+err.Error())
			return
		}
		slog.Info("Xiaodu TTS via Telegram", "chat_id", chatID, "mode", used)
		_ = client.SendMessage(ctx, chatIDStr, fmt.Sprintf("✅ TTS sent (%s).", used))

	case strings.HasPrefix(text, "/xiaodu_say"):
		arg := strings.TrimSpace(strings.TrimPrefix(text, "/xiaodu_say"))
		if arg == "" {
			_ = client.SendMessage(ctx, chatIDStr, "⚠️ Usage: /xiaodu_say <command>")
			return
		}
		used, err := c.Say(ctx, arg)
		if err != nil {
			_ = client.SendMessage(ctx, chatIDStr, "❌ Say failed: "+err.Error())
			return
		}
		slog.Info("Xiaodu say via Telegram", "chat_id", chatID, "mode", used)
		_ = client.SendMessage(ctx, chatIDStr, fmt.Sprintf("✅ Voice command sent (%s).", used))

	case text == "/xiaodu_probe":
		if err := RunXiaoduProbeNow(ctx, r.cfg.Xiaodu); err != nil {
			_ = client.SendMessage(ctx, chatIDStr, "❌ Probe failed: "+err.Error())
			return
		}
		_ = client.SendMessage(ctx, chatIDStr, "✅ Xiaodu speaker is online.")

	case text == "/xiaodu_bduss_check":
		if err := RunXiaoduBDUSSCheckNow(ctx, r.cfg.Xiaodu); err != nil {
			_ = client.SendMessage(ctx, chatIDStr, "❌ BDUSS check failed: "+err.Error())
			return
		}
		_ = client.SendMessage(ctx, chatIDStr, "✅ DuerOS BDUSS is valid.")

	default:
		_ = client.SendMessage(ctx, chatIDStr, "⚠️ Unknown Xiaodu command. Try /xiaodu_status, /xiaodu_volume, /xiaodu_tts, /xiaodu_say, /xiaodu_play, /xiaodu_stop, /xiaodu_pause, /xiaodu_mute, /xiaodu_unmute, /xiaodu_probe, /xiaodu_bduss_check")
	}
}
