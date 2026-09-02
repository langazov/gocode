package patch

import (
	"strings"
	"testing"
)

func TestParseAddDeleteUpdate(t *testing.T) {
	text := strings.Join([]string{
		"*** Begin Patch",
		"*** Add File: hello.txt",
		"+Hello world",
		"*** Update File: src/app.py",
		"*** Move to: src/main.py",
		"@@ def greet():",
		`-print("Hi")`,
		`+print("Hello, world!")`,
		"*** Delete File: obsolete.txt",
		"*** End Patch",
	}, "\n")

	hunks, err := Parse(text)
	if err != nil {
		t.Fatal(err)
	}
	if len(hunks) != 3 {
		t.Fatalf("parsed %d hunks, want 3", len(hunks))
	}
	if hunks[0].Type != HunkAdd || hunks[0].Path != "hello.txt" || hunks[0].Contents != "Hello world" {
		t.Fatalf("unexpected add hunk: %+v", hunks[0])
	}
	update := hunks[1]
	if update.Type != HunkUpdate || update.Path != "src/app.py" || update.MovePath != "src/main.py" {
		t.Fatalf("unexpected update hunk: %+v", update)
	}
	if len(update.Chunks) != 1 || update.Chunks[0].ChangeContext != "def greet():" {
		t.Fatalf("unexpected chunks: %+v", update.Chunks)
	}
	if hunks[2].Type != HunkDelete || hunks[2].Path != "obsolete.txt" {
		t.Fatalf("unexpected delete hunk: %+v", hunks[2])
	}
}

func TestParseRejectsMalformed(t *testing.T) {
	cases := map[string]string{
		"missing markers":   "*** Add File: x\n+y",
		"empty add path":    "*** Begin Patch\n*** Add File:\n+y\n*** End Patch",
		"empty delete path": "*** Begin Patch\n*** Delete File:\n*** End Patch",
		"empty move path":   "*** Begin Patch\n*** Update File: a\n*** Move to:\n@@\n-x\n*** End Patch",
		"bad add line":      "*** Begin Patch\n*** Add File: x\nnot a plus line\n*** End Patch",
		"update no chunk":   "*** Begin Patch\n*** Update File: a\n*** End Patch",
		"bad chunk line":    "*** Begin Patch\n*** Update File: a\n@@\n?bogus\n*** End Patch",
		"stray line":        "*** Begin Patch\nrandom\n*** End Patch",
	}
	for name, text := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse(text); err == nil {
				t.Fatal("expected a parse error")
			}
		})
	}
}

func TestParseStripsHeredoc(t *testing.T) {
	text := "cat <<'EOF'\n*** Begin Patch\n*** Delete File: gone.txt\n*** End Patch\nEOF"
	hunks, err := Parse(text)
	if err != nil {
		t.Fatal(err)
	}
	if len(hunks) != 1 || hunks[0].Type != HunkDelete {
		t.Fatalf("heredoc envelope was not stripped: %+v", hunks)
	}
}

func TestDeriveAppliesChunks(t *testing.T) {
	original := "one\ntwo\nthree\n"
	chunks := []UpdateChunk{{OldLines: []string{"two"}, NewLines: []string{"TWO"}}}
	got, bom, err := Derive("f", chunks, original)
	if err != nil {
		t.Fatal(err)
	}
	if got != "one\nTWO\nthree\n" {
		t.Fatalf("derived %q", got)
	}
	if bom {
		t.Fatal("unexpected BOM")
	}
}

// TestDeriveToleratesTypography exercises the fuzzy comparators: a chunk whose
// quotes were smartened still applies.
func TestDeriveToleratesTypography(t *testing.T) {
	original := "value = \"plain\"\n"
	chunks := []UpdateChunk{{
		OldLines: []string{"value = “plain”"},
		NewLines: []string{"value = \"changed\""},
	}}
	got, _, err := Derive("f", chunks, original)
	if err != nil {
		t.Fatalf("smart quotes defeated the match: %v", err)
	}
	if got != "value = \"changed\"\n" {
		t.Fatalf("derived %q", got)
	}
}

func TestDeriveToleratesTrailingWhitespace(t *testing.T) {
	original := "alpha   \nbeta\n"
	chunks := []UpdateChunk{{OldLines: []string{"alpha"}, NewLines: []string{"ALPHA"}}}
	got, _, err := Derive("f", chunks, original)
	if err != nil {
		t.Fatal(err)
	}
	if got != "ALPHA\nbeta\n" {
		t.Fatalf("derived %q", got)
	}
}

func TestDeriveEndOfFileAnchor(t *testing.T) {
	original := "x\nlast\n"
	chunks := []UpdateChunk{{
		OldLines: []string{"last"}, NewLines: []string{"final"}, EndOfFile: true,
	}}
	got, _, err := Derive("f", chunks, original)
	if err != nil {
		t.Fatal(err)
	}
	if got != "x\nfinal\n" {
		t.Fatalf("derived %q", got)
	}
}

func TestDeriveAppendsWhenNoOldLines(t *testing.T) {
	got, _, err := Derive("f", []UpdateChunk{{NewLines: []string{"appended"}}}, "head\n")
	if err != nil {
		t.Fatal(err)
	}
	if got != "head\nappended\n" {
		t.Fatalf("derived %q", got)
	}
}

func TestDerivePreservesBOM(t *testing.T) {
	original := "\uFEFFone\ntwo\n"
	got, bom, err := Derive("f", []UpdateChunk{{OldLines: []string{"one"}, NewLines: []string{"ONE"}}}, original)
	if err != nil {
		t.Fatal(err)
	}
	if !bom {
		t.Fatal("BOM was dropped")
	}
	if strings.HasPrefix(got, "\uFEFF") {
		t.Fatal("derived content should carry the BOM out of band, not inline")
	}
	if JoinBOM(got, bom) != "\uFEFFONE\ntwo\n" {
		t.Fatalf("rejoined %q", JoinBOM(got, bom))
	}
}

func TestDeriveFailsOnMissingContext(t *testing.T) {
	_, _, err := Derive("f", []UpdateChunk{{
		ChangeContext: "nowhere", OldLines: []string{"x"}, NewLines: []string{"y"},
	}}, "a\nb\n")
	if err == nil || !strings.Contains(err.Error(), "failed to find context") {
		t.Fatalf("expected a context failure, got %v", err)
	}
}

func TestDeriveFailsOnMissingLines(t *testing.T) {
	_, _, err := Derive("f", []UpdateChunk{{OldLines: []string{"absent"}, NewLines: []string{"x"}}}, "a\nb\n")
	if err == nil || !strings.Contains(err.Error(), "failed to find expected lines") {
		t.Fatalf("expected a match failure, got %v", err)
	}
}

func TestBOMRoundTrip(t *testing.T) {
	body, bom := SplitBOM("\uFEFFtext")
	if !bom || body != "text" {
		t.Fatalf("split = %q, %v", body, bom)
	}
	if JoinBOM("text", true) != "\uFEFFtext" {
		t.Fatal("join did not restore the BOM")
	}
	if JoinBOM("\uFEFFtext", false) != "text" {
		t.Fatal("join did not strip an existing BOM")
	}
}

// FuzzParse checks the parser never panics on arbitrary input. Patch text
// comes straight from a model, so malformed input is expected, not exceptional.
func FuzzParse(f *testing.F) {
	f.Add("*** Begin Patch\n*** Add File: a\n+x\n*** End Patch")
	f.Add("*** Begin Patch\n*** Update File: a\n@@ ctx\n-x\n+y\n*** End Patch")
	f.Add("*** Begin Patch\n*** Delete File: a\n*** End Patch")
	f.Add("*** Begin Patch\n*** Update File: a\n@@\n*** End of File\n*** End Patch")
	f.Add("cat <<EOF\n*** Begin Patch\n*** End Patch\nEOF")
	f.Add("")
	f.Fuzz(func(t *testing.T, input string) {
		hunks, err := Parse(input)
		if err != nil {
			return
		}
		for _, hunk := range hunks {
			if hunk.Path == "" {
				t.Fatalf("parsed a hunk with an empty path from %q", input)
			}
			if hunk.Type == HunkUpdate && len(hunk.Chunks) == 0 {
				t.Fatalf("parsed an update hunk with no chunks from %q", input)
			}
		}
	})
}

// FuzzDerive checks that applying arbitrary chunks to arbitrary content either
// fails cleanly or produces newline-terminated output — never a panic.
func FuzzDerive(f *testing.F) {
	f.Add("one\ntwo\n", "two", "TWO")
	f.Add("", "", "")
	f.Add("\uFEFFa\n", "a", "b")
	f.Fuzz(func(t *testing.T, original, oldLine, newLine string) {
		chunks := []UpdateChunk{{OldLines: []string{oldLine}, NewLines: []string{newLine}}}
		got, _, err := Derive("f", chunks, original)
		if err != nil {
			return
		}
		if got != "" && !strings.HasSuffix(got, "\n") {
			t.Fatalf("derived content is not newline-terminated: %q", got)
		}
	})
}
