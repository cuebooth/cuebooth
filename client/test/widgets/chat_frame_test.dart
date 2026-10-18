import 'package:cuebooth_client/screens/chat_frame.dart';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';

void main() {
  const url = 'https://chat.restream.io/embed?token=abc';

  /// The frame on the right, an ordinary Flutter target on the left.
  Future<void> pumpFrame(WidgetTester tester) async {
    await tester.pumpWidget(
      MaterialApp(
        home: Scaffold(
          body: Row(
            children: [
              Expanded(
                child: Builder(
                  builder: (context) => GestureDetector(
                    key: const Key('beside'),
                    behavior: HitTestBehavior.opaque,
                    onLongPress: () => showDialog<void>(
                      context: context,
                      builder: (_) => const AlertDialog(title: Text('Over')),
                    ),
                  ),
                ),
              ),
              const Expanded(child: ChatFrame(url: url)),
            ],
          ),
        ),
      ),
    );
  }

  bool interactive(WidgetTester tester) =>
      tester.widget<ChatFrameView>(find.byType(ChatFrameView)).interactive;

  // A re-minted URL carries a new token; a frame kept across it would go on
  // showing the page the old one loaded.
  testWidgets('a new URL gets a new frame, the same URL keeps it', (
    tester,
  ) async {
    Future<Element> frameFor(String url) async {
      await tester.pumpWidget(
        Directionality(
          textDirection: TextDirection.ltr,
          child: ChatFrame(url: url),
        ),
      );
      return tester.element(find.byType(ChatFrameView));
    }

    final first = await frameFor(url);
    expect(await frameFor(url), same(first));
    expect(await frameFor('$url-reminted'), isNot(same(first)));
  });

  testWidgets('takes pointer events when nothing is over it', (tester) async {
    await pumpFrame(tester);
    expect(tester.widget<ChatFrameView>(find.byType(ChatFrameView)).url, url);
    expect(interactive(tester), isTrue);
  });

  // A dialog is drawn in a canvas, so its buttons over the frame would
  // otherwise be the frame's to take — including the stop-stream confirmation.
  testWidgets('a route above it takes the frame out of the way', (
    tester,
  ) async {
    await pumpFrame(tester);

    await tester.longPress(find.byKey(const Key('beside')));
    await tester.pumpAndSettle();
    expect(find.text('Over'), findsOneWidget);
    expect(interactive(tester), isFalse);

    Navigator.of(tester.element(find.text('Over'))).pop();
    await tester.pumpAndSettle();
    expect(interactive(tester), isTrue);
  });

  // The pane divider's drag crosses the frame when it narrows chat.
  testWidgets('a press beside it holds the frame out of the way until it '
      'is released', (tester) async {
    await pumpFrame(tester);

    final drag = await tester.startGesture(
      tester.getCenter(find.byKey(const Key('beside'))),
    );
    await tester.pump();
    expect(interactive(tester), isFalse);

    await drag.moveTo(tester.getCenter(find.byType(ChatFrame)));
    await tester.pump();
    expect(interactive(tester), isFalse);

    await drag.up();
    await tester.pump();
    expect(interactive(tester), isTrue);
  });

  testWidgets('a release that never arrived does not leave it dead', (
    tester,
  ) async {
    await pumpFrame(tester);
    final beside = tester.getCenter(find.byKey(const Key('beside')));

    // Pressed and never released.
    await tester.startGesture(beside, pointer: 1);
    await tester.pump();
    expect(interactive(tester), isFalse);

    // The same device's next press and release.
    final next = await tester.startGesture(beside, pointer: 2);
    await next.up();
    await tester.pump();
    expect(interactive(tester), isTrue);
  });
}
