import 'dart:async';

import 'package:cuebooth_client/services/session.dart';
import 'package:cuebooth_client/widgets/surface_grid.dart';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';

void main() {
  const oneKey = {
    'type': 'surface-key',
    'key': 0,
    'seq': 1,
    'row': 0,
    'col': 0,
    'key_type': 'BUTTON',
    'pressed': false,
    'color': '#336699',
  };
  Future<(Session, List<Map<String, dynamic>>)> surfaceSession(
    WidgetTester tester, {
    required int rows,
    required int cols,
    List<Map<String, dynamic>> keys = const [],
  }) async {
    final inbound = StreamController<Map<String, dynamic>>();
    final sent = <Map<String, dynamic>>[];
    final session = Session(
      inbound: inbound.stream,
      outbound: (m) {
        sent.add(m);
        return true;
      },
    );
    addTearDown(() async {
      session.dispose();
      await inbound.close();
    });
    await tester.runAsync(() async {
      inbound.add({
        'type': 'hello',
        'proto': '1.0',
        'server_version': '0',
        'server_id': 'p',
      });
      inbound.add({
        'type': 'surface-layout',
        'rows': rows,
        'cols': cols,
        'seq': 0,
        'bitmap_size': 72,
      });
      for (final k in keys) {
        inbound.add(k);
      }
      await Future<void>.delayed(Duration.zero);
    });
    return (session, sent);
  }

  testWidgets('shows a waiting message before any layout', (tester) async {
    final inbound = StreamController<Map<String, dynamic>>();
    final session = Session(inbound: inbound.stream, outbound: (_) => true);
    addTearDown(() async {
      session.dispose();
      await inbound.close();
    });
    await tester.pumpWidget(
      MaterialApp(
        home: Scaffold(body: SurfaceGrid(session: session)),
      ),
    );
    await tester.pump();
    expect(
      find.textContaining('Waiting for the Companion surface'),
      findsOneWidget,
    );
  });

  testWidgets('renders only configured keys as interactive cells', (
    tester,
  ) async {
    // 1×2 grid with a button only at index 0; index 1 stays an inert placeholder.
    final (session, _) = await surfaceSession(
      tester,
      rows: 1,
      cols: 2,
      keys: [
        {
          'type': 'surface-key',
          'key': 0,
          'seq': 1,
          'row': 0,
          'col': 0,
          'key_type': 'BUTTON',
          'pressed': false,
          'color': '#336699',
        },
      ],
    );
    await tester.pumpWidget(
      MaterialApp(
        home: Scaffold(body: SurfaceGrid(session: session)),
      ),
    );
    await tester.pump();
    expect(find.byType(GestureDetector), findsOneWidget);
  });

  testWidgets('a tap sends a surface key press and release', (tester) async {
    final (session, sent) = await surfaceSession(
      tester,
      rows: 1,
      cols: 1,
      keys: [
        {
          'type': 'surface-key',
          'key': 0,
          'seq': 1,
          'row': 0,
          'col': 0,
          'key_type': 'BUTTON',
          'pressed': false,
          'color': '#336699',
        },
      ],
    );
    await tester.pumpWidget(
      MaterialApp(
        home: Scaffold(body: SurfaceGrid(session: session)),
      ),
    );
    await tester.pump();

    await tester.tap(find.byType(GestureDetector));
    await tester.pump();

    final presses = sent.where((m) => m['type'] == 'surface-press').toList();
    expect(presses.any((m) => m['key'] == 0 && m['pressed'] == true), isTrue);
    expect(presses.any((m) => m['key'] == 0 && m['pressed'] == false), isTrue);
  });

  testWidgets('a tap emits exactly one press and one release (no duplicates)', (
    tester,
  ) async {
    // _setDown only emits on an actual state transition, so a single tap yields
    // exactly one down and one up — gesture callbacks that repeat a state (e.g.
    // up then cancel) must not produce duplicate surface-press frames.
    final (session, sent) = await surfaceSession(
      tester,
      rows: 1,
      cols: 1,
      keys: [
        {
          'type': 'surface-key',
          'key': 0,
          'seq': 1,
          'row': 0,
          'col': 0,
          'key_type': 'BUTTON',
          'pressed': false,
          'color': '#336699',
        },
      ],
    );
    await tester.pumpWidget(
      MaterialApp(
        home: Scaffold(body: SurfaceGrid(session: session)),
      ),
    );
    await tester.pump();

    await tester.tap(find.byType(GestureDetector));
    await tester.pump();

    final presses = sent.where((m) => m['type'] == 'surface-press').toList();
    expect(presses.where((m) => m['pressed'] == true).length, 1);
    expect(presses.where((m) => m['pressed'] == false).length, 1);
  });

  testWidgets('the surface never scrolls, however many rows it has', (
    tester,
  ) async {
    // A scrollable grid would let a swipe run a cue: the tap recognizer reports
    // its press from its own 100ms deadline, before the gesture arena resolves,
    // so a scroll that began on a button has already delivered a press — and the
    // drag taking the pointer delivers the release behind it. Keeping every key
    // on screen removes the gesture that could be mistaken for navigation.
    final keys = [
      for (var i = 0; i < 80; i++)
        {
          'type': 'surface-key',
          'key': i,
          'seq': i + 1,
          'row': i ~/ 4,
          'col': i % 4,
          'key_type': 'BUTTON',
          'pressed': false,
          'color': '#336699',
        },
    ];
    final (session, _) = await surfaceSession(
      tester,
      rows: 20,
      cols: 4,
      keys: keys,
    );
    tester.view.physicalSize = const Size(400, 700);
    tester.view.devicePixelRatio = 1.0;
    addTearDown(tester.view.reset);
    await tester.pumpWidget(
      MaterialApp(
        home: Scaffold(body: SurfaceGrid(session: session)),
      ),
    );
    await tester.pump();

    final position = tester
        .state<ScrollableState>(find.byType(Scrollable))
        .position;
    expect(
      position.maxScrollExtent,
      0,
      reason: '20 rows in a short viewport must still fit rather than scroll',
    );
  });

  testWidgets('a held key is released when the surface re-baselines under it', (
    tester,
  ) async {
    // A satellite reconnect (or a subscription change) re-baselines the surface
    // and can drop the key a finger is currently on, unmounting its detector.
    // Companion must not be left holding that button down.
    final inbound = StreamController<Map<String, dynamic>>();
    final sent = <Map<String, dynamic>>[];
    final session = Session(
      inbound: inbound.stream,
      outbound: (m) {
        sent.add(m);
        return true;
      },
    );
    addTearDown(() async {
      session.dispose();
      await inbound.close();
    });
    await tester.runAsync(() async {
      inbound.add({
        'type': 'hello',
        'proto': '1.0',
        'server_version': '0',
        'server_id': 'p',
      });
      inbound.add({
        'type': 'surface-layout',
        'rows': 1,
        'cols': 1,
        'seq': 0,
        'bitmap_size': 72,
      });
      inbound.add({...oneKey, 'seq': 1});
      await Future<void>.delayed(Duration.zero);
    });
    await tester.pumpWidget(
      MaterialApp(
        home: Scaffold(body: SurfaceGrid(session: session)),
      ),
    );
    await tester.pump();

    final gesture = await tester.startGesture(
      tester.getCenter(find.byType(GestureDetector)),
    );
    // The tap recognizer holds the arena until its press deadline, so onTapDown
    // only fires once that has elapsed.
    await tester.pump(const Duration(milliseconds: 200));
    expect(
      sent.where((m) => m['pressed'] == true).length,
      1,
      reason: 'press down should be sent',
    );

    // The key the finger is on is superseded and drops out of the grid.
    await tester.runAsync(() async {
      inbound.add({
        'type': 'surface-layout',
        'rows': 1,
        'cols': 1,
        'seq': 5,
        'bitmap_size': 72,
      });
      await Future<void>.delayed(Duration.zero);
    });
    await tester.pump();

    expect(
      sent
          .where((m) => m['type'] == 'surface-press' && m['pressed'] == false)
          .length,
      1,
      reason: 'the held key must be released when its cell goes away',
    );
    await gesture.up();
  });

  testWidgets(
    'a button cell is discoverable and activatable by assistive tech',
    (tester) async {
      final handle = tester.ensureSemantics();
      final (session, sent) = await surfaceSession(
        tester,
        rows: 1,
        cols: 1,
        keys: [oneKey],
      );
      await tester.pumpWidget(
        MaterialApp(
          home: Scaffold(body: SurfaceGrid(session: session)),
        ),
      );
      await tester.pump();

      // The cell exposes a positional semantics label (Companion bakes the real
      // label into the bitmap, so position is the best accessible name)...
      final labeled = find.bySemanticsLabel('Button row 1, column 1');
      expect(labeled, findsOneWidget);
      // ...and activating it drives a press then release.
      await tester.tap(labeled);
      await tester.pump();
      final presses = sent.where((m) => m['type'] == 'surface-press').toList();
      expect(presses.where((m) => m['pressed'] == true).length, 1);
      expect(presses.where((m) => m['pressed'] == false).length, 1);
      handle.dispose();
    },
  );
}
