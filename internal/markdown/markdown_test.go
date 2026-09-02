package markdown

import (
	"reflect"
	"testing"
)

func TestParseScalars(t *testing.T) {
	doc, err := Parse("---\nname: review\ndescription: Reviews code\nslash: true\nsteps: 12\n---\nBody text\n")
	if err != nil {
		t.Fatal(err)
	}
	if doc.String("name") != "review" {
		t.Fatalf("name = %q", doc.String("name"))
	}
	if doc.String("description") != "Reviews code" {
		t.Fatalf("description = %q", doc.String("description"))
	}
	if !doc.Bool("slash") {
		t.Fatal("slash should be true")
	}
	if steps, ok := doc.Int("steps"); !ok || steps != 12 {
		t.Fatalf("steps = %#v", doc.Frontmatter["steps"])
	}
	if doc.Content != "Body text\n" {
		t.Fatalf("content = %q", doc.Content)
	}
}

// TestParseKeepsUnquotedColons is the case the TypeScript loader sanitizes
// for: a description containing a colon must survive as one string.
func TestParseKeepsUnquotedColons(t *testing.T) {
	doc, err := Parse("---\ndescription: Use when: editing config files\n---\nbody")
	if err != nil {
		t.Fatal(err)
	}
	if got := doc.String("description"); got != "Use when: editing config files" {
		t.Fatalf("description = %q", got)
	}
}

func TestParseQuotedValues(t *testing.T) {
	doc, err := Parse("---\na: \"quoted # not a comment\"\nb: 'single'\nc: plain # trailing\n---\n")
	if err != nil {
		t.Fatal(err)
	}
	if doc.String("a") != "quoted # not a comment" {
		t.Fatalf("a = %q", doc.String("a"))
	}
	if doc.String("b") != "single" {
		t.Fatalf("b = %q", doc.String("b"))
	}
	if doc.String("c") != "plain" {
		t.Fatalf("c = %q", doc.String("c"))
	}
}

func TestParseBlockScalar(t *testing.T) {
	doc, err := Parse("---\nprompt: |\n  line one\n  line two\nname: x\n---\nbody")
	if err != nil {
		t.Fatal(err)
	}
	if got := doc.String("prompt"); got != "line one\nline two\n" {
		t.Fatalf("prompt = %q", got)
	}
	if doc.String("name") != "x" {
		t.Fatalf("the key after a block scalar was lost: %q", doc.String("name"))
	}
}

func TestParseStrippedBlockScalar(t *testing.T) {
	doc, err := Parse("---\nprompt: |-\n  only line\n---\n")
	if err != nil {
		t.Fatal(err)
	}
	if got := doc.String("prompt"); got != "only line" {
		t.Fatalf("prompt = %q", got)
	}
}

func TestParseNestedMap(t *testing.T) {
	doc, err := Parse("---\npermission:\n  bash: ask\n  edit: allow\nmode: subagent\n---\n")
	if err != nil {
		t.Fatal(err)
	}
	nested, ok := doc.Frontmatter["permission"].(map[string]any)
	if !ok {
		t.Fatalf("permission = %#v", doc.Frontmatter["permission"])
	}
	if nested["bash"] != "ask" || nested["edit"] != "allow" {
		t.Fatalf("nested = %#v", nested)
	}
	if doc.String("mode") != "subagent" {
		t.Fatalf("the key after a nested map was lost: %q", doc.String("mode"))
	}
}

func TestParseLists(t *testing.T) {
	doc, err := Parse("---\ntools:\n  - read\n  - write\ninline: [a, b]\n---\n")
	if err != nil {
		t.Fatal(err)
	}
	if got := doc.Frontmatter["tools"]; !reflect.DeepEqual(got, []any{"read", "write"}) {
		t.Fatalf("tools = %#v", got)
	}
	if got := doc.Frontmatter["inline"]; !reflect.DeepEqual(got, []any{"a", "b"}) {
		t.Fatalf("inline = %#v", got)
	}
}

func TestParseWithoutFrontmatter(t *testing.T) {
	doc, err := Parse("# Just markdown\n\nNo header here.")
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Frontmatter) != 0 {
		t.Fatalf("unexpected frontmatter: %#v", doc.Frontmatter)
	}
	if doc.Content != "# Just markdown\n\nNo header here." {
		t.Fatalf("content = %q", doc.Content)
	}
}

func TestParseUnterminatedFrontmatterIsBody(t *testing.T) {
	input := "---\nname: x\nstill going"
	doc, err := Parse(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Frontmatter) != 0 || doc.Content != input {
		t.Fatalf("an unterminated header should be treated as body: %#v", doc)
	}
}

func TestParseCRLF(t *testing.T) {
	doc, err := Parse("---\r\nname: crlf\r\n---\r\nbody\r\n")
	if err != nil {
		t.Fatal(err)
	}
	if doc.String("name") != "crlf" {
		t.Fatalf("name = %q", doc.String("name"))
	}
	if doc.Content != "body\n" {
		t.Fatalf("content = %q", doc.Content)
	}
}

func TestParseIgnoresComments(t *testing.T) {
	doc, err := Parse("---\n# a comment\nname: x\n---\n")
	if err != nil {
		t.Fatal(err)
	}
	if doc.String("name") != "x" {
		t.Fatalf("name = %q", doc.String("name"))
	}
	if len(doc.Frontmatter) != 1 {
		t.Fatalf("comment leaked into frontmatter: %#v", doc.Frontmatter)
	}
}

// FuzzParse checks the parser never panics: frontmatter comes from
// user-authored files and is routinely malformed.
func FuzzParse(f *testing.F) {
	f.Add("---\nname: x\n---\nbody")
	f.Add("---\nprompt: |\n  block\n---\n")
	f.Add("---\nlist:\n  - a\n---\n")
	f.Add("---\n---\n")
	f.Add("no frontmatter")
	f.Add("")
	f.Fuzz(func(t *testing.T, input string) {
		doc, err := Parse(input)
		if err != nil {
			return
		}
		if doc.Frontmatter == nil {
			t.Fatalf("nil frontmatter map for %q", input)
		}
		// Content is always a suffix of the input (verbatim when there is no
		// frontmatter, CRLF-normalized when there is), so it can never grow.
		if len(doc.Content) > len(input) {
			t.Fatalf("content grew past the input for %q", input)
		}
	})
}
