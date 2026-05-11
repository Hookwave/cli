// Package tui renders the interactive listen-mode UI with Bubble Tea.
// Layout: top status bar, scrollable event list, side panel with the
// selected event's headers + body, bottom keybinding hint.
package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/hookwave/hookwave/apps/cli/internal/httpc"
	"github.com/hookwave/hookwave/apps/cli/internal/tunnel"
)

// Options are passed in by the listen command.
type Options struct {
	LocalURL   string
	TunnelOpts tunnel.Options
	ActiveOrg  string
	UserEmail  string
	CLIVersion string
	// API is the authed httpc client used for in-TUI actions like
	// replay. Optional — when nil the action keybinds are disabled.
	API *httpc.Client
	// DashboardURL is the base URL for the web app; used by the `o`
	// keybinding. Empty disables the bind.
	DashboardURL string
}

// Run starts the TUI. Blocks until the user exits or ctx is cancelled.
func Run(ctx context.Context, o Options) error {
	if o.TunnelOpts.OnEvent != nil {
		// We always wire our own OnEvent — refuse to run with a
		// pre-set hook to avoid silently dropping it.
		return errors.New("tui.Run: TunnelOpts.OnEvent must be unset")
	}

	m := newModel(o)
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithContext(ctx))

	// Wire the tunnel's OnEvent callback to push into the program.
	o.TunnelOpts.OnEvent = func(e *tunnel.Event, status int, ms int, err error) {
		p.Send(eventMsg{e: e, status: status, ms: ms, err: err})
	}

	go func() {
		err := tunnel.Run(ctx, o.TunnelOpts)
		if err != nil && !errors.Is(err, context.Canceled) {
			p.Send(tunnelStatusMsg{err: err, fatal: isFatal(err)})
		}
	}()

	_, err := p.Run()
	return err
}

func isFatal(err error) bool {
	// Auth errors are reported by tunnel.Run wrapped; just bail on
	// them. Other errors mean we'll be retrying, so don't kill the UI.
	return strings.Contains(err.Error(), "authentication failed")
}

// --- Bubble Tea model -------------------------------------------------------

type entry struct {
	at     time.Time
	event  *tunnel.Event
	status int
	ms     int
	err    error
}

// inputMode toggles which keybindings the TUI accepts. "list" is the
// default. "filter" diverts keys to the filter input.
type inputMode int

const (
	modeList inputMode = iota
	modeFilter
)

type model struct {
	opts       Options
	entries    []entry
	cursor     int
	width      int
	height     int
	statusLine string
	statusTone lipgloss.Style
	quitting   bool

	mode      inputMode
	filterRaw string // currently being typed
	filter    string // applied filter (lowercased)
}

func newModel(o Options) *model {
	return &model{
		opts:       o,
		statusLine: "connecting…",
	}
}

// statusFlash sets a transient status line and clears it after delay.
type clearStatusMsg struct{}
type replayDoneMsg struct {
	id  string
	err error
}

func (m *model) Init() tea.Cmd { return nil }

type eventMsg struct {
	e      *tunnel.Event
	status int
	ms     int
	err    error
}

type tunnelStatusMsg struct {
	err   error
	fatal bool
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case eventMsg:
		m.entries = append(m.entries, entry{at: time.Now(), event: msg.e, status: msg.status, ms: msg.ms, err: msg.err})
		// Auto-follow the tail unless the user has scrolled up.
		if m.mode == modeList && m.cursor >= len(m.visibleIndices())-2 {
			m.gotoEnd()
		}
		m.statusLine = "connected — receiving events"
	case tunnelStatusMsg:
		if msg.fatal {
			m.statusLine = "✗ " + msg.err.Error()
			m.quitting = true
			return m, tea.Quit
		}
		m.statusLine = "reconnecting… " + msg.err.Error()
	case clearStatusMsg:
		m.statusLine = "connected — receiving events"
	case replayDoneMsg:
		if msg.err != nil {
			m.statusLine = "✗ replay " + shortID(msg.id) + ": " + msg.err.Error()
		} else {
			m.statusLine = "✓ replayed " + shortID(msg.id)
		}
		return m, tea.Tick(2*time.Second, func(time.Time) tea.Msg { return clearStatusMsg{} })
	case tea.KeyMsg:
		if m.mode == modeFilter {
			return m.handleFilterKey(msg)
		}
		return m.handleListKey(msg)
	}
	return m, nil
}

func (m *model) handleListKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	visible := m.visibleIndices()
	// The list shows the *tail* of `visible` capped at the panel's
	// inner height. Cursor must stay within that displayed window
	// so up/down can't try to navigate to rows that are off-screen
	// (which previously caused the "scroll through headers" feel).
	innerHeight := m.height - 4 // 1 header + 1 footer + 2 panel borders
	if innerHeight < 1 {
		innerHeight = 1
	}
	minCursor := 0
	if len(visible) > innerHeight {
		minCursor = len(visible) - innerHeight
	}
	switch msg.String() {
	case "q", "ctrl+c":
		m.quitting = true
		return m, tea.Quit
	case "j", "down":
		if m.cursor < len(visible)-1 {
			m.cursor++
		}
	case "k", "up":
		if m.cursor > minCursor {
			m.cursor--
		}
	case "g":
		m.cursor = minCursor
	case "G":
		m.gotoEnd()
	case "/":
		m.mode = modeFilter
		m.filterRaw = m.filter
	case "esc":
		if m.filter != "" {
			m.filter = ""
			m.statusLine = "filter cleared"
			return m, tea.Tick(1500*time.Millisecond, func(time.Time) tea.Msg { return clearStatusMsg{} })
		}
	case "r":
		return m, m.replaySelected(visible)
	case "c":
		return m, m.copySelectedID(visible)
	case "o":
		return m, m.openSelectedInDashboard(visible)
	}
	return m, nil
}

func (m *model) handleFilterKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEnter:
		m.filter = strings.ToLower(strings.TrimSpace(m.filterRaw))
		m.mode = modeList
		m.cursor = 0
	case tea.KeyEsc:
		m.mode = modeList
		m.filterRaw = ""
	case tea.KeyBackspace:
		if len(m.filterRaw) > 0 {
			m.filterRaw = m.filterRaw[:len(m.filterRaw)-1]
		}
	case tea.KeyRunes:
		m.filterRaw += string(msg.Runes)
	case tea.KeySpace:
		m.filterRaw += " "
	}
	return m, nil
}

// visibleIndices returns the indices of m.entries that pass the filter,
// in display order. With no filter, returns all.
func (m *model) visibleIndices() []int {
	if m.filter == "" {
		out := make([]int, len(m.entries))
		for i := range m.entries {
			out[i] = i
		}
		return out
	}
	out := make([]int, 0, len(m.entries))
	for i, e := range m.entries {
		if matchesFilter(e, m.filter) {
			out = append(out, i)
		}
	}
	return out
}

func matchesFilter(e entry, q string) bool {
	if q == "" {
		return true
	}
	if e.event == nil {
		return false
	}
	hay := strings.ToLower(e.event.Method + " " + e.event.Path + " " + e.event.SourceName)
	return strings.Contains(hay, q)
}

func (m *model) selectedEntry(visible []int) *entry {
	if len(visible) == 0 || m.cursor < 0 || m.cursor >= len(visible) {
		return nil
	}
	idx := visible[m.cursor]
	if idx < 0 || idx >= len(m.entries) {
		return nil
	}
	return &m.entries[idx]
}

func (m *model) gotoEnd() {
	visible := m.visibleIndices()
	if len(visible) > 0 {
		m.cursor = len(visible) - 1
	}
}

// replaySelected fires off a POST /v1/events/replay for the currently
// selected event. Returns a tea.Cmd so Bubble Tea drives it on the
// background loop without blocking input.
func (m *model) replaySelected(visible []int) tea.Cmd {
	sel := m.selectedEntry(visible)
	if sel == nil || sel.event == nil {
		return nil
	}
	if m.opts.API == nil {
		m.statusLine = "replay disabled (CLI not authenticated for actions)"
		return tea.Tick(2*time.Second, func(time.Time) tea.Msg { return clearStatusMsg{} })
	}
	id := sel.event.ID
	api := m.opts.API
	m.statusLine = "↻ replaying " + shortID(id) + "…"
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		err := api.Post(ctx, "/v1/events/replay", map[string]any{"eventIds": []string{id}}, nil)
		return replayDoneMsg{id: id, err: err}
	}
}

func (m *model) copySelectedID(visible []int) tea.Cmd {
	sel := m.selectedEntry(visible)
	if sel == nil || sel.event == nil {
		return nil
	}
	if err := copyToClipboard(sel.event.ID); err != nil {
		m.statusLine = "✗ copy: " + err.Error()
	} else {
		m.statusLine = "✓ copied " + shortID(sel.event.ID)
	}
	return tea.Tick(1500*time.Millisecond, func(time.Time) tea.Msg { return clearStatusMsg{} })
}

func (m *model) openSelectedInDashboard(visible []int) tea.Cmd {
	sel := m.selectedEntry(visible)
	if sel == nil || sel.event == nil {
		return nil
	}
	if m.opts.DashboardURL == "" {
		m.statusLine = "open: dashboard URL unknown"
		return tea.Tick(1500*time.Millisecond, func(time.Time) tea.Msg { return clearStatusMsg{} })
	}
	url := strings.TrimRight(m.opts.DashboardURL, "/") + "/dashboard/events/" + sel.event.ID
	if err := openInBrowser(url); err != nil {
		m.statusLine = "✗ open: " + err.Error()
	} else {
		m.statusLine = "→ opened " + shortID(sel.event.ID)
	}
	return tea.Tick(1500*time.Millisecond, func(time.Time) tea.Msg { return clearStatusMsg{} })
}

// --- View -------------------------------------------------------------------

var (
	headerStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#a78bfa"))
	mutedStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	successFg   = lipgloss.NewStyle().Foreground(lipgloss.Color("#34d399"))
	warnFg      = lipgloss.NewStyle().Foreground(lipgloss.Color("#fbbf24"))
	errorFg     = lipgloss.NewStyle().Foreground(lipgloss.Color("#f87171"))
	selectedRow = lipgloss.NewStyle().Background(lipgloss.Color("#3f3f46")).Foreground(lipgloss.Color("231"))
	panelBorder = lipgloss.NewStyle().Border(lipgloss.NormalBorder()).BorderForeground(lipgloss.Color("238"))
)

func (m *model) View() string {
	if m.width == 0 || m.height == 0 {
		return "" // pre-resize, suppress flicker
	}
	header := m.renderHeader()
	footer := m.renderFooter()

	// Body fills everything between the 1-line header and 1-line
	// footer. JoinVertical inserts one '\n' between sections, so the
	// total rendered height = 1 + bodyHeight + 1 = m.height.
	bodyHeight := m.height - 2
	if bodyHeight < 4 {
		bodyHeight = 4
	}
	// Allow 1 char for the right-panel left border so the two
	// joined panels line up exactly to m.width with no slack.
	listWidth := (m.width * 6) / 10
	detailWidth := m.width - listWidth
	if detailWidth < 10 {
		detailWidth = 10
	}

	list := m.renderList(listWidth, bodyHeight)
	detail := m.renderDetail(detailWidth, bodyHeight)

	body := lipgloss.JoinHorizontal(lipgloss.Top, list, detail)
	return lipgloss.JoinVertical(lipgloss.Left, header, body, footer)
}

func (m *model) renderHeader() string {
	left := headerStyle.Render(" hookwave listen ") +
		mutedStyle.Render(fmt.Sprintf(" → %s ", m.opts.LocalURL))
	right := mutedStyle.Render(fmt.Sprintf(" %s • org %s ", emptyDash(m.opts.UserEmail), shortID(m.opts.ActiveOrg)))
	gap := m.width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		gap = 1
	}
	return left + strings.Repeat(" ", gap) + right
}

func (m *model) renderFooter() string {
	if m.mode == modeFilter {
		// Replace the keybinding line with the filter input.
		prompt := "  / "
		input := m.filterRaw + "▌" // simple block cursor
		hint := mutedStyle.Render("  enter apply • esc cancel ")
		left := prompt + input
		gap := m.width - lipgloss.Width(left) - lipgloss.Width(hint) - 2
		if gap < 1 {
			gap = 1
		}
		return left + strings.Repeat(" ", gap) + hint
	}
	keys := mutedStyle.Render(" j/k scroll • g/G top/bottom • r replay • / filter • c copy id • o open • q quit ")
	gap := m.width - lipgloss.Width(keys) - lipgloss.Width(m.statusLine) - 2
	if gap < 1 {
		gap = 1
	}
	return keys + strings.Repeat(" ", gap) + mutedStyle.Render(m.statusLine) + " "
}

func (m *model) renderList(width, height int) string {
	visible := m.visibleIndices()
	if len(m.entries) == 0 {
		return panelBorder.Width(width).Height(height).Render(
			mutedStyle.Render("\n  (waiting for events — trigger a webhook from your provider)\n"),
		)
	}
	if len(visible) == 0 {
		return panelBorder.Width(width).Height(height).Render(
			mutedStyle.Render(fmt.Sprintf("\n  (no events match \"%s\" — esc to clear)\n", m.filter)),
		)
	}
	innerWidth := width - 2
	innerHeight := height - 2
	if innerHeight < 1 {
		innerHeight = 1
	}
	var b strings.Builder
	// Show the *tail* of `visible` capped at the panel's inner
	// height. No mid-list windowing — the list is stable and just
	// follows the latest events. Cursor is clamped to the visible
	// slice in the key handler so it can't navigate to off-screen
	// rows.
	start := 0
	if len(visible) > innerHeight {
		start = len(visible) - innerHeight
	}
	for i := start; i < len(visible); i++ {
		row := formatRow(m.entries[visible[i]], innerWidth)
		visibleW := lipgloss.Width(row)
		if visibleW < innerWidth {
			row += strings.Repeat(" ", innerWidth-visibleW)
		}
		if i == m.cursor {
			row = selectedRow.Render(row)
		}
		b.WriteString(row)
		b.WriteByte('\n')
	}
	return panelBorder.Width(width).Height(height).Render(b.String())
}

func formatRow(e entry, width int) string {
	t := e.at.Format("15:04:05")
	method := pad(e.event.Method, 5)
	path := e.event.Path
	if path == "" {
		path = "/"
	}

	var status string
	switch {
	case e.err != nil:
		status = errorFg.Render("✗ ERR")
	case e.status >= 200 && e.status < 300:
		status = successFg.Render(fmt.Sprintf("%d", e.status))
	case e.status >= 300 && e.status < 400:
		status = warnFg.Render(fmt.Sprintf("%d", e.status))
	default:
		status = errorFg.Render(fmt.Sprintf("%d", e.status))
	}
	ms := mutedStyle.Render(fmt.Sprintf("%4dms", e.ms))
	src := mutedStyle.Render(truncate(e.event.SourceName, 14))

	line := fmt.Sprintf("%s  %s %s  %s  %s  %s",
		mutedStyle.Render(t), method, status, ms, src, truncate(path, max(0, width-40)))
	return line
}

func (m *model) renderDetail(width, height int) string {
	visible := m.visibleIndices()
	if len(visible) == 0 || m.cursor >= len(visible) {
		return panelBorder.Width(width).Height(height).Render(
			mutedStyle.Render("\n  no event selected\n"),
		)
	}
	e := m.entries[visible[m.cursor]]
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n", headerStyle.Render(e.event.ID))
	fmt.Fprintf(&b, "%s %s\n\n", e.event.Method, e.event.Path)
	if e.err != nil {
		fmt.Fprintf(&b, "%s %s\n\n", errorFg.Render("error:"), e.err.Error())
	} else {
		fmt.Fprintf(&b, "%s %d (%dms)\n\n", mutedStyle.Render("response:"), e.status, e.ms)
	}
	fmt.Fprintln(&b, mutedStyle.Render("headers"))
	for k, v := range e.event.Headers {
		fmt.Fprintf(&b, "  %s: %s\n", k, truncate(v, max(0, width-len(k)-6)))
	}
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, mutedStyle.Render("body"))
	body := e.event.Body
	if len(body) > 4000 {
		body = body[:4000] + "\n…(truncated)"
	}
	fmt.Fprintln(&b, body)
	return panelBorder.Width(width).Height(height).Render(b.String())
}

// --- helpers ----------------------------------------------------------------

func pad(s string, n int) string {
	if len(s) >= n {
		return s
	}
	return s + strings.Repeat(" ", n-len(s))
}

func truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func emptyDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func shortID(s string) string {
	if len(s) <= 8 {
		return s
	}
	return s[:8]
}
