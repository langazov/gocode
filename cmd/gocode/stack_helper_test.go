package main

import (
	"context"
	"testing"
)

// bootStackT boots a stack for a test and registers its database to close on
// cleanup. Windows refuses to delete an open file, so leaving the sqlite
// handle open past the test makes t.TempDir's cleanup fail.
func bootStackT(t *testing.T, ctx context.Context, modelFlag string) *stack {
	t.Helper()
	s, err := bootStack(ctx, modelFlag)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("stack.Close: %v", err)
		}
	})
	return s
}
