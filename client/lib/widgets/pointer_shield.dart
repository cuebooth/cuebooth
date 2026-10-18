import 'package:flutter/foundation.dart';
import 'package:flutter/widgets.dart';

/// Keeps pointer events over [child] for Flutter when it is drawn above a
/// platform view, such as chat's iframe on web.
///
/// The browser delivers an event to the topmost DOM element, and Flutter draws
/// [child] into a canvas, not the DOM — so without this the platform view
/// beneath takes the tap. An empty element under [child] becomes the topmost
/// one instead, and its events bubble to Flutter, where an iframe's never do.
///
/// Off the web there is nothing to shield, and this is [child] alone.
class PointerShield extends StatelessWidget {
  const PointerShield({super.key, required this.child});

  final Widget child;

  @override
  Widget build(BuildContext context) {
    if (!kIsWeb) return child;
    return Stack(
      children: [
        Positioned.fill(child: HtmlElementView.fromTagName(tagName: 'div')),
        child,
      ],
    );
  }
}
