package plugin

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"
)

// The process tier: plugins that are separate executables.
//
// This replaces the dynamic `import()` in packages/opencode/src/plugin/index.ts.
// TypeScript can pull an arbitrary module into its own process; a linked Go
// binary cannot, so an external plugin runs beside it and the two speak
// newline-delimited JSON-RPC 2.0 over the plugin's stdin/stdout.
//
// The protocol is deliberately small, because everything it carries has to
// survive being JSON:
//
//	host -> plugin  {"jsonrpc":"2.0","id":1,"method":"initialize",
//	                 "params":{"protocol":1,"input":{...},"options":{...}}}
//	plugin -> host  {"jsonrpc":"2.0","id":1,"result":{"id":"my-plugin",
//	                 "hooks":["chat.params"],"tools":[{...}]}}
//
//	host -> plugin  {"jsonrpc":"2.0","id":2,"method":"hook",
//	                 "params":{"name":"chat.params","input":{...},"output":{...}}}
//	plugin -> host  {"jsonrpc":"2.0","id":2,"result":{"output":{...}}}
//
//	host -> plugin  {"jsonrpc":"2.0","method":"event","params":{"event":{...}}}
//	plugin -> host  {"jsonrpc":"2.0","method":"log","params":{"message":"..."}}
//
// The handshake is what makes this efficient: the plugin declares up front
// which hooks it implements, so the host only pays a round trip for hooks that
// exist. A plugin that registers nothing costs one spawn and nothing else.
//
// The mutate-the-output contract survives the boundary intact. The host sends
// the current output alongside the input and installs whatever comes back, so
// a chain of hooks spanning both tiers threads the same value in load order.

// ProtocolVersion is the wire version the host speaks. A plugin that cannot
// speak it should fail the handshake rather than guess.
const ProtocolVersion = 1

// DefaultHandshakeTimeout bounds `initialize`. A plugin that cannot introduce
// itself promptly must not hold up boot.
const DefaultHandshakeTimeout = 10 * time.Second

// DefaultCallTimeout bounds a hook or tool call made with a context that has
// no deadline of its own. Without it a wedged plugin wedges the turn.
const DefaultCallTimeout = 30 * time.Second

// Process is a running plugin executable and the client half of the protocol.
type Process struct {
	spec string
	cmd  *exec.Cmd

	writeMu sync.Mutex
	stdin   io.WriteCloser
	encoder *json.Encoder

	nextID atomic.Int64

	mu      sync.Mutex
	pending map[int64]chan rpcResponse
	// closed is set once the reader has stopped, so a call made afterwards
	// fails immediately instead of blocking on a response that cannot arrive.
	closed bool
	// exitErr records why the reader stopped, to attribute a failed call.
	exitErr error

	done chan struct{}

	// CallTimeout overrides [DefaultCallTimeout].
	CallTimeout time.Duration

	// onLog receives the plugin's log notifications and its stderr.
	onLog func(message string)
}

// SpawnConfig describes an executable to launch as a plugin.
type SpawnConfig struct {
	// Command is the executable and its arguments.
	Command []string
	// Dir is the working directory; defaults to the session directory.
	Dir string
	// Env are extra environment variables, as KEY=VALUE.
	Env []string
	// Stderr receives the plugin's diagnostic output. When nil it is
	// discarded — never the process's own stderr, which would land on top of
	// the TUI's alternate screen and corrupt the frame.
	Stderr io.Writer
}

// manifest is what a plugin returns from `initialize`.
type manifest struct {
	// ID is the plugin's own identifier, porting `PluginModule.id`.
	ID string `json:"id"`
	// Hooks names the trigger hooks the plugin implements.
	Hooks []string `json:"hooks"`
	// Tools are the tools it contributes.
	Tools []manifestTool `json:"tools"`
}

type manifestTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

type rpcRequest struct {
	Version string `json:"jsonrpc"`
	ID      *int64 `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rpcResponse struct {
	Version string          `json:"jsonrpc"`
	ID      *int64          `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return e.Message }

// Spawn launches a plugin executable, completes the handshake, and returns the
// hooks its manifest declares. The returned [Instance] owns the process: its
// closer shuts it down.
func Spawn(ctx context.Context, spec string, cfg SpawnConfig, in Input, opts Options, onLog func(string)) (*Instance, error) {
	if len(cfg.Command) == 0 {
		return nil, fmt.Errorf("plugin: %s has no command", spec)
	}
	if onLog == nil {
		onLog = func(string) {}
	}

	// Deliberately not exec.CommandContext: ctx here bounds loading, and it is
	// canceled as soon as loading returns. The process must outlive that — its
	// lifetime is the host's, ended by Close.
	cmd := exec.Command(cfg.Command[0], cfg.Command[1:]...)
	cmd.Dir = cfg.Dir
	if cmd.Dir == "" {
		cmd.Dir = in.Directory
	}
	cmd.Env = append(os.Environ(), cfg.Env...)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("plugin: %s stdin: %w", spec, err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("plugin: %s stdout: %w", spec, err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("plugin: %s stderr: %w", spec, err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("plugin: %s start: %w", spec, err)
	}

	process := &Process{
		spec:    spec,
		cmd:     cmd,
		stdin:   stdin,
		encoder: json.NewEncoder(stdin),
		pending: map[int64]chan rpcResponse{},
		done:    make(chan struct{}),
		onLog:   onLog,
	}
	go process.drainStderr(stderr, cfg.Stderr)
	go process.read(stdout)

	handshake, cancel := context.WithTimeout(ctx, DefaultHandshakeTimeout)
	defer cancel()
	var declared manifest
	err = process.call(handshake, "initialize", map[string]any{
		"protocol": ProtocolVersion,
		"input":    in,
		"options":  opts,
	}, &declared)
	if err != nil {
		_ = process.Close(context.Background())
		return nil, fmt.Errorf("plugin: %s handshake: %w", spec, err)
	}

	instance := &Instance{
		ID:     declared.ID,
		Spec:   spec,
		Source: SourceProcess,
		Hooks:  process.hooks(declared),
		closer: process.Close,
		state:  process.State,
	}
	if instance.ID == "" {
		instance.ID = spec
	}
	return instance, nil
}

// hooks turns a manifest into the same [Hooks] value a native factory returns.
//
// Hook names are not filtered here even though the manifest is untrusted:
// [Host.Add] already drops and reports names outside the catalog, and letting
// one place decide means a process plugin's typo is reported exactly the way a
// native plugin's is.
func (p *Process) hooks(declared manifest) *Hooks {
	hooks := &Hooks{}
	for _, name := range declared.Hooks {
		hooks.entries = append(hooks.entries, entry{name: name, remote: p})
	}
	for _, declaredTool := range declared.Tools {
		if declaredTool.Name == "" {
			continue
		}
		hooks.Tools = append(hooks.Tools, Tool{
			Name:        declaredTool.Name,
			Description: declaredTool.Description,
			Parameters:  declaredTool.Parameters,
			Execute:     p.toolExecutor(declaredTool.Name),
		})
	}
	return hooks
}

// Invoke implements [invoker]: it ships the input and the current output to
// the plugin and installs the mutated output it returns.
func (p *Process) Invoke(ctx context.Context, hook string, in, out any) error {
	var result struct {
		Output json.RawMessage `json:"output"`
	}
	err := p.call(ctx, "hook", map[string]any{
		"name":   hook,
		"input":  in,
		"output": out,
	}, &result)
	if err != nil {
		return err
	}
	// A plugin that read the hook but changed nothing may omit the output.
	// Leaving it alone is the same outcome as echoing it back unchanged, and
	// cheaper.
	if len(result.Output) == 0 || string(result.Output) == "null" {
		return nil
	}
	if err := json.Unmarshal(result.Output, out); err != nil {
		return fmt.Errorf("decode output: %w", err)
	}
	return nil
}

// toolExecutor binds one manifest tool to a `tool` call.
func (p *Process) toolExecutor(name string) func(context.Context, map[string]any, ToolContext) (ToolResult, error) {
	return func(ctx context.Context, args map[string]any, tc ToolContext) (ToolResult, error) {
		var result ToolResult
		err := p.call(ctx, "tool", map[string]any{
			"name":    name,
			"args":    args,
			"context": tc,
		}, &result)
		if err != nil {
			return ToolResult{}, err
		}
		return result, nil
	}
}

// Notify sends a notification, which expects no reply. Event delivery uses it:
// the host must not block a commit on a plugin's acknowledgement.
func (p *Process) Notify(method string, params any) error {
	return p.write(rpcRequest{Version: "2.0", Method: method, Params: params})
}

// call sends a request and waits for its response.
func (p *Process) call(ctx context.Context, method string, params any, result any) error {
	if _, ok := ctx.Deadline(); !ok {
		timeout := p.CallTimeout
		if timeout <= 0 {
			timeout = DefaultCallTimeout
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	id := p.nextID.Add(1)
	reply := make(chan rpcResponse, 1)

	p.mu.Lock()
	if p.closed {
		err := p.exitErr
		p.mu.Unlock()
		if err == nil {
			err = errors.New("plugin process is not running")
		}
		return err
	}
	p.pending[id] = reply
	p.mu.Unlock()

	defer func() {
		p.mu.Lock()
		delete(p.pending, id)
		p.mu.Unlock()
	}()

	if err := p.write(rpcRequest{Version: "2.0", ID: &id, Method: method, Params: params}); err != nil {
		return err
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-p.done:
		p.mu.Lock()
		err := p.exitErr
		p.mu.Unlock()
		if err == nil {
			err = errors.New("plugin process exited")
		}
		return err
	case response := <-reply:
		if response.Error != nil {
			return response.Error
		}
		if result == nil || len(response.Result) == 0 {
			return nil
		}
		return json.Unmarshal(response.Result, result)
	}
}

func (p *Process) write(message rpcRequest) error {
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	if p.stdin == nil {
		return errors.New("plugin process stdin is closed")
	}
	return p.encoder.Encode(message)
}

// read consumes the plugin's stdout, routing responses to their waiting call
// and notifications to the log.
func (p *Process) read(stdout io.Reader) {
	decoder := json.NewDecoder(bufio.NewReader(stdout))
	var readErr error
	for {
		var message rpcResponse
		if err := decoder.Decode(&message); err != nil {
			if !errors.Is(err, io.EOF) {
				readErr = fmt.Errorf("plugin %s: %w", p.spec, err)
			}
			break
		}
		p.handle(message)
	}
	p.shutdownPending(readErr)
}

func (p *Process) handle(message rpcResponse) {
	// A message with a method and no id is a notification from the plugin.
	if message.ID == nil {
		if message.Method == "log" {
			var params struct {
				Message string `json:"message"`
			}
			if err := json.Unmarshal(message.Params, &params); err == nil && params.Message != "" {
				p.onLog(fmt.Sprintf("%s: %s", p.spec, params.Message))
			}
		}
		return
	}
	// A message with both an id and a method is a request from the plugin.
	// The host serves none yet — a plugin reaches the runtime over the HTTP
	// API it was handed at handshake — so answer rather than leave it hanging.
	if message.Method != "" {
		_ = p.write(rpcRequest{Version: "2.0", ID: message.ID, Method: "error", Params: map[string]any{
			"code":    -32601,
			"message": fmt.Sprintf("host does not serve %q", message.Method),
		}})
		return
	}
	p.mu.Lock()
	reply, ok := p.pending[*message.ID]
	p.mu.Unlock()
	if !ok {
		return
	}
	select {
	case reply <- message:
	default:
	}
}

// shutdownPending fails every in-flight call once the plugin's stdout ends.
func (p *Process) shutdownPending(err error) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	p.exitErr = err
	p.mu.Unlock()
	close(p.done)
}

func (p *Process) drainStderr(stderr io.Reader, sink io.Writer) {
	scanner := bufio.NewScanner(stderr)
	for scanner.Scan() {
		line := scanner.Text()
		if sink != nil {
			fmt.Fprintf(sink, "%s\n", line)
			continue
		}
		p.onLog(fmt.Sprintf("%s: %s", p.spec, line))
	}
}

// State reports the process's liveness for the status surfaces: "running"
// while the reader loop is alive, "exited" afterwards. A process whose
// transport died cannot answer hooks — any Trigger against it fails at
// p.call's closed check — so that distinction is what "state" means here.
func (p *Process) State() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return "exited"
	}
	return "running"
}

// Close shuts the plugin down: ask politely, close stdin so a well-behaved
// plugin sees EOF, then kill it if it has not exited.
func (p *Process) Close(ctx context.Context) error {
	_ = p.Notify("shutdown", nil)

	p.writeMu.Lock()
	if p.stdin != nil {
		_ = p.stdin.Close()
		p.stdin = nil
	}
	p.writeMu.Unlock()

	exited := make(chan error, 1)
	go func() { exited <- p.cmd.Wait() }()

	grace := time.NewTimer(2 * time.Second)
	defer grace.Stop()
	select {
	case <-exited:
	case <-ctx.Done():
		_ = p.cmd.Process.Kill()
		<-exited
	case <-grace.C:
		_ = p.cmd.Process.Kill()
		<-exited
	}
	p.shutdownPending(errors.New("plugin process closed"))
	return nil
}
