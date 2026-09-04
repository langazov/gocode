package memory

import "testing"

// The upsert deliberately leaves `pinned` alone on conflict: an agent
// re-saving an instruction through memory_write must not silently un-pin what
// the user pinned through the interface.
//
// This is the load-bearing half of a trade — it also means a Create carrying
// Pinned:true does nothing when the memory already exists, which callers that
// expose a --pin flag have to compensate for (see runMemoryAdd). Both halves
// are pinned here so neither can drift unnoticed.
func TestCreateDoesNotChangePinOnConflict(t *testing.T) {
	s, ctx := testStore(t)
	created := create(t, s, ctx, "prj_1", "Keep this one first")

	pinned := true
	if _, err := s.Update(ctx, created.ID, Patch{Pinned: &pinned}); err != nil {
		t.Fatal(err)
	}

	// An agent re-save: same content, no pin requested.
	resaved, err := s.Create(ctx, Memory{
		Scope: "prj_1", Content: "Keep this one first", Origin: OriginAgent,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !resaved.Pinned {
		t.Error("a re-save un-pinned a memory the user had pinned")
	}

	// The other direction: asking to pin an existing memory through Create
	// does not take, which is why callers follow up with an Update.
	other := create(t, s, ctx, "prj_1", "Ordinary rule")
	askedToPin, err := s.Create(ctx, Memory{
		Scope: "prj_1", Content: "Ordinary rule", Pinned: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if askedToPin.Pinned {
		t.Error("Create now applies Pinned on conflict; runMemoryAdd's follow-up Update is redundant")
	}
	if askedToPin.ID != other.ID {
		t.Errorf("id = %q, want the existing %q", askedToPin.ID, other.ID)
	}
}
