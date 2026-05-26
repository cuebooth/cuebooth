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
				<-errCh
				status <- svc.Status{State: svc.Stopped}
				return false, 0
			}
		case err := <-errCh:
			if err != nil {
				h.logger.Error("server exited with error", "err", err)
			}
			status <- svc.Status{State: svc.Stopped}
			return false, 0
		}
	}
}
