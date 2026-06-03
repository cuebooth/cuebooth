import 'dart:async';

import 'package:cuebooth_client/services/app_state.dart';
import 'package:cuebooth_client/services/session.dart';
import 'package:cuebooth_client/widgets/control_grid.dart';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';

void main() {
  group('ControlButton.isActive', () {
    final state = AppState()
      ..applySnapshot(1, {
        'obs': {'scene': 'camera-only'},
        'camera': {
          'main': {'preset': 'choir'},
        },
      });

    test('scene active when it is the current scene', () {
      expect(
        const ControlButton(label: 'C', kind: ControlKind.scene, name: 'camera-only')
            .isActive(state),
        isTrue,
      );
      expect(
        const ControlButton(label: 'S', kind: ControlKind.scene, name: 'slides-only')
            .isActive(state),
        isFalse,
      );
    });

    test('camera preset active when it is the current preset', () {
      expect(
        const ControlButton(label: 'Choir', kind: ControlKind.cameraPreset, name: 'choir')
            .isActive(state),
        isTrue,
      );
    });

    test('audio mute never active in v1', () {
      expect(
        const ControlButton(
          label: 'M',
          kind: ControlKind.audioMute,
          name: 'choir',
          mute: true,
        ).isActive(state),
        isFalse,
      );
    });
  });

  group('ControlButton.dispatch', () {
    late StreamController<Map<String, dynamic>> inbound;
    late List<Map<String, dynamic>> sent;
    late Session session;

    setUp(() async {
      inbound = StreamController<Map<String, dynamic>>();
      sent = [];
      session = Session(
        inbound: inbound.stream,
        outbound: (m) {
          sent.add(m);
          return true;
        },
      );
      inbound.add({
        'type': 'hello',
        'proto': '1.0',
        'server_version': '0',
        'server_id': 'p',
      });
      await Future<void>.delayed(Duration.zero);
    });

    tearDown(() async {
      session.dispose();
      await inbound.close();
    });

    test('camera preset → camera/preset with camera_id', () {
      const ControlButton(
        label: 'Choir',
        kind: ControlKind.cameraPreset,
        name: 'choir',
        cameraId: 'main',
      ).dispatch(session);
      final cmd = sent.firstWhere((m) => m['type'] == 'cmd');
      expect(cmd['target'], 'camera');
      expect(cmd['action'], 'preset');
      expect(cmd['value'], 'choir');
      expect(cmd['camera_id'], 'main');
    });

    test('audio mute → audio/set_mute with {id, mute}', () {
      const ControlButton(
        label: 'Mute',
        kind: ControlKind.audioMute,
        name: 'non-choir',
        mute: true,
      ).dispatch(session);
      final cmd = sent.firstWhere((m) => m['target'] == 'audio');
      expect(cmd['action'], 'set_mute');
      expect(cmd['value'], {'id': 'non-choir', 'mute': true});
    });
  });

  testWidgets('grid renders, highlights the active scene, and taps send a cmd', (
    tester,
  ) async {
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
        'obs': {'scene': 'camera-only', 'streaming': false, 'recording': false},
      });
      await Future<void>.delayed(Duration.zero);
    });

    await tester.pumpWidget(
      MaterialApp(home: Scaffold(body: ControlGrid(session: session))),
    );
    await tester.pump();

    // The current scene ("Camera") is filled; others are outlined.
    expect(find.widgetWithText(FilledButton, 'Camera'), findsOneWidget);
    expect(find.widgetWithText(OutlinedButton, 'Slides'), findsOneWidget);

    await tester.tap(find.widgetWithText(OutlinedButton, 'Slides'));
    await tester.pump();
    expect(
      sent.any((m) => m['type'] == 'cmd' && m['value'] == 'slides-only'),
      isTrue,
    );
  });
}
