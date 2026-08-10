package main

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func TestUpdateKey_TabCyclesFocusThroughAllFields(t *testing.T) {
	m := initialModel()
	start := m.focus
	for i := 0; i < int(fieldCount); i++ {
		next, _ := m.updateKey(tea.KeyMsg{Type: tea.KeyTab})
		m = next.(model)
	}
	if m.focus != start {
		t.Fatalf("after fieldCount tabs, focus = %v, want back to start %v", m.focus, start)
	}
}

func TestUpdateKey_LeftRightCyclesProviderOnlyWhenFocused(t *testing.T) {
	m := initialModel()
	m.focus = fieldProvider
	start := m.providerIdx

	next, _ := m.updateKey(tea.KeyMsg{Type: tea.KeyRight})
	m = next.(model)
	if m.providerIdx == start {
		t.Fatal("providerIdx did not change on right-arrow while fieldProvider is focused")
	}

	// Focused elsewhere, left/right should do nothing to providerIdx.
	m.focus = fieldAddr
	before := m.providerIdx
	next, _ = m.updateKey(tea.KeyMsg{Type: tea.KeyRight})
	m = next.(model)
	if m.providerIdx != before {
		t.Fatal("providerIdx changed on right-arrow while a text field (not fieldProvider) was focused")
	}
}

func TestUpdateKey_TypingAppendsToFocusedTextField(t *testing.T) {
	m := initialModel()
	m.focus = fieldOpenAIKey
	m.fields[fieldOpenAIKey].value = ""

	for _, r := range "sk-abc" {
		next, _ := m.updateKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = next.(model)
	}
	if got := m.fields[fieldOpenAIKey].value; got != "sk-abc" {
		t.Fatalf("OpenAI key field = %q, want sk-abc", got)
	}

	next, _ := m.updateKey(tea.KeyMsg{Type: tea.KeyBackspace})
	m = next.(model)
	if got := m.fields[fieldOpenAIKey].value; got != "sk-ab" {
		t.Fatalf("after backspace, field = %q, want sk-ab", got)
	}
}

func TestUpdateKey_TypingOnProviderFieldIsIgnored(t *testing.T) {
	m := initialModel()
	m.focus = fieldProvider

	next, _ := m.updateKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	m = next.(model)
	// fieldProvider has no entry in m.fields, so this must not panic and
	// must leave every text field untouched.
	for f, tf := range m.fields {
		if strings.Contains(tf.value, "x") {
			t.Fatalf("typing while fieldProvider focused leaked into field %v: %q", f, tf.value)
		}
	}
}

func TestUpdate_StartedMsgSwitchesToRunningScreenAndSchedulesTick(t *testing.T) {
	m := initialModel()
	next, cmd := m.Update(startedMsg{addr: ":9999"})
	m = next.(model)

	if m.screen != screenRunning {
		t.Fatalf("screen = %v, want screenRunning", m.screen)
	}
	if m.addr != ":9999" {
		t.Fatalf("addr = %q, want :9999", m.addr)
	}
	if cmd == nil {
		t.Fatal("expected a non-nil tea.Cmd (the tick) after starting")
	}
}

func TestUpdate_ErrMsgSetsErrWithoutChangingScreen(t *testing.T) {
	m := initialModel()
	wantErr := errMsg{err: errBoom}
	next, _ := m.Update(wantErr)
	m = next.(model)

	if m.err == nil {
		t.Fatal("expected m.err to be set")
	}
	if m.screen != screenConfig {
		t.Fatalf("screen = %v, want screenConfig (an error shouldn't switch screens)", m.screen)
	}
}

func TestUpdateKey_SOnRunningScreenReturnsToConfig(t *testing.T) {
	m := initialModel()
	m.screen = screenRunning
	next, _ := m.updateKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	m = next.(model)
	if m.screen != screenConfig {
		t.Fatalf("screen = %v, want screenConfig after 's'", m.screen)
	}
}

func TestView_DoesNotPanicOnEitherScreen(t *testing.T) {
	m := initialModel()
	if out := m.View(); out == "" {
		t.Fatal("View() on screenConfig returned empty string")
	}
	m.screen = screenRunning
	m.snap.PreflightDenials = 3
	if out := m.View(); out == "" {
		t.Fatal("View() on screenRunning returned empty string")
	}
}

// errBoom is a fixed sentinel so TestUpdate_ErrMsgSetsErrWithoutChangingScreen
// doesn't need to fabricate a real gateway.New failure.
var errBoom = boomError{}

type boomError struct{}

func (boomError) Error() string { return "boom" }

func TestTick_ReturnsATickMsgAfterAboutOneSecond(t *testing.T) {
	cmd := tick()
	if cmd == nil {
		t.Fatal("tick() returned a nil command")
	}
	start := time.Now()
	msg := cmd()
	if _, ok := msg.(tickMsg); !ok {
		t.Fatalf("tick() command produced %T, want tickMsg", msg)
	}
	if elapsed := time.Since(start); elapsed < 900*time.Millisecond {
		t.Fatalf("tick fired after %v, want close to 1s", elapsed)
	}
}
