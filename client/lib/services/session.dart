import 'dart:async';

import 'package:flutter/foundation.dart';

import 'app_state.dart';
import 'protocol.dart';
import 'server_connection.dart';
import 'surface_state.dart';

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
       state = AppState(),
       surface = SurfaceState() {
    _sub = inbound.listen(_onFrame);
  }

  /// Wires a session to a live [ServerConnection].
  factory Session.forConnection(ServerConnection connection) =>
      Session(inbound: connection.messages, outbound: connection.send);

  final bool Function(Map<String, dynamic>) _outbound;
  late final StreamSubscription<Map<String, dynamic>> _sub;

  /// The mirrored server state.
  final AppState state;

  /// The mirrored Companion Satellite button surface (protocol.md §10).
  final SurfaceState surface;

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

  // True between detecting a delta gap and applying the resync snapshot, so only
  // one get_state is sent per gap episode.
  bool _awaitingResync = false;

  void _onFrame(Map<String, dynamic> frame) {
    switch (frame['type']) {
      case FrameType.hello:
        _onHello(frame);
      case FrameType.state:
        _onState(frame);
      case FrameType.stateDelta:
        _onDelta(frame);
      case FrameType.surfaceLayout:
        _onSurfaceLayout(frame);
      case FrameType.surfaceKey:
        _onSurfaceKey(frame);
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
    _awaitingResync = false; // the snapshot is the resync; resume gap detection
  }

  void _onDelta(Map<String, dynamic> frame) {
    final rev = (frame['rev'] as num?)?.toInt() ?? 0;
    final patch = frame['patch'];
    if (patch is! Map<String, dynamic>) return;
    // Only request a resync once ready: clients MUST NOT send before `hello`
    // (protocol.md §1). A delta can't legitimately precede hello+state, but this
    // keeps us from emitting get_state if frames ever arrive out of order.
    if (_ready &&
        state.applyDelta(rev, patch) == DeltaOutcome.gap &&
        !_awaitingResync) {
      // A dropped frame: re-sync with a fresh snapshot (protocol.md §4). Latch so
      // a burst of further gapped deltas before the snapshot lands doesn't fire
      // a get_state per frame; cleared when the snapshot arrives (_onState).
      _awaitingResync = true;
      _outbound({'type': FrameType.getState});
    }
  }

  void _onSurfaceLayout(Map<String, dynamic> frame) {
    surface.applyLayout(
      (frame['rows'] as num?)?.toInt() ?? 0,
      (frame['cols'] as num?)?.toInt() ?? 0,
      (frame['bitmap_size'] as num?)?.toInt() ?? 0,
    );
  }

  void _onSurfaceKey(Map<String, dynamic> frame) {
    final key = (frame['key'] as num?)?.toInt();
    if (key == null) return;
    surface.applyKey(
      key: key,
      seq: (frame['seq'] as num?)?.toInt() ?? 0,
      row: (frame['row'] as num?)?.toInt() ?? 0,
      col: (frame['col'] as num?)?.toInt() ?? 0,
      keyType: frame['key_type'] as String? ?? 'BUTTON',
      pressed: frame['pressed'] as bool? ?? false,
      color: frame['color'] as String?,
      bitmapBase64: frame['bitmap'] as String?,
    );
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
    if (!_outbound(frame)) {
      // ready but the socket dropped in the gap before handleDisconnected fired.
      _emit(NoticeSeverity.warn, 'Command not sent — connection lost.');
      return null;
    }
    return id;
  }

  /// Presses (or releases) a Companion surface key (protocol.md §10). A normal
  /// tap is a press (true) followed by a release (false). Gated on `ready` like
  /// [sendCommand]; returns false if the press could not be sent. Unlike
  /// `sendCommand`, a dropped press emits no notice — presses are high-frequency
  /// and the surface visibly stops updating when the connection is gone.
  bool sendSurfacePress(int key, bool pressed) {
    if (!_ready || _protoIncompatible) return false;
    return _outbound({
      'type': FrameType.surfacePress,
      'key': key,
      'pressed': pressed,
    });
  }

  /// Marks the session not-ready (e.g. on transport drop), so the UI gates
  /// command sends until the next `hello` and stale state isn't treated as live.
  void handleDisconnected() {
    if (!_ready && !_protoIncompatible) return;
    _ready = false;
    _protoIncompatible = false;
    _awaitingResync = false;
    state.reset();
    surface.reset();
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
    surface.dispose();
    super.dispose();
  }
}
