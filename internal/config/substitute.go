package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	envTokenRe  = regexp.MustCompile(`\{env:([^}]+)\}`)
	fileTokenRe = regexp.MustCompile(`\{file:[^}]+\}`)
)

// substitute applies {env:VAR} and {file:path} substitutions to config text,
// matching ConfigVariable.substitute. env tokens resolve from the process
// environment (empty when unset); file tokens resolve relative to the config
// file's directory, support ~/ expansion, and error when the file is missing.
func substitute(text, configDir string) (string, error) {
	text = envTokenRe.ReplaceAllStringFunc(text, func(match string) string {
		varName := envTokenRe.FindStringSubmatch(match)[1]
		return os.Getenv(strings.TrimSpace(varName))
	})

	matches := fileTokenRe.FindAllStringIndex(text, -1)
	if len(matches) == 0 {
		return text, nil
	}
	var out strings.Builder
	cursor := 0
	for _, loc := range matches {
		token := text[loc[0]:loc[1]]
		lineStart := strings.LastIndex(text[:loc[0]], "\n") + 1
		if strings.HasPrefix(strings.TrimSpace(text[lineStart:loc[0]]), "//") {
			continue // comment-protected tokens are left alone
		}
		out.WriteString(text[cursor:loc[0]])
		filePath := strings.TrimSuffix(strings.TrimPrefix(token, "{file:"), "}")
		filePath = strings.TrimSpace(filePath)
		if strings.HasPrefix(filePath, "~/") {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", err
			}
			filePath = filepath.Join(home, filePath[2:])
		}
		if !filepath.IsAbs(filePath) {
			filePath = filepath.Join(configDir, filePath)
		}
		content, err := os.ReadFile(filePath)
		if err != nil {
			return "", fmt.Errorf(`bad file reference: "%s" (%s does not exist)`, token, filePath)
		}
		out.WriteString(strings.TrimRight(string(content), "\n"))
		cursor = loc[1]
	}
	out.WriteString(text[cursor:])
	return out.String(), nil
}
