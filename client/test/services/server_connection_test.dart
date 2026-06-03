// Tests for ServerConnection: synchronous input validation, plus the
// connect/ready/reconnect transitions driven through an injected fake channel.
import 'dart:async';

import 'package:cuebooth_client/services/server_connection.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:stream_channel/stream_channel.dart';
import 'package:web_socket_channel/web_socket_channel.dart';

/// A minimal in-memory WebSocketChannel for driving ServerConnection
/// deterministically: the test completes/fails `ready` and emits frames.
/// Extends StreamChannelMixin for the transform/pipe helpers.
class FakeWebSocketChannel extends StreamChannelMixin<dynamic>
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
  void failReady(Object error) => _ready.completeError(error);
  void emit(String frame) => _incoming.add(frame);
}

class _FakeSink implements WebSocketSink {
  bool closed = false;
  final List<dynamic> added = [];

  @override
  void add(dynamic data) => added.add(data);

  @override
  void addError(Object error, [StackTrace? stackTrace]) {}

  @override
  Future<void> addStream(Stream<dynamic> stream) async {}

  @override
  Future<void> close([int? closeCode, String? closeReason]) async {
    closed = true;
  }

  @override
  Future<void> get done => Future<void>.value();
}

// Lets microtasks (the awaited channel.ready continuation) run.
Future<void> pump() async {
  await Future<void>.delayed(Duration.zero);
  await Future<void>.delayed(Duration.zero);
}

void main() {
  group('ServerConnection.connect input validation', () {
    test('empty host -> error state, lastError set, no socket opened', () async {
      final conn = ServerConnection();
      addTearDown(conn.dispose);

      await conn.connect('', 7878);

      expect(conn.state, ServerConnectionState.error);
      expect(conn.lastError, isNotNull);
    });

    test('malformed host (throws FormatException) -> error state', () async {
      final conn = ServerConnection();
      addTearDown(conn.dispose);

      await conn.connect('ws://1.2.3.4', 7878);

      expect(conn.state, ServerConnectionState.error);
      expect(conn.lastError, isNotNull);
    });
  });

  group('ServerConnection transport (fake channel)', () {
    test('connected only after ready; frames reach messages', () async {
      final fake = FakeWebSocketChannel();
      final conn = ServerConnection(connectChannel: (_) => fake);
      addTearDown(conn.dispose);

      final frames = <Map<String, dynamic>>[];
      conn.messages.listen(frames.add);

      await conn.connect('host', 7878);
      // Still awaiting readiness — not yet "connected" (no false-connected flash).
      expect(conn.state, ServerConnectionState.connecting);

      fake.completeReady();
      await pump();
      expect(conn.state, ServerConnectionState.connected);

      // A frame that arrives once listening reaches the messages stream.
      fake.emit('{"type":"hello","proto":"1.0","server_id":"pc"}');
      await pump();
      expect(frames, isNotEmpty);
      expect(frames.first['type'], 'hello');
    });

    test('ready failure -> error then reconnecting', () async {
      final fake = FakeWebSocketChannel();
      final conn = ServerConnection(connectChannel: (_) => fake);
      addTearDown(conn.dispose);

      await conn.connect('host', 7878);
      fake.failReady(Exception('connection refused'));
      await pump();

      expect(conn.state, ServerConnectionState.reconnecting);
      expect(conn.lastError, isNotNull);
    });

    test('send only succeeds once connected', () async {
      final fake = FakeWebSocketChannel();
      final conn = ServerConnection(connectChannel: (_) => fake);
      addTearDown(conn.dispose);

      expect(conn.send({'type': 'ping'}), isFalse); // not connected yet

      await conn.connect('host', 7878);
      fake.completeReady();
      await pump();

      expect(conn.send({'type': 'ping', 'id': 'k1'}), isTrue);
    });
  });
}
