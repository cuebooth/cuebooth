import 'package:cuebooth_client/screens/home_screen.dart';
import 'package:cuebooth_client/services/app_state.dart';
import 'package:flutter_test/flutter_test.dart';

AppState _stateFrom(Map<String, dynamic>? chat) {
  final state = AppState();
  state.applySnapshot(1, {
    'stream': {'platform': 'restream', 'chat': ?chat},
  });
  return state;
}

void main() {
  group('ChatPaneAvailability', () {
    test('a server that offers chat gets the pane', () {
      final availability = ChatPaneAvailability();
      expect(availability.offered(_stateFrom({'status': 'ready'})), isTrue);
    });

    test('a server that has answered without chat gets no pane', () {
      final availability = ChatPaneAvailability();
      expect(availability.offered(_stateFrom(null)), isFalse);
    });

    test('the pane is assumed until a server has said otherwise', () {
      expect(ChatPaneAvailability().offered(AppState()), isTrue);
    });

    // State is cleared on disconnect, so this looks exactly like "no chat
    // provider" from the topic alone. Reading it as one takes the pane — and
    // the webview in it — out of the dock on every blip.
    test('a drop does not withdraw a pane the server offered', () {
      final availability = ChatPaneAvailability();
      final state = _stateFrom({'status': 'ready'});
      expect(availability.offered(state), isTrue);

      state.reset();

      expect(state.hasBaseline, isFalse, reason: 'the mirror was cleared');
      expect(availability.offered(state), isTrue);
    });

    // And the other direction: a server with no chat must not sprout a chat
    // pane every time the socket drops, offering to reconnect to something that
    // was never there.
    test('a drop does not restore a pane the server declined', () {
      final availability = ChatPaneAvailability();
      final state = _stateFrom(null);
      expect(availability.offered(state), isFalse);

      state.reset();

      expect(availability.offered(state), isFalse);
    });

    test('a later snapshot is what changes the answer', () {
      final availability = ChatPaneAvailability();
      final state = _stateFrom(null);
      expect(availability.offered(state), isFalse);

      state.applySnapshot(2, {
        'stream': {
          'platform': 'restream',
          'chat': {'status': 'ready'},
        },
      });

      expect(availability.offered(state), isTrue);
    });
  });
}
