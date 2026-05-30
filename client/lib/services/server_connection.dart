import 'dart:async';
import 'dart:convert';

import 'package:flutter/foundation.dart';
import 'package:web_socket_channel/web_socket_channel.dart';

/// Connection state to the CueBooth server.
enum ConnectionState {
  disconnected,
  connecting,
  connected,
  reconnecting,
  error,
}

/// Manages the WebSocket connection to the CueBooth server.
///
/// Authority lies with the server: clients send commands and receive state
/// broadcasts. The transport reconnects with exponential backoff on drops.
/// See ../../../docs/design.md §3.6 (Communication Protocol) and
/// ../../../docs/protocol.md for the wire spec.
class ServerConnection extends ChangeNotifier {
  ServerConnection();

  static const Duration _initialBackoff = Duration(seconds: 1);
  static const Duration _maxBackoff = Duration(seconds: 30);

  Uri? _uri;
  WebSocketChannel? _channel;
  StreamSubscription<dynamic>? _subscription;
  Duration _backoff = _initialBackoff;
  Timer? _reconnectTimer;
  bool _stopRequested = false;

  ConnectionState _state = ConnectionState.disconnected;
  ConnectionState get state => _state;

  String? _lastError;
  String? get lastError => _lastError;

  final _messages = StreamController<Map<String, dynamic>>.broadcast();
  Stream<Map<String, dynamic>> get messages => _messages.stream;

  /// Open a connection to the given host and port. Calling [connect] while a
  /// connection is open closes the previous one first.
  Future<void> connect(String host, int port) async {
    await disconnect();
    _stopRequested = false;
    _uri = Uri(scheme: 'ws', host: host, port: port, path: '/ws');
    _backoff = _initialBackoff;
    _open();
  }

  /// Cleanly close the connection and stop any reconnect attempts.
  Future<void> disconnect() async {
    _stopRequested = true;
    _reconnectTimer?.cancel();
    _reconnectTimer = null;
    await _subscription?.cancel();
    _subscription = null;
    await _channel?.sink.close();
    _channel = null;
    _setState(ConnectionState.disconnected);
  }

  /// Send a command to the server. Returns false if not currently connected.
  bool send(Map<String, dynamic> message) {
    if (_state != ConnectionState.connected || _channel == null) return false;
    _channel!.sink.add(jsonEncode(message));
    return true;
  }

  void _open() {
    final uri = _uri;
    if (uri == null) return;

    _setState(ConnectionState.connecting);
    try {
      final channel = WebSocketChannel.connect(uri);
      _channel = channel;
      _subscription = channel.stream.listen(
        _onMessage,
        onError: _onError,
        onDone: _onDone,
        cancelOnError: true,
      );
      // Optimistically mark connected once the socket opens. If the handshake
      // fails the error handler downgrades the state.
      //
      // NOTE (protocol.md §1): the server sends a `hello` frame first, and
      // clients MUST NOT send any frame on /ws until they receive it. Gating
      // `connected` on receipt of `hello` (and resetting backoff there) is
      // wired in CB-014 alongside the command UI; until then nothing calls
      // send(), so the optimistic state here is safe as a placeholder.
      _backoff = _initialBackoff;
      _setState(ConnectionState.connected);
    } catch (e) {
      _lastError = e.toString();
      _setState(ConnectionState.error);
      _scheduleReconnect();
    }
  }

  void _onMessage(dynamic data) {
    try {
      final decoded = jsonDecode(data is String ? data : data.toString());
      if (decoded is Map<String, dynamic>) {
        _messages.add(decoded);
      }
    } catch (_) {
      // Drop malformed frames silently; the server is the source of truth
      // for message shape.
    }
  }

  void _onError(Object error, StackTrace _) {
    _lastError = error.toString();
    _setState(ConnectionState.error);
    _scheduleReconnect();
  }

  void _onDone() {
    if (_stopRequested) return;
    _scheduleReconnect();
  }

  void _scheduleReconnect() {
    if (_stopRequested) return;
    _reconnectTimer?.cancel();
    _setState(ConnectionState.reconnecting);
    final delay = _backoff;
    _backoff = Duration(
      milliseconds: (_backoff.inMilliseconds * 2).clamp(
        _initialBackoff.inMilliseconds,
        _maxBackoff.inMilliseconds,
      ),
    );
    _reconnectTimer = Timer(delay, _open);
  }

  void _setState(ConnectionState s) {
    if (_state == s) return;
    _state = s;
    notifyListeners();
  }

  @override
  void dispose() {
    disconnect();
    _messages.close();
    super.dispose();
  }
}
