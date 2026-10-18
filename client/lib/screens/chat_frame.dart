import 'package:flutter/gestures.dart';
import 'package:flutter/widgets.dart';

import 'chat_frame_stub.dart'
    if (dart.library.js_interop) 'chat_frame_web.dart';

export 'chat_frame_stub.dart'
    if (dart.library.js_interop) 'chat_frame_web.dart'
    show ChatFrameView;

/// Renders chat in an iframe, on web only.
///
/// A new [url] gets a new frame: an iframe loads its URL once, when it joins
/// the page.
///
/// The browser delivers a pointer event to the topmost DOM element, and Flutter
/// draws into a canvas rather than the DOM, so the frame would take events
/// meant for Flutter content over or beside it. It stops taking them while a
/// route is above it, and while a pointer pressed on Flutter content is down —
/// a press Flutter sees began outside the frame, whose own events never reach
/// it, and a mouse dragged across an iframe otherwise hands the drag to it.
class ChatFrame extends StatefulWidget {
  const ChatFrame({super.key, required this.url});

  final String url;

  @override
  State<ChatFrame> createState() => _ChatFrameState();
}

class _ChatFrameState extends State<ChatFrame> {
  /// Devices with a press down on Flutter content.
  final Set<int> _pressed = {};

  @override
  void initState() {
    super.initState();
    GestureBinding.instance.pointerRouter.addGlobalRoute(_onPointer);
  }

  @override
  void dispose() {
    GestureBinding.instance.pointerRouter.removeGlobalRoute(_onPointer);
    super.dispose();
  }

  void _onPointer(PointerEvent event) {
    // By device rather than pointer, which is new for every press: a release
    // that never arrived would otherwise leave the frame dead for good, where
    // the device's next press and release clear it.
    final changed = switch (event) {
      PointerDownEvent() => _pressed.add(event.device),
      PointerUpEvent() || PointerCancelEvent() || PointerRemovedEvent() =>
        _pressed.remove(event.device),
      _ => false,
    };
    if (changed && mounted) setState(() {});
  }

  @override
  Widget build(BuildContext context) => ChatFrameView(
    key: ValueKey(widget.url),
    url: widget.url,
    interactive: _pressed.isEmpty && (ModalRoute.isCurrentOf(context) ?? true),
  );
}
