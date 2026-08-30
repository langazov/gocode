package flag

import (
	"os"
	"runtime"
	"strings"
)

func Truthy(key string) bool {
	value := strings.ToLower(os.Getenv(key))
	return value == "true" || value == "1"
}

func String(key string) string {
	return os.Getenv(key)
}

func enabledByExperimental(key string) bool {
	if _, ok := os.LookupEnv(key); !ok {
		return Truthy("OPENCODE_EXPERIMENTAL")
	}
	return Truthy(key)
}

func OtelExporterOtlpEndpoint() string { return String("OTEL_EXPORTER_OTLP_ENDPOINT") }
func OtelExporterOtlpHeaders() string  { return String("OTEL_EXPORTER_OTLP_HEADERS") }

func AutoHeapSnapshot() bool        { return Truthy("OPENCODE_AUTO_HEAP_SNAPSHOT") }
func GitBashPath() string           { return String("OPENCODE_GIT_BASH_PATH") }
func Config() string                { return String("OPENCODE_CONFIG") }
func ConfigContent() string         { return String("OPENCODE_CONFIG_CONTENT") }
func DisableAutoupdate() bool       { return Truthy("OPENCODE_DISABLE_AUTOUPDATE") }
func AlwaysNotifyUpdate() bool      { return Truthy("OPENCODE_ALWAYS_NOTIFY_UPDATE") }
func DisablePrune() bool            { return Truthy("OPENCODE_DISABLE_PRUNE") }
func DisableTerminalTitle() bool    { return Truthy("OPENCODE_DISABLE_TERMINAL_TITLE") }
func ShowTtfd() bool                { return Truthy("OPENCODE_SHOW_TTFD") }
func DisableAutocompact() bool      { return Truthy("OPENCODE_DISABLE_AUTOCOMPACT") }
func DisableModelsFetch() bool      { return Truthy("OPENCODE_DISABLE_MODELS_FETCH") }
func DisableMouse() bool            { return Truthy("OPENCODE_DISABLE_MOUSE") }
func FakeVcs() string               { return String("OPENCODE_FAKE_VCS") }
func ServerPassword() string        { return String("OPENCODE_SERVER_PASSWORD") }
func ServerUsername() string        { return String("OPENCODE_SERVER_USERNAME") }
func ExperimentalFilewatcher() bool { return Truthy("OPENCODE_EXPERIMENTAL_FILEWATCHER") }
func ExperimentalDisableFilewatcher() bool {
	return Truthy("OPENCODE_EXPERIMENTAL_DISABLE_FILEWATCHER")
}
func ModelsUrl() string  { return String("OPENCODE_MODELS_URL") }
func ModelsPath() string { return String("OPENCODE_MODELS_PATH") }
func Db() string         { return String("OPENCODE_DB") }

func WorkspaceId() string          { return String("OPENCODE_WORKSPACE_ID") }
func ExperimentalWorkspaces() bool { return enabledByExperimental("OPENCODE_EXPERIMENTAL_WORKSPACES") }
func DisableProjectConfig() bool   { return Truthy("OPENCODE_DISABLE_PROJECT_CONFIG") }
func ExperimentalReferences() bool { return enabledByExperimental("OPENCODE_EXPERIMENTAL_REFERENCES") }
func TuiConfig() string            { return String("OPENCODE_TUI_CONFIG") }
func ConfigDir() string            { return String("OPENCODE_CONFIG_DIR") }
func Pure() bool                   { return Truthy("OPENCODE_PURE") }
func Permission() string           { return String("OPENCODE_PERMISSION") }
func PluginMetaFile() string       { return String("OPENCODE_PLUGIN_META_FILE") }
func ExperimentalDisableCopyOnSelect() bool {
	if _, ok := os.LookupEnv("OPENCODE_EXPERIMENTAL_DISABLE_COPY_ON_SELECT"); !ok {
		return runtime.GOOS == "windows"
	}
	return Truthy("OPENCODE_EXPERIMENTAL_DISABLE_COPY_ON_SELECT")
}

func DisableFff() bool {
	if _, ok := os.LookupEnv("OPENCODE_DISABLE_FFF"); !ok {
		return runtime.GOOS == "windows"
	}
	return Truthy("OPENCODE_DISABLE_FFF")
}

func Client() string {
	if value := String("OPENCODE_CLIENT"); value != "" {
		return value
	}
	return "cli"
}
