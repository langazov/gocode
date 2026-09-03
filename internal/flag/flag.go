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
		return Truthy("GOCODE_EXPERIMENTAL")
	}
	return Truthy(key)
}

func OtelExporterOtlpEndpoint() string { return String("OTEL_EXPORTER_OTLP_ENDPOINT") }
func OtelExporterOtlpHeaders() string  { return String("OTEL_EXPORTER_OTLP_HEADERS") }

func AutoHeapSnapshot() bool        { return Truthy("GOCODE_AUTO_HEAP_SNAPSHOT") }
func GitBashPath() string           { return String("GOCODE_GIT_BASH_PATH") }
func Config() string                { return String("GOCODE_CONFIG") }
func ConfigContent() string         { return String("GOCODE_CONFIG_CONTENT") }
func DisableAutoupdate() bool       { return Truthy("GOCODE_DISABLE_AUTOUPDATE") }
func AlwaysNotifyUpdate() bool      { return Truthy("GOCODE_ALWAYS_NOTIFY_UPDATE") }
func DisablePrune() bool            { return Truthy("GOCODE_DISABLE_PRUNE") }
func DisableTerminalTitle() bool    { return Truthy("GOCODE_DISABLE_TERMINAL_TITLE") }
func ShowTtfd() bool                { return Truthy("GOCODE_SHOW_TTFD") }
func DisableAutocompact() bool      { return Truthy("GOCODE_DISABLE_AUTOCOMPACT") }
func DisableModelsFetch() bool      { return Truthy("GOCODE_DISABLE_MODELS_FETCH") }
func DisableMouse() bool            { return Truthy("GOCODE_DISABLE_MOUSE") }
func FakeVcs() string               { return String("GOCODE_FAKE_VCS") }
func ServerPassword() string        { return String("GOCODE_SERVER_PASSWORD") }
func ServerUsername() string        { return String("GOCODE_SERVER_USERNAME") }
func ExperimentalFilewatcher() bool { return Truthy("GOCODE_EXPERIMENTAL_FILEWATCHER") }
func ExperimentalDisableFilewatcher() bool {
	return Truthy("GOCODE_EXPERIMENTAL_DISABLE_FILEWATCHER")
}
func ModelsUrl() string  { return String("GOCODE_MODELS_URL") }
func ModelsPath() string { return String("GOCODE_MODELS_PATH") }
func Db() string         { return String("GOCODE_DB") }

func WorkspaceId() string          { return String("GOCODE_WORKSPACE_ID") }
func ExperimentalWorkspaces() bool { return enabledByExperimental("GOCODE_EXPERIMENTAL_WORKSPACES") }
func DisableProjectConfig() bool   { return Truthy("GOCODE_DISABLE_PROJECT_CONFIG") }
func ExperimentalReferences() bool { return enabledByExperimental("GOCODE_EXPERIMENTAL_REFERENCES") }
func TuiConfig() string            { return String("GOCODE_TUI_CONFIG") }
func ConfigDir() string            { return String("GOCODE_CONFIG_DIR") }
func Pure() bool                   { return Truthy("GOCODE_PURE") }
func Permission() string           { return String("GOCODE_PERMISSION") }
func PluginMetaFile() string       { return String("GOCODE_PLUGIN_META_FILE") }
func ExperimentalDisableCopyOnSelect() bool {
	if _, ok := os.LookupEnv("GOCODE_EXPERIMENTAL_DISABLE_COPY_ON_SELECT"); !ok {
		return runtime.GOOS == "windows"
	}
	return Truthy("GOCODE_EXPERIMENTAL_DISABLE_COPY_ON_SELECT")
}

func DisableFff() bool {
	if _, ok := os.LookupEnv("GOCODE_DISABLE_FFF"); !ok {
		return runtime.GOOS == "windows"
	}
	return Truthy("GOCODE_DISABLE_FFF")
}

func Client() string {
	if value := String("GOCODE_CLIENT"); value != "" {
		return value
	}
	return "cli"
}
