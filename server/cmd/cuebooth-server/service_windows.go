//go:build windows

package main

import (
	"context"
	"log/slog"

	"golang.org/x/sys/windows/svc"
)

// serviceName is what the binary registers itself as with the Windows SCM.
const serviceName = "CueBoothServer"

// runAsService detects whether the process was launched by the Windows Service
// Control Manager (SCM). When yes, it runs the service event loop and returns
// true; the caller should exit without further work. When no, it returns false
// and main runs the server interactively (e.g. during development).
func runAsService(logger *slog.Logger, configPath string) bool {
	isService, err := svc.IsWindowsService()
	if err != nil {
		logger.Error("svc.IsWindowsService failed", "err", err)
		return false
	}
	if !isService {
		return false
	}

	logger.Info("launched by SCM, running as Windows service", "name", serviceName)
	if err := svc.Run(serviceName, &serviceHandler{logger: logger, configPath: configPath}); err != nil {
		logger.Error("service exited with error", "err", err)
	}
	return true
}

type serviceHandler struct {
	logger     *slog.Logger
	configPath string
}

func (h *serviceHandler) Execute(args []string, requests <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	const accepted = svc.AcceptStop | svc.AcceptShutdown
	status <- svc.Status{State: svc.StartPending}

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- run(ctx, h.logger, h.configPath) }()

	status <- svc.Status{State: svc.Running, Accepts: accepted}

	for {
		select {
		case req := <-requests:
			switch req.Cmd {
			case svc.Interrogate:
				status <- req.CurrentStatus
			case svc.Stop, svc.Shutdown:
				status <- svc.Status{State: svc.StopPending}
				cancel()
				return h.stopped(status, <-errCh)
			}
		case err := <-errCh:
			return h.stopped(status, err)
		}
	}
}

// stopped reports the service as stopped and maps run's exit to an SCM result.
// A non-nil error is surfaced as a service-specific failure (non-zero exit
// code with svcSpecificEC = true) so the SCM can trigger any configured
// recovery actions, e.g. restart-on-failure. A clean exit returns 0.
func (h *serviceHandler) stopped(status chan<- svc.Status, err error) (bool, uint32) {
	status <- svc.Status{State: svc.Stopped}
	if err != nil {
		h.logger.Error("server exited with error", "err", err)
		return true, 1
	}
	return false, 0
}
