// Package signin is the gocoder.org sign-in screen: a small Bubble Tea
// program offering register / log in / skip, with the forms, validation,
// progress and confirmation around a single injected Submit call.
//
// It owns presentation only. Talking to the website and storing the key
// belong to the caller's Submit, which keeps this package free of network
// code and lets its tests drive every screen without a server.
package signin

import (
	"context"
	"errors"
	"image/color"
	"strings"
	"unicode"
	"unicode/utf8"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/langazov/gocode-go/internal/tui/theme"
)

// Screen is where the program starts, or is.
type Screen int

const (
	// ScreenMenu offers register, log in and (optionally) skip.
	ScreenMenu Screen = iota
	// ScreenRegister is the create-account form.
	ScreenRegister
	// ScreenLogin is the log-in form.
	ScreenLogin

	screenDone
)

// Credentials is what the forms collect.
type Credentials struct {
	Email       string
	DisplayName string
	Password    string
}

// Result is what a successful Submit reports, for the confirmation screen.
type Result struct {
	Name      string
	Email     string
	KeyPrefix string
	Path      string
}

// Status is how the program ended.
type Status int

const (
	// StatusCancelled: ctrl+c, or esc where there is nowhere to go back to.
	StatusCancelled Status = iota
	// StatusSkipped: the user chose to continue without an account.
	StatusSkipped
	// StatusSignedIn: Submit succeeded and the user confirmed.
	StatusSignedIn
)

// Outcome reports how the program ended.
type Outcome struct {
	Status Status
	Result Result
	// LastErr is the most recent Submit error, kept even when the user then
	// skipped, so a caller can tell "chose not to" from "could not".
	LastErr error
}

// Options configures one run.
type Options struct {
	Theme theme.Theme
	// AutoTheme lets the terminal's reported background choose between the
	// default dark and light palettes, for a user who never picked a theme.
	AutoTheme bool
	// Site is the host shown to the user, e.g. "gocoder.org".
	Site  string
	Start Screen
	// AllowSkip adds "Skip for now" to the menu and makes esc there skip.
	AllowSkip bool
	// Notice is an optional line shown above the forms.
	Notice string
	// Submit registers (register == true) or logs in, and stores the
	// result. Its error message is shown to the user as-is.
	Submit func(ctx context.Context, register bool, c Credentials) (Result, error)
}

// Run shows the sign-in screen until the user signs in, skips or cancels.
// It renders inline rather than on the alternate screen, so the one-line
// confirmation it leaves behind stays in the terminal's scrollback.
func Run(ctx context.Context, opts Options, programOptions ...tea.ProgramOption) (Outcome, error) {
	programOptions = append([]tea.ProgramOption{tea.WithContext(ctx)}, programOptions...)
	final, err := tea.NewProgram(newModel(ctx, opts), programOptions...).Run()
	if err != nil {
		return Outcome{}, err
	}
	return final.(*model).outcome, nil
}

const (
	defaultContentWidth = 50
	minContentWidth     = 30
)

type submitDoneMsg struct {
	result Result
	err    error
}

type field struct {
	label string
	input textinput.Model
	// meter shows a strength gauge under a new password.
	meter bool
}

type menuItem struct {
	title, desc string
	screen      Screen
	skip        bool
}

type model struct {
	ctx  context.Context
	opts Options
	st   styles

	screen Screen
	menu   int

	fields []field
	// focus indexes fields; len(fields) is the submit button.
	focus int
	// email survives switching between the two forms.
	email string

	formErr  string
	errField int

	busy bool
	spin spinner.Model

	result   Result
	outcome  Outcome
	quitting bool
	width    int
}

func newModel(ctx context.Context, opts Options) *model {
	if opts.Site == "" {
		opts.Site = "gocoder.org"
	}
	m := &model{ctx: ctx, opts: opts, st: newStyles(opts.Theme), errField: -1}
	m.spin = spinner.New(spinner.WithSpinner(spinner.MiniDot), spinner.WithStyle(m.st.brand))
	if opts.Start == ScreenRegister || opts.Start == ScreenLogin {
		m.openForm(opts.Start)
	}
	return m
}

func (m *model) Init() tea.Cmd {
	cmds := []tea.Cmd{textinput.Blink}
	if m.opts.AutoTheme {
		cmds = append(cmds, tea.RequestBackgroundColor)
	}
	return tea.Batch(cmds...)
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.resizeInputs()
		return m, nil
	case tea.BackgroundColorMsg:
		if m.opts.AutoTheme {
			t := theme.Dark()
			if !msg.IsDark() {
				t = theme.Light()
			}
			m.applyTheme(t)
		}
		return m, nil
	case submitDoneMsg:
		m.busy = false
		if msg.err != nil {
			m.outcome.LastErr = msg.err
			m.formErr = msg.err.Error()
			m.errField = -1
			return m, nil
		}
		m.outcome.LastErr = nil
		m.result = msg.result
		m.screen = screenDone
		return m, nil
	case spinner.TickMsg:
		if !m.busy {
			return m, nil
		}
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd
	case tea.KeyPressMsg:
		if msg.String() == "ctrl+c" {
			return m.finish(StatusCancelled)
		}
		if m.busy {
			return m, nil
		}
		switch m.screen {
		case ScreenMenu:
			return m.updateMenu(msg)
		case screenDone:
			switch msg.String() {
			case "enter", "space", "esc", "q":
				return m.finish(StatusSignedIn)
			}
			return m, nil
		default:
			return m.updateForm(msg)
		}
	}
	// Anything else (cursor blink) belongs to the focused input.
	if m.inForm() && m.focus < len(m.fields) {
		var cmd tea.Cmd
		m.fields[m.focus].input, cmd = m.fields[m.focus].input.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m *model) View() tea.View {
	if m.quitting {
		if m.outcome.Status == StatusSignedIn {
			return tea.NewView(m.doneLine() + "\n")
		}
		return tea.NewView("")
	}
	return tea.NewView(m.render())
}

func (m *model) finish(s Status) (tea.Model, tea.Cmd) {
	m.outcome.Status = s
	if s == StatusSignedIn {
		m.outcome.Result = m.result
	}
	m.quitting = true
	return m, tea.Quit
}

func (m *model) inForm() bool {
	return m.screen == ScreenRegister || m.screen == ScreenLogin
}

// ---- menu ----

func (m *model) menuItems() []menuItem {
	items := []menuItem{
		{title: "Create an account", desc: "Free · just an email and a password", screen: ScreenRegister},
		{title: "Log in", desc: "Use an existing " + m.opts.Site + " account", screen: ScreenLogin},
	}
	if m.opts.AllowSkip {
		items = append(items, menuItem{title: "Skip for now", desc: "Run gocode login any time", skip: true})
	}
	return items
}

func (m *model) updateMenu(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	items := m.menuItems()
	switch key := msg.String(); key {
	case "up", "k", "shift+tab":
		m.menu = (m.menu - 1 + len(items)) % len(items)
	case "down", "j", "tab":
		m.menu = (m.menu + 1) % len(items)
	case "enter", "space":
		return m.choose(items[m.menu])
	case "1", "2", "3":
		if i := int(key[0] - '1'); i < len(items) {
			m.menu = i
			return m.choose(items[i])
		}
	case "esc", "q":
		if m.opts.AllowSkip {
			return m.finish(StatusSkipped)
		}
		return m.finish(StatusCancelled)
	}
	return m, nil
}

func (m *model) choose(item menuItem) (tea.Model, tea.Cmd) {
	if item.skip {
		return m.finish(StatusSkipped)
	}
	return m, m.openForm(item.screen)
}

// ---- forms ----

func (m *model) openForm(screen Screen) tea.Cmd {
	m.screen = screen
	m.formErr, m.errField = "", -1
	email := m.newInput("you@example.com", false)
	email.SetValue(m.email)
	if screen == ScreenRegister {
		m.fields = []field{
			{label: "Email", input: email},
			{label: "Display name", input: m.newInput("How gocoder.org greets you", false)},
			{label: "Password", input: m.newInput("At least 8 characters", true), meter: true},
			{label: "Confirm password", input: m.newInput("Type it again", true)},
		}
	} else {
		m.fields = []field{
			{label: "Email", input: email},
			{label: "Password", input: m.newInput("", true)},
		}
	}
	m.syncPlaceholders()
	start := 0
	if m.email != "" {
		start = 1
	}
	return m.focusField(start)
}

func (m *model) newInput(placeholder string, secret bool) textinput.Model {
	in := textinput.New()
	in.Prompt = ""
	in.Placeholder = placeholder
	in.CharLimit = 254
	if secret {
		in.CharLimit = 128
		in.EchoMode = textinput.EchoPassword
		in.EchoCharacter = '•'
	}
	in.SetWidth(m.contentWidth() - 3)
	in.SetStyles(m.st.inputStyles())
	return in
}

func (m *model) focusField(i int) tea.Cmd {
	m.focus = i
	var cmd tea.Cmd
	for j := range m.fields {
		if j == i {
			cmd = m.fields[j].input.Focus()
		} else {
			m.fields[j].input.Blur()
		}
	}
	return cmd
}

func (m *model) updateForm(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	onButton := m.focus == len(m.fields)
	switch msg.String() {
	case "esc":
		if m.opts.Start == ScreenMenu {
			m.email = strings.TrimSpace(m.fields[0].input.Value())
			m.screen, m.fields, m.formErr, m.errField = ScreenMenu, nil, "", -1
			return m, nil
		}
		return m.finish(StatusCancelled)
	case "tab", "down":
		return m, m.focusField((m.focus + 1) % (len(m.fields) + 1))
	case "shift+tab", "up":
		return m, m.focusField((m.focus + len(m.fields)) % (len(m.fields) + 1))
	case "enter":
		if m.focus < len(m.fields)-1 {
			return m, m.focusField(m.focus + 1)
		}
		return m.submit()
	case "space":
		if onButton {
			return m.submit()
		}
	}
	if onButton {
		return m, nil
	}
	before := m.fields[m.focus].input.Value()
	var cmd tea.Cmd
	m.fields[m.focus].input, cmd = m.fields[m.focus].input.Update(msg)
	if m.fields[m.focus].input.Value() != before {
		// A stale complaint about what the user is now fixing only nags.
		m.formErr, m.errField = "", -1
		m.syncPlaceholders()
	}
	return m, cmd
}

// syncPlaceholders previews the display name registration will fall back
// to, so leaving it empty is a visible choice rather than a surprise.
func (m *model) syncPlaceholders() {
	if m.screen != ScreenRegister {
		return
	}
	if local := localPart(m.fields[0].input.Value()); local != "" {
		m.fields[1].input.Placeholder = local
	} else {
		m.fields[1].input.Placeholder = "How gocoder.org greets you"
	}
}

func (m *model) submit() (tea.Model, tea.Cmd) {
	creds, bad, err := m.validate()
	if err != nil {
		m.formErr, m.errField = err.Error(), bad
		return m, m.focusField(bad)
	}
	m.formErr, m.errField = "", -1
	m.email = creds.Email
	m.busy = true
	register := m.screen == ScreenRegister
	ctx, submit := m.ctx, m.opts.Submit
	return m, tea.Batch(m.spin.Tick, func() tea.Msg {
		result, err := submit(ctx, register, creds)
		return submitDoneMsg{result: result, err: err}
	})
}

// validate checks what the server would reject anyway, so the common
// mistakes are caught instantly and point at the field to fix.
func (m *model) validate() (Credentials, int, error) {
	email := strings.TrimSpace(m.fields[0].input.Value())
	if !plausibleEmail(email) {
		return Credentials{}, 0, errors.New("enter a valid email address")
	}
	if m.screen == ScreenLogin {
		password := m.fields[1].input.Value()
		if password == "" {
			return Credentials{}, 1, errors.New("enter your password")
		}
		return Credentials{Email: email, Password: password}, -1, nil
	}
	name := strings.TrimSpace(m.fields[1].input.Value())
	if name == "" {
		name = localPart(email)
	}
	if utf8.RuneCountInString(name) > 64 {
		return Credentials{}, 1, errors.New("display name must be at most 64 characters")
	}
	password := m.fields[2].input.Value()
	if len(password) < 8 {
		return Credentials{}, 2, errors.New("password must be at least 8 characters")
	}
	if len(password) > 128 {
		return Credentials{}, 2, errors.New("password must be at most 128 characters")
	}
	if m.fields[3].input.Value() != password {
		return Credentials{}, 3, errors.New("passwords don't match")
	}
	return Credentials{Email: email, DisplayName: name, Password: password}, -1, nil
}

func plausibleEmail(email string) bool {
	at := strings.LastIndex(email, "@")
	return at > 0 && at < len(email)-1 && strings.Contains(email[at+1:], ".") &&
		!strings.ContainsFunc(email, unicode.IsSpace)
}

func localPart(email string) string {
	local, _, ok := strings.Cut(strings.TrimSpace(email), "@")
	if !ok {
		return ""
	}
	return local
}

// strength scores a password 0-4 on length and character variety. It is a
// nudge, not a gate: the server's only rule is 8-128 bytes.
func strength(password string) (int, string) {
	var lower, upper, digit, symbol bool
	for _, r := range password {
		switch {
		case unicode.IsLower(r):
			lower = true
		case unicode.IsUpper(r):
			upper = true
		case unicode.IsDigit(r):
			digit = true
		default:
			symbol = true
		}
	}
	if len(password) < 8 {
		return 0, "too short"
	}
	score := 1
	if len(password) >= 12 {
		score++
	}
	if lower && upper {
		score++
	}
	if digit || symbol {
		score++
	}
	if digit && symbol && len(password) >= 12 {
		score++
	}
	score = min(score, 4)
	return score, [...]string{"too short", "weak", "fair", "good", "strong"}[score]
}

// ---- layout ----

func (m *model) contentWidth() int {
	if m.width > 0 && m.width-10 < defaultContentWidth {
		return max(minContentWidth, m.width-10)
	}
	return defaultContentWidth
}

func (m *model) resizeInputs() {
	for i := range m.fields {
		m.fields[i].input.SetWidth(m.contentWidth() - 3)
	}
}

func (m *model) applyTheme(t theme.Theme) {
	m.st = newStyles(t)
	m.spin.Style = m.st.brand
	for i := range m.fields {
		m.fields[i].input.SetStyles(m.st.inputStyles())
	}
}

func (m *model) render() string {
	header := m.st.brand.Render("gocode") + m.st.muted.Render("  ×  "+m.opts.Site)
	var body string
	switch m.screen {
	case ScreenMenu:
		body = m.viewMenu()
	case screenDone:
		body = m.viewDone()
	default:
		body = m.viewForm()
	}
	parts := []string{header, ""}
	if m.opts.Notice != "" && m.screen != screenDone {
		parts = append(parts, m.st.warning.Render(m.opts.Notice), "")
	}
	parts = append(parts, body)
	inner := lipgloss.NewStyle().Width(m.contentWidth()).Render(lipgloss.JoinVertical(lipgloss.Left, parts...))
	card := m.st.card.Render(inner)
	return "\n" + indent(card, "  ") + "\n" + indent(m.helpLine(), "   ") + "\n"
}

func (m *model) viewMenu() string {
	rows := []string{
		m.st.title.Render("Connect gocode to " + m.opts.Site),
		m.st.muted.Render("An account gives the rag-plugin hosted embeddings — semantic code search with no API key of your own."),
		"",
	}
	for i, item := range m.menuItems() {
		pointer, title := "  ", m.st.item
		if i == m.menu {
			pointer, title = m.st.brand.Render("❯ "), m.st.itemSelected
		}
		rows = append(rows, pointer+title.Render(item.title), "  "+m.st.muted.Render(item.desc))
		if i < len(m.menuItems())-1 {
			rows = append(rows, "")
		}
	}
	return lipgloss.JoinVertical(lipgloss.Left, rows...)
}

func (m *model) viewForm() string {
	title, action, busy := "Log in to "+m.opts.Site, "Log in", "Signing you in…"
	if m.screen == ScreenRegister {
		title, action, busy = "Create your "+m.opts.Site+" account", "Create account", "Creating your account…"
	}
	rows := []string{m.st.title.Render(title), ""}
	for i, f := range m.fields {
		bar, label := "  ", m.st.label
		if i == m.focus {
			bar, label = m.st.barFocused.Render("┃ "), m.st.labelFocused
		}
		if i == m.errField {
			bar = m.st.errorText.Render("┃ ")
		}
		rows = append(rows, "  "+label.Render(f.label), bar+f.input.View())
		if f.meter && f.input.Value() != "" {
			rows = append(rows, "  "+m.meter(f.input.Value()))
		}
		rows = append(rows, "")
	}
	if m.formErr != "" {
		rows = append(rows, m.st.errorText.Render("✗ "+m.formErr), "")
	}
	if m.busy {
		rows = append(rows, m.spin.View()+" "+m.st.muted.Render(busy))
	} else {
		button := m.st.button
		if m.focus == len(m.fields) {
			button = m.st.buttonFocused
		}
		rows = append(rows, button.Render(action))
	}
	return lipgloss.JoinVertical(lipgloss.Left, rows...)
}

func (m *model) meter(password string) string {
	score, label := strength(password)
	fill := [...]lipgloss.Style{m.st.errorText, m.st.errorText, m.st.warning, m.st.info, m.st.successText}[score]
	var b strings.Builder
	for i := 1; i <= 4; i++ {
		if i <= score {
			b.WriteString(fill.Render("━━━"))
		} else {
			b.WriteString(m.st.faint.Render("━━━"))
		}
		b.WriteString(" ")
	}
	return b.String() + fill.Render(label)
}

func (m *model) viewDone() string {
	name := m.result.Name
	if name == "" {
		name = m.result.Email
	}
	rows := []string{
		m.st.successText.Bold(true).Render("✓ You're signed in"),
		"",
		m.st.text.Render("Welcome, " + name + "."),
		m.st.muted.Render(m.result.Email),
		"",
		m.st.label.Render("API key  ") + m.st.text.Render(m.result.KeyPrefix+"…"),
		m.st.label.Render("Saved to ") + m.st.muted.Render(m.result.Path),
		"",
		m.st.muted.Render("The rag-plugin now embeds through " + m.opts.Site + " — no API key of your own needed."),
		"",
		m.st.buttonFocused.Render("Continue"),
	}
	return lipgloss.JoinVertical(lipgloss.Left, rows...)
}

func (m *model) doneLine() string {
	who := m.result.Email
	if m.result.Name != "" {
		who = m.result.Name + " <" + m.result.Email + ">"
	}
	return m.st.successText.Render("✓") + m.st.text.Render(" Signed in to "+m.opts.Site+" as "+who) +
		m.st.muted.Render(" · API key "+m.result.KeyPrefix+"… saved")
}

func (m *model) helpLine() string {
	var pairs []string
	switch {
	case m.busy:
		pairs = []string{"ctrl+c", "cancel"}
	case m.screen == ScreenMenu:
		esc := "cancel"
		if m.opts.AllowSkip {
			esc = "skip"
		}
		pairs = []string{"↑/↓", "choose", "enter", "select", "esc", esc}
	case m.screen == screenDone:
		pairs = []string{"enter", "continue"}
	default:
		esc := "cancel"
		if m.opts.Start == ScreenMenu {
			esc = "back"
		}
		pairs = []string{"tab", "next field", "enter", "submit", "esc", esc}
	}
	parts := make([]string, 0, len(pairs)/2)
	for i := 0; i < len(pairs); i += 2 {
		parts = append(parts, m.st.key.Render(pairs[i])+" "+m.st.muted.Render(pairs[i+1]))
	}
	return strings.Join(parts, m.st.faint.Render("  •  "))
}

func indent(block, prefix string) string {
	lines := strings.Split(block, "\n")
	for i, line := range lines {
		lines[i] = prefix + line
	}
	return strings.Join(lines, "\n")
}

// ---- styles ----

type styles struct {
	colors theme.Colors
	dark   bool

	card, brand, title, text, muted, faint, key lipgloss.Style
	label, labelFocused, barFocused             lipgloss.Style
	errorText, warning, info, successText       lipgloss.Style
	item, itemSelected, button, buttonFocused   lipgloss.Style
}

func newStyles(t theme.Theme) styles {
	c := t.Colors
	fg := func(col color.Color) lipgloss.Style { return lipgloss.NewStyle().Foreground(col) }
	return styles{
		colors:        c,
		dark:          t.Dark,
		card:          lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(c.BorderActive).Padding(1, 3),
		brand:         fg(c.Primary).Bold(true),
		title:         fg(c.Text).Bold(true),
		text:          fg(c.Text),
		muted:         fg(c.TextMuted),
		faint:         fg(c.BorderSubtle),
		key:           fg(c.Text),
		label:         fg(c.TextMuted),
		labelFocused:  fg(c.Text).Bold(true),
		barFocused:    fg(c.Primary),
		errorText:     fg(c.Error),
		warning:       fg(c.Warning),
		info:          fg(c.Info),
		successText:   fg(c.Success),
		item:          fg(c.Text),
		itemSelected:  fg(c.Primary).Bold(true),
		button:        lipgloss.NewStyle().Padding(0, 2).Foreground(c.TextMuted).Background(c.BackgroundElement),
		buttonFocused: lipgloss.NewStyle().Padding(0, 2).Bold(true).Foreground(c.Background).Background(c.Primary),
	}
}

func (s styles) inputStyles() textinput.Styles {
	st := textinput.DefaultStyles(s.dark)
	st.Focused.Text = s.text
	st.Focused.Placeholder = s.faint
	st.Focused.Prompt = lipgloss.NewStyle()
	st.Blurred.Text = s.muted
	st.Blurred.Placeholder = s.faint
	st.Blurred.Prompt = lipgloss.NewStyle()
	st.Cursor.Color = s.colors.Primary
	return st
}
