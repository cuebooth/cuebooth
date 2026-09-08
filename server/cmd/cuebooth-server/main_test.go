package main

import (
	"log/slog"
	"strings"
	"testing"
)

// `make build` without `make web` is a supported build, and the difference is
// invisible until someone opens a browser and gets the no-client page. The
// startup line is the only thing that says so beforehand, so it has to be
// distinguishable from the ordinary case rather than merely present.
func TestWebClientStatus(t *testing.T) {
	bundledLevel, bundledMsg := webClientStatus(true)
	missingLevel, missingMsg := webClientStatus(false)

	if bundledLevel != slog.LevelInfo {
		t.Errorf("bundled level = %v, want INFO", bundledLevel)
	}
	if missingLevel != slog.LevelWarn {
		t.Errorf("missing level = %v, want WARN (an operator has to notice this one)", missingLevel)
	}
	if bundledMsg == missingMsg {
		t.Error("both builds log the same line, so the log says nothing")
	}
	if !strings.Contains(missingMsg, "make web") {
		t.Errorf("missing message = %q, want it to name the command that fixes it", missingMsg)
	}
}
