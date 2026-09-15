/// Shell-like history recall for the composer: feed it the (most-recent-
/// first) list of previously sent prompts and the field's cursor state —
/// [older] on ↑, [newer] on ↓ — and it returns the text to show, or null to
/// leave the field alone so the caller falls through to normal cursor
/// movement.
///
/// Kept separate from the widget so the recall/draft-restore state machine
/// is unit-testable without pumping a widget tree.
class PromptHistoryCursor {
  /// Index into the caller-supplied history while browsing; null once the
  /// field holds a fresh, un-recalled draft.
  int? _index;
  String? _draft;

  bool get isBrowsing => _index != null;

  /// ↑ pressed. [atStart] means the cursor sits collapsed at offset 0 —
  /// required to *start* a recall, so normal cursor-up inside a multi-line
  /// draft isn't hijacked. Once browsing, further calls keep stepping back
  /// regardless of where the cursor lands after a recall.
  String? older(
    List<String> history, {
    required bool atStart,
    required String currentText,
  }) {
    if (_index == null && !atStart) return null;
    if (history.isEmpty) return null;
    final next = (_index ?? -1) + 1;
    if (next >= history.length) return null;
    if (_index == null) _draft = currentText;
    _index = next;
    return history[next];
  }

  /// ↓ pressed. Only acts while browsing, and only from [atEnd] — otherwise
  /// it's just moving the cursor down a line.
  String? newer(List<String> history, {required bool atEnd}) {
    final index = _index;
    if (index == null || !atEnd) return null;
    if (index == 0) {
      _index = null;
      final draft = _draft ?? '';
      _draft = null;
      return draft;
    }
    _index = index - 1;
    return history[_index!];
  }

  /// A manual edit — any text other than what [older]/[newer] last handed
  /// back — abandons the browse, so the next ↑ starts a fresh recall.
  void noteEdit(String text, String? lastRecalled) {
    if (_index != null && text != lastRecalled) {
      _index = null;
      _draft = null;
    }
  }
}
