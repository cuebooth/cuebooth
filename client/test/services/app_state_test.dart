import 'package:cuebooth_client/services/app_state.dart';
import 'package:flutter_test/flutter_test.dart';

void main() {
  group('AppState', () {
    test('applySnapshot sets baseline and typed accessors', () {
      final s = AppState();
      s.applySnapshot(5, {
        'obs': {'scene': 'camera-only', 'streaming': true, 'recording': false},
        'camera': {
          'main': {'preset': 'choir'},
        },
        'stream': {'platform': 'restream', 'viewers': 12},
      });
      expect(s.rev, 5);
      expect(s.hasBaseline, isTrue);
      expect(s.obsScene, 'camera-only');
      expect(s.streaming, isTrue);
      expect(s.recording, isFalse);
      expect(s.cameraPreset('main'), 'choir');
      expect(s.cameraPreset('front'), isNull);
      expect(s.streamPlatform, 'restream');
      expect(s.streamViewers, 12);
    });

    test('applySnapshot replaces wholesale — a topic absent later is removed', () {
      final s = AppState();
      s.applySnapshot(1, {
        'obs': {'scene': 'a'},
        'camera': {
          'main': {'preset': 'choir'},
        },
      });
      expect(s.cameraPreset('main'), 'choir');
      s.applySnapshot(2, {
        'obs': {'scene': 'b'},
      }); // camera absent
      expect(s.obsScene, 'b');
      expect(s.cameraPreset('main'), isNull);
    });

    test('accessors are null before a baseline', () {
      final s = AppState();
      expect(s.streaming, isNull);
      expect(s.obsScene, isNull);
      expect(s.hasBaseline, isFalse);
    });

    test('applyDelta folds a contiguous patch and bumps rev', () {
      final s = AppState();
      s.applySnapshot(1, {
        'obs': {'scene': 'a', 'streaming': false, 'recording': false},
      });
      final outcome = s.applyDelta(2, {
        'obs': {'streaming': true},
      });
      expect(outcome, DeltaOutcome.applied);
      expect(s.rev, 2);
      expect(s.streaming, isTrue);
      expect(s.obsScene, 'a'); // untouched
    });

    test('delta before any baseline reports gap', () {
      final s = AppState();
      expect(s.applyDelta(1, {'obs': {}}), DeltaOutcome.gap);
    });

    test('stale/duplicate delta is ignored', () {
      final s = AppState();
      s.applySnapshot(5, {
        'obs': {'scene': 'a'},
      });
      expect(s.applyDelta(5, {'obs': {}}), DeltaOutcome.stale);
      expect(s.applyDelta(4, {'obs': {}}), DeltaOutcome.stale);
      expect(s.rev, 5);
    });

    test('non-contiguous delta reports gap and does not apply', () {
      final s = AppState();
      s.applySnapshot(1, {
        'obs': {'scene': 'a'},
      });
      final outcome = s.applyDelta(3, {
        'obs': {'scene': 'b'},
      });
      expect(outcome, DeltaOutcome.gap);
      expect(s.rev, 1); // unchanged
      expect(s.obsScene, 'a'); // unchanged
    });

    test('reset clears state and notifies', () {
      final s = AppState();
      s.applySnapshot(3, {
        'obs': {'scene': 'a'},
      });
      var notified = 0;
      s.addListener(() => notified++);
      s.reset();
      expect(s.rev, 0);
      expect(s.hasBaseline, isFalse);
      expect(s.obsScene, isNull);
      expect(notified, 1);
    });
  });

  group('AppState chat (protocol.md §4 stream.chat)', () {
    AppState withChat(Map<String, dynamic>? chat) {
      final s = AppState();
      s.applySnapshot(1, {
        'stream': {'platform': 'restream', 'chat': ?chat},
      });
      return s;
    }

    // No provider configured is a different thing from a provider awaiting
    // authorization, and the UI shows each differently.
    test('an absent chat object is unconfigured, not unauthorized', () {
      final s = withChat(null);
      expect(s.chatConfigured, isFalse);
      expect(s.chatNeedsAuth, isFalse);
      expect(s.chatReady, isFalse);
      expect(s.chatProvider, isNull);
    });

    test('needs_auth is configured but not ready', () {
      final s = withChat({'provider': 'restream', 'status': 'needs_auth'});
      expect(s.chatConfigured, isTrue);
      expect(s.chatNeedsAuth, isTrue);
      expect(s.chatReady, isFalse);
      expect(s.chatProvider, 'restream');
    });

    test('ready is configured and ready', () {
      final s = withChat({'provider': 'restream', 'status': 'ready'});
      expect(s.chatConfigured, isTrue);
      expect(s.chatNeedsAuth, isFalse);
      expect(s.chatReady, isTrue);
    });

    // A status the client doesn't know is neither ready nor needs_auth, so a
    // future server value shows an inert panel rather than a wrong one.
    test('an unknown status is neither ready nor needing auth', () {
      final s = withChat({'provider': 'restream', 'status': 'something-new'});
      expect(s.chatConfigured, isTrue);
      expect(s.chatNeedsAuth, isFalse);
      expect(s.chatReady, isFalse);
    });

    test('a delta can flip the status without a new snapshot', () {
      final s = withChat({'provider': 'restream', 'status': 'needs_auth'});
      s.applyDelta(2, {
        'stream': {
          'chat': {'status': 'ready'},
        },
      });
      expect(s.chatReady, isTrue);
      expect(s.chatNeedsAuth, isFalse);
      expect(s.chatProvider, 'restream');
    });

    test('no stream topic at all is unconfigured', () {
      final s = AppState();
      s.applySnapshot(1, {
        'obs': {'scene': 'a'},
      });
      expect(s.chatConfigured, isFalse);
    });
  });
}
