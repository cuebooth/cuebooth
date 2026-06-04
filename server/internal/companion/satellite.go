package companion

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The Satellite client connects to Bitfocus Companion's Satellite API and acts
// as a remote surface — the same mechanism a Stream Deck Satellite or
// Companion's own web emulator uses. Companion renders each button to a bitmap
// and pushes it to us (KEY-STATE); we forward those bitmaps to CueBooth clients
// so the operator sees exactly the buttons Companion is configured with, with
// live feedback, and nothing to configure client-side. Presses travel back the
// other way (KEY-PRESS).
//
// Wire protocol (Bitfocus Companion "Satellite API"): a line-delimited text
// protocol over TCP (default port 16622). Each message is
//
//	COMMAND-NAME ARG1=VAL1 ARG2="val with spaces"\n
//
// On connect Companion sends `BEGIN ...` (and, on newer versions, `CAPS ...`);
// we register a surface with `ADD-DEVICE`, after which Companion streams
// `KEY-STATE` for every key and on every feedback change. We reply to `PING`
// with `PONG` and send `KEY-PRESS` when the operator taps a key.
//
// Transport: we speak the protocol over TCP (Companion's default port 16622).
// Companion 3.5+ also exposes the same protocol over WebSocket (port 16623) —
// the message set is identical, so the transport is isolated here (a single
// net.Conn obtained via dial) and moving to WebSocket later is cheap if it earns
// its keep (e.g. per-message framing without manual line buffering, or TLS). TCP
// is the v1 choice because its line framing is fully specified and verified
// against the documented protocol; we're otherwise happy to assume modern (3.x)
// Companion.

// SatelliteDefaults are the out-of-the-box surface dimensions: a 32-key,
// 8-per-row grid (a Stream Deck XL layout) with 72px button bitmaps. This
// matches the operator's primary Companion page (8 columns × 4 rows). All are
// configurable; see config.SatelliteConfig.
const (
	DefaultSatelliteAddr = "localhost:16622"
	defaultDeviceID      = "cuebooth"
	defaultProductName   = "CueBooth"
	DefaultSatRows       = 4
	DefaultSatCols       = 8
	DefaultSatBitmapSize = 72

	// satReconnectBackoff is the delay between satellite reconnect attempts.
	// Companion is local, so a short fixed backoff is fine.
	satReconnectBackoff = 2 * time.Second
	// satPingInterval drives our keepalive PING to Companion; a failed write
	// (or any read error) drops the connection and triggers a reconnect.
	// ~2s matches the cadence Companion's own Satellite surface uses.
	satPingInterval = 2 * time.Second
	// satDialTimeout bounds a single dial attempt.
	satDialTimeout = 5 * time.Second
)

// ErrSatelliteNotConnected is returned by Press when no Companion satellite
// connection is currently established. The caller surfaces it as a
// device_unavailable nak.
var ErrSatelliteNotConnected = errors.New("companion: satellite not connected")

// SatelliteConfig configures the surface a Satellite registers with Companion.
type SatelliteConfig struct {
	// Addr is the Companion satellite endpoint (host:port), e.g.
	// "localhost:16622".
	Addr string
	// DeviceID is the stable surface identifier. Companion remembers per-surface
	// settings (such as the assigned page) keyed by this id, so keep it stable.
	DeviceID string
	// ProductName is the human-readable surface name shown in Companion.
	ProductName string
	// Rows and Cols are the surface's key grid dimensions.
	Rows, Cols int
	// BitmapSize is the requested button bitmap edge length in pixels (square).
	// 0 (or unset) selects DefaultSatBitmapSize; NewSatellite normalizes any
	// non-positive value to the default.
	BitmapSize int
}

func (c SatelliteConfig) keysTotal() int  { return c.Rows * c.Cols }
func (c SatelliteConfig) keysPerRow() int { return c.Cols }

// SatelliteKey is one key's current rendered state, as pushed by Companion.
type SatelliteKey struct {
	// Key is the flat key index (0-based). Row = Key / cols, Col = Key % cols.
	Key int
	// Type is the Companion key type: "BUTTON", "PAGEUP", "PAGEDOWN", or
	// "PAGENUM". Non-BUTTON types are navigation affordances Companion expects
	// the surface to render itself; they may carry no bitmap.
	Type string
	// Pressed is the button's current pressed state (from feedback).
	Pressed bool
	// Color is the button's background color as "#rrggbb" (or "" if not sent).
	Color string
	// BitmapBase64 is Companion's rendered button image: base64-encoded 8-bit
	// RGB pixel data, BitmapSize×BitmapSize. Empty when no bitmap was sent.
	BitmapBase64 string
}

// Satellite is a client for Companion's Satellite API. It maintains a single
// registered surface, reconnecting as needed, and is safe for concurrent use.
type Satellite struct {
	cfg    SatelliteConfig
	dial   func(ctx context.Context) (net.Conn, error)
	logger *slog.Logger

	onKey    func(SatelliteKey)
	onLayout func(rows, cols, bitmapSize int)
	onClear  func()

	mu  sync.Mutex
	out chan<- string // current session's outbound queue; nil when disconnected
}

// outBuffer bounds the per-session outbound queue. A surface registers, presses,
// and pings — all tiny and infrequent — so a wedged writer that backs this up is
// a dead connection; enqueue then reports not-connected rather than blocking the
// caller (a command handler) forever.
const outBuffer = 32

// SatelliteOption configures a Satellite.
type SatelliteOption func(*Satellite)

// WithSatelliteLogger sets the logger (default slog.Default()).
func WithSatelliteLogger(l *slog.Logger) SatelliteOption {
	return func(s *Satellite) {
		if l != nil {
			s.logger = l
		}
	}
}

// WithSatelliteDialer overrides how the TCP connection is established. Intended
// for tests (e.g. an in-memory net.Pipe); production uses the default dialer.
func WithSatelliteDialer(dial func(ctx context.Context) (net.Conn, error)) SatelliteOption {
	return func(s *Satellite) {
		if dial != nil {
			s.dial = dial
		}
	}
}

// OnKey registers the callback invoked for every KEY-STATE Companion pushes.
func (s *Satellite) OnKey(fn func(SatelliteKey)) { s.onKey = fn }

// OnLayout registers the callback invoked once per (re)connection with the
// surface dimensions, so a consumer can (re)baseline its grid.
func (s *Satellite) OnLayout(fn func(rows, cols, bitmapSize int)) { s.onLayout = fn }

// OnClear registers the callback invoked on a KEYS-CLEAR (Companion asking the
// surface to blank all keys, e.g. on page change before new bitmaps arrive).
func (s *Satellite) OnClear(fn func()) { s.onClear = fn }

// NewSatellite builds a Satellite from cfg, applying defaults for any unset
// fields.
func NewSatellite(cfg SatelliteConfig, opts ...SatelliteOption) *Satellite {
	if cfg.Addr == "" {
		cfg.Addr = DefaultSatelliteAddr
	}
	if cfg.DeviceID == "" {
		cfg.DeviceID = defaultDeviceID
	}
	if cfg.ProductName == "" {
		cfg.ProductName = defaultProductName
	}
	if cfg.Rows <= 0 {
		cfg.Rows = DefaultSatRows
	}
	if cfg.Cols <= 0 {
		cfg.Cols = DefaultSatCols
	}
	if cfg.BitmapSize <= 0 {
		cfg.BitmapSize = DefaultSatBitmapSize
	}
	s := &Satellite{
		cfg:    cfg,
		logger: slog.Default(),
	}
	s.dial = func(ctx context.Context) (net.Conn, error) {
		d := net.Dialer{Timeout: satDialTimeout}
		return d.DialContext(ctx, "tcp", cfg.Addr)
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Layout reports the configured surface dimensions and bitmap edge size.
func (s *Satellite) Layout() (rows, cols, bitmapSize int) {
	return s.cfg.Rows, s.cfg.Cols, s.cfg.BitmapSize
}

// Run maintains the satellite connection until ctx is cancelled, reconnecting
// with a fixed backoff after any drop. It returns only when ctx is done.
func (s *Satellite) Run(ctx context.Context) {
	for {
		if err := ctx.Err(); err != nil {
			return
		}
		if err := s.session(ctx); err != nil && ctx.Err() == nil {
			s.logger.Warn("companion satellite session ended", "err", err, "addr", s.cfg.Addr)
		}
		// Back off before reconnecting, but exit promptly on shutdown.
		select {
		case <-ctx.Done():
			return
		case <-time.After(satReconnectBackoff):
		}
	}
}

// session runs one connection from dial through to disconnect. A single writer
// goroutine owns conn.Write (so presses, pings, and the registration can't
// interleave bytes), fed by a buffered channel; no caller ever holds a lock
// across a socket write. It registers the surface, then reads until the
// connection or ctx ends.
func (s *Satellite) session(ctx context.Context) error {
	conn, err := s.dial(ctx)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}

	out := make(chan string, outBuffer)
	writerDone := make(chan struct{})
	go s.writer(conn, out, writerDone)
	s.setOut(out)
	// Teardown order matters: close the socket first to unblock any in-progress
	// read or write, then clear the outbound handle and close the queue under the
	// lock (atomic with enqueue's send, so a racing Press/ping can't send on a
	// closed channel), then wait for the writer to drain and exit.
	defer func() {
		conn.Close()
		s.mu.Lock()
		s.out = nil
		close(out)
		s.mu.Unlock()
		<-writerDone
	}()

	if err := s.register(); err != nil {
		return fmt.Errorf("register: %w", err)
	}
	if s.onLayout != nil {
		s.onLayout(s.cfg.Rows, s.cfg.Cols, s.cfg.BitmapSize)
	}
	s.logger.Info("companion satellite connected", "addr", s.cfg.Addr, "device_id", s.cfg.DeviceID)

	// Cancel the read when ctx is done (shutdown) by closing the connection,
	// which unblocks the blocking Read below.
	readDone := make(chan struct{})
	defer close(readDone)
	go func() {
		select {
		case <-ctx.Done():
			conn.Close()
		case <-readDone:
		}
	}()

	go s.pingLoop(ctx, readDone)

	return s.readLoop(conn)
}

// writer is the sole writer of conn, draining the outbound queue until it is
// closed (session teardown) or a write fails. On failure it closes conn to
// unblock the read loop, which ends the session and triggers a reconnect.
func (s *Satellite) writer(conn net.Conn, out <-chan string, done chan<- struct{}) {
	defer close(done)
	for line := range out {
		if _, err := conn.Write([]byte(line + "\n")); err != nil {
			conn.Close()
			// Drain remaining queued lines so session teardown's close(out) +
			// range exit isn't blocked by a full buffer.
			for range out {
			}
			return
		}
	}
}

func (s *Satellite) setOut(out chan<- string) {
	s.mu.Lock()
	s.out = out
	s.mu.Unlock()
}

// register sends the ADD-DEVICE that declares our surface to Companion. After
// this, Companion streams KEY-STATE for the surface's keys.
func (s *Satellite) register() error {
	// COLORS=hex gives a usable per-key background color alongside the bitmap;
	// TEXT/TEXT_STYLE are left off since we render Companion's bitmaps. BitmapSize
	// is always positive after NewSatellite normalizes it.
	line := fmt.Sprintf(
		"ADD-DEVICE DEVICEID=%s PRODUCT_NAME=%q KEYS_TOTAL=%d KEYS_PER_ROW=%d BITMAPS=%d COLORS=hex",
		s.cfg.DeviceID, s.cfg.ProductName, s.cfg.keysTotal(), s.cfg.keysPerRow(), s.cfg.BitmapSize,
	)
	return s.enqueue(line)
}

// Press sends a KEY-PRESS for the given flat key index. pressed=true is a
// key-down, false a key-up; a normal tap is a down followed by an up.
func (s *Satellite) Press(key int, pressed bool) error {
	line := fmt.Sprintf("KEY-PRESS DEVICEID=%s KEY=%d PRESSED=%s",
		s.cfg.DeviceID, key, boolWire(pressed))
	if err := s.enqueue(line); err != nil {
		return fmt.Errorf("companion: satellite key-press: %w", err)
	}
	return nil
}

// enqueue hands a protocol line to the session's writer goroutine. It never
// blocks: if there's no connection, or the writer has backed up (a wedged
// connection), it reports ErrSatelliteNotConnected rather than stalling the
// caller. The writer appends the newline terminator and is the sole conn.Write
// caller, so concurrent presses/pings/registration can't interleave bytes.
//
// The send happens under s.mu, paired with session teardown which clears s.out
// and closes the channel under the same lock: that makes "is the channel still
// open?" and "send on it" atomic, so a press/ping racing a disconnect can never
// send on a closed channel (which would panic the process). The send is
// non-blocking, so the lock is held only momentarily.
func (s *Satellite) enqueue(line string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.out == nil {
		return ErrSatelliteNotConnected
	}
	select {
	case s.out <- line:
		return nil
	default:
		return ErrSatelliteNotConnected
	}
}

// readLoop reads and dispatches protocol lines until an error (including the
// connection being closed on shutdown).
func (s *Satellite) readLoop(conn net.Conn) error {
	// bufio.Reader.ReadString grows to fit long lines (a 72×72 RGB bitmap is
	// ~20 KB base64), unlike bufio.Scanner's fixed token cap.
	r := bufio.NewReader(conn)
	for {
		line, err := r.ReadString('\n')
		if line != "" {
			s.handleLine(strings.TrimRight(line, "\r\n"))
		}
		if err != nil {
			return err
		}
	}
}

func (s *Satellite) handleLine(line string) {
	if line == "" {
		return
	}
	cmd, args := parseSatelliteLine(line)
	switch cmd {
	case "KEY-STATE":
		s.handleKeyState(args)
	case "PING":
		// Reply with PONG echoing the payload (the text after the command).
		_ = s.enqueue("PONG" + strings.TrimPrefix(line, "PING"))
	case "PONG":
		// Reply to our own keepalive PING; nothing to do.
	case "KEYS-CLEAR":
		if s.onClear != nil {
			s.onClear()
		}
	case "BEGIN", "CAPS", "ADD-DEVICE", "REMOVE-DEVICE":
		// Handshake/ack lines we don't act on; log at debug for diagnostics.
		s.logger.Debug("companion satellite line", "cmd", cmd)
	default:
		s.logger.Debug("companion satellite: ignoring command", "cmd", cmd)
	}
}

func (s *Satellite) handleKeyState(args map[string]string) {
	if s.onKey == nil {
		return
	}
	// Simple-mode KEY identifies the key; advanced-mode CONTROLID is unused here.
	keyStr, ok := args["KEY"]
	if !ok {
		return
	}
	key, err := strconv.Atoi(keyStr)
	if err != nil {
		s.logger.Debug("companion satellite: bad KEY", "value", keyStr)
		return
	}
	typ := args["TYPE"]
	if typ == "" {
		typ = "BUTTON"
	}
	s.onKey(SatelliteKey{
		Key:          key,
		Type:         typ,
		Pressed:      parseWireBool(args["PRESSED"]),
		Color:        args["COLOR"],
		BitmapBase64: args["BITMAP"],
	})
}

// pingLoop sends a periodic keepalive PING. It stops when ctx is cancelled or
// the session ends (done closed); a write failure means the connection is gone,
// so it stops and lets the read loop surface the error.
func (s *Satellite) pingLoop(ctx context.Context, done <-chan struct{}) {
	t := time.NewTicker(satPingInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-done:
			return
		case <-t.C:
			if err := s.enqueue("PING cuebooth"); err != nil {
				return
			}
		}
	}
}

// parseSatelliteLine splits a protocol line into its command and KEY=VALUE
// argument map. Values may be double-quoted to contain spaces.
func parseSatelliteLine(line string) (cmd string, args map[string]string) {
	toks := tokenizeSatellite(line)
	args = make(map[string]string)
	if len(toks) == 0 {
		return "", args
	}
	cmd = toks[0]
	for _, tok := range toks[1:] {
		eq := strings.IndexByte(tok, '=')
		if eq < 0 {
			args[tok] = ""
			continue
		}
		args[tok[:eq]] = tok[eq+1:]
	}
	return cmd, args
}

// tokenizeSatellite splits on spaces, treating a double-quoted run as a single
// token (quotes are stripped) and a backslash as escaping the next character —
// matching Companion's line parser, which uses quotes to allow spaces in a value
// and backslashes to embed quotes/backslashes within one.
func tokenizeSatellite(s string) []string {
	var toks []string
	var b strings.Builder
	inQuote := false
	flush := func() {
		if b.Len() > 0 {
			toks = append(toks, b.String())
			b.Reset()
		}
	}
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '\\' && i+1 < len(s):
			i++
			b.WriteByte(s[i]) // emit the escaped character literally
		case c == '"':
			inQuote = !inQuote
		case c == ' ' && !inQuote:
			flush()
		default:
			b.WriteByte(c)
		}
	}
	flush()
	return toks
}

// boolWire renders a bool the way Companion accepts it on the wire.
func boolWire(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// parseWireBool reads a Companion boolean, which may be "1"/"0" or
// "true"/"false" (Companion sends "1"/"0").
func parseWireBool(v string) bool {
	return v == "1" || strings.EqualFold(v, "true")
}
