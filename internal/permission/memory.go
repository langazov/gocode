package permission

import "sync"

// MemorySaved is an in-memory SavedStore for tests and single-process use.
type MemorySaved struct {
	mu    sync.Mutex
	rules Ruleset
}

func (s *MemorySaved) Add(action string, resources []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, resource := range resources {
		s.rules = append(s.rules, Rule{Action: action, Resource: resource, Effect: Allow})
	}
	return nil
}

func (s *MemorySaved) List() (Ruleset, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(Ruleset, len(s.rules))
	copy(out, s.rules)
	return out, nil
}

// StaticRules is a RulesProvider returning a fixed ruleset.
type StaticRules struct {
	Rules Ruleset
}

func (s StaticRules) Configured(sessionID, agent string) (Ruleset, error) {
	return s.Rules, nil
}
