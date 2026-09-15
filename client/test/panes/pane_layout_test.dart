import 'package:cuebooth_client/panes/pane.dart';
import 'package:cuebooth_client/panes/pane_layout.dart';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:shared_preferences/shared_preferences.dart';

PaneSpec _pane(
  String id, {
  PaneEdge edge = PaneEdge.right,
  bool fillsCentre = false,
  bool startsPinned = true,
}) => PaneSpec(
  id: id,
  title: id,
  icon: Icons.square,
  edge: edge,
  fillsCentre: fillsCentre,
  startsPinned: startsPinned,
  builder: (_) => const SizedBox.shrink(),
);

Future<PaneLayout> _layout(List<PaneSpec> panes) async {
  SharedPreferences.setMockInitialValues({});
  return PaneLayout(panes: panes, prefs: await SharedPreferences.getInstance());
}

void main() {
  group('placement', () {
    test('startsPinned decides the arrangement before anything is saved', () async {
      final layout = await _layout([
        _pane('grid', fillsCentre: true),
        _pane('chat'),
        _pane('levels', startsPinned: false),
      ]);

      expect(layout.isPinned('grid'), isTrue);
      expect(layout.isPinned('chat'), isTrue);
      expect(layout.isPinned('levels'), isFalse);
      // An unpinned pane waits behind its tab rather than opening over the dock.
      expect(layout.isVisible('levels'), isFalse);
    });

    test('unpinning retracts the pane to its tab', () async {
      final layout = await _layout([_pane('chat')]);

      layout.togglePin('chat');

      expect(layout.isPinned('chat'), isFalse);
      expect(layout.isVisible('chat'), isFalse);
      expect(layout.tabsAt(PaneEdge.right).map((p) => p.id), ['chat']);
      expect(layout.pinnedAt(PaneEdge.right), isEmpty);
    });

    test('pinning a summoned pane puts it back in the layout', () async {
      final layout = await _layout([_pane('chat', startsPinned: false)]);
      layout.toggleSummoned('chat');
      expect(layout.floating.map((p) => p.id), ['chat']);

      layout.togglePin('chat');

      expect(layout.isPinned('chat'), isTrue);
      // It is in the dock now, so it must not also be floating over it.
      expect(layout.floating, isEmpty);
      expect(layout.pinnedAt(PaneEdge.right).map((p) => p.id), ['chat']);
    });

    test('summoning does nothing to a pinned pane', () async {
      final layout = await _layout([_pane('chat')]);

      layout.toggleSummoned('chat');

      expect(layout.isPinned('chat'), isTrue);
      expect(layout.floating, isEmpty);
    });

    test('the centre pane is not also an edge pane', () async {
      final layout = await _layout([
        _pane('grid', edge: PaneEdge.left, fillsCentre: true),
      ]);

      expect(layout.centrePane?.id, 'grid');
      expect(layout.pinnedAt(PaneEdge.left), isEmpty);
    });

    test('unpinning the centre pane empties the centre', () async {
      final layout = await _layout([
        _pane('grid', edge: PaneEdge.left, fillsCentre: true),
      ]);

      layout.togglePin('grid');

      expect(layout.centrePane, isNull);
      expect(layout.tabsAt(PaneEdge.left).map((p) => p.id), ['grid']);
    });
  });

  group('sizing', () {
    test('a fraction dragged past its bounds is clamped, not applied', () async {
      final layout = await _layout([_pane('chat')]);

      layout.setFraction('chat', 0.99);
      expect(layout.fractionOf('chat'), maxPaneFraction);

      layout.setFraction('chat', -1);
      expect(layout.fractionOf('chat'), minPaneFraction);
    });
  });

  group('availability', () {
    test('a disabled pane offers neither a pane nor a tab', () async {
      final layout = await _layout([_pane('chat')]);

      layout.setEnabled('chat', false);

      expect(layout.isPinned('chat'), isFalse);
      expect(layout.isVisible('chat'), isFalse);
      expect(layout.pinnedAt(PaneEdge.right), isEmpty);
      expect(layout.tabsAt(PaneEdge.right), isEmpty);
    });

    test('re-enabling restores the arrangement rather than a default', () async {
      final layout = await _layout([_pane('chat')]);
      layout.togglePin('chat'); // unpinned by the operator
      layout.setEnabled('chat', false);

      layout.setEnabled('chat', true);

      expect(layout.isPinned('chat'), isFalse);
      expect(layout.tabsAt(PaneEdge.right).map((p) => p.id), ['chat']);
    });

    test('disabling puts away a summoned pane', () async {
      final layout = await _layout([_pane('chat', startsPinned: false)]);
      layout.toggleSummoned('chat');

      layout.setEnabled('chat', false);

      expect(layout.floating, isEmpty);
    });
  });

  group('persistence', () {
    test('pins and sizes survive a restart', () async {
      SharedPreferences.setMockInitialValues({});
      final prefs = await SharedPreferences.getInstance();
      final panes = [_pane('grid', fillsCentre: true), _pane('chat')];

      final first = PaneLayout(panes: panes, prefs: prefs);
      first.togglePin('chat');
      first.setFraction('chat', 0.42);
      await first.save();

      final second = PaneLayout(panes: panes, prefs: prefs);
      await second.load();

      expect(second.isPinned('chat'), isFalse);
      expect(second.fractionOf('chat'), closeTo(0.42, 1e-9));
      expect(second.isPinned('grid'), isTrue);
    });

    test('a summoned pane does not reopen on the next launch', () async {
      SharedPreferences.setMockInitialValues({});
      final prefs = await SharedPreferences.getInstance();
      final panes = [_pane('chat', startsPinned: false)];

      final first = PaneLayout(panes: panes, prefs: prefs);
      first.toggleSummoned('chat');
      await first.save();

      final second = PaneLayout(panes: panes, prefs: prefs);
      await second.load();

      expect(second.isVisible('chat'), isFalse);
    });

    test('unreadable stored layout falls back to defaults', () async {
      SharedPreferences.setMockInitialValues({'pane_layout_v1': 'not json'});
      final layout = PaneLayout(
        panes: [_pane('chat')],
        prefs: await SharedPreferences.getInstance(),
      );

      await layout.load();

      expect(layout.isPinned('chat'), isTrue);
    });

    test('entries for panes that no longer exist are ignored', () async {
      SharedPreferences.setMockInitialValues({
        'pane_layout_v1':
            '{"pinned":{"ghost":false},"fractions":{"ghost":0.5}}',
      });
      final layout = PaneLayout(
        panes: [_pane('chat')],
        prefs: await SharedPreferences.getInstance(),
      );

      await layout.load();

      expect(layout.isPinned('chat'), isTrue);
      expect(layout.spec('ghost'), isNull);
    });

    test('a stored layout pinning two panes to the centre keeps one', () async {
      SharedPreferences.setMockInitialValues({
        'pane_layout_v1': '{"pinned":{"a":true,"b":true},"fractions":{}}',
      });
      final layout = PaneLayout(
        panes: [_pane('a', fillsCentre: true), _pane('b', fillsCentre: true)],
        prefs: await SharedPreferences.getInstance(),
      );

      await layout.load();

      // Whichever it keeps, exactly one occupies the centre — the other has
      // nowhere to render and is released to its tab.
      expect(layout.centrePane, isNotNull);
      expect(
        [layout.isPinned('a'), layout.isPinned('b')].where((p) => p).length,
        1,
      );
    });
  });
}
