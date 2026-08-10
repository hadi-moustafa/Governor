// governorctl is a bubbletea/lipgloss TUI: configure a provider and a
// budget cap on-screen (no manual env var or config file editing
// required), start the gateway daemon in-process, and watch its live
// metrics update while it serves traffic.
package main

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/hadi-moustafa/governor/gateway"
	"github.com/hadi-moustafa/governor/internal/config"
	"github.com/hadi-moustafa/governor/internal/metrics"
)

// screen is which view the model is showing.
type screen int

const (
	screenConfig screen = iota
	screenRunning
)

// field indexes model.fields. providers is a distinct, non-text focus
// stop (cycled with left/right rather than typed).
type field int

const (
	fieldProvider field = iota
	fieldAddr
	fieldProviderURL
	fieldCapDollars
	fieldOpenAIKey
	fieldAnthropicKey
	fieldStart
	fieldCount
)

var providers = []string{"mock", "openai", "anthropic"}

// textField is one editable line in the config form.
type textField struct {
	label string
	value string
	mask  bool // true for API keys: rendered as asterisks
}

// model is bubbletea's Model for governorctl. Its Update/View methods are
// pure functions of (model, msg) -> model — no global state — so they're
// directly unit-testable without a real terminal.
type model struct {
	screen      screen
	focus       field
	providerIdx int
	fields      map[field]*textField
	err         error

	srv     *http.Server
	metrics *metrics.Counters
	snap    metrics.Snapshot
	addr    string
}

// tickMsg drives the live metrics refresh while screenRunning.
type tickMsg time.Time

// startedMsg / stopErrMsg report the outcome of starting/stopping the
// in-process gateway daemon.
type startedMsg struct {
	srv     *http.Server
	metrics *metrics.Counters
	addr    string
}
type errMsg struct{ err error }

func initialModel() model {
	cfg, _ := config.Load() // a malformed env var just falls back to zero values in the form; the user can retype it

	m := model{
		fields: map[field]*textField{
			fieldAddr:         {label: "Listen addr", value: cfg.Addr},
			fieldProviderURL:  {label: "Mock provider URL", value: cfg.ProviderURL},
			fieldCapDollars:   {label: "Cap (USD)", value: fmt.Sprintf("%.2f", float64(cfg.CapMicros)/1_000_000)},
			fieldOpenAIKey:    {label: "OpenAI API key", value: cfg.OpenAIAPIKey, mask: true},
			fieldAnthropicKey: {label: "Anthropic API key", value: cfg.AnthropicAPIKey, mask: true},
		},
	}
	for i, p := range providers {
		if p == cfg.Provider {
			m.providerIdx = i
		}
	}
	return m
}

func (m model) Init() tea.Cmd {
	return nil
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		return m.updateKey(msg)
	case startedMsg:
		m.screen = screenRunning
		m.srv = msg.srv
		m.metrics = msg.metrics
		m.addr = msg.addr
		m.err = nil
		return m, tick()
	case errMsg:
		m.err = msg.err
		return m, nil
	case tickMsg:
		if m.metrics != nil {
			m.snap = m.metrics.Snapshot()
		}
		if m.screen == screenRunning {
			return m, tick()
		}
		return m, nil
	}
	return m, nil
}

func tick() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m model) updateKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.Type == tea.KeyCtrlC {
		return m, m.quit()
	}

	if m.screen == screenRunning {
		switch msg.String() {
		case "q":
			return m, m.quit()
		case "s":
			return m.stopGateway(), nil
		}
		return m, nil
	}

	// screenConfig
	switch msg.Type {
	case tea.KeyTab, tea.KeyDown:
		m.focus = (m.focus + 1) % fieldCount
		return m, nil
	case tea.KeyShiftTab, tea.KeyUp:
		m.focus = (m.focus - 1 + fieldCount) % fieldCount
		return m, nil
	case tea.KeyLeft, tea.KeyRight:
		if m.focus == fieldProvider {
			delta := 1
			if msg.Type == tea.KeyLeft {
				delta = -1
			}
			m.providerIdx = (m.providerIdx + delta + len(providers)) % len(providers)
		}
		return m, nil
	case tea.KeyEnter:
		if m.focus == fieldStart {
			return m, m.startGateway()
		}
		return m, nil
	case tea.KeyBackspace:
		if f := m.fields[m.focus]; f != nil && len(f.value) > 0 {
			f.value = f.value[:len(f.value)-1]
		}
		return m, nil
	case tea.KeyRunes, tea.KeySpace:
		if f := m.fields[m.focus]; f != nil {
			f.value += msg.String()
		}
		return m, nil
	}
	return m, nil
}

func (m model) selectedProvider() string {
	return providers[m.providerIdx]
}

func (m model) startGateway() tea.Cmd {
	provider := m.selectedProvider()
	addr := m.fields[fieldAddr].value
	capDollars, _ := parseFloat(m.fields[fieldCapDollars].value)

	cfg := gateway.Config{
		Provider:        provider,
		ProviderURL:     m.fields[fieldProviderURL].value,
		OpenAIAPIKey:    m.fields[fieldOpenAIKey].value,
		AnthropicAPIKey: m.fields[fieldAnthropicKey].value,
		CapDollars:      capDollars,
	}

	return func() tea.Msg {
		gw, err := gateway.New(cfg)
		if err != nil {
			return errMsg{err}
		}
		srv := &http.Server{Addr: addr, Handler: gw}
		go srv.ListenAndServe() //nolint:errcheck // shutdown-triggered close is expected, surfaced via stopGateway instead
		return startedMsg{srv: srv, metrics: gw.Metrics, addr: addr}
	}
}

func (m model) stopGateway() model {
	if m.srv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = m.srv.Shutdown(ctx)
	}
	m.srv = nil
	m.metrics = nil
	m.screen = screenConfig
	return m
}

func (m model) quit() tea.Cmd {
	if m.srv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = m.srv.Shutdown(ctx)
	}
	return tea.Quit
}

func parseFloat(s string) (float64, error) {
	var f float64
	_, err := fmt.Sscanf(s, "%f", &f)
	return f, err
}

// gold and maroon are Governor's TUI palette: gold for emphasis (titles,
// the focused field) and maroon for structure (borders, unfocused text) —
// chosen to read clearly on a dark terminal background.
var (
	gold   = lipgloss.Color("#FFD700")
	maroon = lipgloss.Color("#B03060")
)

var (
	bannerStyle  = lipgloss.NewStyle().Bold(true).Foreground(gold)
	welcomeStyle = lipgloss.NewStyle().Italic(true).Foreground(gold)
	descStyle    = lipgloss.NewStyle().Foreground(maroon)
	titleStyle   = lipgloss.NewStyle().Bold(true).Foreground(gold)
	focusedStyle = lipgloss.NewStyle().Foreground(gold).Bold(true)
	blurredStyle = lipgloss.NewStyle().Foreground(maroon)
	errorStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#FF5555"))
	helpStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#8B6914"))
	boxStyle     = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(maroon).Padding(1, 2)
)

// asciiArt is Governor's banner, shown once on the config screen. Kept to
// plain ASCII rather than box-drawing/full-block glyphs or emoji: those
// don't measure consistently across every terminal font (many render
// wider than lipgloss's width calculation expects), which throws off the
// box border alignment around it. Plain ASCII can't do that.
const asciiArt = `===== G O V E R N O R =====`

func (m model) View() string {
	if m.screen == screenRunning {
		return m.viewRunning()
	}
	return m.viewConfig()
}

func (m model) viewConfig() string {
	var b strings.Builder
	b.WriteString(bannerStyle.Render(asciiArt))
	b.WriteString("\n\n")
	b.WriteString(welcomeStyle.Render("Welcome to Governor — your LLM spend, under control."))
	b.WriteString("\n")
	b.WriteString(descStyle.Render("Set a provider and a hard budget cap below, then start the gateway to meter and cap streaming LLM spend live."))
	b.WriteString("\n\n")
	b.WriteString(titleStyle.Render("configure"))
	b.WriteString("\n\n")

	line := func(f field, label, value string) {
		style := blurredStyle
		cursor := "  "
		if m.focus == f {
			style = focusedStyle
			cursor = "> "
		}
		b.WriteString(cursor + style.Render(fmt.Sprintf("%-20s %s", label, value)) + "\n")
	}

	line(fieldProvider, "Provider", m.selectedProvider()+"  (←/→ to change)")
	for _, f := range []field{fieldAddr, fieldProviderURL, fieldCapDollars} {
		tf := m.fields[f]
		line(f, tf.label, tf.value)
	}
	for _, f := range []field{fieldOpenAIKey, fieldAnthropicKey} {
		tf := m.fields[f]
		line(f, tf.label, mask(tf.value))
	}

	startStyle := blurredStyle
	cursor := "  "
	if m.focus == fieldStart {
		startStyle = focusedStyle
		cursor = "> "
	}
	b.WriteString("\n" + cursor + startStyle.Render("[ Start gateway ]") + "\n")

	if m.err != nil {
		b.WriteString("\n" + errorStyle.Render("error: "+m.err.Error()) + "\n")
	}

	b.WriteString("\n" + helpStyle.Render("tab/shift+tab: move  ←/→: change provider  enter: start  ctrl+c: quit"))

	return boxStyle.Render(b.String())
}

func (m model) viewRunning() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("governorctl — running"))
	b.WriteString("\n\n")
	b.WriteString(fmt.Sprintf("provider: %s\n", m.selectedProvider()))
	b.WriteString(fmt.Sprintf("listening: %s\n", m.addr))
	b.WriteString(fmt.Sprintf("cap: $%s\n\n", m.fields[fieldCapDollars].value))

	b.WriteString(titleStyle.Render("live usage") + "\n")
	b.WriteString(fmt.Sprintf("preflight denials:   %d\n", m.snap.PreflightDenials))
	b.WriteString(fmt.Sprintf("mid-stream cutoffs:  %d\n", m.snap.MidStreamCutoffs))
	b.WriteString(fmt.Sprintf("streams completed:   %d\n", m.snap.StreamsCompleted))
	b.WriteString(fmt.Sprintf("streams errored:     %d\n", m.snap.StreamsErrored))
	b.WriteString(fmt.Sprintf("reconciliations:     %d\n", m.snap.Reconciliations))
	b.WriteString(fmt.Sprintf("refunds issued:      %d\n", m.snap.RefundsIssued))
	b.WriteString(fmt.Sprintf("drift (micros):      %d\n", m.snap.DriftMicrosTotal))

	b.WriteString("\n" + helpStyle.Render("s: stop  q/ctrl+c: quit"))

	return boxStyle.Render(b.String())
}

func mask(s string) string {
	if s == "" {
		return "(not set)"
	}
	return strings.Repeat("*", len(s))
}
