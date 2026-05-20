package main

import (
	"bufio"
	"fmt"
	"strings"
	"syscall"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/mattn/go-runewidth"
)

type mode int

const (
	modeNormal mode = iota
	modeSearch
)

type renderedLine struct {
	raw             string
	rendered        string // multiline pretty-printed
	renderedCompact string // single-line compact
	compactWidth    int    // visible width of renderedCompact
	logTime         time.Time
	hasTime         bool
	rawTimeVal      string // original value from JSON (unquoted str or raw number)
	rawTimeIsStr    bool   // true = was JSON string, false = JSON number
	isSeparator     bool
}

type model struct {
	reader          *bufio.Reader
	lines           []renderedLine
	viewport        viewport.Model
	searchInput     textinput.Model
	mode            mode
	searchQuery     string
	matchIndex      int
	matchLines      []int
	width           int
	height          int
	atBottom        bool
	stdinDone       bool
	lineMode        bool
	hOffset         int
	maxContentWidth int
	now             time.Time
	pipeCmd         string
}

// newModel builds the model. title overrides the auto-detected source
// command shown on the left of the status bar; empty means auto-detect.
func newModel(r *bufio.Reader, title string) model {
	vp := viewport.New(0, 0)
	ti := textinput.New()
	ti.Placeholder = "search…"
	ti.CharLimit = 200
	if title == "" {
		title = detectPipeCommand()
	}
	return model{
		reader:      r,
		viewport:    vp,
		searchInput: ti,
		atBottom:    true,
		lineMode:    true,
		now:         time.Now(),
		pipeCmd:     title,
	}
}

func (m model) Init() tea.Cmd {
	return tea.Batch(readLine(m.reader), doTick())
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.viewport.Width = msg.Width
		m.viewport.Height = msg.Height - 1
		m.searchInput.Width = msg.Width - 2
		m.viewport.SetContent(m.buildContent())
		if m.atBottom {
			m.viewport.GotoBottom()
		}
		return m, nil

	case lineMsg:
		compact := colorizeJSONCompact(msg.line)
		cw := visWidth(compact)
		logTime, rawTimeVal, rawTimeIsStr, hasTime := parseLogTime(msg.line)
		rl := renderedLine{
			raw:             msg.line,
			rendered:        colorizeJSON(msg.line),
			renderedCompact: compact,
			compactWidth:    cw,
			logTime:         logTime,
			hasTime:         hasTime,
			rawTimeVal:      rawTimeVal,
			rawTimeIsStr:    rawTimeIsStr,
		}
		if cw > m.maxContentWidth {
			m.maxContentWidth = cw
		}
		m.lines = append(m.lines, rl)
		if m.searchQuery != "" {
			m.rebuildMatchLines()
		}
		m.viewport.SetContent(m.buildContent())
		if m.atBottom {
			m.viewport.GotoBottom()
		}
		return m, readLine(m.reader)

	case eofMsg:
		m.stdinDone = true
		return m, nil

	case tickMsg:
		m.now = time.Time(msg)
		m.viewport.SetContent(m.buildContent())
		if m.atBottom {
			m.viewport.GotoBottom()
		}
		return m, doTick()

	case tea.KeyMsg:
		return m.handleKey(msg)

	case tea.MouseMsg:
		wasAtBottom := m.viewport.AtBottom()
		var cmd tea.Cmd
		m.viewport, cmd = m.viewport.Update(msg)
		nowAtBottom := m.viewport.AtBottom()
		if !wasAtBottom && nowAtBottom {
			m.atBottom = true
		} else if wasAtBottom && !nowAtBottom {
			m.atBottom = false
		}
		return m, cmd
	}

	return m, nil
}

func (m model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.mode {

	case modeNormal:
		switch msg.String() {
		case "l":
			m.lineMode = !m.lineMode
			m.hOffset = 0
			m.viewport.SetContent(m.buildContent())
			return m, nil

		case "left":
			if m.lineMode && m.hOffset > 0 {
				m.hOffset -= 8
				if m.hOffset < 0 {
					m.hOffset = 0
				}
				m.viewport.SetContent(m.buildContent())
			}
			return m, nil

		case "right":
			if m.lineMode {
				maxOff := m.maxContentWidth - m.width
				if maxOff > 0 && m.hOffset < maxOff {
					m.hOffset += 8
					if m.hOffset > maxOff {
						m.hOffset = maxOff
					}
					m.viewport.SetContent(m.buildContent())
				}
			}
			return m, nil

		case "home":
			if m.lineMode {
				m.hOffset = 0
				m.viewport.SetContent(m.buildContent())
				return m, nil
			}
			m.viewport.GotoTop()
			m.atBottom = false
			return m, nil

		case "end":
			if m.lineMode {
				maxOff := m.maxContentWidth - m.width
				if maxOff > 0 {
					m.hOffset = maxOff
				}
				m.viewport.SetContent(m.buildContent())
				return m, nil
			}
			m.viewport.GotoBottom()
			m.atBottom = true
			return m, nil

		case "/":
			m.mode = modeSearch
			m.searchInput.Focus()
			m.searchInput.SetValue("")
			m.searchQuery = ""
			m.matchLines = nil
			m.viewport.SetContent(m.buildContent())
			return m, textinput.Blink

		case "n":
			m.nextMatch(+1)
			m.viewport.SetContent(m.buildContent())
			return m, nil

		case "N":
			m.nextMatch(-1)
			m.viewport.SetContent(m.buildContent())
			return m, nil

		case "g":
			m.viewport.GotoTop()
			m.atBottom = false
			return m, nil

		case "G":
			m.viewport.GotoBottom()
			m.atBottom = true
			return m, nil

		case "-":
			m.lines = append(m.lines, renderedLine{isSeparator: true})
			m.viewport.SetContent(m.buildContent())
			if m.atBottom {
				m.viewport.GotoBottom()
			}
			return m, nil

		case "q":
			return m, tea.Quit

		case "ctrl+c":
			// Forward SIGINT to the rest of the pipeline so the producing
			// command terminates with us instead of leaving the shell hanging.
			signalPipelineSiblings(syscall.SIGINT)
			return m, tea.Quit

		default:
			wasAtBottom := m.viewport.AtBottom()
			var cmd tea.Cmd
			m.viewport, cmd = m.viewport.Update(msg)
			nowAtBottom := m.viewport.AtBottom()
			if !wasAtBottom && nowAtBottom {
				m.atBottom = true
			} else if wasAtBottom && !nowAtBottom {
				m.atBottom = false
			}
			return m, cmd
		}

	case modeSearch:
		switch msg.String() {
		case "esc":
			m.mode = modeNormal
			m.searchInput.Blur()
			m.searchQuery = ""
			m.matchLines = nil
			m.viewport.SetContent(m.buildContent())
			return m, nil
		case "enter":
			m.searchQuery = m.searchInput.Value()
			m.mode = modeNormal
			m.searchInput.Blur()
			m.rebuildMatchLines()
			m.viewport.SetContent(m.buildContent())
			if len(m.matchLines) > 0 {
				m.matchIndex = 0
				m.scrollToMatch(m.matchLines[0])
			}
			return m, nil
		default:
			var cmd tea.Cmd
			m.searchInput, cmd = m.searchInput.Update(msg)
			live := m.searchInput.Value()
			if live != m.searchQuery {
				m.searchQuery = live
				m.rebuildMatchLines()
				m.viewport.SetContent(m.buildContent())
			}
			return m, cmd
		}
	}

	return m, nil
}

const (
	ansiBlackBg = "\033[48;2;0;0;0m"
	footBg      = "\033[48;2;150;180;200m" // greyish sky blue
	footFg      = "\033[38;2;0;0;0m"       // black text
	ansiReset   = "\033[0m"
)

func (m model) View() string {
	// Black background only for the viewport area. Re-inject it after every SGR
	// reset so lipgloss token resets don't restore the transparent default.
	vp := m.viewport.View()
	vp = strings.ReplaceAll(vp, "\033[m", "\033[m"+ansiBlackBg)
	vp = strings.ReplaceAll(vp, "\033[0m", "\033[0m"+ansiBlackBg)
	var sb strings.Builder
	for _, line := range strings.Split(vp, "\n") {
		sb.WriteString(ansiBlackBg)
		sb.WriteString(line)
		sb.WriteString(ansiBlackBg + "\033[K" + ansiReset)
		sb.WriteByte('\n')
	}
	sb.WriteString(m.statusBar())
	return sb.String()
}

// statusText is the right-aligned part: mode, scroll state, match count.
func (m model) statusText() string {
	prefix := ""
	if m.lineMode {
		if m.hOffset > 0 {
			prefix = fmt.Sprintf("[line +%d] ", m.hOffset)
		} else {
			prefix = "[line] "
		}
	}
	suffix := ""
	if m.searchQuery != "" {
		suffix = fmt.Sprintf(" [%d/%d matches]", m.matchIndex+1, len(m.matchLines))
	}
	state := "streaming…"
	if m.stdinDone {
		state = "(EOF)"
	} else if !m.atBottom {
		state = "↑ scrolled — G to tail"
	}
	return prefix + state + suffix
}

func (m model) statusBar() string {
	w := m.width
	if w <= 0 {
		if m.mode == modeSearch {
			return "/" + m.searchInput.View()
		}
		return ""
	}

	if m.mode == modeSearch {
		in := "/" + m.searchInput.View()
		// keep the blue bar through the input's own SGR resets
		in = strings.ReplaceAll(in, "\033[0m", "\033[0m"+footBg+footFg)
		pad := w - visWidth(in)
		if pad < 0 {
			pad = 0
		}
		return footBg + footFg + in + strings.Repeat(" ", pad) + ansiReset
	}

	right := m.statusText()
	rw := runewidth.StringWidth(right)
	if rw >= w {
		right = truncCmd(right, w)
		return footBg + footFg + right + ansiReset
	}

	left := truncCmd(m.pipeCmd, w-rw-1)
	gap := w - runewidth.StringWidth(left) - rw
	if gap < 1 {
		gap = 1
	}
	return footBg + footFg + left + strings.Repeat(" ", gap) + right + ansiReset
}

func (m *model) buildContent() string {
	if m.lineMode {
		return m.buildContentLine()
	}
	return m.buildContentMulti()
}

func (m *model) buildContentLine() string {
	n := len(m.lines)
	var sb strings.Builder
	for i, rl := range m.lines {
		if rl.isSeparator {
			sb.WriteString("\033[38;2;224;108;117m\033[48;2;0;0;0m\033[1m")
			sb.WriteString(strings.Repeat("─", m.width))
			sb.WriteString("\033[0m")
			if i < n-1 {
				sb.WriteByte('\n')
			}
			continue
		}
		line := rl.renderedCompact
		if rl.hasTime {
			line = replaceRenderedTime(line, rl.rawTimeVal, rl.rawTimeIsStr, rl.logTime, m.now)
		}
		if m.searchQuery != "" {
			line = highlightSearch(line, rl.raw, m.searchQuery)
		}
		if rl.hasTime {
			alpha := ageAlpha(rl.logTime, m.now)
			line = fadeString(line, alpha)
		}
		line = hClip(line, m.hOffset, m.width)
		sb.WriteString(line)
		sb.WriteString("\033[0m")
		if i < n-1 {
			sb.WriteByte('\n')
		}
	}
	return sb.String()
}

func (m *model) buildContentMulti() string {
	type displayLine struct {
		text      string
		sourceIdx int
	}
	var all []displayLine
	for i, rl := range m.lines {
		if rl.isSeparator {
			all = append(all, displayLine{text: "", sourceIdx: i})
			continue
		}
		for _, p := range strings.Split(rl.rendered, "\n") {
			all = append(all, displayLine{text: p, sourceIdx: i})
		}
	}
	total := len(all)
	var sb strings.Builder
	for i, dl := range all {
		rl := m.lines[dl.sourceIdx]
		if rl.isSeparator {
			sb.WriteString("\033[38;2;224;108;117m\033[48;2;0;0;0m\033[1m")
			sb.WriteString(strings.Repeat("─", m.width))
			sb.WriteString("\033[0m")
			if i < total-1 {
				sb.WriteByte('\n')
			}
			continue
		}
		line := dl.text
		if rl.hasTime {
			line = replaceRenderedTime(line, rl.rawTimeVal, rl.rawTimeIsStr, rl.logTime, m.now)
		}
		if m.searchQuery != "" {
			line = highlightSearch(line, rl.raw, m.searchQuery)
		}
		if rl.hasTime {
			alpha := ageAlpha(rl.logTime, m.now)
			line = fadeString(line, alpha)
		}
		sb.WriteString(line)
		if i < total-1 {
			sb.WriteByte('\n')
		}
	}
	return sb.String()
}

func (m *model) rebuildMatchLines() {
	m.matchLines = m.matchLines[:0]
	lq := strings.ToLower(m.searchQuery)
	if lq == "" {
		return
	}
	for i, rl := range m.lines {
		if strings.Contains(strings.ToLower(rl.raw), lq) {
			m.matchLines = append(m.matchLines, i)
		}
	}
}

func (m *model) nextMatch(dir int) {
	if len(m.matchLines) == 0 {
		return
	}
	m.matchIndex = (m.matchIndex + dir + len(m.matchLines)) % len(m.matchLines)
	m.scrollToMatch(m.matchLines[m.matchIndex])
}

func (m *model) scrollToMatch(sourceIdx int) {
	var displayOffset int
	if m.lineMode {
		displayOffset = sourceIdx
	} else {
		for i := 0; i < sourceIdx && i < len(m.lines); i++ {
			displayOffset += strings.Count(m.lines[i].rendered, "\n") + 1
		}
	}
	target := displayOffset - m.viewport.Height/3
	if target < 0 {
		target = 0
	}
	m.viewport.SetYOffset(target)
	m.atBottom = false
}
