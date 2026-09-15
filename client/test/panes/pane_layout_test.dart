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
  final layout = PaneLayout(
    panes: panes,
    prefs: await SharedPreferences.getInstance(),
  );
  addTearDown(layout.dispose);
  return layout;
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

    test('a disabled pane cannot be summoned', () async {
      final layout = await _layout([_pane('chat', startsPinned: false)]);
      layout.setEnabled('chat', false);

      layout.toggleSummoned('chat');
      layout.setEnabled('chat', true);

      // Nothing renders a tab for a disabled pane, so a summons it accepted
      // could only surface later — the pane opening on its own when the server
      // reports a provider.
      expect(layout.isVisible('chat'), isFalse);
      expect(layout.floating, isEmpty);
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

    test('a layout resolving after disposal does not notify', () async {
      SharedPreferences.setMockInitialValues({
        'pane_layout_v1': '{"pinned":{"chat":false},"fractions":{}}',
      });
      // Deliberately without prefs: that is how the app builds it, and it is
      // the only arrangement where the read actually suspends. Injecting them
      // makes load() run start to finish before dispose() is even called.
      final layout = PaneLayout(panes: [_pane('chat')]);

      final pending = layout.load();
      layout.dispose();

      // The screen that built it can be popped before prefs resolve; notifying
      // a disposed notifier throws.
      await expectLater(pending, completes);
    });

    test('a drag writes a couple of times, not once per frame', () async {
      SharedPreferences.setMockInitialValues({});
      final prefs = await SharedPreferences.getInstance();
      final layout = PaneLayout(panes: [_pane('chat')], prefs: prefs);
      addTearDown(layout.dispose);
      await layout.load();

      // A drag lands a change per frame, and each write is a platform round
      // trip and a file write.
      for (var i = 0; i < 20; i++) {
        layout.setFraction('chat', 0.2 + i * 0.01);
      }
      await Future<void>.delayed(Duration.zero);
      await Future<void>.delayed(Duration.zero);

      expect(layout.writeCount, lessThanOrEqualTo(2));
      // And the value that survives is where the drag ended.
      expect(prefs.getString('pane_layout_v1'), contains('0.39'));
    });

    // The gesture's save waits on the read; if the read gives up on disposal
    // without merging, that save writes the defaults of every untouched pane.
    test('a gesture then disposal does not write defaults over the rest', () async {
      SharedPreferences.setMockInitialValues({
        'pane_layout_v1':
            '{"pinned":{"a":false,"b":false},"fractions":{"a":0.5,"b":0.55}}',
      });
      // Without injected prefs, so the read actually suspends — injecting them
      // makes it finish before the gesture and disposal it is racing.
      final layout = PaneLayout(panes: [_pane('a'), _pane('b')]);

      final pending = layout.load();
      layout.setFraction('b', 0.42);
      layout.dispose();
      await pending;
      await Future<void>.delayed(Duration.zero);
      await Future<void>.delayed(Duration.zero);

      final prefs = await SharedPreferences.getInstance();
      final stored = prefs.getString('pane_layout_v1')!;
      expect(stored, contains('"a":false'), reason: "'a' keeps its stored pin");
      expect(stored, contains('0.5'), reason: "'a' keeps its stored size");
    });

    // save() waits on the read so it cannot write over it — but if nothing had
    // asked for the read, there was nothing to wait on and the write landed on
    // top of the stored layout anyway.
    test('a save before any load does not discard what was stored', () async {
      SharedPreferences.setMockInitialValues({
        'pane_layout_v1': '{"pinned":{"a":false},"fractions":{"a":0.55}}',
      });
      final prefs = await SharedPreferences.getInstance();
      final layout = PaneLayout(panes: [_pane('a'), _pane('b')], prefs: prefs);
      addTearDown(layout.dispose);

      layout.togglePin('b'); // no load() first
      await layout.save();

      expect(prefs.getString('pane_layout_v1'), contains('0.55'));
    });

    // Nothing pinned the clamp applied to a stored value: removing it left the
    // whole suite green while a hand-edited or older file could hand the dock a
    // fraction it has no bounds for.
    test('a stored fraction outside the bounds is clamped', () async {
      SharedPreferences.setMockInitialValues({
        'pane_layout_v1': '{"pinned":{},"fractions":{"a":5.0,"b":-9.0}}',
      });
      final layout = PaneLayout(
        panes: [_pane('a'), _pane('b')],
        prefs: await SharedPreferences.getInstance(),
      );
      addTearDown(layout.dispose);

      await layout.load();

      expect(layout.fractionOf('a'), maxPaneFraction);
      expect(layout.fractionOf('b'), minPaneFraction);
    });

    // An Error is not an Exception, and the home screen gates its whole dock on
    // this future: an escape leaves the operator with no controls at all and no
    // explanation. A non-String under the key makes getString throw one.
    test('a read that throws an Error still settles', () async {
      SharedPreferences.setMockInitialValues({'flutter.pane_layout_v1': 12});
      final layout = PaneLayout(panes: [_pane('a')]);
      addTearDown(layout.dispose);

      await expectLater(layout.load(), completes);
      expect(layout.isPinned('a'), isTrue, reason: 'defaults are usable');
    });

    test('a fraction for something that is not a pane is not stored', () async {
      SharedPreferences.setMockInitialValues({});
      final prefs = await SharedPreferences.getInstance();
      final layout = PaneLayout(panes: [_pane('a')], prefs: prefs);
      addTearDown(layout.dispose);
      await layout.load();

      layout.setFraction('ghost', 0.4);
      await layout.save();

      expect(prefs.getString('pane_layout_v1'), isNot(contains('ghost')));
    });

    test('a change made as the layout goes away is still written', () async {
      SharedPreferences.setMockInitialValues({});
      final prefs = await SharedPreferences.getInstance();
      final layout = PaneLayout(panes: [_pane('chat')], prefs: prefs);
      await layout.load();

      layout.setFraction('chat', 0.42);
      layout.dispose();
      await Future<void>.delayed(Duration.zero);
      await Future<void>.delayed(Duration.zero);

      expect(prefs.getString('pane_layout_v1'), contains('0.42'));
    });

    test('a layout loaded after the operator moved something keeps the gesture',
        () async {
      SharedPreferences.setMockInitialValues({
        'pane_layout_v1': '{"pinned":{"chat":true},"fractions":{"chat":0.5}}',
      });
      final layout = PaneLayout(
        panes: [_pane('chat')],
        prefs: await SharedPreferences.getInstance(),
      );

      layout.togglePin('chat'); // the operator unpins before prefs resolve
      await layout.load();

      expect(layout.isPinned('chat'), isFalse);
    });

    // The gesture must not cost the rest of the arrangement: a wholesale bail
    // reverted every untouched pane to its default and the save queued behind
    // the gesture then wrote that loss to disk.
    test('a gesture during load costs only the pane it touched', () async {
      // 'b' is stored pinned so the gesture disagrees with the disk — stored
      // and gestured agreeing would make the first expectation unable to fail.
      SharedPreferences.setMockInitialValues({
        'pane_layout_v1':
            '{"pinned":{"a":false,"b":true},"fractions":{"a":0.5,"b":0.55}}',
      });
      final layout = PaneLayout(
        panes: [_pane('a'), _pane('b')],
        prefs: await SharedPreferences.getInstance(),
      );

      layout.togglePin('b'); // 'b' was pinned by default, so this unpins it
      await layout.load();

      expect(layout.isPinned('b'), isFalse, reason: 'the gesture stands');
      expect(layout.isPinned('a'), isFalse, reason: "'a' keeps what was stored");
      expect(layout.fractionOf('a'), closeTo(0.5, 1e-9));
    });
  });

  group('registration', () {
    test('two panes claiming the centre is rejected at registration', () {
      // The second would render nowhere and offer no tab to recover it.
      expect(
        () => PaneLayout(
          panes: [_pane('a', fillsCentre: true), _pane('b', fillsCentre: true)],
        ),
        throwsA(isA<AssertionError>()),
      );
    });
  });
}
