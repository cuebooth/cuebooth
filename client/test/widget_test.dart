// Basic smoke test: the app boots to the connect screen.
//
// Real widget tests for the control surface (button grid, faders, PTZ, video,
// slide status) land with their UI in later phases (CB-015, CB-025+, ...).
// This placeholder also keeps `flutter test` exiting 0 against an otherwise
// empty test/ directory, so the CI workflow (#68) isn't dead on arrival.
import 'package:flutter_test/flutter_test.dart';

import 'package:cuebooth_client/main.dart';

void main() {
  testWidgets('app starts on the connect screen', (WidgetTester tester) async {
    await tester.pumpWidget(const CueBoothApp());

    expect(find.text('Connect to CueBooth'), findsOneWidget);
  });
}
