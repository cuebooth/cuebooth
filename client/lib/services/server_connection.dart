import 'dart:async';
import 'dart:convert';

import 'package:flutter/foundation.dart';
import 'package:web_socket_channel/web_socket_channel.dart';

/// Connection state to the CueBooth server.
///
/// Named to avoid colliding with Flutter's built-in `ConnectionState`
/// (from `AsyncSnapshot`/`StreamBuilder`), which screens consuming
/// [ServerConnection.messages] will have in scope.
enum ServerConnectionState {
  disconnected,
  connecting,
  connected,
  reconnecting,
  // `connect()` sets `error` (no auto-reconnect) on a bad server address. The
  // connection-failure path (channel.ready failing, or _onError) also passes
  // through `error` before `_scheduleReconnect()` moves it to `reconnecting`.
  // The connect screen listens for `error` to surface the failure and stop the
  // retry loop; `lastError` carries the detail.
  error,
}

/// Creates a [WebSocketChannel] for [uri]. Injectable so tests can drive the
/// connect/reconnect logic deterministically without a real socket.
typedef ChannelFactory = WebSocketChannel Function(Uri uri);

/// Manages the WebSocket connection to the CueBooth server.
///
/// Authority lies with the server: clients send commands and receive state
/// broadcasts. The transport reconnects with exponential backoff on drops.
/// See ../../../docs/design.md §3.6 (Communication Protocol) and
/// ../../../docs/protocol.md for the wire spec.
class ServerConnection extends ChangeNotifier {
  ServerConnection({ChannelFactory? connectChannel})
    : _connectChannel = connectChannel ?? WebSocketChannel.connect;

  /// How a [WebSocketChannel] is created (injectable for tests).
  final ChannelFactory _connectChannel;

  static const Duration _initialBackoff = Duration(seconds: 1);
  static const Duration _maxBackoff = Duration(seconds: 30);

  Uri? _uri;
  WebSocketChannel? _channel;
  StreamSubscription<dynamic>? _subscription;
  Duration _backoff = _initialBackoff;
  Timer? _reconnectTimer;
  bool _stopRequested = false;

  // Bumped on every connect/disconnect/dispose to invalidate already-queued
  // reconnect Timer callbacks (Timer.cancel() can't unschedule one that has
  // already fired). _open() ignores callbacks whose captured generation is
  // stale, preventing a duplicate socket from a connect()-during-reconnect.
  int _generation = 0;

  ServerConnectionState _state = ServerConnectionState.disconnected;
  ServerConnectionState get state => _state;

  String? _lastError;
  String? get lastError => _lastError;

  final _messages = StreamController<Map<String, dynamic>>.broadcast();
  Stream<Map<String, dynamic>> get messages => _messages.stream;

  /// Open a connection to the given host and port. Calling [connect] while a
  /// connection is open closes the previous one first.
  Future<void> connect(String host, int port) async {
    await disconnect();
    _stopRequested = false;
    // Reject obviously-invalid input up front with an error state (no auto-
    // reconnect) instead of letting it fail asynchronously into the reconnect
    // loop. An empty host produces ws://:port, which doesn't throw but never
    // connects; fuller validation/feedback is CB-014.
    if (host.trim().isEmpty) {
      _lastError = 'Server address is required.';
      _setState(ServerConnectionState.error);
      return;
    }
    // ws:// (cleartext) is the v1 scheme: the server is reached by LAN IP or
    // Tailscale address with no public TLS cert, and Tailscale already encrypts
    // in transit. wss:// — the TLS equivalent, needed for HTTPS-hosted web
    // (mixed content) or TLS-fronted deployments — is future work. See
    // docs/protocol.md §1 and design.md §3.5.
    final Uri uri;
    try {
      uri = Uri(scheme: 'ws', host: host, port: port, path: '/ws');
    } on FormatException catch (e) {
      // Bad user input (a pasted "ws://host", a host with a slash, etc.) makes
      // the Uri constructor throw. Surface it as an error state instead of
      // letting it crash the caller, and don't auto-reconnect on input error.
      _lastError = 'Invalid server address: ${e.message}';
      _setState(ServerConnectionState.error);
      return;
    }
    _uri = uri;
    _backoff = _initialBackoff;
    // Claim a fresh token for this attempt so a concurrent connect() (e.g. a
    // Connect double-tap) or an already-queued stale timer is superseded.
    _open(++_generation);
  }

  /// Cleanly close the connection and stop any reconnect attempts.
  Future<void> disconnect() async {
    _stopRequested = true;
    _generation++;
    _reconnectTimer?.cancel();
    _reconnectTimer = null;
    await _subscription?.cancel();
    _subscription = null;
    await _channel?.sink.close();
    _channel = null;
    _setState(ServerConnectionState.disconnected);
  }

  /// Send a command to the server. Returns false if not currently connected.
  bool send(Map<String, dynamic> message) {
    if (_state != ServerConnectionState.connected || _channel == null) {
      return false;
    }
    _channel!.sink.add(jsonEncode(message));
    return true;
  }

  Future<void> _open(int gen) async {
    // A reconnect Timer that already fired can't be unscheduled by
    // Timer.cancel() — its callback is enqueued — so _open may run after
    // teardown or after a newer connect(). Bail if teardown has begun, or if a
    // later connect/disconnect/dispose has superseded this attempt (stale
    // generation); otherwise a stale callback would open a duplicate socket and
    // orphan _channel/_subscription. Mirrors the _stopRequested guard in
    // _onError/_onDone/_scheduleReconnect.
    if (_stopRequested || gen != _generation) return;

    final uri = _uri;
    if (uri == null) return;

    // Defensively drop any existing connection before opening a new one. The
    // generation guard alone doesn't fully cover concurrent connect() calls
    // (each increments _generation right before its own synchronous _open, so
    // both can pass the guard); closing here ensures a second open can never
    // orphan a live socket/subscription still feeding the messages stream.
    _subscription?.cancel();
    _subscription = null;
    _channel?.sink.close();
    _channel = null;

    _setState(ServerConnectionState.connecting);

    final WebSocketChannel channel;
    try {
      channel = _connectChannel(uri);
    } catch (e) {
      // Synchronous construction failure (e.g. a bad URI that slipped past
      // validation). Async connect failures surface via channel.ready below.
      _lastError = e.toString();
      _setState(ServerConnectionState.error);
      _scheduleReconnect();
      return;
    }
    _channel = channel;

    // Gate `connected` on the socket actually opening (channel.ready, available
    // in web_socket_channel 3.x). WebSocketChannel.connect() returns without
    // throwing even when the server is unreachable, so without this the status
    // would flash "connected" against a down server every retry. ready throwing
    // is the connect failure; we reconnect from here. (A fresh connect() resets
    // the backoff; an automatic retry must not, so the delay can escalate.)
    try {
      await channel.ready;
    } catch (e) {
      // The connect attempt failed. Close the dead channel and drop our
      // reference either way — otherwise it lingers (open, still in _channel)
      // through the reconnect backoff, which can grow. In the superseded case a
      // newer attempt owns _channel, so don't null it out from under them.
      if (_stopRequested || gen != _generation) {
        await channel.sink.close();
        return;
      }
      _channel = null;
      await channel.sink.close();
      _lastError = e.toString();
      _setState(ServerConnectionState.error);
      _scheduleReconnect();
      return;
    }

    // A newer connect()/disconnect()/dispose() may have superseded this attempt
    // while we awaited readiness; if so, drop this socket without wiring it up.
    if (_stopRequested || gen != _generation) {
      await channel.sink.close();
      return;
    }

    // Confirmed reachable: reset the backoff and start consuming frames. The
    // server sends `hello` first and clients MUST NOT send before receiving it
    // (protocol.md §1); the Session enforces that send-gating. The single-
    // subscription stream buffers any frame that arrived during the ready await,
    // so listening now doesn't lose the immediate `hello`.
    _backoff = _initialBackoff;
    _subscription = channel.stream.listen(
      _onMessage,
      onError: _onError,
      onDone: _onDone,
      cancelOnError: true,
    );
    _setState(ServerConnectionState.connected);
  }

  void _onMessage(dynamic data) {
    // A frame can arrive after teardown has begun (subscription cancellation
    // is async); never add to a closed controller.
    if (_messages.isClosed) return;
    // Protocol v1 frames are text JSON (docs/protocol.md §2). Ignore non-String
    // frames rather than stringifying them: toString() on a binary List<int>
    // never yields valid JSON and would only fall through to the catch below.
    if (data is! String) return;
    try {
      final decoded = jsonDecode(data);
      if (decoded is Map<String, dynamic>) {
        _messages.add(decoded);
      }
    } catch (_) {
      // Drop malformed frames silently; the server is the source of truth
      // for message shape.
    }
  }

  void _onError(Object error, StackTrace _) {
    if (_stopRequested) return;
    _lastError = error.toString();
    _setState(ServerConnectionState.error);
    _scheduleReconnect();
  }

  void _onDone() {
    if (_stopRequested) return;
    _scheduleReconnect();
  }

  void _scheduleReconnect() {
    if (_stopRequested) return;
    _reconnectTimer?.cancel();
    _setState(ServerConnectionState.reconnecting);
    final delay = _backoff;
    _backoff = Duration(
      milliseconds: (_backoff.inMilliseconds * 2).clamp(
        _initialBackoff.inMilliseconds,
        _maxBackoff.inMilliseconds,
      ),
    );
    // Bump the generation so an earlier reconnect Timer that already fired (its
    // _open callback enqueued, immune to Timer.cancel()) goes stale and bails in
    // _open instead of triggering an extra open cycle.
    final gen = ++_generation;
    _reconnectTimer = Timer(delay, () => _open(gen));
  }

  void _setState(ServerConnectionState s) {
    if (_state == s) return;
    _state = s;
    notifyListeners();
  }

  @override
  void dispose() {
    // Synchronous teardown only — do not delegate to the async disconnect(),
    // which would let awaited work (and _setState -> notifyListeners) run
    // after super.dispose(). Cancellations are fire-and-forget here.
    _stopRequested = true;
    _generation++;
    _reconnectTimer?.cancel();
    _reconnectTimer = null;
    _subscription?.cancel();
    _subscription = null;
    _channel?.sink.close();
    _channel = null;
    _messages.close();
    super.dispose();
  }
}
