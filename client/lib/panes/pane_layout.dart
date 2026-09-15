import 'dart:async';
import 'dart:convert';

import 'package:flutter/foundation.dart';
import 'package:shared_preferences/shared_preferences.dart';

import 'pane.dart';

/// Bounds on the fraction of the window a pinned edge pane may be dragged to.
///
/// Dragged to zero a pane is still present but unreadable, with no handle left
/// to drag it back; past the upper bound it starves the centre.
const double minPaneFraction = 0.15;
const double maxPaneFraction = 0.6;
const double defaultPaneFraction = 0.3;

/// The operator's arrangement: which panes are pinned into the layout, which
/// wait behind a tab, and how large the pinned ones are.
///
/// Layout belongs to the client and to this device — a booth tablet keeps its
/// arrangement and the server neither stores nor knows it (design.md §3.5).
class PaneLayout extends ChangeNotifier {
  PaneLayout({required List<PaneSpec> panes, SharedPreferences? prefs})
    : assert(
        panes.where((p) => p.fillsCentre).length <= 1,
        'the centre is what the edges leave over, so a second pane claiming it '
        'would render nowhere and offer no tab to recover it',
      ),
      assert(
        panes.map((p) => p.id).toSet().length == panes.length,
        'pane ids key the persisted layout, so a duplicate silently replaces '
        'the pane it collides with',
      ),
      _panes = {for (final p in panes) p.id: p},
      _prefs = prefs {
    for (final pane in panes) {
      _pinned[pane.id] = pane.startsPinned;
    }
  }

  static const _storageKey = 'pane_layout_v1';

  final Map<String, PaneSpec> _panes;
  final Map<String, bool> _pinned = {};
  final Map<String, double> _fractions = {};

  /// Panes the deployment does not offer — a server with no chat provider gets
  /// no chat pane and no tab for one, rather than a pane that can only explain
  /// itself. Not persisted: it is the server's answer, not the operator's
  /// arrangement.
  final Set<String> _disabled = {};

  /// Unpinned panes currently summoned. Deliberately not persisted: a pane put
  /// away with a tap should not come back on the next launch, which is how a
  /// tool window behaves in an IDE.
  final Set<String> _summoned = {};

  SharedPreferences? _prefs;

  /// Panes the operator has moved. A [load] resolving afterwards must not
  /// overwrite those gestures, but the panes they did not touch still want
  /// what was stored.
  final Set<String> _rearranged = {};

  bool _disposed = false;

  bool _writing = false;
  bool _unsaved = false;

  /// Writes made, for tests that care that a drag does not make one per frame.
  @visibleForTesting
  int writeCount = 0;

  @override
  void dispose() {
    _disposed = true;
    // A change made in the last moments is still the operator's.
    if (_unsaved) unawaited(save());
    super.dispose();
  }

  /// Coalesces writes. A divider drag lands a change per frame, and each write
  /// is a platform round trip and a file write; only where the divider came to
  /// rest is worth storing. Changes arriving while a write is in flight ride on
  /// the one that follows it rather than queueing a write each.
  void _saveSoon() {
    _unsaved = true;
    if (_writing) return;
    unawaited(_drainSaves());
  }

  Future<void> _drainSaves() async {
    _writing = true;
    while (_unsaved) {
      _unsaved = false;
      await save();
    }
    _writing = false;
  }

  /// [load] and [save] await a platform channel, so the layout can outlive the
  /// screen that built it.
  void _notify() {
    if (_disposed) return;
    notifyListeners();
  }

  Iterable<PaneSpec> get panes => _panes.values;

  PaneSpec? spec(String id) => _panes[id];

  bool isPinned(String id) => (_pinned[id] ?? false) && isEnabled(id);

  bool isEnabled(String id) => !_disabled.contains(id);

  /// Whether the pane is on screen: pinned panes always are, unpinned ones only
  /// while summoned.
  bool isVisible(String id) =>
      isEnabled(id) && (isPinned(id) || _summoned.contains(id));

  /// Declares whether the deployment offers this pane at all.
  void setEnabled(String id, bool enabled) {
    if (!_panes.containsKey(id)) return;
    final changed = enabled ? _disabled.remove(id) : _disabled.add(id);
    if (!changed) return;
    if (!enabled) _summoned.remove(id);
    _notify();
  }

  /// The pane filling the centre, if one is pinned there.
  PaneSpec? get centrePane {
    for (final pane in _panes.values) {
      if (pane.fillsCentre && isPinned(pane.id)) return pane;
    }
    return null;
  }

  /// Panes pinned to [edge], in registration order. The centre pane is not one
  /// of them even though it carries an edge for when it is unpinned.
  List<PaneSpec> pinnedAt(PaneEdge edge) => [
    for (final pane in _panes.values)
      if (!pane.fillsCentre && pane.edge == edge && isPinned(pane.id)) pane,
  ];

  /// Unpinned panes belonging to [edge], summoned or not. These are what the
  /// edge's tab strip offers.
  List<PaneSpec> tabsAt(PaneEdge edge) => [
    for (final pane in _panes.values)
      if (pane.edge == edge && isEnabled(pane.id) && !isPinned(pane.id)) pane,
  ];

  /// Unpinned panes currently floating over the dock.
  List<PaneSpec> get floating => [
    for (final pane in _panes.values)
      if (isEnabled(pane.id) &&
          !isPinned(pane.id) &&
          _summoned.contains(pane.id))
        pane,
  ];

  double fractionOf(String id) => _fractions[id] ?? defaultPaneFraction;

  void setFraction(String id, double value) {
    final clamped = value.clamp(minPaneFraction, maxPaneFraction);
    if (_fractions[id] == clamped) return;
    _fractions[id] = clamped;
    _rearranged.add(id);
    _notify();
    _saveSoon();
  }

  /// Pins a floating pane into the layout, or releases a pinned one to its tab.
  ///
  /// Unpinning retracts the pane rather than leaving it hovering over the space
  /// it just vacated; the operator summons it again from the tab.
  void togglePin(String id) {
    if (!_panes.containsKey(id)) return;
    final nowPinned = !isPinned(id);
    _pinned[id] = nowPinned;
    _summoned.remove(id);
    _rearranged.add(id);
    _notify();
    _saveSoon();
  }

  /// Summons an unpinned pane, or puts it away. Pinned panes ignore this: they
  /// are on screen by virtue of being pinned.
  void toggleSummoned(String id) {
    if (isPinned(id) || !_panes.containsKey(id)) return;
    if (!_summoned.remove(id)) _summoned.add(id);
    _notify();
  }

  /// Reads the stored arrangement, once.
  ///
  /// Held so [save] can wait for it: a gesture made while this is in flight
  /// would otherwise write the current defaults over the stored layout before
  /// it had been read, and the read would then find only what the gesture wrote.
  Future<void>? _loading;

  Future<void> load() => _loading ??= _load();

  Future<void> _load() async {
    final prefs = _prefs ??= await SharedPreferences.getInstance();
    if (_disposed) return;
    final raw = prefs.getString(_storageKey);
    if (raw == null) return;

    Object? decoded;
    try {
      decoded = jsonDecode(raw);
    } on FormatException {
      // An unreadable layout is not worth failing a launch over; the defaults
      // are always usable.
      return;
    }
    if (decoded is! Map) return;

    final pinned = decoded['pinned'];
    if (pinned is Map) {
      for (final entry in pinned.entries) {
        final id = entry.key;
        final value = entry.value;
        if (id is! String || value is! bool || !_panes.containsKey(id)) continue;
        if (_rearranged.contains(id)) continue;
        _pinned[id] = value;
      }
    }

    final fractions = decoded['fractions'];
    if (fractions is Map) {
      for (final entry in fractions.entries) {
        final id = entry.key;
        final value = entry.value;
        if (id is! String || value is! num || !_panes.containsKey(id)) continue;
        if (_rearranged.contains(id)) continue;
        _fractions[id] = value.toDouble().clamp(
          minPaneFraction,
          maxPaneFraction,
        );
      }
    }

    _notify();
  }

  Future<void> save() async {
    try {
      await _loading;
      final prefs = _prefs ??= await SharedPreferences.getInstance();
      writeCount++;
      await prefs.setString(
        _storageKey,
        jsonEncode({'pinned': _pinned, 'fractions': _fractions}),
      );
    } on Exception {
      // Persistence is a convenience; failing it must not break a gesture or
      // take the layout down with it.
    }
  }
}
