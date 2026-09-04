package plugin

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"testing"

	"github.com/langazov/gocode-go/internal/tool"
)

// helperEnv marks the re-executed test binary as the plugin under test. It is
// the standard Go trick for exercising a subprocess protocol without shipping
// a second binary or depending on an interpreter being installed.
const helperEnv = "GOCODE_PLUGIN_HELPER"

// TestHelperPlugin is not a test: when the marker is set it is the plugin
// process, speaking the protocol on stdin/stdout until the host closes it.
func TestHelperPlugin(t *testing.T) {
	if os.Getenv(helperEnv) != "1" {
		t.Skip("helper process; runs only when re-executed by a test")
	}
	// Exit before the testing framework can write PASS to stdout, which would
	// arrive on the host's decoder as garbage after the last response.
	defer os.Exit(0)
	serveHelperPlugin()
}

func serveHelperPlugin() {
	decoder := json.NewDecoder(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	for {
		var request struct {
			ID     *int64          `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := decoder.Decode(&request); err != nil {
			if err == io.EOF {
				return
			}
			return
		}
		switch request.Method {
		case "shutdown":
			return
		case "initialize":
			var params struct {
				Protocol int     `json:"protocol"`
				Input    Input   `json:"input"`
				Options  Options `json:"options"`
			}
			_ = json.Unmarshal(request.Params, &params)
			// Echo the configured greeting back through the tool, proving the
			// options bag survived the handshake.
			greeting, _ := params.Options["greeting"].(string)
			_ = encoder.Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      request.ID,
				"result": manifest{
					ID:    "helper",
					Hooks: []string{ChatParams.Name(), "chat.nonsense"},
					Tools: []manifestTool{{
						Name:        "helper_echo",
						Description: greeting,
						Parameters:  map[string]any{"type": "object"},
					}},
				},
			})
		case "hook":
			var params struct {
				Name   string          `json:"name"`
				Input  ChatInput       `json:"input"`
				Output json.RawMessage `json:"output"`
			}
			_ = json.Unmarshal(request.Params, &params)
			var out ChatParamsOutput
			_ = json.Unmarshal(params.Output, &out)
			// Prove the hook saw both the input and the host's current
			// output: it only acts when the agent matches, and it keeps the
			// max token value the host already set.
			if params.Input.Agent == "build" {
				out.Temperature = Float(0.25)
			}
			_ = encoder.Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      request.ID,
				"result":  map[string]any{"output": out},
			})
		case "tool":
			var params struct {
				Name    string         `json:"name"`
				Args    map[string]any `json:"args"`
				Context ToolContext    `json:"context"`
			}
			_ = json.Unmarshal(request.Params, &params)
			text, _ := params.Args["text"].(string)
			_ = encoder.Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      request.ID,
				"result": ToolResult{
					Output: params.Context.SessionID + ":" + text,
				},
			})
		default:
			_ = encoder.Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      request.ID,
				"error":   map[string]any{"code": -32601, "message": "unknown method " + request.Method},
			})
		}
	}
}

// spawnHelper starts the test binary as a plugin process.
func spawnHelper(t *testing.T, opts Options) *Instance {
	t.Helper()
	instance, err := Spawn(context.Background(), "helper", SpawnConfig{
		Command: []string{os.Args[0], "-test.run=TestHelperPlugin"},
		Dir:     t.TempDir(),
		Env:     []string{helperEnv + "=1"},
		Stderr:  io.Discard,
	}, Input{Directory: t.TempDir()}, opts, func(string) {})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	t.Cleanup(func() { _ = instance.closer(context.Background()) })
	return instance
}

// A process plugin's hooks reach the host through the same Trigger a native
// plugin's do, and the mutate-the-output contract survives the JSON round
// trip in both directions.
func TestProcessPluginHookRoundTrip(t *testing.T) {
	instance := spawnHelper(t, nil)
	if instance.ID != "helper" {
		t.Errorf("ID = %q, want the id from the handshake manifest", instance.ID)
	}
	if instance.Source != SourceProcess {
		t.Errorf("Source = %q, want %q", instance.Source, SourceProcess)
	}

	host, reports := testHost(t)
	host.Add(instance)

	// The manifest also declared a hook the host does not know; it must be
	// dropped rather than installed.
	if host.Registered("chat.nonsense") {
		t.Error("an unknown hook from the manifest was installed")
	}
	if len(*reports) != 1 {
		t.Errorf("reports = %v, want the unknown hook reported once", *reports)
	}

	out := ChatParamsOutput{MaxOutputTokens: Int(4096)}
	if err := Trigger(context.Background(), host, ChatParams, ChatInput{Agent: "build"}, &out); err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	if out.Temperature == nil || *out.Temperature != 0.25 {
		t.Errorf("Temperature = %v, want 0.25 from the process plugin", out.Temperature)
	}
	if out.MaxOutputTokens == nil || *out.MaxOutputTokens != 4096 {
		t.Errorf("MaxOutputTokens = %v, want the host's value preserved across the round trip", out.MaxOutputTokens)
	}

	// A hook whose input does not match leaves the output alone.
	untouched := ChatParamsOutput{}
	if err := Trigger(context.Background(), host, ChatParams, ChatInput{Agent: "plan"}, &untouched); err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	if untouched.Temperature != nil {
		t.Errorf("Temperature = %v, want nil when the plugin declined", untouched.Temperature)
	}
}

// A tool declared in the handshake manifest is executable through the
// runtime's registry, and the options bag reached the plugin.
func TestProcessPluginTool(t *testing.T) {
	instance := spawnHelper(t, Options{"greeting": "configured description"})
	host, _ := testHost(t)
	host.Add(instance)

	tools := host.Tools()
	if len(tools) != 1 || tools[0].Name != "helper_echo" {
		t.Fatalf("Tools() = %+v, want the manifest's helper_echo", tools)
	}
	if tools[0].Description != "configured description" {
		t.Errorf("Description = %q, want the value from the options bag", tools[0].Description)
	}

	registry := tool.NewRegistry()
	RegisterTools(registry, host, t.TempDir(), t.TempDir())
	output, err := registry.Execute(context.Background(), "helper_echo",
		map[string]any{"text": "hello"}, tool.ExecContext{SessionID: "ses_9"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if output != "ses_9:hello" {
		t.Errorf("output = %q, want the session context and args to have crossed the boundary", output)
	}
}

// A call made after the plugin dies fails instead of blocking forever on a
// response that cannot arrive.
func TestProcessCallAfterExit(t *testing.T) {
	instance := spawnHelper(t, nil)
	if err := instance.closer(context.Background()); err != nil {
		t.Fatalf("close: %v", err)
	}

	host, _ := testHost(t)
	host.Add(instance)
	out := ChatParamsOutput{}
	err := Trigger(context.Background(), host, ChatParams, ChatInput{Agent: "build"}, &out)
	if err == nil {
		t.Fatal("Trigger returned nil after the plugin exited, want a failure")
	}
}

// A plugin that cannot be started fails the load rather than the process.
func TestSpawnMissingCommand(t *testing.T) {
	_, err := Spawn(context.Background(), "missing", SpawnConfig{
		Command: []string{filepathJoinNonexistent(t)},
	}, Input{Directory: t.TempDir()}, nil, func(string) {})
	if err == nil {
		t.Fatal("Spawn returned nil error for a command that does not exist")
	}
}

func filepathJoinNonexistent(t *testing.T) string {
	t.Helper()
	return t.TempDir() + string(os.PathSeparator) + "definitely-not-here"
}

// exec.Command is referenced by Spawn; keep the import honest for readers
// grepping this file for how the subprocess is started.
var _ = exec.Command
