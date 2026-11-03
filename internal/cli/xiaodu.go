package cli

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"switch-monitor/internal/adapter"
	"switch-monitor/internal/config"
	"switch-monitor/internal/runner"
)

func newXiaoduClient(cfg *config.XiaoduConfig) *adapter.XiaoduClient {
	return adapter.NewXiaoduClient(cfg.IP, cfg.PortOrDefault(), adapter.XiaoduDuerOSConfig{
		ClientID: cfg.ClientID,
		CUID:     cfg.CUID,
		BDUSS:    cfg.BDUSS,
		SceneID:  cfg.SceneID,
	})
}

// RunXiaodu handles local Xiaodu speaker commands.
func RunXiaodu(ctx context.Context, cfg *config.MonitorConfig, args []string) error {
	if cfg.Xiaodu == nil || !cfg.Xiaodu.Enabled {
		slog.Error("CLI xiaodu: integration disabled in config")
		return fmt.Errorf("xiaodu is not enabled in config")
	}
	if len(args) < 1 {
		return fmt.Errorf("xiaodu: expected subcommand (status, discover, volume, mute, unmute, play, stop, pause, seek, tts, say, probe, bduss-check, skill-serve, skill-test, skill-chat, codebuddy-models)")
	}

	// The AI skill webhook only needs cloud and LLM access, so it is dispatched
	// before the speaker address is resolved: it must stay testable while the
	// device itself is offline.
	if IsXiaoduSkillSubcommand(args[0]) {
		switch args[0] {
		case "skill-serve":
			return RunXiaoduSkillServe(ctx, cfg)
		case "skill-test":
			return RunXiaoduSkillTest(ctx, cfg, args[1:])
		case "skill-chat":
			return RunXiaoduSkillChat(ctx, cfg)
		case "codebuddy-models":
			return RunXiaoduCodeBuddyModels(ctx, cfg)
		}
	}

	if strings.TrimSpace(cfg.Xiaodu.IP) == "" && !cfg.Xiaodu.AutoDiscoverIPOrDefault() {
		return fmt.Errorf("xiaodu ip is required in config (or enable auto_discover_ip with bduss)")
	}

	// bduss-check and discover only talk to the cloud, so they must keep working
	// while the speaker itself is unreachable.
	if args[0] == "bduss-check" {
		if err := newXiaoduClient(cfg.Xiaodu).CheckBDUSS(ctx); err != nil {
			return fmt.Errorf("bduss check failed: %w", err)
		}
		fmt.Println("DuerOS BDUSS is valid.")
		return nil
	}
	if args[0] == "discover" {
		dev, err := runner.DiscoverXiaoduNow(ctx, cfg.Xiaodu)
		if err != nil {
			return fmt.Errorf("discovery failed: %w", err)
		}
		fmt.Println(runner.FormatXiaoduDevice(dev, cfg.Xiaodu.IP))
		return nil
	}

	c, err := runner.XiaoduClientFor(ctx, cfg.Xiaodu)
	if err != nil {
		return fmt.Errorf("cannot resolve xiaodu speaker: %w", err)
	}
	slog.Info("CLI xiaodu command", "subcommand", args[0], "args", args[1:], "ip", c.IP())

	switch args[0] {
	case "status":
		st, err := c.GetStatus(ctx)
		if err != nil {
			return err
		}
		fmt.Println(runner.FormatXiaoduStatus(st))
		return nil

	case "volume":
		if len(args) < 2 {
			return fmt.Errorf("volume: missing level 0-100")
		}
		v, err := strconv.Atoi(args[1])
		if err != nil {
			return fmt.Errorf("invalid volume %q", args[1])
		}
		got, err := c.SetVolume(ctx, v)
		if err != nil {
			return err
		}
		fmt.Printf("Volume set to %d.\n", got)
		return nil

	case "mute":
		if err := c.SetMute(ctx, true); err != nil {
			return err
		}
		fmt.Println("Muted.")
		return nil

	case "unmute":
		if err := c.SetMute(ctx, false); err != nil {
			return err
		}
		fmt.Println("Unmuted.")
		return nil

	case "play":
		if len(args) < 2 {
			return fmt.Errorf("play: missing url")
		}
		title := "Audio"
		if len(args) > 2 {
			title = strings.Join(args[2:], " ")
		}
		if err := c.PlayURL(ctx, args[1], title); err != nil {
			return err
		}
		fmt.Printf("Playing: %s\n  URL: %s\n", title, args[1])
		return nil

	case "stop":
		if err := c.Stop(ctx); err != nil {
			return err
		}
		fmt.Println("Stopped.")
		return nil

	case "pause":
		if err := c.Pause(ctx); err != nil {
			return err
		}
		fmt.Println("Paused.")
		return nil

	case "seek":
		if len(args) < 2 {
			return fmt.Errorf("seek: missing position (HH:MM:SS or seconds)")
		}
		if err := c.Seek(ctx, args[1]); err != nil {
			return err
		}
		fmt.Printf("Seeked to %s.\n", args[1])
		return nil

	case "tts":
		text := strings.TrimSpace(strings.Join(args[1:], " "))
		if text == "" {
			return fmt.Errorf("tts: missing text")
		}
		used, err := c.TTS(ctx, text)
		if err != nil {
			return err
		}
		fmt.Printf("TTS sent (%s): %s\n", used, text)
		return nil

	case "say":
		text := strings.TrimSpace(strings.Join(args[1:], " "))
		if text == "" {
			return fmt.Errorf("say: missing text")
		}
		used, err := c.Say(ctx, text)
		if err != nil {
			return err
		}
		fmt.Printf("Voice command sent (%s): %s\n", used, text)
		return nil

	case "probe":
		if err := c.ProbeOnline(ctx); err != nil {
			runner.InvalidateXiaoduIPCache()
			return fmt.Errorf("probe failed: %w", err)
		}
		fmt.Printf("Xiaodu speaker is online (DLNA reachable at %s).\n", c.IP())
		return nil

	default:
		return fmt.Errorf("unknown xiaodu subcommand %q", args[0])
	}
}
