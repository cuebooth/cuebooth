// The app wires ServerConnection's state changes to Session.handleDisconnected
// (main.dart). That bridge is what resets the mirrored surface when the
// transport drops, and nothing else calls it — so it is asserted here against
// the sequence an operator actually hits: the production PC restarts, and the
// replacement server numbers its surface from zero again.
import 'dart:async';

import 'package:cuebooth_client/services/server_connection.dart';
import 'package:cuebooth_client/services/session.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:stream_channel/stream_channel.dart';
import 'package:web_socket_channel/web_socket_channel.dart';

class _FakeChannel extends StreamChannelMixin<dynamic>
    implements WebSocketChannel {
  final _incoming = StreamController<dynamic>();
  final _ready = Completer<void>();
  final _sink = _FakeSink();

  @override
  Stream<dynamic> get stream => _incoming.stream;
  @override
  WebSocketSink get sink => _sink;
  @override
  Future<void> get ready => _ready.future;
  @override
  int? get closeCode => null;
  @override
  String? get closeReason => null;
  @override
  String? get protocol => null;

  void completeReady() => _ready.complete();
  void emit(String frame) => _incoming.add(frame);
  Future<void> endStream() => _incoming.close();
}

class _FakeSink implements WebSocketSink {
  final List<dynamic> added = [];
  @override
  void add(dynamic data) => added.add(data);
  @override
  void addError(Object error, [StackTrace? stackTrace]) {}
  @override
  Future<void> addStream(Stream<dynamic> stream) async {}
  @override
  Future<void> close([int? closeCode, String? closeReason]) async {}
  @override
  Future<void> get done => Future<void>.value();
}

Future<void> pump() async {
  await Future<void>.delayed(Duration.zero);
  await Future<void>.delayed(Duration.zero);
}

String keyFrame({
  required int seq,
  required String colour,
  required bool pressed,
}) =>
    '{"type":"surface-key","key":0,"seq":$seq,"row":0,"col":0,'
    '"key_type":"BUTTON","pressed":$pressed,"color":"$colour"}';

void main() {
  test('a server restart resyncs the surface rather than freezing it', () async {
    final channels = <_FakeChannel>[];
    final connection = ServerConnection(
      connectChannel: (_) {
        final c = _FakeChannel();
        channels.add(c);
        return c;
      },
    );
    final session = Session.forConnection(connection);
    // The wiring under test: main.dart hooks the transport's state changes to
    // the session so a drop clears state that the next server won't overwrite.
    connection.addListener(() {
      if (connection.state != ServerConnectionState.connected) {
        session.handleDisconnected();
      }
    });
    addTearDown(() {
      session.dispose();
      connection.dispose();
    });

    await connection.connect('host', 7878);
    channels[0].completeReady();
    await pump();
    channels[0].emit('{"type":"hello","proto":"1.0","server_id":"pc"}');
    channels[0].emit(
      '{"type":"surface-layout","rows":1,"cols":1,"seq":0,"bitmap_size":72}',
    );
    // A long-running server has counted its surface up over the service.
    channels[0].emit(keyFrame(seq: 1500, colour: '#aa0000', pressed: false));
    await pump();
    expect(session.surface.keyAt(0)?.color, 0xFFaa0000);

    // The production PC restarts.
    await channels[0].endStream();
    await pump();
    expect(connection.state, ServerConnectionState.reconnecting);

    await Future<void>.delayed(const Duration(milliseconds: 1400));
    expect(channels.length, 2, reason: 'the client must redial');
    channels[1].completeReady();
    await pump();

    // The replacement server numbers from zero. Without the drop resetting the
    // per-key sequences, every one of these is rejected as stale and the grid
    // stays frozen on the old render for the life of the app.
    channels[1].emit('{"type":"hello","proto":"1.0","server_id":"pc"}');
    channels[1].emit(
      '{"type":"surface-layout","rows":1,"cols":1,"seq":0,"bitmap_size":72}',
    );
    channels[1].emit(keyFrame(seq: 1, colour: '#0000cc', pressed: true));
    await pump();

    expect(
      session.surface.keyAt(0)?.color,
      0xFF0000cc,
      reason: 'the post-restart render must be applied, not rejected as stale',
    );
    expect(session.surface.keyAt(0)?.pressed, isTrue);
  });
}
