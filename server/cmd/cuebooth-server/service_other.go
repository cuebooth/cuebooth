//go:build !windows

package main

import "log/slog"

// runAsService is a no-op on non-Windows platforms; the binary always runs
// interactively. Windows-specific service registration lives in
// service_windows.go.
func runAsService(_ *slog.Logger, _ string) bool { return false }
