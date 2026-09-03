package main

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"

	"github.com/langazov/gocode-go/internal/config"
)

// debugConfigCmd prints every config source consulted at startup, in merge
// order, plus the merged result with secrets redacted.
func debugConfigCmd(args []string) error {
	cfg, sources, err := config.LoadTraced()
	if err != nil {
		fmt.Fprintf(os.Stderr, "config load failed: %v\n", err)
	}
	fmt.Println("config sources (merge order, last wins):")
	for i, source := range sources {
		status := "missing"
		if source.Error != "" {
			status = "ERROR: " + source.Error
		} else if source.Found {
			status = "loaded"
		}
		fmt.Printf("  %d. [%s] %s — %s\n", i+1, source.Kind, source.Path, status)
	}
	if cfg == nil {
		return nil
	}

	encoded, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	redacted := redactSecrets(encoded)
	fmt.Println("\nmerged config (secrets redacted):")
	fmt.Println(string(redacted))
	return nil
}

var secretKeyRe = regexp.MustCompile(`(?i)("?(api_?key|key|token|secret|password)"?\s*:\s*")([^"]{6})[^"]*(")`)

func redactSecrets(data []byte) []byte {
	return secretKeyRe.ReplaceAll(data, []byte(`$1$3…$4`))
}
