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
  final bool interactive;

  @override
  State<ChatFrameView> createState() => _ChatFrameViewState();
}

class _ChatFrameViewState extends State<ChatFrameView> {
  web.HTMLIFrameElement? _frame;

  @override
  void didUpdateWidget(ChatFrameView oldWidget) {
    super.didUpdateWidget(oldWidget);
    if (_frame case final frame?) {
      setChatFrameInteractive(frame, widget.interactive);
    }
  }

  @override
  Widget build(BuildContext context) => HtmlElementView.fromTagName(
    tagName: 'iframe',
    onElementCreated: (element) {
      final frame = _frame = element as web.HTMLIFrameElement;
      configureChatFrame(frame, widget.url);
      setChatFrameInteractive(frame, widget.interactive);
    },
  );
}

/// Prepares [frame] to show chat from [url], before it joins the page.
void configureChatFrame(web.HTMLIFrameElement frame, String url) {
  // Sandbox flags are fixed when a navigation starts, so they go on first.
  frame.setAttribute('sandbox', chatFrameSandbox);
  frame.style
    ..border = 'none'
    ..width = '100%'
    ..height = '100%';
  frame.src = url;
}

void setChatFrameInteractive(web.HTMLIFrameElement frame, bool interactive) {
  frame.style.pointerEvents = interactive ? 'auto' : 'none';
}
