// switch-monitor polls managed switches and sends email alerts on port issues.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"switch-monitor/internal/cli"
	"switch-monitor/internal/config"
	"switch-monitor/internal/logging"
	"switch-monitor/internal/runner"
)

// Set at link time, e.g. go build -ldflags "-X main.version=1.0.0 -X main.commit=abc123".
var (
	version = "dev"
	commit  = "unknown"
)

func main() {
	showVersion := flag.Bool("version", false, "print version and git commit, then exit")
	cfgPath := flag.String("config", "config.yaml", "path to YAML config file")
	once := flag.Bool("once", false, "run one check cycle and exit (useful for cron)")
	noEmail := flag.Bool("no-email", false, "skip sending email alerts (useful for testing)")
	noCalendar := flag.Bool("no-calendar", false, "skip calendar repair events (useful for testing)")
	flag.Parse()

	if *showVersion {
		fmt.Printf("switch-monitor %s\ncommit %s\n", version, commit)
		os.Exit(0)
	}

	args := flag.Args()
	if len(args) > 0 {
		switch args[0] {
		case "help", "-h", "--help":
			cli.PrintSubcommandHelp(os.Stdout)
			os.Exit(0)
		}
	}

	cfg, err := config.LoadConfig(*cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if err := logging.Setup(cfg.LogDir, cfg.LogFile, cfg.LogLevel, true); err != nil {
		fmt.Fprintf(os.Stderr, "logging setup: %v\n", err)
		os.Exit(1)
	}

	if len(args) > 0 {
		ctx := context.Background()
		switch args[0] {
		case "ikuai":
			if err := cli.RunIkuai(ctx, cfg, args[1:]); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			os.Exit(0)
		case "mihomo":
			if err := cli.RunMihomo(ctx, cfg, args[1:]); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			os.Exit(0)
		case "xiaodu":
			if err := cli.RunXiaodu(ctx, cfg, args[1:]); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			os.Exit(0)
		default:
			fmt.Fprintf(os.Stderr, "unknown command %q\n\n", args[0])
			cli.PrintSubcommandHelp(os.Stderr)
			os.Exit(1)
		}
	}

	r := runner.New(cfg, *cfgPath, *noEmail, *noCalendar)
	r.RunLoop(*once)
}
