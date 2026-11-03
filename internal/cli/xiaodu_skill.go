package cli

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"switch-monitor/internal/adapter"
	"switch-monitor/internal/config"
	"switch-monitor/internal/runner"
)

// skillSubcommands are handled before the speaker client is built: the webhook
// only needs cloud/LLM access, so it must stay testable while the speaker is
// offline or its address is unknown.
var skillSubcommands = map[string]bool{
	"skill-serve":      true,
	"skill-test":       true,
	"skill-chat":       true,
	"codebuddy-models": true,
}

// IsXiaoduSkillSubcommand reports whether a subcommand belongs to the AI skill.
func IsXiaoduSkillSubcommand(name string) bool { return skillSubcommands[name] }

func skillConfig(cfg *config.MonitorConfig) (*config.XiaoduSkillConfig, error) {
	if cfg.Xiaodu == nil || !cfg.Xiaodu.Enabled {
		return nil, fmt.Errorf("xiaodu is not enabled in config")
	}
	if cfg.Xiaodu.Skill == nil {
		return nil, fmt.Errorf("xiaodu.skill is not configured")
	}
	return cfg.Xiaodu.Skill, nil
}

// RunXiaoduCodeBuddyModels lists the models available to the logged-in
// CodeBuddy account (GET /v3/config → craft agent). Useful for picking a
// llm_model value without guessing.
func RunXiaoduCodeBuddyModels(ctx context.Context, cfg *config.MonitorConfig) error {
	skill, err := skillConfig(cfg)
	if err != nil {
		return err
	}
	client, err := adapter.NewCodeBuddyClient(adapter.CodeBuddyOptions{
		Server:           skill.LLMBaseURL,
		APIKey:           skill.LLMAPIKey,
		TokenFile:        skill.CodeBuddyTokenFile,
		RefreshTokenFile: skill.CodeBuddyRefreshTokenFile,
		Proxy:            skill.LLMProxy,
	})
	if err != nil {
		return fmt.Errorf("init codebuddy client: %w", err)
	}
	models, err := client.ListModels(ctx)
	if err != nil {
		return fmt.Errorf("list codebuddy models: %w", err)
	}
	fmt.Printf("CodeBuddy models (%d, server %s):\n", len(models), client.Server())
	active := skill.LLMModelOrDefault()
	for _, m := range models {
		marker := "  "
		if m.ID == active {
			marker = "* "
		}
		reasoning := ""
		if m.Reasoning {
			reasoning = " [thinking]"
		}
		maxTokens := ""
		if m.MaxTokens > 0 {
			maxTokens = fmt.Sprintf(" max=%dk", m.MaxTokens/1024)
		}
		fmt.Printf("%s%-26s %-22s%s%s\n", marker, m.ID, m.Name, reasoning, maxTokens)
	}
	fmt.Printf("\n* = currently configured (llm_model: %s)\n", active)
	return nil
}

// RunXiaoduSkillServe runs the DuerOS webhook in the foreground until SIGINT/SIGTERM.
// Useful behind a reverse proxy while bringing the skill up for the first time.
func RunXiaoduSkillServe(ctx context.Context, cfg *config.MonitorConfig) error {
	skill, err := skillConfig(cfg)
	if err != nil {
		return err
	}
	srv, err := runner.NewDuerOSSkillServer(skill, nil)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	fmt.Printf("DuerOS skill webhook on %s%s (provider %s, model %s)\n",
		skill.ListenOrDefault(), skill.PathOrDefault(), srv.Provider(), srv.Model())
	fmt.Printf("Configure the Web Service URL as https://<your-public-host>%s\n", skill.PathOrDefault())
	fmt.Println("Press Ctrl-C to stop.")
	return srv.ListenAndServe(ctx)
}

// RunXiaoduSkillTest sends one synthetic utterance through the real handler and
// prints what the speaker would say. No HTTP, no DBP skill, no speaker required:
// it exercises config, LLM connectivity, sanitisation and truncation.
func RunXiaoduSkillTest(ctx context.Context, cfg *config.MonitorConfig, args []string) error {
	text := strings.TrimSpace(strings.Join(args, " "))
	if text == "" {
		return fmt.Errorf("skill-test: missing text, e.g. xiaodu skill-test \"用一句话介绍你自己\"")
	}
	skill, err := skillConfig(cfg)
	if err != nil {
		return err
	}
	srv, err := runner.NewDuerOSSkillServer(skill, nil)
	if err != nil {
		return err
	}

	resp := srv.HandleRequest(ctx, syntheticIntentRequest("cli-test", text))
	spoken := skillSpeech(resp)
	fmt.Printf("问: %s\n答: %s\n", text, spoken)
	fmt.Printf("(provider=%s model=%s end_session=%v chars=%d)\n",
		srv.Provider(), srv.Model(), resp.Response.ShouldEndSession, len([]rune(spoken)))
	return nil
}

// RunXiaoduSkillChat is an interactive multi-turn session against the same
// handler the webhook uses, so conversation memory can be verified locally.
func RunXiaoduSkillChat(ctx context.Context, cfg *config.MonitorConfig) error {
	skill, err := skillConfig(cfg)
	if err != nil {
		return err
	}
	srv, err := runner.NewDuerOSSkillServer(skill, nil)
	if err != nil {
		return err
	}

	fmt.Printf("多轮会话测试 (provider=%s model=%s)。输入内容回车发送；说“退出”结束。\n",
		srv.Provider(), srv.Model())
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	turn := 0
	for {
		fmt.Print("你> ")
		if !scanner.Scan() {
			fmt.Println()
			return scanner.Err()
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		turn++
		resp := srv.HandleRequest(ctx, syntheticIntentRequest("cli-chat", line))
		fmt.Printf("小度> %s\n", skillSpeech(resp))
		if resp.Response.ShouldEndSession {
			fmt.Printf("(会话已结束，共 %d 轮)\n", turn)
			return nil
		}
	}
}

// syntheticIntentRequest builds the payload DuerOS would POST for an utterance
// that matched the skill's default intent.
func syntheticIntentRequest(sessionID, text string) *adapter.DuerOSRequest {
	req := &adapter.DuerOSRequest{Version: "2.0"}
	req.Session.SessionID = sessionID
	req.Request.Type = adapter.DuerOSTypeIntent
	req.Request.Query = &adapter.DuerOSQuery{Type: "TEXT", Original: text}
	// Mirrors what the platform sends when the default intent carries the whole
	// utterance in a slot, so extraction is exercised the same way.
	req.Request.Intents = []adapter.DuerOSIntent{{
		Name: "ai.dueros.common.default_intent",
		Slots: map[string]adapter.DuerOSSlot{
			"query": {Name: "query", Value: text},
		},
	}}
	return req
}

func skillSpeech(resp *adapter.DuerOSResponse) string {
	if resp == nil || resp.Response.OutputSpeech == nil {
		return ""
	}
	return resp.Response.OutputSpeech.Text
}
