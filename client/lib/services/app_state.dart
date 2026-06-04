import 'package:flutter/foundation.dart';

import 'protocol.dart';

/// The client's mirror of the server's authoritative state (protocol.md §4).
///
/// The server is the source of truth: this holds the latest `state` snapshot and
/// folds in each `state-delta`. State is kept as the decoded JSON map with typed
/// accessors on top, mirroring the server's generic representation, so new
/// topics/fields surface without model churn.
class AppState extends ChangeNotifier {
  Map<String, dynamic> _data = {};
  int _rev = 0;
  bool _haveBaseline = false;

  /// The current revision (protocol.md §4). 0 until the first snapshot.
  int get rev => _rev;

  /// Whether a `state` snapshot has been received yet.
  bool get hasBaseline => _haveBaseline;

  /// Read-only view of the raw state map (for tests/diagnostics).
  Map<String, dynamic> get raw => Map.unmodifiable(_data);

  /// Applies a full `state` snapshot. A snapshot is the authoritative, complete
  /// state for the client's subscription, so it replaces the state wholesale: a
  /// topic absent from the snapshot is no longer present, not retained stale.
  /// This becomes the new baseline revision.
  void applySnapshot(int rev, Map<String, dynamic> topicsData) {
    _data = Map<String, dynamic>.of(topicsData);
    _rev = rev;
    _haveBaseline = true;
    notifyListeners();
  }

  /// Applies a sparse `state-delta` (JSON-Merge-Patch). Returns a result telling
  /// the caller whether it was applied or a re-sync is needed:
  ///   - [DeltaOutcome.applied]: contiguous (`rev == current+1`), folded in.
  ///   - [DeltaOutcome.stale]: `rev <= current` (duplicate/out-of-order), ignored.
  ///   - [DeltaOutcome.gap]: `rev > current+1` (dropped frame) — caller should
  ///     request a fresh snapshot via `get_state` (protocol.md §4).
  DeltaOutcome applyDelta(int rev, Map<String, dynamic> patch) {
    if (!_haveBaseline) {
      return DeltaOutcome.gap; // no baseline yet → resync
    }
    if (rev <= _rev) {
      return DeltaOutcome.stale;
    }
    if (rev != _rev + 1) {
      return DeltaOutcome.gap;
    }
    applyMergePatch(_data, patch);
    _rev = rev;
    notifyListeners();
    return DeltaOutcome.applied;
  }

  /// Clears all state (e.g. on disconnect) so stale data isn't shown as live.
  void reset() {
    if (_data.isEmpty && !_haveBaseline && _rev == 0) return;
    _data = {};
    _rev = 0;
    _haveBaseline = false;
    notifyListeners();
  }

  // ---- typed accessors (protocol.md §4) ----

  Map<String, dynamic>? _topic(String name) {
    final v = _data[name];
    return v is Map<String, dynamic> ? v : null;
  }

  /// The active OBS scene preset name, or null if unknown.
  String? get obsScene => _topic('obs')?['scene'] as String?;

  /// Whether OBS is streaming. Null if unknown (no obs state yet).
  bool? get streaming => _topic('obs')?['streaming'] as bool?;

  /// Whether OBS is recording. Null if unknown.
  bool? get recording => _topic('obs')?['recording'] as bool?;

  /// The last-recalled preset for camera [id] (empty string = no active
  /// preset), or null if the camera is unknown.
  String? cameraPreset(String id) {
    final cam = _topic('camera')?[id];
    return cam is Map<String, dynamic> ? cam['preset'] as String? : null;
  }

  /// Streaming-platform metadata (protocol.md §4 `stream`), e.g. viewers.
  String? get streamPlatform => _topic('stream')?['platform'] as String?;
  int? get streamViewers => (_topic('stream')?['viewers'] as num?)?.toInt();
}

/// Outcome of applying a `state-delta`.
enum DeltaOutcome { applied, stale, gap }
