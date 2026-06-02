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

	"github.com/cuebooth/cuebooth/server/internal/api"
	"github.com/cuebooth/cuebooth/server/internal/companion"
	"github.com/cuebooth/cuebooth/server/internal/config"
)

const defaultConfigPath = "configs/cuebooth.toml"

// version is the server build version, advertised to clients in the protocol
// hello frame. Wired to a real build stamp when release packaging lands (CB-087).
const version = "0.1.0"

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

	comp, err := companion.New(cfg.Companion.BaseURL, companion.WithLogger(logger))
	if err != nil {
		return err
	}

	srv := api.NewServer(cfg, comp, api.WithLogger(logger), api.WithVersion(version))

	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// Run blocks until ctx is cancelled (signal or, under the Windows SCM, a
	// stop request), then shuts the HTTP/WebSocket server down gracefully.
	err = srv.Run(ctx)

	logger.Info("cuebooth-server stopping")

	// TODO(phase2+): bound teardown of the remaining subsystems (audio/OSC,
	// VISCA, OBS, HID) here once those exist to shut down.

	return err
}
