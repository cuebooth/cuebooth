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
	// satRegisterTimeout bounds the wait for Companion's ADD-DEVICE reply. It
	// sits below satReadTimeout deliberately: nothing feeds the read side while
	// the verdict is outstanding (the keepalive starts after it), so a longer
	// value would let the read deadline fire first and report a bare i/o timeout
	// instead of naming the registration as the thing that never completed.
	satRegisterTimeout = 4 * time.Second
)

// satReadTimeout bounds how long the read loop waits for any line before
// treating the connection as dead. Companion answers every PING, so a healthy
// link is never quiet for a whole interval; without this, a peer that vanishes
// without a FIN (sleeping PC, downed switch port) is only noticed when TCP gives
// up retransmitting, which is minutes on Linux.
const satReadTimeout = 3 * satPingInterval

// ErrSatelliteNotConnected is returned by Press when no Companion satellite
// connection is currently established. The caller surfaces it as a
// device_unavailable nak.
var ErrSatelliteNotConnected = errors.New("companion: satellite not connected")

// ErrSatelliteQueueFull is returned when the session is up but its outbound
// queue has not drained. It is distinct from ErrSatelliteNotConnected because a
// backed-up writer is transient: the keepalive skips a tick rather than giving
// up on the connection.
var ErrSatelliteQueueFull = errors.New("companion: satellite send queue full")

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

	// readTimeout is satReadTimeout, per-instance so a test can shorten it
	// without a shared global the running sessions would race on.
	readTimeout time.Duration

	mu  sync.Mutex
	out chan<- string // current session's outbound queue; nil when disconnected
	// registered is true once Companion has accepted this session's ADD-DEVICE.
	// Press is refused until then: Companion discards key presses for a surface
	// it has not registered, so reporting them as sent would be a lie.
	registered bool
	// reg carries Companion's verdict on our ADD-DEVICE for the current session:
	// nil once it replies OK, an error if it replies ERROR. Buffered and
	// single-use, so the read loop never blocks delivering it.
	reg chan error
}

// ErrSatelliteRejected is returned when Companion refuses the ADD-DEVICE
// registration. The surface is not live, so the session is torn down and retried
// rather than left connected with a grid that will never receive key states.
var ErrSatelliteRejected = errors.New("companion: satellite registration rejected")

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

// OnClear registers the callback invoked on a KEYS-CLEAR — Companion asking the
// surface to blank every key. Companion 3.4.1 and 5.0.3 were both observed not
// to send it on a page change: they re-push a KEY-STATE for every key instead
// (see satellite_live_test.go for the captured frames). It stays handled because
// the protocol defines it and other versions may use it.
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
		cfg:         cfg,
		logger:      slog.Default(),
		readTimeout: satReadTimeout,
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
	reg := make(chan error, 1)
	go s.writer(conn, out, writerDone)
	s.setSession(out, reg)

	// Read from the start: the registration verdict arrives on the wire, so the
	// reader has to be running before we wait for it.
	readErr := make(chan error, 1)
	readerExited := make(chan struct{})
	go func() {
		readErr <- s.readLoop(conn)
		close(readerExited)
	}()

	// Teardown order matters: close the socket first to unblock any in-progress
	// read or write; wait for the reader to exit before clearing session state,
	// so a line it is still dispatching cannot land on the next session's
	// callbacks or acknowledge a registration that never happened; then clear the
	// outbound handle and close the queue under the lock (atomic with enqueue's
	// send, so a racing Press/ping can't send on a closed channel), and wait for
	// the writer to drain and exit.
	defer func() {
		conn.Close()
		<-readerExited
		s.mu.Lock()
		s.out = nil
		s.reg = nil
		s.registered = false
		close(out)
		s.mu.Unlock()
		<-writerDone
	}()

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

	if err := s.register(); err != nil {
		return fmt.Errorf("register: %w", err)
	}

	// Companion answers ADD-DEVICE with OK or ERROR, and sends no key states at
	// all when it refuses. Waiting for that verdict keeps a refused surface from
	// being reported as connected and leaving clients on a grid that never
	// updates.
	select {
	case err := <-reg:
		if err != nil {
			return err
		}
	case err := <-readErr:
		if err == nil {
			err = errors.New("connection closed during registration")
		}
		return err
	case <-time.After(satRegisterTimeout):
		return fmt.Errorf("companion: no ADD-DEVICE reply within %s", satRegisterTimeout)
	case <-ctx.Done():
		return ctx.Err()
	}

	s.logger.Info("companion satellite registered", "addr", s.cfg.Addr, "device_id", s.cfg.DeviceID)

	go s.pingLoop(ctx, readDone)

	return <-readErr
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

func (s *Satellite) setSession(out chan<- string, reg chan error) {
	s.mu.Lock()
	s.out = out
	s.reg = reg
	s.mu.Unlock()
}

// signalRegistration delivers Companion's ADD-DEVICE verdict to session. The
// channel is buffered and cleared on teardown, so a duplicate or late reply is
// dropped rather than blocking the read loop.
func (s *Satellite) signalRegistration(err error) {
	s.mu.Lock()
	reg := s.reg
	s.reg = nil
	s.registered = err == nil
	s.mu.Unlock()
	if reg == nil {
		return
	}
	select {
	case reg <- err:
	default:
	}
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
	s.mu.Lock()
	ready := s.registered
	s.mu.Unlock()
	if !ready {
		return ErrSatelliteNotConnected
	}
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
		return ErrSatelliteQueueFull
	}
}

// readLoop reads and dispatches protocol lines until an error (including the
// connection being closed on shutdown).
func (s *Satellite) readLoop(conn net.Conn) error {
	// bufio.Reader.ReadString grows to fit long lines (a 72×72 RGB bitmap is
	// ~20 KB base64), unlike bufio.Scanner's fixed token cap.
	r := bufio.NewReader(conn)
	for {
		// Refreshed per line rather than set once: any traffic proves the peer is
		// alive, and our PING guarantees traffic well inside the window.
		if err := conn.SetReadDeadline(time.Now().Add(s.readTimeout)); err != nil {
			return err
		}
		line, err := r.ReadString('\n')
		if err != nil {
			// ReadString returns what it had along with the error, so anything
			// here is a line the peer never terminated — a bitmap cut short by
			// the read deadline or a mid-line close. Parsing it would publish a
			// truncated KEY-STATE under a fresh seq, beating the intact frame.
			return err
		}
		s.handleLine(strings.TrimRight(line, "\r\n"))
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
	case "ADD-DEVICE":
		// The verdict on our registration: "ADD-DEVICE OK DEVICEID=..." or
		// "ADD-DEVICE ERROR DEVICEID=... MESSAGE=...", the latter carrying no
		// key states at all.
		if _, bad := args["ERROR"]; bad {
			msg := args["MESSAGE"]
			if msg == "" {
				msg = "no message"
			}
			s.logger.Error("companion rejected the satellite surface",
				"device_id", s.cfg.DeviceID, "message", msg,
				"keys_total", s.cfg.keysTotal(), "keys_per_row", s.cfg.keysPerRow())
			s.signalRegistration(fmt.Errorf("%w: %s", ErrSatelliteRejected, msg))
			return
		}
		if _, ok := args["OK"]; ok {
			// The layout is raised here, on the read goroutine, so it is
			// necessarily ahead of every KEY-STATE that follows it on the wire.
			// Raising it from the session goroutine instead would let key states
			// this loop had already dispatched be re-baselined away by a layout
			// that logically preceded them.
			if s.onLayout != nil {
				s.onLayout(s.cfg.Rows, s.cfg.Cols, s.cfg.BitmapSize)
			}
			s.signalRegistration(nil)
		}
	case "KEY-PRESS":
		// Companion acknowledges every press with OK or ERROR. A rejected press
		// is the operator's tap doing nothing, so it must not vanish at debug
		// level the way an ack can.
		if _, bad := args["ERROR"]; bad {
			s.logger.Error("companion rejected a key press",
				"device_id", s.cfg.DeviceID, "message", args["MESSAGE"])
		}
	case "ERROR":
		// A protocol-level complaint about something we sent; without this it
		// would vanish at debug level and the surface would look merely idle.
		s.logger.Error("companion satellite protocol error", "message", args["MESSAGE"])
	case "BEGIN", "CAPS", "REMOVE-DEVICE":
		// Handshake lines we don't act on; log at debug for diagnostics.
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
			// A full queue means the writer is behind, not that the peer is
			// gone; skipping the tick keeps the keepalive alive across a burst,
			// and a genuinely dead connection ends the session via the read
			// deadline anyway.
			if err := s.enqueue("PING cuebooth"); err != nil && !errors.Is(err, ErrSatelliteQueueFull) {
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
