// Package loginpage serves a one-shot sign-in form on the loopback interface.
//
// An ACP client may not be handed a secret through form elicitation
// (agentclientprotocol.com protocol/v1/elicitation "Form mode": passwords and
// API keys MUST go through URL mode). So when the agent needs one — a
// gocoder.org password, a provider API key — it serves this page on
// 127.0.0.1, asks the client to open its URL, and receives the secret
// straight from the user's browser. The secret never crosses the protocol.
//
// The page lives at an unguessable path and accepts submissions only from a
// loopback origin, so another site in the same browser cannot post to it and
// another local user cannot find it by scanning ports.
package loginpage

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"html/template"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Field is one input on the form.
type Field struct {
	Name  string
	Label string
	// Type is the input type: "text", "email" or "password"; "select" uses
	// Options.
	Type        string
	Options     []Option
	Placeholder string
	Required    bool
	// Value pre-fills the field.
	Value string
}

// Option is one choice of a select field.
type Option struct {
	Value string
	Label string
}

// Form is the page to serve.
type Form struct {
	Title  string
	Intro  string
	Submit string
	Fields []Field
}

// SubmitFunc handles one submission. An error is shown above the form and
// the user may try again; success ends the page with done as its message.
type SubmitFunc func(ctx context.Context, values map[string]string) (done string, err error)

// Page is a running form.
type Page struct {
	// URL is what the user's browser has to open.
	URL string

	listener net.Listener
	server   *http.Server
	result   chan struct{}
	once     sync.Once
}

// Serve starts the form. It keeps serving until a submission succeeds, ctx
// ends, or Close is called; Wait reports which.
func Serve(ctx context.Context, form Form, submit SubmitFunc) (*Page, error) {
	token, err := randomToken()
	if err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	page := &Page{
		URL:      "http://" + listener.Addr().String() + "/" + token,
		listener: listener,
		result:   make(chan struct{}),
	}
	h := &handler{form: form, submit: submit, path: "/" + token, ctx: ctx, page: page}
	page.server = &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second}
	go page.server.Serve(listener)
	go func() {
		select {
		case <-ctx.Done():
		case <-page.result:
		}
		// Give the success page a moment to reach the browser before the
		// listener goes away.
		time.Sleep(200 * time.Millisecond)
		page.server.Close()
	}()
	return page, nil
}

// Wait blocks until a submission succeeds (nil) or ctx ends (its error).
func (p *Page) Wait(ctx context.Context) error {
	select {
	case <-p.result:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Close stops serving.
func (p *Page) Close() { p.server.Close() }

func (p *Page) finish() { p.once.Do(func() { close(p.result) }) }

type handler struct {
	form   Form
	submit SubmitFunc
	path   string
	ctx    context.Context
	page   *Page

	// submitting serializes submissions: a double-clicked button must not
	// sign in twice.
	submitting sync.Mutex
	mu         sync.Mutex
	done       string
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != h.path {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'")
	w.Header().Set("Referrer-Policy", "no-referrer")

	h.mu.Lock()
	done := h.done
	h.mu.Unlock()
	if done != "" {
		h.render(w, http.StatusOK, view{Form: h.form, Done: done})
		return
	}
	switch r.Method {
	case http.MethodGet:
		h.render(w, http.StatusOK, view{Form: h.form})
	case http.MethodPost:
		if !loopbackOrigin(r) {
			http.Error(w, "cross-origin submission refused", http.StatusForbidden)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		values := map[string]string{}
		for _, field := range h.form.Fields {
			values[field.Name] = r.PostForm.Get(field.Name)
		}
		h.submitting.Lock()
		defer h.submitting.Unlock()
		h.mu.Lock()
		done := h.done
		h.mu.Unlock()
		if done != "" {
			h.render(w, http.StatusOK, view{Form: h.form, Done: done})
			return
		}
		message, err := h.submit(h.ctx, values)
		if err != nil {
			h.render(w, http.StatusOK, view{Form: refill(h.form, values), Error: err.Error()})
			return
		}
		if message == "" {
			message = "Done. You can close this window."
		}
		h.mu.Lock()
		h.done = message
		h.mu.Unlock()
		h.render(w, http.StatusOK, view{Form: h.form, Done: message})
		h.page.finish()
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// refill keeps what the user typed after a failed submission, except
// secrets.
func refill(form Form, values map[string]string) Form {
	fields := make([]Field, len(form.Fields))
	for i, field := range form.Fields {
		if field.Type != "password" {
			field.Value = values[field.Name]
		}
		fields[i] = field
	}
	form.Fields = fields
	return form
}

// loopbackOrigin accepts a POST only from this page itself: browsers send
// Origin on form posts, and a page on any other site has a different one.
// A request without Origin or Referer (curl from the same machine) is
// accepted; it already had to know the unguessable path.
func loopbackOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		origin = r.Header.Get("Referer")
	}
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return parsed.Host == r.Host
}

func randomToken() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

type view struct {
	Form  Form
	Error string
	Done  string
}

func (h *handler) render(w http.ResponseWriter, status int, v view) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_ = pageTemplate.Execute(w, v)
}

var pageTemplate = template.Must(template.New("page").Funcs(template.FuncMap{
	"lines": func(s string) []string { return strings.Split(s, "\n") },
}).Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Form.Title}} · gocode</title>
<style>
:root { --bg:#f6f6f4; --card:#ffffff; --fg:#1d1d1b; --muted:#6b6b66; --line:#d9d9d4; --accent:#2f6fde; --error:#b3261e; --ok:#1f7a3a; }
@media (prefers-color-scheme: dark) { :root { --bg:#141413; --card:#1e1e1c; --fg:#ececea; --muted:#9a9a94; --line:#33332f; --accent:#6e9cf0; --error:#f2867d; --ok:#6fcf8e; } }
* { box-sizing: border-box; }
body { margin:0; min-height:100vh; display:flex; align-items:center; justify-content:center; padding:16px; background:var(--bg); color:var(--fg); font:15px/1.5 system-ui, -apple-system, "Segoe UI", sans-serif; }
main { width:100%; max-width:420px; background:var(--card); border:1px solid var(--line); border-radius:12px; padding:28px; }
h1 { font-size:20px; margin:0 0 6px; }
p { margin:0 0 18px; color:var(--muted); }
label { display:block; font-weight:600; margin:14px 0 6px; font-size:14px; }
input, select { width:100%; padding:10px 12px; border:1px solid var(--line); border-radius:8px; background:var(--bg); color:var(--fg); font:inherit; }
input:focus, select:focus { outline:2px solid var(--accent); outline-offset:1px; }
button { margin-top:22px; width:100%; padding:11px; border:0; border-radius:8px; background:var(--accent); color:#fff; font:inherit; font-weight:600; cursor:pointer; }
.error { color:var(--error); margin:0 0 6px; }
.ok { color:var(--ok); font-weight:600; }
.brand { font-size:12px; letter-spacing:.08em; text-transform:uppercase; color:var(--muted); margin-bottom:14px; }
</style>
</head>
<body>
<main>
<div class="brand">gocode</div>
<h1>{{.Form.Title}}</h1>
{{if .Done}}
<p class="ok">{{range lines .Done}}{{.}}<br>{{end}}</p>
{{else}}
{{if .Form.Intro}}<p>{{.Form.Intro}}</p>{{end}}
{{if .Error}}<p class="error" role="alert">{{.Error}}</p>{{end}}
<form method="post" autocomplete="on">
{{range .Form.Fields}}
<label for="{{.Name}}">{{.Label}}</label>
{{if eq .Type "select"}}
<select id="{{.Name}}" name="{{.Name}}"{{if .Required}} required{{end}}>
{{$value := .Value}}{{range .Options}}<option value="{{.Value}}"{{if eq .Value $value}} selected{{end}}>{{.Label}}</option>{{end}}
</select>
{{else}}
<input id="{{.Name}}" name="{{.Name}}" type="{{.Type}}" value="{{.Value}}"{{if .Placeholder}} placeholder="{{.Placeholder}}"{{end}}{{if .Required}} required{{end}}{{if eq .Type "password"}} autocomplete="current-password"{{end}}>
{{end}}
{{end}}
<button type="submit">{{.Form.Submit}}</button>
</form>
{{end}}
</main>
</body>
</html>
`))
