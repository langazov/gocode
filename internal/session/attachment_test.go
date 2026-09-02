package session

import (
	"encoding/json"
	"testing"

	"github.com/anomalyco/opencode-go/internal/llm"
)

// TestAttachmentsReachTheModel is the point of the whole chain: Prompt.Files
// was stored on the message and rendered as pills in the interface, but never
// translated for the request — so an attached image was invisible to the model.
func TestAttachmentsReachTheModel(t *testing.T) {
	data, _ := json.Marshal(UserMessage{
		Text: "what is in this image?",
		Files: []FileAttachment{{
			URI:  "data:image/png;base64,AAAA",
			Mime: "image/png",
			Name: "shot.png",
		}},
	})
	messages, err := toLLMMessage(StoredMessage{ID: "m1", Type: TypeUser, Data: data}, ModelRef{})
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 {
		t.Fatalf("got %d messages, want 1", len(messages))
	}

	var text, image int
	for _, part := range messages[0].Content {
		switch part.Type {
		case llm.PartText:
			text++
		case llm.PartImage:
			image++
			if part.Mime != "image/png" {
				t.Errorf("mime = %q, want image/png", part.Mime)
			}
			if part.Data != "AAAA" {
				t.Errorf("data = %q, want the base64 payload without the data: prefix", part.Data)
			}
		}
	}
	if text != 1 || image != 1 {
		t.Errorf("got %d text and %d image parts, want 1 each", text, image)
	}
}

// TestNonDataAttachmentsAreSkipped: a file: reference recorded by another
// client has no bytes to send, and forwarding it would be a dead reference.
func TestNonDataAttachmentsAreSkipped(t *testing.T) {
	data, _ := json.Marshal(UserMessage{
		Text:  "hi",
		Files: []FileAttachment{{URI: "file:///tmp/a.png", Mime: "image/png"}},
	})
	messages, err := toLLMMessage(StoredMessage{ID: "m1", Type: TypeUser, Data: data}, ModelRef{})
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range messages[0].Content {
		if part.Type == llm.PartImage {
			t.Errorf("a file: attachment must not be forwarded: %+v", part)
		}
	}
}

func TestDecodeDataURI(t *testing.T) {
	cases := []struct {
		uri  string
		mime string
		data string
		ok   bool
	}{
		{"data:image/png;base64,AAAA", "image/png", "AAAA", true},
		{"data:application/pdf;base64,QkJC", "application/pdf", "QkJC", true},
		{"file:///tmp/a.png", "", "", false},
		{"https://example.com/a.png", "", "", false},
		{"data:text/plain,hello", "", "", false}, // not base64
		{"", "", "", false},
	}
	for _, c := range cases {
		mime, data, ok := decodeDataURI(c.uri)
		if ok != c.ok || mime != c.mime || data != c.data {
			t.Errorf("decodeDataURI(%q) = (%q, %q, %v), want (%q, %q, %v)",
				c.uri, mime, data, ok, c.mime, c.data, c.ok)
		}
	}
}

// TestTextOnlyMessageIsUnchanged: adding attachment support must not alter a
// message that has none.
func TestTextOnlyMessageIsUnchanged(t *testing.T) {
	data, _ := json.Marshal(UserMessage{Text: "just text"})
	messages, err := toLLMMessage(StoredMessage{ID: "m1", Type: TypeUser, Data: data}, ModelRef{})
	if err != nil {
		t.Fatal(err)
	}
	if len(messages[0].Content) != 1 || messages[0].Content[0].Type != llm.PartText {
		t.Errorf("content = %+v, want a single text part", messages[0].Content)
	}
}
