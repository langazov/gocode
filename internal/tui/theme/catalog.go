package theme

// catalog.go ports theme/index.ts's DEFAULT_THEMES + resolveTheme: the full
// set of bundled TS TUI themes (packages/tui/src/theme/assets/*.json,
// copied verbatim into ./assets), resolved into this package's Colors the
// same way the TS side resolves them into its own richer Theme type — walk
// defs then theme for string refs, recurse through {dark,light} variant
// objects picking the active mode, hex strings terminate the chain. Only
// the subset of keys this port's Colors models (see theme.go) is read out
// of each file; the extra diff/markdown/syntax keys those JSON files also
// carry (consumed in TS by opentui's SyntaxStyle, which this port doesn't
// have — see markdown.go's glamour-based substitute) are ignored.
//
// Every asset is registered under two names, "<id>-dark" and "<id>-light"
// (id being the file's basename), rather than mirroring TS's separate
// name+mode selection: this port's theme.Resolve already takes one combined
// name per call site (see the pre-existing "gocode-dark"/"gocode-light"
// pair for the ported default), and splitting mode out into its own bit of
// app state is a larger change than this catalog needs. A handful of assets
// (aura, ayu) define no {dark,light} variants at all — every value in them
// is a flat ref/hex — so their "-dark" and "-light" entries resolve to the
// same colors, which matches how TS would render them too: resolveColor
// only branches on mode when it hits a variant object.

import (
	"embed"
	"encoding/json"
	"fmt"
	"image/color"
	"sort"
	"strings"

	"charm.land/lipgloss/v2"
)

//go:embed assets/*.json
var assetsFS embed.FS

// themeAsset mirrors ThemeJson from theme/index.ts: defs are always literal
// hex strings in every bundled asset (verified against all 33 files), so
// unlike TS's `HexColor | RefName` this is typed directly as a string map;
// theme entries are decoded to `any` since a value is either a string
// (hex or ref) or a {dark, light} object.
type themeAsset struct {
	Defs  map[string]string `json:"defs"`
	Theme map[string]any    `json:"theme"`
}

// catalog maps a registered name ("dracula-dark", "one-dark-light", ...) to
// its resolved Theme, built once from the embedded assets.
var catalog = buildCatalog()

func buildCatalog() map[string]Theme {
	entries, err := assetsFS.ReadDir("assets")
	if err != nil {
		// The assets are embedded at build time; a failure here means the
		// embed itself is broken, not anything a caller can recover from.
		panic("theme: reading embedded assets: " + err.Error())
	}

	out := make(map[string]Theme, len(entries)*2)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		data, err := assetsFS.ReadFile("assets/" + entry.Name())
		if err != nil {
			panic("theme: reading " + entry.Name() + ": " + err.Error())
		}
		var asset themeAsset
		if err := json.Unmarshal(data, &asset); err != nil {
			panic("theme: parsing " + entry.Name() + ": " + err.Error())
		}
		for _, mode := range [2]string{"dark", "light"} {
			colors, err := resolveThemeColors(asset, mode)
			if err != nil {
				panic(fmt.Sprintf("theme: resolving %s (%s): %v", entry.Name(), mode, err))
			}
			out[id+"-"+mode] = Theme{
				Name:            id + "-" + mode,
				Dark:            mode == "dark",
				ThinkingOpacity: thinkingOpacity(asset),
				Colors:          colors.Normalize(),
			}
		}
	}
	return out
}

// thinkingOpacity mirrors resolveTheme's `theme.theme.thinkingOpacity ?? 0.6`.
// No bundled asset sets it, but a future one might.
func thinkingOpacity(asset themeAsset) float64 {
	v, ok := asset.Theme["thinkingOpacity"]
	if !ok {
		return 0.6
	}
	if f, ok := v.(float64); ok {
		return f
	}
	return 0.6
}

// resolveThemeColors reads the Colors subset out of asset for mode.
func resolveThemeColors(asset themeAsset, mode string) (Colors, error) {
	var c Colors
	var firstErr error
	get := func(key string, required bool) color.Color {
		if firstErr != nil {
			return nil
		}
		v, ok := asset.Theme[key]
		if !ok {
			if required {
				firstErr = fmt.Errorf("missing %q", key)
			}
			return nil
		}
		col, err := resolveColorValue(v, mode, asset, nil)
		if err != nil {
			firstErr = fmt.Errorf("%s: %w", key, err)
			return nil
		}
		return col
	}

	c.Primary = get("primary", true)
	c.Secondary = get("secondary", true)
	c.Accent = get("accent", true)
	c.Info = get("info", true)
	c.Error = get("error", true)
	c.Warning = get("warning", true)
	c.Success = get("success", true)
	c.Text = get("text", true)
	c.TextMuted = get("textMuted", true)
	c.Background = get("background", true)
	c.BackgroundPanel = get("backgroundPanel", true)
	c.BackgroundElement = get("backgroundElement", true)
	c.Border = get("border", true)
	c.BorderActive = get("borderActive", true)
	c.BorderSubtle = get("borderSubtle", true)
	// Optional: Normalize() supplies the fallback when a theme doesn't set
	// these, exactly like theme/index.ts's own backward-compatibility path.
	c.SelectedListItemText = get("selectedListItemText", false)
	c.BackgroundMenu = get("backgroundMenu", false)

	if firstErr != nil {
		return Colors{}, firstErr
	}
	return c, nil
}

// resolveColorValue is resolveColor from theme/index.ts: v is a hex string,
// a "transparent"/"none" literal, a def/theme-key reference, or a
// {dark, light} variant object. chain tracks refs already visited on this
// path to catch cycles the same way TS's `chain.includes(c)` does.
func resolveColorValue(v any, mode string, asset themeAsset, chain []string) (color.Color, error) {
	switch t := v.(type) {
	case string:
		if t == "transparent" || t == "none" {
			// lipgloss.NoColor: foreground falls back to the terminal's
			// default text color and background isn't drawn at all — the
			// terminal-safe equivalent of TS's RGBA alpha-0, since lipgloss
			// styles have no real alpha compositing.
			return lipgloss.NoColor{}, nil
		}
		if strings.HasPrefix(t, "#") {
			return lipgloss.Color(t), nil
		}
		for _, seen := range chain {
			if seen == t {
				return nil, fmt.Errorf("circular color reference: %s -> %s", strings.Join(chain, " -> "), t)
			}
		}
		next, ok := lookupRef(asset, t)
		if !ok {
			return nil, fmt.Errorf("color reference %q not found in defs or theme", t)
		}
		return resolveColorValue(next, mode, asset, append(chain, t))
	case map[string]any:
		val, ok := t[mode]
		if !ok {
			return nil, fmt.Errorf("variant has no %q entry", mode)
		}
		return resolveColorValue(val, mode, asset, chain)
	default:
		return nil, fmt.Errorf("unsupported color value %#v", v)
	}
}

// lookupRef is theme/index.ts's `defs[c] ?? theme.theme[c]`.
func lookupRef(asset themeAsset, name string) (any, bool) {
	if v, ok := asset.Defs[name]; ok {
		return v, true
	}
	if v, ok := asset.Theme[name]; ok {
		return v, true
	}
	return nil, false
}

// Names returns every selectable theme name, sorted for a stable dialog
// listing: the two hand-ported defaults first (see Dark/Light), then every
// catalog entry alphabetically.
func Names() []string {
	names := make([]string, 0, len(catalog)+2)
	names = append(names, "gocode-dark", "gocode-light")
	for name := range catalog {
		names = append(names, name)
	}
	sort.Slice(names[2:], func(i, j int) bool { return names[i+2] < names[j+2] })
	return names
}
