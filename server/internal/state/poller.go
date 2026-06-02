package state

import (
	"context"
	"log/slog"
	"time"
)

// DefaultPollInterval is the cadence at which the Poller refreshes its sources
// when no interval is configured (CB-013 acceptance: ~1s default).
const DefaultPollInterval = time.Second

// Source is something the Poller reads each tick to refresh part of the state —
// today a Companion variable, in Phase 2 a direct-OSC subscription. Fetch does
// the I/O and returns a mutator that applies the freshly-read value to the
// State; keeping the network call out of the returned closure lets the Poller
// gather all sources first and then apply them under a single Store lock.
//
// A non-nil error means the source was unreadable this tick (e.g. Companion was
// momentarily unreachable); the Poller logs it and leaves prior state intact.
type Source interface {
	Fetch(ctx context.Context) (apply func(*State), err error)
}

// SourceFunc adapts a function to a Source.
type SourceFunc func(ctx context.Context) (func(*State), error)

// Fetch implements Source.
func (f SourceFunc) Fetch(ctx context.Context) (func(*State), error) { return f(ctx) }

// Poller periodically refreshes a set of Sources into a Store and invokes
// onChange whenever the resulting state actually changes.
type Poller struct {
	store    *Store
	interval time.Duration
	sources  []Source
	logger   *slog.Logger
}

// NewPoller builds a Poller. interval <= 0 uses DefaultPollInterval. logger
// defaults to slog.Default(). Changes are broadcast through the Store's observer
// (set by the server), so the poller just feeds the Store.
func NewPoller(store *Store, interval time.Duration, logger *slog.Logger, sources ...Source) *Poller {
	if interval <= 0 {
		interval = DefaultPollInterval
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Poller{store: store, interval: interval, sources: sources, logger: logger}
}

// Run polls until ctx is cancelled. It does an immediate first tick so initial
// state is populated promptly rather than after one interval.
func (p *Poller) Run(ctx context.Context) {
	if len(p.sources) == 0 {
		// Nothing to poll (e.g. Phase 1 with no Companion variable bindings yet).
		// State is still driven by command handlers; just wait for shutdown.
		<-ctx.Done()
		return
	}

	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()

	p.tick(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.tick(ctx)
		}
	}
}

func (p *Poller) tick(ctx context.Context) {
	// Sources are fetched sequentially. That's fine for the small, fast set
	// expected here (a handful of local Companion variable reads); if a future
	// deployment adds many or slow sources such that one Fetch could delay the
	// others past the interval, fetch them concurrently with a bounded errgroup.
	appliers := make([]func(*State), 0, len(p.sources))
	for _, src := range p.sources {
		apply, err := src.Fetch(ctx)
		if err != nil {
			if ctx.Err() == nil {
				p.logger.Warn("state poll source failed", "err", err)
			}
			continue
		}
		if apply != nil {
			appliers = append(appliers, apply)
		}
	}
	if len(appliers) == 0 {
		return
	}

	// The Store observer broadcasts any resulting delta (atomically with its
	// revision), so the poller only needs to feed the Store.
	if _, err := p.store.Update(func(st *State) {
		for _, apply := range appliers {
			apply(st)
		}
	}); err != nil {
		p.logger.Error("state poll update failed", "err", err)
	}
}
