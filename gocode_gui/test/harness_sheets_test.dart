import 'dart:convert';
import 'dart:io';

import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:gocode_gui/app/theme.dart';
import 'package:gocode_gui/core/api/client.dart';
import 'package:gocode_gui/core/connection/controller.dart';
import 'package:gocode_gui/features/harness/harness_library_screen.dart';
import 'package:gocode_gui/features/harness/harness_stage_sheet.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';

Object _fixture(String name) =>
    jsonDecode(File('test/fixtures/harness/$name.json').readAsStringSync());

/// On a wide window the stage and library-entry details open as side
/// sheets through showGeneralDialog, which supplies no Material: their
/// InkWells, buttons and tabs threw "No Material widget found" until the
/// sheets brought their own.
Future<void> _open(
  WidgetTester tester,
  Future<void> Function(BuildContext) open,
) async {
  final client = GocodeClient(
    baseUrl: 'http://gocode.test',
    httpClient: MockClient((request) async {
      final path = request.url.path;
      final body = path.contains('/stage/')
          ? _fixture('stage')
          : path.startsWith('/api/harness/library/')
          ? _fixture('entry')
          : path == '/api/harness/run'
          ? [_fixture('run')]
          : null;
      return body == null
          ? http.Response('{"error":"no route"}', 404)
          : http.Response(
              jsonEncode(body),
              200,
              headers: {'content-type': 'application/json'},
            );
    }),
  );
  tester.view.physicalSize = const Size(1400, 1000);
  tester.view.devicePixelRatio = 1;
  addTearDown(tester.view.reset);
  await tester.pumpWidget(
    ProviderScope(
      overrides: [apiClientProvider.overrideWithValue(client)],
      child: MaterialApp(
        theme: AppTheme.dark(),
        home: Builder(
          builder: (context) => TextButton(
            onPressed: () => open(context),
            child: const Text('open'),
          ),
        ),
      ),
    ),
  );
  await tester.tap(find.text('open'));
  await tester.pumpAndSettle();
}

void main() {
  testWidgets('the stage side sheet builds without a Scaffold', (tester) async {
    await _open(
      tester,
      (context) => showHarnessStageSheet(
        context,
        runID: 'fix-login--k1-parallel',
        label: 'agg1',
      ),
    );
    expect(tester.takeException(), isNull);
    expect(find.text('Notes'), findsOneWidget);
  });

  testWidgets('the library entry side sheet builds without a Scaffold', (
    tester,
  ) async {
    await _open(
      tester,
      (context) => showHarnessEntrySheet(context, domain: 'auth', task: 't'),
    );
    expect(tester.takeException(), isNull);
    expect(find.byType(InkWell), findsWidgets);
  });
}
