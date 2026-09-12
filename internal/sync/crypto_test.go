package sync

import (
	"encoding/json"
	"strings"
	"testing"
)

// The fixed vectors here MUST stay identical to the website's
// src/lib/synccrypto.test.ts — together they prove Go's crypto/cipher and
// the browser's WebCrypto produce interchangeable envelopes.

const (
	vectorPassword = "correct horse battery staple"
	vectorSalt     = "00112233445566778899aabbccddeeff"
	// Low iteration count keeps the vector derivation fast; the path is
	// identical, only the loop count differs from production.
	vectorIter = 1000
)

func TestSealOpenRoundTrip(t *testing.T) {
	bundle := `{"version":1,"files":{"global":"{\"theme\":\"x\"}"}}`
	key, err := DeriveKey(vectorPassword, vectorSalt, vectorIter)
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	env, err := Seal(bundle, key, vectorSalt, vectorIter)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if env.Version != 1 || env.KDF != "pbkdf2-sha256" || env.Iter != vectorIter {
		t.Errorf("envelope metadata wrong: %+v", env)
	}
	if len(env.Nonce) != 24 { // 12 bytes hex
		t.Errorf("nonce: got %d hex chars, want 24", len(env.Nonce))
	}
	plaintext, err := Open(env, key)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if plaintext != bundle {
		t.Errorf("round-trip mismatch:\n got %q\nwant %q", plaintext, bundle)
	}
}

func TestOpenRejectsWrongPassword(t *testing.T) {
	key, _ := DeriveKey(vectorPassword, vectorSalt, vectorIter)
	env, _ := Seal("secret", key, vectorSalt, vectorIter)
	other, err := DeriveKey("wrong password", vectorSalt, vectorIter)
	if err != nil {
		t.Fatalf("derive other: %v", err)
	}
	if _, err := Open(env, other); err == nil {
		t.Fatal("opening with the wrong key must fail")
	} else if !strings.Contains(err.Error(), "decryption failed") {
		t.Errorf("expected ErrDecrypt-shaped error, got %v", err)
	}
}

func TestOpenRejectsTampering(t *testing.T) {
	key, _ := DeriveKey(vectorPassword, vectorSalt, vectorIter)
	env, _ := Seal("secret", key, vectorSalt, vectorIter)
	flipped := env.CT[:len(env.CT)-4] + "AAAA"
	if _, err := Open(&Envelope{Version: env.Version, KDF: env.KDF, Iter: env.Iter,
		Salt: env.Salt, Nonce: env.Nonce, CT: flipped}, key); err == nil {
		t.Fatal("tampered ciphertext must fail authentication")
	}
}

func TestFreshNoncePerSeal(t *testing.T) {
	key, _ := DeriveKey(vectorPassword, vectorSalt, vectorIter)
	a, _ := Seal("same", key, vectorSalt, vectorIter)
	b, _ := Seal("same", key, vectorSalt, vectorIter)
	if a.Nonce == b.Nonce {
		t.Error("nonce reuse across seals")
	}
	if a.CT == b.CT {
		t.Error("identical ciphertext across seals (nonce not used?)")
	}
}

func TestRekeyPreservesSalt(t *testing.T) {
	key, _ := DeriveKey(vectorPassword, vectorSalt, vectorIter)
	env, _ := Seal("payload", key, vectorSalt, vectorIter)
	rekeyed, err := Rekey("payload", env, "new-password-123")
	if err != nil {
		t.Fatalf("rekey: %v", err)
	}
	if rekeyed.Salt != env.Salt {
		t.Errorf("salt rotated: %s -> %s (must be preserved)", env.Salt, rekeyed.Salt)
	}
	// The CLI derives against the same salt, so the new password must open it.
	newKey, _ := DeriveKey("new-password-123", rekeyed.Salt, rekeyed.Iter)
	if plaintext, err := Open(rekeyed, newKey); err != nil || plaintext != "payload" {
		t.Errorf("rekeyed envelope did not open: %v %q", err, plaintext)
	}
}

func TestEnvelopeJSONShapeMatchesWebsite(t *testing.T) {
	key, _ := DeriveKey(vectorPassword, vectorSalt, vectorIter)
	env, _ := Seal("x", key, vectorSalt, vectorIter)
	encoded, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// The exact field names the website's parseEnvelope expects.
	for _, field := range []string{`"v":1`, `"kdf":"pbkdf2-sha256"`, `"iter":1000`, `"salt":"`, `"nonce":"`, `"ct":"`} {
		if !strings.Contains(string(encoded), field) {
			t.Errorf("envelope JSON missing %s: %s", field, encoded)
		}
	}
}

func TestBundleRoundTrip(t *testing.T) {
	b := NewBundle()
	b.Files[KeyGlobal] = "{\"theme\":\"dark\"}"
	b.Files[KeyTheme] = "{\"theme\":\"gocode-dark\"}"
	b.Files[ProjectKey("https://github.com/a/b.git", "gocode.json")] = "{}"
	plaintext, err := b.Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	parsed, err := ParseBundle(plaintext)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(parsed.Files) != 3 || parsed.Files[KeyGlobal] != "{\"theme\":\"dark\"}" {
		t.Errorf("bundle round-trip mismatch: %+v", parsed.Files)
	}
	if _, err := ParseBundle(`{"version":2}`); err == nil {
		t.Error("future bundle version must be rejected")
	}
}

func TestProjectKey(t *testing.T) {
	url, rel, ok := ParseProjectKey(ProjectKey("https://x/y.git", ".gocode/gocode.json"))
	if !ok || url != "https://x/y.git" || rel != ".gocode/gocode.json" {
		t.Errorf("project key split failed: %q %q %v", url, rel, ok)
	}
	if _, _, ok := ParseProjectKey("global"); ok {
		t.Error("non-project key parsed as project")
	}
	if _, _, ok := ParseProjectKey("project:missing-separator"); ok {
		t.Error("malformed project key accepted")
	}
}
