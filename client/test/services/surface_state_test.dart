import 'dart:convert';
import 'dart:typed_data';

import 'package:cuebooth_client/services/surface_state.dart';
import 'package:flutter_test/flutter_test.dart';

void main() {
  group('SurfaceState', () {
    test('applyLayout sets dimensions and hasLayout', () {
      final s = SurfaceState();
      expect(s.hasLayout, isFalse);
      s.applyLayout(4, 8, 72);
      expect(s.rows, 4);
      expect(s.cols, 8);
      expect(s.bitmapSize, 72);
      expect(s.hasLayout, isTrue);
      s.dispose();
    });

    test('applyKey records a color-only key and parses the color', () {
      final s = SurfaceState()..applyLayout(4, 8, 72);
      s.applyKey(
        key: 9,
        seq: 1,
        row: 1,
        col: 1,
        keyType: 'BUTTON',
        pressed: true,
        color: '#ff8800',
      );
      final k = s.keyAt(9)!;
      expect(k.row, 1);
      expect(k.col, 1);
      expect(k.pressed, isTrue);
      expect(k.color, 0xFFff8800);
      expect(k.image, isNull);
      s.dispose();
    });

    test('a stale (older or equal seq) update for a key is ignored', () {
      final s = SurfaceState()..applyLayout(1, 1, 72);
      s.applyKey(key: 0, seq: 5, row: 0, col: 0, keyType: 'BUTTON', pressed: true);
      s.applyKey(key: 0, seq: 3, row: 0, col: 0, keyType: 'BUTTON', pressed: false);
      expect(s.keyAt(0)!.pressed, isTrue, reason: 'older seq must be ignored');
      s.applyKey(key: 0, seq: 5, row: 0, col: 0, keyType: 'BUTTON', pressed: false);
      expect(s.keyAt(0)!.pressed, isTrue, reason: 'equal seq must be ignored');
      s.applyKey(key: 0, seq: 6, row: 0, col: 0, keyType: 'BUTTON', pressed: false);
      expect(s.keyAt(0)!.pressed, isFalse, reason: 'newer seq applies');
      s.dispose();
    });

    test('applyLayout drops existing keys (re-baseline)', () {
      final s = SurfaceState()..applyLayout(1, 1, 72);
      s.applyKey(key: 0, seq: 1, row: 0, col: 0, keyType: 'BUTTON', pressed: false);
      expect(s.keyAt(0), isNotNull);
      s.applyLayout(2, 2, 96);
      expect(s.keyAt(0), isNull);
      expect(s.bitmapSize, 96);
      s.dispose();
    });

    test('reset clears the surface and notifies', () {
      final s = SurfaceState()..applyLayout(4, 8, 72);
      s.applyKey(key: 0, seq: 1, row: 0, col: 0, keyType: 'BUTTON', pressed: false);
      var notified = 0;
      s.addListener(() => notified++);
      s.reset();
      expect(s.hasLayout, isFalse);
      expect(s.keyAt(0), isNull);
      expect(notified, 1);
      s.dispose();
    });

    test('an invalid color is dropped to null', () {
      final s = SurfaceState()..applyLayout(1, 1, 72);
      s.applyKey(key: 0, seq: 1, row: 0, col: 0, keyType: 'BUTTON', pressed: false, color: 'green');
      expect(s.keyAt(0)!.color, isNull);
      s.dispose();
    });

    testWidgets('decodes a base64 RGB bitmap into an image', (tester) async {
      // A 2×2 image: 4 pixels × 3 bytes (RGB).
      final rgb = Uint8List.fromList([
        255, 0, 0, // red
        0, 255, 0, // green
        0, 0, 255, // blue
        255, 255, 255, // white
      ]);
      final s = SurfaceState()..applyLayout(1, 1, 2);
      await tester.runAsync(() async {
        s.applyKey(
          key: 0,
          seq: 1,
          row: 0,
          col: 0,
          keyType: 'BUTTON',
          pressed: false,
          bitmapBase64: base64.encode(rgb),
        );
        // Let the async decode complete.
        await Future<void>.delayed(const Duration(milliseconds: 50));
      });
      final img = s.keyAt(0)!.image;
      expect(img, isNotNull);
      expect(img!.width, 2);
      expect(img.height, 2);
      s.dispose();
    });

    testWidgets('a malformed (too-short) bitmap is ignored, color kept', (tester) async {
      final s = SurfaceState()..applyLayout(1, 1, 4); // expects 4×4×3 = 48 bytes
      await tester.runAsync(() async {
        s.applyKey(
          key: 0,
          seq: 1,
          row: 0,
          col: 0,
          keyType: 'BUTTON',
          pressed: false,
          color: '#112233',
          bitmapBase64: base64.encode(Uint8List.fromList([1, 2, 3])),
        );
        await Future<void>.delayed(const Duration(milliseconds: 50));
      });
      expect(s.keyAt(0)!.image, isNull);
      expect(s.keyAt(0)!.color, 0xFF112233);
      s.dispose();
    });
  });
}
