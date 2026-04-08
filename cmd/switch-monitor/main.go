// switch-monitor polls managed switches and sends email alerts on port issues.
package main

import (
	"flag"
	"fmt"
	"os"

	"switch-monitor/internal/config"
	"switch-monitor/internal/logging"
	"switch-monitor/internal/runner"
)

func main() {
	cfgPath := flag.String("config", "config.yaml", "path to YAML config file")
	once := flag.Bool("once", false, "run one check cycle and exit (useful for cron)")
	noEmail := flag.Bool("no-email", false, "skip sending email alerts (useful for testing)")
	noCalendar := flag.Bool("no-calendar", false, "skip calendar repair events (useful for testing)")
	flag.Parse()

	cfg, err := config.LoadConfig(*cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if err := logging.Setup(cfg.LogDir, cfg.LogFile, cfg.LogLevel, true); err != nil {
		fmt.Fprintf(os.Stderr, "logging setup: %v\n", err)
		os.Exit(1)
	}

	r := runner.New(cfg, *cfgPath, *noEmail, *noCalendar)
	r.RunLoop(*once)
}
