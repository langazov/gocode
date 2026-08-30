package credential

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/anomalyco/opencode-go/internal/db"
)

func openStore(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	database, err := db.OpenAndMigrate(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	return NewStore(database)
}

func TestCreateAndGet(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()

	info, err := store.Create(ctx, CreateInput{
		IntegrationID: "integ_1",
		Label:         "work",
		Value:         Key{Key: "sk-secret"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if info.ID == "" {
		t.Fatal("expected generated ID")
	}
	got, err := store.Get(ctx, info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("expected credential")
	}
	key, ok := got.Value.(Key)
	if !ok {
		t.Fatalf("expected Key value, got %T", got.Value)
	}
	if key.Key != "sk-secret" {
		t.Fatalf("expected key roundtrip, got %s", key.Key)
	}
}

func TestCreateReplacesPerIntegration(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	if _, err := store.Create(ctx, CreateInput{IntegrationID: "integ_1", Value: Key{Key: "first"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(ctx, CreateInput{IntegrationID: "integ_1", Value: Key{Key: "second"}}); err != nil {
		t.Fatal(err)
	}
	list, err := store.List(ctx, "integ_1")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("expected replacement, got %d credentials", len(list))
	}
	if list[0].Value.(Key).Key != "second" {
		t.Fatalf("expected second value, got %v", list[0].Value)
	}
}

func TestDefaultLabel(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	info, err := store.Create(ctx, CreateInput{IntegrationID: "integ_1", Value: Key{Key: "k"}})
	if err != nil {
		t.Fatal(err)
	}
	if info.Label != "default" {
		t.Fatalf("expected default label, got %s", info.Label)
	}
}

func TestOAuthRoundtrip(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	oauth := OAuth{
		MethodID: "method_1",
		Refresh:  "refresh-token",
		Access:   "access-token",
		Expires:  1735689600000,
		Metadata: map[string]any{"org": "acme"},
	}
	info, err := store.Create(ctx, CreateInput{IntegrationID: "integ_oauth", Value: oauth})
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(ctx, info.ID)
	if err != nil {
		t.Fatal(err)
	}
	decoded, ok := got.Value.(OAuth)
	if !ok {
		t.Fatalf("expected OAuth value, got %T", got.Value)
	}
	if decoded.Access != "access-token" || decoded.Refresh != "refresh-token" || decoded.Expires != 1735689600000 {
		t.Fatalf("unexpected oauth roundtrip: %+v", decoded)
	}
	if decoded.Metadata["org"] != "acme" {
		t.Fatalf("expected metadata roundtrip, got %v", decoded.Metadata)
	}
}

func TestUpdate(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	info, err := store.Create(ctx, CreateInput{IntegrationID: "integ_1", Label: "old", Value: Key{Key: "k"}})
	if err != nil {
		t.Fatal(err)
	}
	newLabel := "new"
	if err := store.Update(ctx, info.ID, Updates{Label: &newLabel}); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(ctx, info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Label != "new" {
		t.Fatalf("expected updated label, got %s", got.Label)
	}
	if err := store.Update(ctx, info.ID, Updates{Value: Key{Key: "rotated"}}); err != nil {
		t.Fatal(err)
	}
	got, err = store.Get(ctx, info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Value.(Key).Key != "rotated" {
		t.Fatalf("expected rotated key, got %v", got.Value)
	}
	if got.Label != "new" {
		t.Fatalf("label should be preserved, got %s", got.Label)
	}
}

func TestRemoveAndAll(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	a, err := store.Create(ctx, CreateInput{IntegrationID: "integ_a", Value: Key{Key: "a"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(ctx, CreateInput{IntegrationID: "integ_b", Value: Key{Key: "b"}}); err != nil {
		t.Fatal(err)
	}
	if err := store.Remove(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	all, err := store.All(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("expected 1 remaining, got %d", len(all))
	}
	if all[0].IntegrationID != "integ_b" {
		t.Fatalf("expected integ_b to remain, got %s", all[0].IntegrationID)
	}
}

func TestGetMissing(t *testing.T) {
	store := openStore(t)
	got, err := store.Get(context.Background(), ID("cred_missing"))
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("expected nil for missing credential, got %+v", got)
	}
}
