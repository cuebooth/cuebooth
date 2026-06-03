/// Wire-protocol constants and helpers for the CueBooth client.
///
/// The normative spec is `docs/protocol.md`; this is its Dart-side encoding for
/// the v1 message set the client uses.
library;

/// On-wire protocol version the client implements (protocol.md §1).
const String protoVersion = '1.0';

/// Frame `type` values (protocol.md §2).
class FrameType {
  static const hello = 'hello';
  static const state = 'state';
  static const stateDelta = 'state-delta';
  static const ack = 'ack';
  static const nak = 'nak';
  static const event = 'event';
  static const error = 'error';
  static const cmd = 'cmd';
  static const subscribe = 'subscribe';
  static const unsubscribe = 'unsubscribe';
  static const getState = 'get_state';
  static const ping = 'ping';
  static const pong = 'pong';
}

/// Command targets (protocol.md §3/§5).
class Target {
  static const camera = 'camera';
  static const audio = 'audio';
  static const scene = 'scene';
  static const slides = 'slides';
  static const streaming = 'streaming';
  static const recording = 'recording';
}

/// State topics (protocol.md §3). The default subscription is all of these.
const List<String> topics = ['audio', 'camera', 'obs', 'slides', 'stream'];

/// Returns the major component of a `MAJOR.MINOR` protocol version string, or
/// null if it isn't well-formed (protocol.md §1: integer before the first dot,
/// no leading zeros). Clients must refuse a server whose major differs.
int? protoMajor(String? proto) {
  if (proto == null) return null;
  final dot = proto.indexOf('.');
  if (dot <= 0) return null;
  final major = proto.substring(0, dot);
  if (major.length > 1 && major.startsWith('0')) return null; // no leading zeros
  return int.tryParse(major);
}

/// The major version this client speaks.
final int clientProtoMajor = protoMajor(protoVersion)!;

/// Applies a JSON-Merge-Patch (`state-delta` rules, protocol.md §4) to [target]
/// and returns the result:
///   - object values merge recursively,
///   - a null value deletes the key,
///   - arrays and scalars replace wholesale.
///
/// [target] is mutated in place when it is a map; otherwise a new value is
/// returned (so callers should use the return value).
dynamic applyMergePatch(dynamic target, dynamic patch) {
  if (patch is Map) {
    final Map<String, dynamic> base =
        target is Map<String, dynamic> ? target : <String, dynamic>{};
    patch.forEach((key, value) {
      if (value == null) {
        base.remove(key);
      } else {
        base[key] = applyMergePatch(base[key], value);
      }
    });
    return base;
  }
  // Scalars and arrays replace wholesale.
  return patch;
}
