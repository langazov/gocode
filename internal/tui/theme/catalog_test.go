package theme

import "testing"

// TestNamesIncludesEveryBundledTheme guards against a catalog build that
// silently drops an asset (e.g. a future file this loader can't parse).
func TestNamesIncludesEveryBundledTheme(t *testing.T) {
	entries, err := assetsFS.ReadDir("assets")
	if err != nil {
		t.Fatal(err)
	}
	names := Names()
	set := make(map[string]bool, len(names))
	for _, n := range names {
		set[n] = true
	}
	for _, e := range entries {
		id := e.Name()[:len(e.Name())-len(".json")]
		for _, mode := range [2]string{"dark", "light"} {
			if !set[id+"-"+mode] {
				t.Errorf("Names() is missing %q", id+"-"+mode)
			}
		}
	}
}

// TestResolveEveryCatalogTheme is a smoke test that every bundled asset
// resolves to usable, distinct-enough colors for both modes, catching a
// resolver bug (a bad ref, a missed key) across the whole catalog at once
// rather than one theme at a time.
func TestResolveEveryCatalogTheme(t *testing.T) {
	for _, name := range Names() {
		th := Resolve(name)
		if th.Name != name && !(name == "gocode-dark" || name == "gocode-light") {
			t.Errorf("Resolve(%q).Name = %q", name, th.Name)
		}
		if th.Background == nil || th.Text == nil || th.Primary == nil {
			t.Errorf("Resolve(%q) left a core color nil", name)
		}
	}
}

// TestOpencodeCatalogEntryMatchesHandPortedDefault cross-checks the
// generic JSON resolver in catalog.go against Dark()/Light(), the
// hand-transcribed values copied from the same opencode.json this loader
// also reads — if they ever disagree, one of the two is wrong.
func TestOpencodeCatalogEntryMatchesHandPortedDefault(t *testing.T) {
	cases := []struct {
		catalogName string
		want        Theme
	}{
		{"opencode-dark", Dark()},
		{"opencode-light", Light()},
	}
	for _, c := range cases {
		got, ok := catalog[c.catalogName]
		if !ok {
			t.Fatalf("catalog missing %q", c.catalogName)
		}
		want := c.want.Colors.Normalize()
		if Hex(got.Primary) != Hex(want.Primary) ||
			Hex(got.Secondary) != Hex(want.Secondary) ||
			Hex(got.Accent) != Hex(want.Accent) ||
			Hex(got.Info) != Hex(want.Info) ||
			Hex(got.Error) != Hex(want.Error) ||
			Hex(got.Warning) != Hex(want.Warning) ||
			Hex(got.Success) != Hex(want.Success) ||
			Hex(got.Text) != Hex(want.Text) ||
			Hex(got.TextMuted) != Hex(want.TextMuted) ||
			Hex(got.Background) != Hex(want.Background) ||
			Hex(got.BackgroundPanel) != Hex(want.BackgroundPanel) ||
			Hex(got.BackgroundElement) != Hex(want.BackgroundElement) ||
			Hex(got.Border) != Hex(want.Border) ||
			Hex(got.BorderActive) != Hex(want.BorderActive) ||
			Hex(got.BorderSubtle) != Hex(want.BorderSubtle) {
			t.Errorf("%s: catalog resolver disagrees with the hand-ported default\n got  %+v\n want %+v", c.catalogName, got.Colors, want)
		}
	}
}
