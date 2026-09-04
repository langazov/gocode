package lsp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/langazov/gocode-go/internal/config"
	"github.com/langazov/gocode-go/internal/lspprotocol"
)

// newFakeService builds a service whose only server is the fake, so the tests
// do not depend on what happens to be installed on the machine.
func newFakeService(t *testing.T, directory string, extensions []string) *Service {
	t.Helper()
	command, env, _ := fakeServerCommand()
	service := &Service{
		directory: normalizePath(directory),
		clients:   map[string]*Client{},
		broken:    map[string]bool{},
		spawning:  map[string]*sync.WaitGroup{},
		enabled:   true,
		servers: []Server{{
			ID:         "fake",
			Extensions: extensions,
			Command:    command,
			Env:        env,
		}},
	}
	t.Cleanup(service.Shutdown)
	return service
}

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestClientHandshakeAndDiagnostics is the end-to-end path: spawn a server,
// complete the handshake, open a file, and receive its diagnostics.
func TestClientHandshakeAndDiagnostics(t *testing.T) {
	dir := t.TempDir()
	file := writeFile(t, dir, "main.fake", "ok line\nthis is a BUG here\nalso WARN here\n")

	service := newFakeService(t, dir, []string{".fake"})
	service.Touch(context.Background(), file, true)

	diagnostics := service.DiagnosticsFor(file)
	if len(diagnostics) != 2 {
		t.Fatalf("got %d diagnostics, want 2: %+v", len(diagnostics), diagnostics)
	}

	var errors, warnings int
	for _, item := range diagnostics {
		switch item.Severity {
		case SeverityError:
			errors++
			if item.Range.Start.Line != 1 {
				t.Errorf("error is on line %d, want 1", item.Range.Start.Line)
			}
		case SeverityWarning:
			warnings++
		}
	}
	if errors != 1 || warnings != 1 {
		t.Errorf("got %d errors and %d warnings, want 1 each", errors, warnings)
	}
}

// TestTouchReportsUpdatedDiagnostics: the point of Touch(wait=true) is that an
// edit's consequences are visible in the same turn.
func TestTouchReportsUpdatedDiagnostics(t *testing.T) {
	dir := t.TempDir()
	file := writeFile(t, dir, "main.fake", "clean\n")

	service := newFakeService(t, dir, []string{".fake"})
	ctx := context.Background()

	service.Touch(ctx, file, true)
	if got := service.DiagnosticsFor(file); len(got) != 0 {
		t.Fatalf("a clean file reported %d diagnostics", len(got))
	}

	// Edit the file the way the edit tool would, then touch again.
	if err := os.WriteFile(file, []byte("clean\nnow a BUG appears\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	service.Touch(ctx, file, true)

	got := service.DiagnosticsFor(file)
	if len(got) != 1 || got[0].Severity != SeverityError {
		t.Fatalf("after the edit got %+v, want one error", got)
	}

	// And the reverse: fixing it must clear the report.
	if err := os.WriteFile(file, []byte("clean\nall better\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	service.Touch(ctx, file, true)
	if got := service.DiagnosticsFor(file); len(got) != 0 {
		t.Fatalf("after the fix got %+v, want none", got)
	}
}

// TestServiceIgnoresUnhandledExtensions: a server must not be started for a
// file it does not claim.
func TestServiceIgnoresUnhandledExtensions(t *testing.T) {
	dir := t.TempDir()
	file := writeFile(t, dir, "notes.txt", "this has a BUG in it\n")

	service := newFakeService(t, dir, []string{".fake"})
	service.Touch(context.Background(), file, true)

	if got := service.Status(); len(got) != 0 {
		t.Errorf("started %v for an unhandled extension", got)
	}
}

// TestServiceIgnoresFilesOutsideDirectory ports the containsPath guard: a file
// elsewhere on disk is not this instance's business.
func TestServiceIgnoresFilesOutsideDirectory(t *testing.T) {
	dir := t.TempDir()
	outside := writeFile(t, t.TempDir(), "other.fake", "a BUG lives here\n")

	service := newFakeService(t, dir, []string{".fake"})
	service.Touch(context.Background(), outside, true)

	if got := service.Status(); len(got) != 0 {
		t.Errorf("started a server for a file outside the working directory: %v", got)
	}
}

// TestServiceReusesClient: repeated touches must not spawn a server per call.
func TestServiceReusesClient(t *testing.T) {
	dir := t.TempDir()
	file := writeFile(t, dir, "main.fake", "a BUG\n")

	service := newFakeService(t, dir, []string{".fake"})
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		service.Touch(ctx, file, true)
	}
	if got := service.Status(); len(got) != 1 {
		t.Fatalf("got %d clients after 5 touches, want 1: %+v", len(got), got)
	}
}

// TestServiceConcurrentTouchesSpawnOnce covers the spawn deduplication: tools
// run in parallel, and each must not start its own copy of the server.
func TestServiceConcurrentTouchesSpawnOnce(t *testing.T) {
	dir := t.TempDir()
	service := newFakeService(t, dir, []string{".fake"})
	ctx := context.Background()

	var group sync.WaitGroup
	for i := 0; i < 8; i++ {
		file := writeFile(t, dir, "file"+itoa(i)+".fake", "a BUG\n")
		group.Add(1)
		go func() {
			defer group.Done()
			service.Touch(ctx, file, true)
		}()
	}
	group.Wait()

	if got := service.Status(); len(got) != 1 {
		t.Fatalf("got %d clients from concurrent touches, want 1: %+v", len(got), got)
	}
}

// TestBrokenServerIsNotRetried: a server that cannot start must be attempted
// once, not on every file touch.
func TestBrokenServerIsNotRetried(t *testing.T) {
	dir := t.TempDir()
	file := writeFile(t, dir, "main.fake", "text\n")

	service := &Service{
		directory: normalizePath(dir),
		clients:   map[string]*Client{},
		broken:    map[string]bool{},
		spawning:  map[string]*sync.WaitGroup{},
		enabled:   true,
		servers: []Server{{
			ID:         "missing",
			Extensions: []string{".fake"},
			Command:    []string{"gocode-no-such-language-server"},
		}},
	}
	t.Cleanup(service.Shutdown)

	ctx := context.Background()
	service.Touch(ctx, file, true)
	service.Touch(ctx, file, true)

	service.mu.Lock()
	broken := len(service.broken)
	service.mu.Unlock()
	if broken != 1 {
		t.Errorf("broken set has %d entries, want 1", broken)
	}
	if got := service.Status(); len(got) != 0 {
		t.Errorf("a server that cannot start must not appear connected: %v", got)
	}
}

func TestServiceDisabledByConfig(t *testing.T) {
	cfg := &config.Config{}
	if err := jsonUnmarshalConfig(`{"lsp": false}`, cfg); err != nil {
		t.Fatal(err)
	}
	service := New(t.TempDir(), cfg)
	if service.Enabled() {
		t.Error("`lsp: false` must disable the subsystem")
	}
	// Every method must stay safe once disabled.
	service.Touch(context.Background(), "x.go", true)
	if got := service.Status(); got != nil {
		t.Errorf("Status = %v, want nil when disabled", got)
	}
}

func TestConfigDisablesOneServer(t *testing.T) {
	cfg := &config.Config{}
	if err := jsonUnmarshalConfig(`{"lsp": {"gopls": {"disabled": true}}}`, cfg); err != nil {
		t.Fatal(err)
	}
	service := New(t.TempDir(), cfg)
	if !service.Enabled() {
		t.Fatal("disabling one server must not disable the subsystem")
	}
	for _, server := range service.servers {
		if server.ID == "gopls" {
			t.Error("gopls should have been removed")
		}
	}
	if len(service.servers) == 0 {
		t.Error("the other built-ins must remain")
	}
}

func TestConfigAddsCustomServer(t *testing.T) {
	cfg := &config.Config{}
	if err := jsonUnmarshalConfig(`{"lsp": {"mylang": {"command": ["mylang-ls", "--stdio"], "extensions": [".ml2"], "env": {"A": "B"}}}}`, cfg); err != nil {
		t.Fatal(err)
	}
	service := New(t.TempDir(), cfg)

	var found *Server
	for i, server := range service.servers {
		if server.ID == "mylang" {
			found = &service.servers[i]
		}
	}
	if found == nil {
		t.Fatal("the configured server is missing")
	}
	if len(found.Command) != 2 || found.Command[0] != "mylang-ls" {
		t.Errorf("command = %v", found.Command)
	}
	if len(found.Extensions) != 1 || found.Extensions[0] != ".ml2" {
		t.Errorf("extensions = %v", found.Extensions)
	}
	if found.Env["A"] != "B" {
		t.Errorf("env = %v", found.Env)
	}
}

// TestConfigOverridesBuiltinCommand: pointing a built-in at a different binary
// must keep its extensions and root markers.
func TestConfigOverridesBuiltinCommand(t *testing.T) {
	cfg := &config.Config{}
	if err := jsonUnmarshalConfig(`{"lsp": {"gopls": {"command": ["/custom/gopls"]}}}`, cfg); err != nil {
		t.Fatal(err)
	}
	service := New(t.TempDir(), cfg)
	for _, server := range service.servers {
		if server.ID != "gopls" {
			continue
		}
		if server.Command[0] != "/custom/gopls" {
			t.Errorf("command = %v, want the override", server.Command)
		}
		if len(server.Extensions) == 0 || len(server.RootMarkers) == 0 {
			t.Errorf("overriding the command must keep extensions and root markers: %+v", server)
		}
		return
	}
	t.Fatal("gopls is missing")
}

func TestRootDetection(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "go.mod", "module x\n")
	writeFile(t, dir, "nested/deep/main.go", "package main\n")
	nested := filepath.Join(dir, "nested", "deep", "main.go")

	server := Server{ID: "gopls", RootMarkers: []string{"go.mod"}}
	root, ok := server.Root(nested, dir)
	if !ok {
		t.Fatal("expected a root")
	}
	if root != normalizePath(dir) {
		t.Errorf("root = %q, want %q", root, normalizePath(dir))
	}
}

// TestRootPrefersNearestMarker: a monorepo's inner module is the root, not the
// outer one.
func TestRootPrefersNearestMarker(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "go.mod", "module outer\n")
	writeFile(t, dir, "inner/go.mod", "module inner\n")
	file := writeFile(t, dir, "inner/pkg/main.go", "package main\n")

	server := Server{ID: "gopls", RootMarkers: []string{"go.mod"}}
	root, ok := server.Root(file, dir)
	if !ok {
		t.Fatal("expected a root")
	}
	if want := normalizePath(filepath.Join(dir, "inner")); root != want {
		t.Errorf("root = %q, want the nearest module %q", root, want)
	}
}

// TestStrictRootDeclinesWithoutMarker: a strict server must not run on an
// unrelated tree.
func TestStrictRootDeclinesWithoutMarker(t *testing.T) {
	dir := t.TempDir()
	file := writeFile(t, dir, "main.py", "x = 1\n")

	strict := Server{ID: "ruff", RootMarkers: []string{"pyproject.toml"}, StrictRoot: true}
	if _, ok := strict.Root(file, dir); ok {
		t.Error("a strict server must decline when no marker is found")
	}

	lenient := Server{ID: "pyright", RootMarkers: []string{"pyproject.toml"}}
	root, ok := lenient.Root(file, dir)
	if !ok || root != normalizePath(dir) {
		t.Errorf("a lenient server should fall back to the working directory, got %q ok=%v", root, ok)
	}
}

func TestServerHandles(t *testing.T) {
	server := Server{Extensions: []string{".go"}}
	if !server.Handles("/x/main.go") {
		t.Error("should handle .go")
	}
	if server.Handles("/x/main.rs") {
		t.Error("should not handle .rs")
	}
	// A whole-filename entry, for files with no extension.
	docker := Server{Extensions: []string{"Dockerfile"}}
	if !docker.Handles("/x/Dockerfile") {
		t.Error("should handle Dockerfile by name")
	}
	// An empty extension list means every file.
	any := Server{}
	if !any.Handles("/x/whatever.xyz") {
		t.Error("an empty extension list should match everything")
	}
}

// Markdown went unserved for a while: the repo shipped mdlsp but no registry
// entry claimed .md, so clientsFor matched nothing and the miss was silent.
func TestBuiltinServerHandlesMarkdown(t *testing.T) {
	var found *Server
	for i := range builtinServers {
		if builtinServers[i].ID == "mdlsp" {
			found = &builtinServers[i]
			break
		}
	}
	if found == nil {
		t.Fatal("no builtin server with ID mdlsp")
	}
	if !found.Handles("/x/README.md") {
		t.Error("mdlsp should handle .md")
	}
	if got := languageID("/x/README.md"); got != "markdown" {
		t.Errorf("languageID(.md) = %q, want markdown", got)
	}
}

func TestURIRoundTrip(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix path shapes")
	}
	cases := []string{"/tmp/a.go", "/tmp/with space/b.go", "/tmp/hash#1/c.go"}
	for _, path := range cases {
		uri := uriFromPath(path)
		if !strings.HasPrefix(uri, "file://") {
			t.Errorf("uriFromPath(%q) = %q, want a file:// URI", path, uri)
		}
		back, ok := pathFromURI(uri)
		if !ok || back != path {
			t.Errorf("round trip of %q gave %q (uri %q, ok=%v)", path, back, uri, ok)
		}
	}
	if _, ok := pathFromURI("untitled:foo"); ok {
		t.Error("a non-file URI must not resolve to a path")
	}
}

func TestReport(t *testing.T) {
	if got := Report("a.go", nil); got != "" {
		t.Errorf("no diagnostics should render nothing, got %q", got)
	}
	// Warnings alone render nothing: the model cannot act on them and they
	// crowd out the errors.
	warn := []Diagnostic{{Severity: SeverityWarning, Message: "unused"}}
	if got := Report("a.go", warn); got != "" {
		t.Errorf("warnings alone should render nothing, got %q", got)
	}

	issues := []Diagnostic{
		{Severity: SeverityError, Message: "boom", Range: Range{Start: Position{Line: 4, Character: 2}}},
		{Severity: SeverityWarning, Message: "meh"},
	}
	got := Report("a.go", issues)
	if !strings.Contains(got, `<diagnostics file="a.go">`) {
		t.Errorf("missing the wrapper: %q", got)
	}
	if !strings.Contains(got, "ERROR [5:3] boom") {
		t.Errorf("positions must be 1-based: %q", got)
	}
	if strings.Contains(got, "meh") {
		t.Errorf("warnings must not be included: %q", got)
	}
}

// maxDiagnosticsPerFile re-exports the shared limit for this test; the
// constant itself lives in internal/lspprotocol now.
const maxDiagnosticsPerFile = lspprotocol.MaxDiagnosticsPerFile

func TestReportTruncates(t *testing.T) {
	var issues []Diagnostic
	for i := 0; i < 25; i++ {
		issues = append(issues, Diagnostic{Severity: SeverityError, Message: "e" + itoa(i)})
	}
	got := Report("a.go", issues)
	if !strings.Contains(got, "... and 5 more") {
		t.Errorf("expected a truncation note, got %q", got)
	}
	if strings.Count(got, "ERROR") != maxDiagnosticsPerFile {
		t.Errorf("got %d entries, want %d", strings.Count(got, "ERROR"), maxDiagnosticsPerFile)
	}
}

// jsonUnmarshalConfig parses a config document the way the loader does, so the
// LSP section's boolean-or-record union is exercised through real JSON.
func jsonUnmarshalConfig(document string, cfg *config.Config) error {
	return json.Unmarshal([]byte(document), cfg)
}

// TestUnchangedTouchDoesNotStall is a regression for a 5s stall per call: when
// a file is already open at these exact contents, Open sends nothing, so
// nothing will ever be published and waiting can only burn the timeout. Reading
// the same file twice in a turn is common, so this was seconds of dead time.
func TestUnchangedTouchDoesNotStall(t *testing.T) {
	dir := t.TempDir()
	file := writeFile(t, dir, "main.fake", "a BUG here\n")

	service := newFakeService(t, dir, []string{".fake"})
	ctx := context.Background()
	service.Touch(ctx, file, true)

	start := time.Now()
	for i := 0; i < 3; i++ {
		service.Touch(ctx, file, true)
	}
	elapsed := time.Since(start)

	// Three no-op touches used to cost 3 x diagnosticsDocumentWait.
	if elapsed > diagnosticsDocumentWait {
		t.Errorf("three unchanged touches took %v; an unchanged file must not wait for a publish", elapsed)
	}
	if got := service.DiagnosticsFor(file); len(got) != 1 {
		t.Errorf("diagnostics were lost across no-op touches: %+v", got)
	}
}

// TestPublishBeforeWaitIsNotMissed is a regression for a race: a server can
// publish before the caller reaches WaitForDiagnostics, and a waiter that only
// listens for future publishes then blocks for the full timeout. The sequence
// counter is what closes the window.
func TestPublishBeforeWaitIsNotMissed(t *testing.T) {
	dir := t.TempDir()
	file := writeFile(t, dir, "main.fake", "a BUG here\n")

	service := newFakeService(t, dir, []string{".fake"})
	ctx := context.Background()
	clients := service.clientsFor(ctx, file)
	if len(clients) != 1 {
		t.Fatalf("got %d clients, want 1", len(clients))
	}
	client := clients[0]

	// Drive the exact ordering by hand: publish first, then wait on the
	// sequence captured beforehand.
	seq := client.PublishSeq(file)
	if _, err := client.Open(file); err != nil {
		t.Fatal(err)
	}
	// Give the publish time to land before the wait starts.
	time.Sleep(200 * time.Millisecond)

	start := time.Now()
	client.WaitForDiagnostics(ctx, file, seq)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("waiting took %v for a publish that already arrived", elapsed)
	}
}

// TestNullLSPConfigMeansDefault is a regression for a latent bug: the loader
// round-trips config through JSON, and unmarshalling `null` into a bool
// succeeds while leaving it false — which the boolean branch then read as
// `lsp: false` and used to switch every server off.
func TestNullLSPConfigMeansDefault(t *testing.T) {
	cfg := &config.Config{}
	if err := jsonUnmarshalConfig(`{"lsp": null}`, cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.LSP.Disabled() {
		t.Error("`lsp: null` means unconfigured, not disabled")
	}
	if !New(t.TempDir(), cfg).Enabled() {
		t.Error("a null lsp section must leave the subsystem enabled")
	}

	// The round trip the loader actually performs must survive too.
	encoded, err := json.Marshal(map[string]any{"lsp": cfg.LSP})
	if err != nil {
		t.Fatal(err)
	}
	var reloaded config.Config
	if err := json.Unmarshal(encoded, &reloaded); err != nil {
		t.Fatal(err)
	}
	if reloaded.LSP.Disabled() {
		t.Errorf("a config round trip disabled LSP: %s", encoded)
	}
}

// TestDiagnoseExplainsMissingBinary covers the case the sidebar could not
// distinguish: a server that handles the file but is not installed.
func TestDiagnoseExplainsMissingBinary(t *testing.T) {
	dir := t.TempDir()
	file := writeFile(t, dir, "main.fake", "text\n")

	service := &Service{
		directory: normalizePath(dir),
		clients:   map[string]*Client{},
		broken:    map[string]bool{},
		spawning:  map[string]*sync.WaitGroup{},
		enabled:   true,
		servers: []Server{
			{ID: "missing", Extensions: []string{".fake"}, Command: []string{"gocode-no-such-server"}},
			{ID: "other", Extensions: []string{".rs"}, Command: []string{"gocode-no-such-server"}},
		},
	}
	t.Cleanup(service.Shutdown)

	explanations := service.Diagnose(file)
	if len(explanations) != 2 {
		t.Fatalf("got %d explanations, want one per server", len(explanations))
	}

	byID := map[string]Explanation{}
	for _, item := range explanations {
		byID[item.ServerID] = item
	}
	if !byID["missing"].Handles {
		t.Error("the .fake server should report that it handles the file")
	}
	if byID["missing"].Installed {
		t.Error("a binary that does not exist must report as not installed")
	}
	if byID["missing"].Command != "gocode-no-such-server" {
		t.Errorf("the command must be named so a PATH problem is actionable, got %q", byID["missing"].Command)
	}
	if byID["other"].Handles {
		t.Error("a server for another language must report that it does not handle the file")
	}
}

// TestDiagnoseReportsRunning: once a server is up, Diagnose says so.
func TestDiagnoseReportsRunning(t *testing.T) {
	dir := t.TempDir()
	file := writeFile(t, dir, "main.fake", "a BUG\n")

	service := newFakeService(t, dir, []string{".fake"})
	service.Touch(context.Background(), file, true)

	explanations := service.Diagnose(file)
	if len(explanations) != 1 {
		t.Fatalf("got %d explanations, want 1", len(explanations))
	}
	if !explanations[0].Running {
		t.Errorf("a connected server must report Running: %+v", explanations[0])
	}
}
