// Command plugin-echo is a worked example of a gocode process plugin.
//
// A process plugin is any executable that speaks newline-delimited JSON-RPC
// 2.0 on stdin/stdout. It is the Go port's replacement for the dynamic
// `import()` a TypeScript opencode plugin is loaded with: the host cannot pull
// unknown code into a linked binary, so it runs it beside itself instead.
// Nothing here is Go-specific — the same 120 lines in Python or Node work
// identically.
//
// Install it by pointing at the directory from gocode.json:
//
//	{ "plugin": [["./examples/plugin-echo", { "banner": "hello" }]] }
//
// The directory's gocode-plugin.json says how to run it.
//
// Three rules, and they are the whole protocol:
//
//  1. Answer `initialize` with a manifest naming the hooks and tools you
//     implement. The host only calls what you declare, so a plugin that
//     declares nothing costs one process and no round trips.
//  2. Answer `hook` by returning the output you were handed, mutated. You get
//     the current output, not a blank one, because earlier plugins may have
//     already changed it — mutate, do not replace.
//  3. Exit on `shutdown` or on EOF.
//
// Never write anything but protocol messages to stdout. Diagnostics go to
// stderr, which the host captures into its log.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// request is one incoming JSON-RPC message. A message with no id is a
// notification and must not be answered.
type request struct {
	ID     *int64          `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

var (
	out = json.NewEncoder(os.Stdout)
	// banner is set from the options bag at handshake time.
	banner = "echo"
)

func main() {
	in := json.NewDecoder(os.Stdin)
	for {
		var message request
		if err := in.Decode(&message); err != nil {
			if !errors.Is(err, io.EOF) {
				fmt.Fprintln(os.Stderr, "decode:", err)
			}
			return
		}
		if message.Method == "shutdown" {
			return
		}
		if err := dispatch(message); err != nil {
			fmt.Fprintln(os.Stderr, message.Method+":", err)
		}
	}
}

func dispatch(message request) error {
	switch message.Method {
	case "initialize":
		return initialize(message)
	case "hook":
		return hook(message)
	case "tool":
		return callTool(message)
	default:
		return reply(message.ID, nil, fmt.Errorf("unknown method %q", message.Method))
	}
}

// initialize declares what this plugin implements. The host's Input carries
// the session directory and the URL of its own HTTP API, which is how a plugin
// reads or changes runtime state — there is no callback channel.
func initialize(message request) error {
	var params struct {
		Protocol int `json:"protocol"`
		Input    struct {
			Directory string `json:"directory"`
			ServerURL string `json:"serverURL"`
		} `json:"input"`
		Options map[string]any `json:"options"`
	}
	if err := json.Unmarshal(message.Params, &params); err != nil {
		return err
	}
	if text, ok := params.Options["banner"].(string); ok && text != "" {
		banner = text
	}
	fmt.Fprintf(os.Stderr, "starting in %s\n", params.Input.Directory)

	return reply(message.ID, map[string]any{
		"id":    "plugin-echo",
		"hooks": []string{"tool.execute.after", "experimental.chat.system.transform"},
		"tools": []map[string]any{{
			"name":        "echo",
			"description": "Echoes the text it is given. " + banner,
			"parameters": map[string]any{
				"type":       "object",
				"properties": map[string]any{"text": map[string]any{"type": "string"}},
				"required":   []string{"text"},
			},
		}},
	}, nil)
}

// hook answers a trigger. The output arrives carrying whatever the host and
// earlier plugins put there; return it changed, or return it as-is to decline.
func hook(message request) error {
	var params struct {
		Name   string          `json:"name"`
		Input  json.RawMessage `json:"input"`
		Output json.RawMessage `json:"output"`
	}
	if err := json.Unmarshal(message.Params, &params); err != nil {
		return err
	}

	switch params.Name {
	case "experimental.chat.system.transform":
		var output struct {
			System []string `json:"system"`
		}
		if err := json.Unmarshal(params.Output, &output); err != nil {
			return err
		}
		output.System = append(output.System, "Prefer short answers.")
		return reply(message.ID, map[string]any{"output": output}, nil)

	case "tool.execute.after":
		var input struct {
			Tool string `json:"tool"`
		}
		if err := json.Unmarshal(params.Input, &input); err != nil {
			return err
		}
		var output struct {
			Title    string         `json:"title,omitempty"`
			Output   string         `json:"output"`
			Metadata map[string]any `json:"metadata,omitempty"`
		}
		if err := json.Unmarshal(params.Output, &output); err != nil {
			return err
		}
		// Trim trailing blank lines from every tool's result. A pointless
		// edit, but it shows the shape: read the output, change it, send it
		// back.
		output.Output = strings.TrimRight(output.Output, "\n")
		return reply(message.ID, map[string]any{"output": output}, nil)
	}

	// A hook that was declared but is not handled must still answer, or the
	// host waits out its timeout. Omitting "output" means "unchanged".
	return reply(message.ID, map[string]any{}, nil)
}

// callTool runs one of the tools declared in the manifest.
func callTool(message request) error {
	var params struct {
		Name    string         `json:"name"`
		Args    map[string]any `json:"args"`
		Context struct {
			SessionID string `json:"sessionID"`
			Directory string `json:"directory"`
		} `json:"context"`
	}
	if err := json.Unmarshal(message.Params, &params); err != nil {
		return err
	}
	if params.Name != "echo" {
		return reply(message.ID, nil, fmt.Errorf("unknown tool %q", params.Name))
	}
	text, _ := params.Args["text"].(string)
	return reply(message.ID, map[string]any{
		"title":    "echo",
		"output":   banner + ": " + text,
		"metadata": map[string]any{"session": params.Context.SessionID},
	}, nil)
}

// reply writes a response. A request always gets exactly one.
func reply(id *int64, result any, failure error) error {
	if id == nil {
		return nil
	}
	message := map[string]any{"jsonrpc": "2.0", "id": id}
	if failure != nil {
		message["error"] = map[string]any{"code": -32603, "message": failure.Error()}
	} else {
		message["result"] = result
	}
	return out.Encode(message)
}
