// Unit tests for ServerConnection's synchronous input-validation paths.
//
// These cover connect()'s up-front rejection of bad input (no socket opened,
// no network needed). The reconnect/backoff/generation-token/teardown behavior
// is the most intricate part of this class but needs WebSocketChannel.connect
// to be injectable to drive deterministically with fake timers — that refactor
// lands with the connect-flow wiring in CB-014, where those tests belong.
import 'package:flutter_test/flutter_test.dart';

import 'package:cuebooth_client/services/server_connection.dart';

void main() {
  group('ServerConnection.connect input validation', () {
    test(
      'empty host -> error state, lastError set, no socket opened',
      () async {
        final conn = ServerConnection();
        addTearDown(conn.dispose);

        await conn.connect('', 7878);

        expect(conn.state, ServerConnectionState.error);
        expect(conn.lastError, isNotNull);
      },
    );

    test('malformed host (throws FormatException) -> error state', () async {
      final conn = ServerConnection();
      addTearDown(conn.dispose);

      // A pasted full URL as the host makes the Uri constructor throw.
      await conn.connect('ws://1.2.3.4', 7878);

      expect(conn.state, ServerConnectionState.error);
      expect(conn.lastError, isNotNull);
    });
  });
}
