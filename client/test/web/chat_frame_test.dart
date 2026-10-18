@TestOn('browser')
library;

import 'package:cuebooth_client/screens/chat_frame_web.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:web/web.dart' as web;

void main() {
  const url = 'https://chat.restream.io/embed?token=abc';

  test('loads the URL in a sandboxed frame that fills its view', () {
    final frame = web.HTMLIFrameElement();
    configureChatFrame(frame, url);

    expect(frame.src, url);
    expect(frame.getAttribute('sandbox'), chatFrameSandbox);
    expect(frame.style.width, '100%');
    expect(frame.style.height, '100%');
  });

  // A link in chat that could navigate the top window would replace CueBooth
  // mid-service, with nothing to bring it back.
  test('the sandbox withholds top navigation and allows popups', () {
    final flags = chatFrameSandbox.split(' ');
    expect(flags.where((f) => f.startsWith('allow-top-navigation')), isEmpty);
    expect(flags, contains('allow-popups'));
    expect(flags, contains('allow-popups-to-escape-sandbox'));
    expect(flags, contains('allow-scripts'));
  });
}
