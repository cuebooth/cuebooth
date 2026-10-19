import 'package:flutter/foundation.dart';
import 'package:flutter/widgets.dart';
import 'package:web/web.dart' as web;

/// Popups are allowed, and escape the sandbox, so a `target=_blank` link opens
/// an ordinary tab. Top navigation is withheld, so nothing in chat can navigate
/// CueBooth itself away. `allow-same-origin` keeps the embed on its own origin
/// rather than an opaque one; it grants no access to this page, which is a
/// different origin.
const chatFrameSandbox =
    'allow-scripts allow-same-origin allow-popups '
    'allow-popups-to-escape-sandbox';

class ChatFrameView extends StatefulWidget {
  const ChatFrameView({super.key, required this.url, required this.interactive});

  final String url;

  /// Whether the frame takes pointer events.
  final ValueListenable<bool> interactive;

  @override
  State<ChatFrameView> createState() => _ChatFrameViewState();
}

class _ChatFrameViewState extends State<ChatFrameView> {
  VoidCallback? _unbind;

  @override
  void dispose() {
    _unbind?.call();
    super.dispose();
  }

  // The element is created once per state, so a new URL or notifier needs a
  // new view; ChatFrame keys it by URL and keeps one notifier for its life.
  @override
  Widget build(BuildContext context) => HtmlElementView.fromTagName(
    tagName: 'iframe',
    onElementCreated: (element) => _unbind = bindChatFrame(
      element as web.HTMLIFrameElement,
      widget.url,
      widget.interactive,
    ),
  );
}

/// Prepares [frame] to show chat from [url] before it joins the page, and
/// keeps whether it takes pointer events in step with [interactive] until the
/// returned callback is called.
VoidCallback bindChatFrame(
  web.HTMLIFrameElement frame,
  String url,
  ValueListenable<bool> interactive,
) {
  // Sandbox flags are fixed when a navigation starts, so they go on first.
  frame.setAttribute('sandbox', chatFrameSandbox);
  frame.style
    ..border = 'none'
    ..width = '100%'
    ..height = '100%';
  frame.src = url;

  void apply() =>
      frame.style.pointerEvents = interactive.value ? 'auto' : 'none';
  apply();
  interactive.addListener(apply);
  return () => interactive.removeListener(apply);
}
