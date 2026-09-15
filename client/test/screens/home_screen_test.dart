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
  group('chatPaneOffered', () {
    test('a server that offers chat gets the pane', () {
      expect(chatPaneOffered(_stateFrom({'status': 'ready'})), isTrue);
    });

    test('a server that has answered without chat gets no pane', () {
      expect(chatPaneOffered(_stateFrom(null)), isFalse);
    });

    // State is cleared on disconnect, so this looks exactly like "no chat
    // provider" from the topic alone. Reading it as one takes the pane — and
    // the webview in it — out of the dock on every blip.
    test('a dropped connection is not an answer', () {
      final state = _stateFrom({'status': 'ready'});
      expect(chatPaneOffered(state), isTrue);

      state.reset();

      expect(state.hasBaseline, isFalse, reason: 'the mirror was cleared');
      expect(chatPaneOffered(state), isTrue);
    });

    test('nothing heard yet is not an answer either', () {
      expect(chatPaneOffered(AppState()), isTrue);
    });
  });
}
