import 'package:flutter_test/flutter_test.dart';
import 'package:gocode_gui/features/session/prompt_history.dart';

void main() {
  group('PromptHistoryCursor', () {
    test('ignores ↑ when the cursor is not at the start of a draft', () {
      final cursor = PromptHistoryCursor();
      final text = cursor.older(
        ['older prompt'],
        atStart: false,
        currentText: 'wor|king on something',
      );
      expect(text, isNull);
      expect(cursor.isBrowsing, isFalse);
    });

    test('↑ from the start recalls the most recent prompt first', () {
      final cursor = PromptHistoryCursor();
      final text = cursor.older(
        ['second', 'first'],
        atStart: true,
        currentText: '',
      );
      expect(text, 'second');
      expect(cursor.isBrowsing, isTrue);
    });

    test('repeated ↑ keeps stepping back regardless of cursor position', () {
      final cursor = PromptHistoryCursor();
      cursor.older(['second', 'first'], atStart: true, currentText: '');
      // Not at the start any more (cursor lands at the end after a recall),
      // but browsing is already underway, so it should still step back.
      final text = cursor.older(
        ['second', 'first'],
        atStart: false,
        currentText: 'second',
      );
      expect(text, 'first');
    });

    test('↑ at the oldest entry returns null instead of looping', () {
      final cursor = PromptHistoryCursor();
      cursor.older(['only'], atStart: true, currentText: '');
      final text = cursor.older(
        ['only'],
        atStart: false,
        currentText: 'only',
      );
      expect(text, isNull);
    });

    test('↓ is ignored unless already browsing', () {
      final cursor = PromptHistoryCursor();
      final text = cursor.newer(['second', 'first'], atEnd: true);
      expect(text, isNull);
    });

    test('↓ is ignored unless the cursor is at the end', () {
      final cursor = PromptHistoryCursor();
      cursor.older(['second', 'first'], atStart: true, currentText: '');
      final text = cursor.newer(['second', 'first'], atEnd: false);
      expect(text, isNull);
      expect(cursor.isBrowsing, isTrue);
    });

    test('↓ walks back to the original draft and stops browsing', () {
      final cursor = PromptHistoryCursor();
      cursor.older(['second', 'first'], atStart: true, currentText: 'draft');
      final text = cursor.newer(['second', 'first'], atEnd: true);
      expect(text, 'draft');
      expect(cursor.isBrowsing, isFalse);
    });

    test('an empty draft is restored as empty, not dropped', () {
      final cursor = PromptHistoryCursor();
      cursor.older(['second'], atStart: true, currentText: '');
      final text = cursor.newer(['second'], atEnd: true);
      expect(text, '');
    });

    test('an edit away from the recalled text abandons the browse', () {
      final cursor = PromptHistoryCursor();
      cursor.older(['second', 'first'], atStart: true, currentText: '');
      cursor.noteEdit('second, but edited', 'second');
      expect(cursor.isBrowsing, isFalse);
      // Now behaves like a fresh draft: ↑ needs to start at the top again.
      final text = cursor.older(
        ['second', 'first'],
        atStart: false,
        currentText: 'second, but edited',
      );
      expect(text, isNull);
    });

    test('a no-op text change (our own recall) does not abandon browsing', () {
      final cursor = PromptHistoryCursor();
      cursor.older(['second', 'first'], atStart: true, currentText: '');
      cursor.noteEdit('second', 'second');
      expect(cursor.isBrowsing, isTrue);
    });

    test('empty history is a no-op', () {
      final cursor = PromptHistoryCursor();
      expect(
        cursor.older([], atStart: true, currentText: ''),
        isNull,
      );
    });
  });
}
