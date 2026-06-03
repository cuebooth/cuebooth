import 'dart:async';

import 'package:flutter/foundation.dart';

import 'app_state.dart';
import 'protocol.dart';
import 'server_connection.dart';

/// A user-facing notification surfaced by the session (a command rejection, an
/// out-of-band event, or a protocol error) for the UI to show transiently.
class SessionNotice {
  const SessionNotice(this.severity, this.message);
  final NoticeSeverity severity;
  final String message;
}

enum NoticeSeverity { info, warn, error }

/// Drives the client↔server protocol on top of a transport: it folds inbound
/// frames into [state], tracks the `hello` handshake, and sends commands.
///
/// It depends on an inbound frame [Stream] and an [outbound] send function
/// rather than the concrete [ServerConnection] so it can be unit-tested without
/// a socket; [Session.forConnection] wires it to a real connection.
class Session extends ChangeNotifier {
  Session({
    required Stream<Map<String, dynamic>> inbound,
    required bool Function(Map<String, dynamic>) outbound,
    // An initializing formal can't be used: the field is private, and named
    // parameters can't be private.
    // ignore: prefer_initializing_formals
  }) : _outbound = outbound,
       state = AppState() {
    _sub = inbound.listen(_onFrame);
  }

  /// Wires a session to a live [ServerConnection].
  factory Session.forConnection(ServerConnection connection) =>
      Session(inbound: connection.messages, outbound: connection.send);

  final bool Function(Map<String, dynamic>) _outbound;
  late final StreamSubscription<Map<String, dynamic>> _sub;

  /// The mirrored server state.
  final AppState state;

  // hello handshake (protocol.md §1)
  bool _ready = false;
  String? _serverVersion;
  String? _serverId;
  bool _protoIncompatible = false;

  /// True once a `hello` with a compatible major version has been received.
  /// Clients MUST NOT send command frames before this (protocol.md §1).
  bool get ready => _ready;
  String? get serverVersion => _serverVersion;
  String? get serverId => _serverId;

  /// True if the server's protocol major version differs from the client's;
  /// the session refuses to operate (protocol.md §1).
  bool get protocolIncompatible => _protoIncompatible;

  final _notices = StreamController<SessionNotice>.broadcast();

  /// Transient, user-facing notices (naks, events, protocol errors) for the UI.
  Stream<SessionNotice> get notices => _notices.stream;

  int _cmdSeq = 0;

  void _onFrame(Map<String, dynamic> frame) {
    switch (frame['type']) {
      case FrameType.hello:
        _onHello(frame);
      case FrameType.state:
        _onState(frame);
      case FrameType.stateDelta:
        _onDelta(frame);
      case FrameType.nak:
        _emit(NoticeSeverity.error, _errorText(frame['error'], 'command rejected'));
      case FrameType.event:
        _onEvent(frame);
      case FrameType.error:
        _emit(NoticeSeverity.error, _errorText(frame, 'protocol error'));
      case FrameType.ack:
      case FrameType.pong:
        break; // acks/pongs need no UI action in v1
      default:
        break; // unknown types are ignored (protocol.md §2/§7)
    }
  }

  void _onHello(Map<String, dynamic> frame) {
    _serverVersion = frame['server_version'] as String?;
    _serverId = frame['server_id'] as String?;
    final major = protoMajor(frame['proto'] as String?);
    if (major != clientProtoMajor) {
      _protoIncompatible = true;
      _ready = false;
      _emit(
        NoticeSeverity.error,
        'Incompatible server protocol (${frame['proto']}); this client speaks $protoVersion.',
      );
      notifyListeners();
      return;
    }
    _protoIncompatible = false;
    _ready = true;
    notifyListeners();
  }

  void _onState(Map<String, dynamic> frame) {
    final rev = (frame['rev'] as num?)?.toInt() ?? 0;
    final topicsData = <String, dynamic>{};
    for (final entry in frame.entries) {
      if (entry.key != 'type' && entry.key != 'rev') {
        topicsData[entry.key] = entry.value;
      }
    }
    state.applySnapshot(rev, topicsData);
  }

  void _onDelta(Map<String, dynamic> frame) {
    final rev = (frame['rev'] as num?)?.toInt() ?? 0;
    final patch = frame['patch'];
    if (patch is! Map<String, dynamic>) return;
    if (state.applyDelta(rev, patch) == DeltaOutcome.gap) {
      // A dropped frame: re-sync with a fresh snapshot (protocol.md §4).
      _outbound({'type': FrameType.getState});
    }
  }

  void _onEvent(Map<String, dynamic> frame) {
    final sev = switch (frame['severity']) {
      'error' => NoticeSeverity.error,
      'warn' => NoticeSeverity.warn,
      _ => NoticeSeverity.info,
    };
    final msg = frame['message'];
    if (msg is String) _emit(sev, msg);
  }

  /// Sends a `cmd` frame and returns its correlation id, or null if the session
  /// isn't ready (no `hello` yet, or an incompatible server). [value] may be a
  /// string, number, bool, map, or null per the action (protocol.md §5).
  String? sendCommand(
    String target,
    String action, {
    Object? value,
    String? cameraId,
  }) {
    if (!_ready || _protoIncompatible) {
      _emit(NoticeSeverity.warn, 'Not connected to the server yet.');
      return null;
    }
    final id = 'c${++_cmdSeq}';
    final frame = <String, dynamic>{
      'type': FrameType.cmd,
      'id': id,
      'target': target,
      'action': action,
    };
    if (value != null) frame['value'] = value;
    if (cameraId != null) frame['camera_id'] = cameraId;
    _outbound(frame);
    return id;
  }

  /// Marks the session not-ready (e.g. on transport drop), so the UI gates
  /// command sends until the next `hello` and stale state isn't treated as live.
  void handleDisconnected() {
    if (!_ready && !_protoIncompatible) return;
    _ready = false;
    _protoIncompatible = false;
    state.reset();
    notifyListeners();
  }

  void _emit(NoticeSeverity severity, String message) {
    if (!_notices.isClosed) _notices.add(SessionNotice(severity, message));
  }

  String _errorText(Object? error, String fallback) {
    if (error is Map && error['message'] is String) {
      return error['message'] as String;
    }
    return fallback;
  }

  @override
  void dispose() {
    _sub.cancel();
    _notices.close();
    state.dispose();
    super.dispose();
  }
}
