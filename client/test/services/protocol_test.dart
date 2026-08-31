import 'package:cuebooth_client/services/protocol.dart';
import 'package:flutter_test/flutter_test.dart';

void main() {
  group('protoMajor', () {
    test('parses the integer major before the first dot', () {
      expect(protoMajor('1.0'), 1);
      expect(protoMajor('2.5'), 2);
      expect(protoMajor('10.3'), 10);
    });

    test('rejects malformed versions', () {
      expect(protoMajor(null), isNull);
      expect(protoMajor('1'), isNull); // no dot
      expect(protoMajor('.5'), isNull); // empty major
      expect(protoMajor('01.0'), isNull); // leading zero
      expect(protoMajor('x.0'), isNull);
    });

    test('client speaks major 1', () => expect(clientProtoMajor, 1));
  });

  group('applyMergePatch (protocol.md §4)', () {
    test('merges objects recursively, leaving siblings intact', () {
      final target = {
        'obs': {'scene': 'a', 'streaming': false},
      };
      applyMergePatch(target, {
        'obs': {'streaming': true},
      });
      expect(target, {
        'obs': {'scene': 'a', 'streaming': true},
      });
    });

    test('null deletes a key', () {
      final target = {
        'obs': {'scene': 'a', 'recording': true},
      };
      applyMergePatch(target, {
        'obs': {'recording': null},
      });
      expect(target, {
        'obs': {'scene': 'a'},
      });
    });

    test('a non-object value replaces wholesale (incl. arrays)', () {
      final target = {
        'slides': {
          'pending_actions': ['x', 'y'],
        },
      };
      applyMergePatch(target, {
        'slides': {
          'pending_actions': ['z'],
        },
      });
      expect(target['slides'], {
        'pending_actions': ['z'],
      });
    });

    test('object patch onto an absent key creates it (nulls stripped)', () {
      final target = <String, dynamic>{};
      applyMergePatch(target, {
        'camera': {
          'main': {'preset': 'choir', 'stale': null},
        },
      });
      expect(target, {
        'camera': {
          'main': {'preset': 'choir'},
        },
      });
    });
  });
}
