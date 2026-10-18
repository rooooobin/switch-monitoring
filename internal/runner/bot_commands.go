package runner

import (
	"context"
	"log/slog"
	"strings"

	"switch-monitor/internal/telegram"
)

// TelegramCommands is the bot command menu registered with setMyCommands and
// shown in the README as a BotFather /setcommands block.
//
// Keep the order stable: DNAT commands are grouped, then proxy, xiaodu, LLM.
// Telegram requires lowercase names (a-z, 0-9, underscore, max 32 chars) and
// descriptions of at most 256 characters.
func TelegramCommands() []telegram.BotCommand {
	return []telegram.BotCommand{
		{Command: "check", Description: "Force an immediate poll of all switches"},
		{Command: "list_dnat", Description: "List iKuai port mapping rules"},
		{Command: "add_dnat", Description: "Add iKuai port mapping (use: /add_dnat <name> <wan_port> <lan_addr> <lan_port> [tcp|udp|tcp+udp] [comment])"},
		{Command: "edit_dnat", Description: "Edit iKuai port mapping (use: /edit_dnat <id> wan=8080 comment=new comment)"},
		{Command: "del_dnat", Description: "Delete iKuai port mapping (use: /del_dnat <id> [id...])"},
		{Command: "del_dnat_confirm", Description: "Confirm deletion requested by /del_dnat"},
		{Command: "dnat_help", Description: "Show iKuai port mapping command usage"},
		{Command: "enable_dnat", Description: "Enable an iKuai port mapping rule (use: /enable_dnat <id>)"},
		{Command: "disable_dnat", Description: "Disable an iKuai port mapping rule (use: /disable_dnat <id>)"},
		{Command: "list_proxy", Description: "List Mihomo selector outbounds"},
		{Command: "set_proxy", Description: "Switch Mihomo selector (use: /set_proxy <name>)"},
		{Command: "delay_proxy", Description: "Test proxy latency in ms (use: /delay_proxy <name>)"},
		{Command: "xiaodu_status", Description: "Xiaodu speaker status (volume, playback, DuerOS config)"},
		{Command: "xiaodu_volume", Description: "Set Xiaodu volume (use: /xiaodu_volume <0-100>)"},
		{Command: "xiaodu_mute", Description: "Mute Xiaodu speaker"},
		{Command: "xiaodu_unmute", Description: "Unmute Xiaodu speaker"},
		{Command: "xiaodu_play", Description: "Play audio URL via DLNA (use: /xiaodu_play <url>)"},
		{Command: "xiaodu_stop", Description: "Stop playback"},
		{Command: "xiaodu_pause", Description: "Pause playback"},
		{Command: "xiaodu_tts", Description: "Speak text via DuerOS TTS (use: /xiaodu_tts <text>)"},
		{Command: "xiaodu_say", Description: "Send voice command (e.g. \"现在几点了\")"},
		{Command: "xiaodu_probe", Description: "Check Xiaodu DLNA reachability"},
		{Command: "xiaodu_bduss_check", Description: "Validate DuerOS BDUSS credentials"},
		{Command: "llm_usage", Description: "Show DeepSeek + 百炼 usage"},
		{Command: "deepseek_balance", Description: "Show DeepSeek balance only"},
		{Command: "bailian_usage", Description: "Show Bailian usage only"},
	}
}

// TelegramCommandsBotFather renders the menu in BotFather /setcommands format
// ("command - description", one per line).
func TelegramCommandsBotFather() string {
	var sb strings.Builder
	for _, c := range TelegramCommands() {
		sb.WriteString(c.Command)
		sb.WriteString(" - ")
		sb.WriteString(c.Description)
		sb.WriteString("\n")
	}
	return sb.String()
}

// syncTelegramCommands registers the command menu on every configured bot.
// Failures are logged and never abort the monitor loop.
func (r *Runner) syncTelegramCommands(ctx context.Context) {
	if r.cfg.Telegram == nil || !r.cfg.Telegram.Enabled || !r.cfg.Telegram.ListenCommands {
		return
	}
	commands := TelegramCommands()
	seen := make(map[string]bool, len(r.cfg.Telegram.Recipients))
	for _, rcpt := range r.cfg.Telegram.Recipients {
		if rcpt.Token == "" || seen[rcpt.Token] {
			continue
		}
		seen[rcpt.Token] = true
		client, err := telegram.NewClient(rcpt.Token, rcpt.Proxy)
		if err != nil {
			slog.Warn("Telegram command menu skipped: client init failed", "error", err)
			continue
		}
		if err := client.SetMyCommands(ctx, commands); err != nil {
			slog.Warn("Failed to register Telegram command menu", "error", err)
			continue
		}
		slog.Info("Registered Telegram command menu", "commands", len(commands))
	}
}
