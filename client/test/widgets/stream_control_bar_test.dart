import 'dart:async';

import 'package:cuebooth_client/services/session.dart';
import 'package:cuebooth_client/widgets/stream_control_bar.dart';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';

void main() {
  Future<(Session, List<Map<String, dynamic>>)> readySession(
    WidgetTester tester, {
    required bool streaming,
    required bool recording,
    int? viewers,
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
        'type': 'state',
        'rev': 1,
        'obs': {'scene': 'x', 'streaming': streaming, 'recording': recording},
        if (viewers != null) 'stream': {'platform': 'restream', 'viewers': viewers},
      });
      await Future<void>.delayed(Duration.zero);
    });
    return (session, sent);
  }

  testWidgets('offline/idle: shows start controls and viewers; taps send start', (
    tester,
  ) async {
    final (session, sent) = await readySession(
      tester,
      streaming: false,
      recording: false,
      viewers: 12,
    );
    await tester.pumpWidget(
      MaterialApp(home: Scaffold(body: StreamControlBar(session: session))),
    );
    await tester.pump();

    expect(find.text('Offline'), findsOneWidget);
    expect(find.text('Idle'), findsOneWidget);
    expect(find.text('12 viewers'), findsOneWidget);

    await tester.tap(find.widgetWithText(OutlinedButton, 'Go Live'));
    await tester.tap(find.widgetWithText(OutlinedButton, 'Record'));
    await tester.pump();

    expect(
      sent.any((m) => m['target'] == 'streaming' && m['action'] == 'start'),
      isTrue,
    );
    expect(
      sent.any((m) => m['target'] == 'recording' && m['action'] == 'start'),
      isTrue,
    );
  });

  testWidgets('live/recording: shows LIVE/REC and stop sends stop', (tester) async {
    final (session, sent) = await readySession(
      tester,
      streaming: true,
      recording: true,
    );
    await tester.pumpWidget(
      MaterialApp(home: Scaffold(body: StreamControlBar(session: session))),
    );
    await tester.pump();

    expect(find.text('LIVE'), findsOneWidget);
    expect(find.text('REC'), findsOneWidget);
    // Both controls now read "Stop"; the stream control is first in the bar.
    expect(find.widgetWithText(FilledButton, 'Stop'), findsNWidgets(2));

    await tester.tap(find.widgetWithText(FilledButton, 'Stop').first);
    await tester.pump();
    expect(
      sent.any((m) => m['target'] == 'streaming' && m['action'] == 'stop'),
      isTrue,
    );
  });
}
