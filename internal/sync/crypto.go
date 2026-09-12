// Package sync keeps a gocode user's settings identical on every machine
// they sign in from, against their gocoder.org account.
//
// The settings travel as one bundle — the global config file, the TUI theme
// pick, and per-project gocode.json files — sealed with AES-256-GCM under a
// key derived from the account password (PBKDF2-HMAC-SHA256). The server
// stores only the envelope: it never sees the password, the key, or the
// settings themselves.
//
// The envelope format is shared byte-for-byte with the website's WebCrypto
// implementation (website/frontend/src/lib/synccrypto.ts); the fixed test
// vectors in crypto_test.go and synccrypto.test.ts must stay identical.
package sync

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
)

// EnvelopeVersion is the only envelope version this client reads or writes.
const EnvelopeVersion = 1

// DefaultIterations is the PBKDF2 iteration count for new envelopes. The
// count lives in the envelope, so it can be raised without a protocol
// change; 310k keeps derivation under ~100ms in a browser.
const DefaultIterations = 310_000

// Envelope is the on-the-wire ciphertext the server stores verbatim.
type Envelope struct {
	Version int    `json:"v"`
	KDF     string `json:"kdf"`
	Iter    int    `json:"iter"`
	Salt    string `json:"salt"`  // hex, 16 bytes
	Nonce   string `json:"nonce"` // hex, 12 bytes
	CT      string `json:"ct"`    // base64url
}

// Key is a derived 256-bit AES-GCM key.
type Key [32]byte

// DeriveKey derives the sync key from the account password and the
// envelope's salt. The same (password, salt, iter) triple always yields the
// same key on Go and WebCrypto.
func DeriveKey(password, saltHex string, iterations int) (Key, error) {
	salt, err := hex.DecodeString(saltHex)
	if err != nil {
		return Key{}, fmt.Errorf("sync: salt is not hex: %w", err)
	}
	if len(salt) < 8 {
		return Key{}, fmt.Errorf("sync: salt too short (%d bytes)", len(salt))
	}
	if iterations < 1000 {
		return Key{}, fmt.Errorf("sync: iteration count %d is suspiciously low", iterations)
	}
	key, err := pbkdf2.Key(sha256.New, password, salt, iterations, 32)
	if err != nil {
		return Key{}, fmt.Errorf("sync: derive key: %w", err)
	}
	var out Key
	copy(out[:], key)
	return out, nil
}

// NewSalt returns a fresh 16-byte salt, hex-encoded.
func NewSalt() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("sync: crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(b)
}

// ErrDecrypt reports a wrong password or tampered ciphertext — either way
// the envelope must not be trusted and nothing must be overwritten.
var ErrDecrypt = fmt.Errorf("sync: decryption failed (wrong password or tampered data)")

// Seal encrypts plaintext under key with a fresh nonce, reusing saltHex and
// iterations in the envelope so a future open derives the same key.
func Seal(plaintext string, key Key, saltHex string, iterations int) (*Envelope, error) {
	nonce := make([]byte, 12)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("sync: nonce: %w", err)
	}
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	ct := gcm.Seal(nil, nonce, []byte(plaintext), nil)
	return &Envelope{
		Version: EnvelopeVersion,
		KDF:     "pbkdf2-sha256",
		Iter:    iterations,
		Salt:    saltHex,
		Nonce:   hex.EncodeToString(nonce),
		CT:      base64.RawURLEncoding.EncodeToString(ct),
	}, nil
}

// Open decrypts an envelope. A GCM authentication failure is ErrDecrypt —
// the caller pauses sync rather than writing garbage.
func Open(env *Envelope, key Key) (string, error) {
	if env.Version != EnvelopeVersion {
		return "", fmt.Errorf("%w: envelope version %d", ErrDecrypt, env.Version)
	}
	nonce, err := hex.DecodeString(env.Nonce)
	if err != nil {
		return "", fmt.Errorf("%w: bad nonce encoding", ErrDecrypt)
	}
	ct, err := base64.RawURLEncoding.DecodeString(env.CT)
	if err != nil {
		return "", fmt.Errorf("%w: bad ciphertext encoding", ErrDecrypt)
	}
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	plaintext, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return "", ErrDecrypt
	}
	return string(plaintext), nil
}

// Rekey re-encrypts plaintext under newPassword, preserving the envelope's
// salt — the salt an offline CLI derived its key against must never rotate.
func Rekey(plaintext string, env *Envelope, newPassword string) (*Envelope, error) {
	key, err := DeriveKey(newPassword, env.Salt, env.Iter)
	if err != nil {
		return nil, err
	}
	return Seal(plaintext, key, env.Salt, env.Iter)
}
