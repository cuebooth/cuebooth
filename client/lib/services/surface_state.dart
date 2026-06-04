import 'dart:async';
import 'dart:convert';
import 'dart:ui' as ui;

import 'package:flutter/foundation.dart';

/// One key on the Companion surface, as rendered by Companion (protocol.md §10).
///
/// [image] is Companion's rendered button bitmap (decoded from the base64 RGB
/// the server forwards); [color] is the background color fallback shown until an
/// image is available or when bitmaps are disabled.
@immutable
class SurfaceKey {
  const SurfaceKey({
    required this.key,
    required this.row,
    required this.col,
    required this.keyType,
    required this.pressed,
    this.color,
    this.image,
  });

  final int key;
  final int row;
  final int col;

  /// Companion key type: "BUTTON", "PAGEUP", "PAGEDOWN", "PAGENUM".
  final String keyType;
  final bool pressed;

  /// Background color as a 0xAARRGGBB value, or null if none was sent.
  final int? color;

  /// Companion's rendered button image, or null until one is decoded.
  final ui.Image? image;

  SurfaceKey copyWith({ui.Image? image}) => SurfaceKey(
    key: key,
    row: row,
    col: col,
    keyType: keyType,
    pressed: pressed,
    color: color,
    image: image ?? this.image,
  );
}

/// The client's mirror of the Companion Satellite surface (protocol.md §10).
///
/// The surface is auto-discovered from Companion — its dimensions and every
/// button's label/icon/feedback are whatever Companion is configured with, so
/// there is nothing to define client-side. [applyLayout] sets the grid and
/// [applyKey] folds in each key update; bitmaps are decoded asynchronously.
class SurfaceState extends ChangeNotifier {
  int _rows = 0;
  int _cols = 0;
  int _bitmapSize = 0;
  final Map<int, SurfaceKey> _keys = {};
  // Last surface-update sequence applied per key, for last-write-wins ordering
  // (the initial cached frame and a live update can race; see protocol.md §10).
  final Map<int, int> _seq = {};

  int get rows => _rows;
  int get cols => _cols;
  int get bitmapSize => _bitmapSize;

  /// True once a surface-layout has been received.
  bool get hasLayout => _rows > 0 && _cols > 0;

  /// The key at flat index [index], or null if none has arrived yet.
  SurfaceKey? keyAt(int index) => _keys[index];

  /// Applies a surface-layout: (re)sets the grid dimensions and drops all keys,
  /// since the server re-pushes every key after a (re)registration.
  void applyLayout(int rows, int cols, int bitmapSize) {
    _rows = rows;
    _cols = cols;
    _bitmapSize = bitmapSize;
    _disposeKeys();
    _keys.clear();
    _seq.clear();
    notifyListeners();
  }

  /// Folds in one key update. Stale updates (seq not newer than the last applied
  /// for this key) are ignored. A bitmap, if present, is decoded asynchronously
  /// and the key updated again when it's ready.
  void applyKey({
    required int key,
    required int seq,
    required int row,
    required int col,
    required String keyType,
    required bool pressed,
    String? color,
    String? bitmapBase64,
  }) {
    final last = _seq[key];
    if (last != null && seq <= last) return;
    _seq[key] = seq;

    // Carry the existing image forward so the button doesn't blank while a new
    // bitmap decodes (or on a color/pressed-only update).
    _keys[key] = SurfaceKey(
      key: key,
      row: row,
      col: col,
      keyType: keyType,
      pressed: pressed,
      color: _parseColor(color),
      image: _keys[key]?.image,
    );
    notifyListeners();

    if (bitmapBase64 != null && bitmapBase64.isNotEmpty && _bitmapSize > 0) {
      unawaited(_decodeBitmap(key, seq, bitmapBase64));
    }
  }

  /// Clears the surface (e.g. on disconnect) so stale buttons aren't shown.
  void reset() {
    if (_keys.isEmpty && !hasLayout) return;
    _rows = 0;
    _cols = 0;
    _bitmapSize = 0;
    _disposeKeys();
    _keys.clear();
    _seq.clear();
    notifyListeners();
  }

  Future<void> _decodeBitmap(int key, int seq, String base64Bitmap) async {
    final ui.Image image;
    try {
      image = await _rgbToImage(base64Bitmap, _bitmapSize);
    } catch (_) {
      return; // malformed bitmap — keep the color fallback
    }
    // A newer update (or a reset/layout change) may have superseded this decode.
    final current = _keys[key];
    if (_seq[key] != seq || current == null) {
      image.dispose();
      return;
    }
    final previous = current.image;
    _keys[key] = current.copyWith(image: image);
    if (previous != null && previous != image) previous.dispose();
    notifyListeners();
  }

  void _disposeKeys() {
    for (final k in _keys.values) {
      k.image?.dispose();
    }
  }

  @override
  void dispose() {
    _disposeKeys();
    _keys.clear();
    super.dispose();
  }

  /// Parses a Companion COLOR ("#rrggbb") into an opaque 0xAARRGGBB value, or
  /// null if absent/unparseable.
  static int? _parseColor(String? color) {
    if (color == null || !color.startsWith('#') || color.length != 7) return null;
    final rgb = int.tryParse(color.substring(1), radix: 16);
    return rgb == null ? null : 0xFF000000 | rgb;
  }

  /// Decodes base64 8-bit RGB pixel data (size×size) into a [ui.Image]. The
  /// Satellite bitmap is 3 bytes/pixel; Flutter wants RGBA, so an opaque alpha
  /// channel is added.
  static Future<ui.Image> _rgbToImage(String base64Bitmap, int size) async {
    final rgb = base64.decode(base64Bitmap);
    final pixelCount = size * size;
    if (rgb.length < pixelCount * 3) {
      throw const FormatException('bitmap shorter than declared dimensions');
    }
    final rgba = Uint8List(pixelCount * 4);
    for (var i = 0, j = 0; i < pixelCount; i++) {
      rgba[j++] = rgb[i * 3];
      rgba[j++] = rgb[i * 3 + 1];
      rgba[j++] = rgb[i * 3 + 2];
      rgba[j++] = 0xFF;
    }
    final completer = Completer<ui.Image>();
    ui.decodeImageFromPixels(rgba, size, size, ui.PixelFormat.rgba8888, completer.complete);
    return completer.future;
  }
}
