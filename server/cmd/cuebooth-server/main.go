// Command cuebooth-server is the CueBooth orchestration daemon.
//
// It runs on the production PC and brokers between Bitfocus Companion,
// directly-controlled hardware (mixer, PTZ camera, OBS, slide clicker),
// and connected Flutter clients over a WebSocket API.
//
// See docs/design.md for the full architecture.
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/cuebooth/cuebooth/server/internal/config"
)

const defaultConfigPath = "configs/cuebooth.toml"

func main() {
	configPath := flag.String("config", defaultConfigPath, "path to the cuebooth.toml configuration file")
	flag.Parse()

	// TODO(phase1): when launched by the Windows SCM there is no console
	// attached, so stderr is discarded and these logs are lost. Route the
	// service path to the Windows Event Log (or a file) instead of os.Stderr.
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	if runAsService(logger, *configPath) {
		return
	}

	if err := run(context.Background(), logger, *configPath); err != nil {
		logger.Error("server exited with error", "err", err)
		os.Exit(1)
	}
}

// run loads configuration, brings the server up, and blocks until the context
// is cancelled or a termination signal is received.
func run(ctx context.Context, logger *slog.Logger, configPath string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}

	logger.Info("cuebooth-server starting",
		"listen", cfg.Server.Listen,
		"companion", cfg.Companion.BaseURL,
	)

	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer cancel()

	<-ctx.Done()

	logger.Info("cuebooth-server stopping")

	// TODO(phase1): bound subsystem teardown (audio/OSC, VISCA, OBS, HID, API)
	// with a timeout context here once those subsystems exist to shut down.

	return nil
}
