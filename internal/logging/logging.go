// Package logging sets up file and console logging plus optional JSONL history.
package logging

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Setup configures the global slog logger to write to both a log file and stdout.
// logLevel must be one of DEBUG, INFO, WARN/WARNING, ERROR.
func Setup(logDir, logFile, logLevel string, console bool) error {
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return fmt.Errorf("create log dir: %w", err)
	}

	level := parseLevel(logLevel)

	filePath := filepath.Join(logDir, logFile)
	f, err := os.OpenFile(filePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open log file: %w", err)
	}

	opts := &slog.HandlerOptions{Level: level}

	var writers []io.Writer
	writers = append(writers, f)
	if console {
		writers = append(writers, os.Stderr)
	}
	w := io.MultiWriter(writers...)
	handler := slog.NewTextHandler(w, opts)
	slog.SetDefault(slog.New(handler))
	return nil
}

func parseLevel(s string) slog.Level {
	switch strings.ToUpper(s) {
	case "DEBUG":
		return slog.LevelDebug
	case "WARN", "WARNING":
		return slog.LevelWarn
	case "ERROR":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// historyRecord mirrors the Python JSONL history format.
type historyRecord struct {
	TS        string  `json:"ts"`
	Switch    string  `json:"switch"`
	Port      int     `json:"port"`
	LinkUp    bool    `json:"link_up"`
	SpeedMbps *int    `json:"speed_mbps"`
}

// AppendHistory appends one JSON line to the history file.
func AppendHistory(historyPath, switchName string, portID int, linkUp bool, speedMbps *int) error {
	if historyPath == "" {
		return nil
	}
	dir := filepath.Dir(historyPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create history dir: %w", err)
	}
	f, err := os.OpenFile(historyPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open history file: %w", err)
	}
	defer func() {
		if cerr := f.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()

	rec := historyRecord{
		TS:        time.Now().UTC().Format(time.RFC3339),
		Switch:    switchName,
		Port:      portID,
		LinkUp:    linkUp,
		SpeedMbps: speedMbps,
	}
	line, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(f, "%s\n", line)
	return err
}
