package mcp

import "testing"

func TestStoreSetGetRemoveRoundTrip(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	if _, ok := StoreGet("myserver", "https://example.com"); ok {
		t.Fatal("expected no entry before Set")
	}

	entry := Entry{ServerURL: "https://example.com", ClientID: "abc", AccessToken: "tok123", RefreshToken: "ref456"}
	if err := StoreSet("myserver", entry); err != nil {
		t.Fatal(err)
	}

	got, ok := StoreGet("myserver", "https://example.com")
	if !ok {
		t.Fatal("expected entry after Set")
	}
	if got.ClientID != "abc" || got.AccessToken != "tok123" {
		t.Fatalf("got = %+v", got)
	}

	if err := StoreRemove("myserver"); err != nil {
		t.Fatal(err)
	}
	if _, ok := StoreGet("myserver", "https://example.com"); ok {
		t.Fatal("expected no entry after Remove")
	}
}

// TestStoreGetInvalidatesOnURLChange mirrors getForUrl() in mcp/auth.ts:
// stored credentials only apply to the server URL they were recorded
// against, so a server whose URL changed doesn't reuse stale credentials.
func TestStoreGetInvalidatesOnURLChange(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	if err := StoreSet("s", Entry{ServerURL: "https://old.example.com", AccessToken: "tok"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := StoreGet("s", "https://new.example.com"); ok {
		t.Fatal("expected no entry for a different URL")
	}
	if _, ok := StoreGet("s", "https://old.example.com"); !ok {
		t.Fatal("expected the entry to still match its original URL")
	}
}

func TestHasTokens(t *testing.T) {
	if (Entry{}).HasTokens() {
		t.Fatal("empty entry should report no tokens")
	}
	if !(Entry{AccessToken: "x"}).HasTokens() {
		t.Fatal("entry with an access token should report tokens present")
	}
}
