package config

import (
	"encoding/json"
	"fmt"
)

// PluginSpec is one entry of the `plugin` array.
//
// It ports the two config forms in packages/plugin/src/index.ts, where the
// array is typed `Array<string | [string, PluginOptions]>`: a bare reference,
// or a reference paired with a settings object the plugin's factory receives.
//
//	"plugin": ["./tools/lint", ["review", { "strict": true }]]
type PluginSpec struct {
	// Ref is the plugin reference: a built-in name, a path, or a name
	// installed under the plugin directory.
	Ref string
	// Options is the settings object, nil for the bare form.
	Options map[string]any
}

// String returns the reference, so a spec prints as the user wrote it.
func (p PluginSpec) String() string { return p.Ref }

// UnmarshalJSON accepts both config forms.
func (p *PluginSpec) UnmarshalJSON(data []byte) error {
	var ref string
	if err := json.Unmarshal(data, &ref); err == nil {
		*p = PluginSpec{Ref: ref}
		return nil
	}

	var tuple []json.RawMessage
	if err := json.Unmarshal(data, &tuple); err != nil {
		return fmt.Errorf("plugin: entry must be a string or [string, object]: %w", err)
	}
	if len(tuple) == 0 {
		return fmt.Errorf("plugin: entry is empty")
	}
	if err := json.Unmarshal(tuple[0], &ref); err != nil {
		return fmt.Errorf("plugin: first element must be a string: %w", err)
	}
	spec := PluginSpec{Ref: ref}
	if len(tuple) > 1 {
		if err := json.Unmarshal(tuple[1], &spec.Options); err != nil {
			return fmt.Errorf("plugin %s: options must be an object: %w", ref, err)
		}
	}
	*p = spec
	return nil
}

// MarshalJSON writes back whichever form the entry came in as, so a config
// round-trip does not rewrite the user's file.
func (p PluginSpec) MarshalJSON() ([]byte, error) {
	if len(p.Options) == 0 {
		return json.Marshal(p.Ref)
	}
	return json.Marshal([]any{p.Ref, p.Options})
}
