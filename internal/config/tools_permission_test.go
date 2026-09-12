package config

import (
	"testing"

	"github.com/langazov/gocode-go/internal/permission"
)

func TestGocodePermissionEnvInjectsRules(t *testing.T) {
	t.Setenv("GOCODE_PERMISSION", `{"edit": {"*.env": "deny"}}`)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	rules, err := cfg.Permission.Ruleset()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range rules {
		if r.Action == "edit" && r.Resource == "*.env" && r.Effect == "deny" {
			found = true
		}
	}
	if !found {
		t.Fatalf("injected rule missing: %v", rules)
	}
}

// TestToolRulesetDisablesByAction pins the "tools" map translation: a false
// entry becomes a blanket deny on the collapsed action, so disabling one of
// the three write spellings gates all of them, and the explicit permission
// block still wins where both speak.
func TestToolRulesetDisablesByAction(t *testing.T) {
	cfg := Config{Tools: map[string]bool{
		"bash": false, "write": false, "read": true,
	}}
	rules := cfg.ToolRuleset()
	var bashDeny, editDeny, readDeny int
	for _, rule := range rules {
		switch {
		case rule.Action == "bash" && rule.Effect == permission.Deny:
			bashDeny++
		case rule.Action == "edit" && rule.Effect == permission.Deny:
			editDeny++
		case rule.Action == "read":
			readDeny++
		}
	}
	if bashDeny != 1 {
		t.Fatalf("bash deny count = %d, want 1", bashDeny)
	}
	if editDeny != 1 {
		t.Fatalf("write must collapse onto the shared edit action: %d", editDeny)
	}
	if readDeny != 0 {
		t.Fatalf("enabled entries must add nothing, got %d read rules", readDeny)
	}
}

// TestToolActionCollapsesWriteAliases checks the collapse table directly.
func TestToolActionCollapsesWriteAliases(t *testing.T) {
	for _, name := range []string{"edit", "write", "apply_patch"} {
		if got := ToolAction(name); got != "edit" {
			t.Errorf("ToolAction(%q) = %q, want edit", name, got)
		}
	}
	if got := ToolAction("bash"); got != "bash" {
		t.Errorf("ToolAction(bash) = %q", got)
	}
}

// TestToolsRulesMergeUnderPermission pins the precedence: the explicit
// permission block overrides the tools map, matching the upstream merge
// order (permission wins over tools).
func TestToolsRulesMergeUnderPermission(t *testing.T) {
	cfg := Config{
		Tools: map[string]bool{"bash": false},
	}
	toolRules := cfg.ToolRuleset()
	var perm Permission
	if err := perm.UnmarshalJSON([]byte(`{"bash":"ask"}`)); err != nil {
		t.Fatal(err)
	}
	permRules, err := perm.Ruleset()
	if err != nil {
		t.Fatal(err)
	}
	merged := permission.Merge(toolRules, permRules)
	rule := permission.Evaluate("bash", "ls", merged)
	if rule.Effect != permission.Ask {
		t.Fatalf("permission block must win over tools map: %v", rule.Effect)
	}
}
