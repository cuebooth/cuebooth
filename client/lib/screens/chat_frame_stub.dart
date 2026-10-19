import 'package:flutter/foundation.dart';
import 'package:flutter/widgets.dart';

/// Off the web there is no DOM to put an iframe in, and `HtmlElementView`
/// throws. Only tests reach this.
class ChatFrameView extends StatelessWidget {
  const ChatFrameView({super.key, required this.url, required this.interactive});

  final String url;

  /// Whether the frame takes pointer events.
  final ValueListenable<bool> interactive;

  @override
  Widget build(BuildContext context) => const SizedBox.expand();
}
