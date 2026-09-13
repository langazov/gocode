import 'package:integration_test/integration_test_driver.dart';

/// Host side of the performance runs: collects the frame-timing summaries the
/// app reports and writes them to build/integration_response_data.json.
///
///   flutter drive --profile -d macos \
///     --driver=test_driver/perf_driver.dart \
///     --target=integration_test/perf/open_session_perf.dart
Future<void> main() => integrationDriver();
