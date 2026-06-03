import 'dart:async';

import 'package:cuebooth_client/services/protocol.dart';
import 'package:cuebooth_client/services/session.dart';
import 'package:flutter_test/flutter_test.dart';

void main() {
  late StreamController<Map<String, dynamic>> inbound;
  late List<Map<String, dynamic>> sent;
  late Session session;

  setUp(() {
    inbound = StreamController<Map<String, dynamic>>();
    sent = [];
    session = Session(
      inbound: inbound.stream,
      outbound: (m) {
        sent.add(m);
        return true;
      },
    );
  });

  tearDown(() async {
    session.dispose();
    await inbound.close();
  });

  // Pushes a frame and lets the stream listener process it.
  Future<void> feed(Map<String, dynamic> frame) async {
    inbound.add(frame);
    await Future<void>.delayed(Duration.zero);
  }

  Map<String, dynamic> hello([String proto = '1.0']) => {
    'type': 'hello',
    'proto': proto,
    'server_version': '0.1.0',
    'server_id': 'production-pc',
  };

  test('hello marks ready and records server info', () async {
    expect(session.ready, isFalse);
    await feed(hello());
    expect(session.ready, isTrue);
    expect(session.serverVersion, '0.1.0');
    expect(session.serverId, 'production-pc');
    expect(session.protocolIncompatible, isFalse);
  });

  test('incompatible major version refuses to operate', () async {
    await feed(hello('2.0'));
    expect(session.ready, isFalse);
    expect(session.protocolIncompatible, isTrue);
    // Commands are gated.
    expect(session.sendCommand(Target.scene, 'set', value: 'x'), isNull);
  });

  test('state snapshot then delta updates AppState', () async {
    await feed(hello());
    await feed({
      'type': 'state',
      'rev': 1,
      'obs': {'scene': 'a', 'streaming': false, 'recording': false},
    });
    expect(session.state.obsScene, 'a');
    await feed({
      'type': 'state-delta',
      'rev': 2,
      'patch': {
        'obs': {'streaming': true},
      },
    });
    expect(session.state.streaming, isTrue);
    expect(session.state.rev, 2);
  });

  test('a delta gap triggers a get_state resync and is not applied', () async {
    await feed(hello());
    await feed({
      'type': 'state',
      'rev': 1,
      'obs': {'scene': 'a'},
    });
    await feed({
      'type': 'state-delta',
      'rev': 3, // skips 2
      'patch': {
        'obs': {'scene': 'b'},
      },
    });
    expect(sent.any((m) => m['type'] == FrameType.getState), isTrue);
    expect(session.state.obsScene, 'a');
  });

  test('a gap episode sends exactly one get_state until the snapshot lands', () async {
    await feed(hello());
    await feed({
      'type': 'state',
      'rev': 1,
      'obs': {'scene': 'a'},
    });
    // Two gapped deltas (both skip rev 2): both gap, but only one get_state.
    await feed({
      'type': 'state-delta',
      'rev': 3,
      'patch': {
        'obs': {'scene': 'b'},
      },
    });
    await feed({
      'type': 'state-delta',
      'rev': 4,
      'patch': {
        'obs': {'scene': 'c'},
      },
    });
    expect(sent.where((m) => m['type'] == FrameType.getState).length, 1);

    // The resync snapshot clears the latch; a later gap triggers a new get_state.
    await feed({
      'type': 'state',
      'rev': 10,
      'obs': {'scene': 'x'},
    });
    await feed({
      'type': 'state-delta',
      'rev': 12, // gap again
      'patch': {
        'obs': {'scene': 'y'},
      },
    });
    expect(sent.where((m) => m['type'] == FrameType.getState).length, 2);
  });

  test('nak surfaces an error notice', () async {
    final notices = <SessionNotice>[];
    session.notices.listen(notices.add);
    await feed(hello());
    await feed({
      'type': 'nak',
      'id': 'c1',
      'error': {'code': 'unknown_preset', 'message': 'no preset foo'},
    });
    await Future<void>.delayed(Duration.zero);
    expect(
      notices.where(
        (n) =>
            n.severity == NoticeSeverity.error && n.message.contains('no preset foo'),
      ),
      isNotEmpty,
    );
  });

  test('event frame surfaces a notice at its severity', () async {
    final notices = <SessionNotice>[];
    session.notices.listen(notices.add);
    await feed(hello());
    await feed({
      'type': 'event',
      'severity': 'warn',
      'message': 'Suppressed feedback on presenter-lapel',
    });
    await Future<void>.delayed(Duration.zero);
    expect(notices.single.severity, NoticeSeverity.warn);
    expect(notices.single.message, contains('feedback'));
  });

  test('sendCommand is gated until hello, then emits a cmd frame', () async {
    expect(session.sendCommand(Target.scene, 'set', value: 'x'), isNull);
    await feed(hello());
    final id = session.sendCommand(
      Target.camera,
      'preset',
      value: 'choir',
      cameraId: 'main',
    );
    expect(id, isNotNull);
    final cmd = sent.firstWhere((m) => m['type'] == FrameType.cmd);
    expect(cmd['id'], id);
    expect(cmd['target'], 'camera');
    expect(cmd['action'], 'preset');
    expect(cmd['value'], 'choir');
    expect(cmd['camera_id'], 'main');
  });

  test('a delta arriving before hello does not trigger get_state', () async {
    await feed({
      'type': 'state-delta',
      'rev': 1,
      'patch': {
        'obs': {'scene': 'a'},
      },
    });
    expect(sent.where((m) => m['type'] == FrameType.getState), isEmpty);
  });

  test('sendCommand returns null and warns when the transport rejects it', () async {
    final ctrl = StreamController<Map<String, dynamic>>();
    final s = Session(inbound: ctrl.stream, outbound: (_) => false);
    addTearDown(() async {
      s.dispose();
      await ctrl.close();
    });
    final notices = <SessionNotice>[];
    s.notices.listen(notices.add);
    ctrl.add(hello());
    await Future<void>.delayed(Duration.zero);

    expect(s.ready, isTrue);
    expect(s.sendCommand(Target.scene, 'set', value: 'x'), isNull);
    await Future<void>.delayed(Duration.zero);
    expect(notices.any((n) => n.severity == NoticeSeverity.warn), isTrue);
  });

  test('handleDisconnected drops ready and resets state', () async {
    await feed(hello());
    await feed({
      'type': 'state',
      'rev': 1,
      'obs': {'scene': 'a'},
    });
    session.handleDisconnected();
    expect(session.ready, isFalse);
    expect(session.state.hasBaseline, isFalse);
  });
}
