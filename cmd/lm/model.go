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
	modeHelp
)

type viewMode int

const (
	viewCompressed viewMode = iota // key=value, time as first column, no braces (default)
	viewLine                       // colorized JSON, single line
	viewMulti                      // colorized JSON, pretty-printed
)

type renderedLine struct {
	raw                string
	rendered           string // multiline pretty-printed JSON
	renderedCompact    string // single-line compact JSON
	renderedCompressed string // key=value body (time stripped, no braces)
	compactWidth       int    // visible width of renderedCompact
	compressedWidth    int    // visible width of time col + space + body
	logTime            time.Time
	hasTime            bool
	rawTimeVal         string // original value from JSON (unquoted str or raw number)
	rawTimeIsStr       bool   // true = was JSON string, false = JSON number
	isSeparator        bool
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
	atBottom           bool
	stdinDone          bool
	viewMode           viewMode
	lastJSONView       viewMode // remembered so `j` returns to whichever JSON view was last used
	hOffset            int
	maxContentWidth    int // for viewLine
	maxCompressedWidth int // for viewCompressed
	now                time.Time
	pipeCmd            string
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
		reader:       r,
		viewport:     vp,
		searchInput:  ti,
		atBottom:     true,
		viewMode:     viewCompressed,
		lastJSONView: viewLine,
		now:          time.Now(),
		pipeCmd:      title,
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
		compressed := colorizeCompressed(msg.line)
		cw := visWidth(compact)
		zw := timeColWidth + 1 + visWidth(compressed)
		logTime, rawTimeVal, rawTimeIsStr, hasTime := parseLogTime(msg.line)
		rl := renderedLine{
			raw:                msg.line,
			rendered:           colorizeJSON(msg.line),
			renderedCompact:    compact,
			renderedCompressed: compressed,
			compactWidth:       cw,
			compressedWidth:    zw,
			logTime:            logTime,
			hasTime:            hasTime,
			rawTimeVal:         rawTimeVal,
			rawTimeIsStr:       rawTimeIsStr,
		}
		if cw > m.maxContentWidth {
			m.maxContentWidth = cw
		}
		if zw > m.maxCompressedWidth {
			m.maxCompressedWidth = zw
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
		case "j":
			// Toggle between compressed and the JSON view (returns to whichever
			// JSON sub-view was last used).
			if m.viewMode == viewCompressed {
				m.viewMode = m.lastJSONView
			} else {
				m.lastJSONView = m.viewMode
				m.viewMode = viewCompressed
			}
			m.hOffset = 0
			m.viewport.SetContent(m.buildContent())
			return m, nil

		case "l":
			// Cycle JSON sub-views; from compressed jump straight to line.
			switch m.viewMode {
			case viewLine:
				m.viewMode = viewMulti
			case viewMulti:
				m.viewMode = viewLine
			case viewCompressed:
				m.viewMode = viewLine
			}
			m.lastJSONView = m.viewMode
			m.hOffset = 0
			m.viewport.SetContent(m.buildContent())
			return m, nil

		case "left":
			if m.viewMode != viewMulti && m.hOffset > 0 {
				m.hOffset -= 8
				if m.hOffset < 0 {
					m.hOffset = 0
				}
				m.viewport.SetContent(m.buildContent())
			}
			return m, nil

		case "right":
			if m.viewMode != viewMulti {
				maxOff := m.currentMaxWidth() - m.width
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
			if m.viewMode != viewMulti {
				m.hOffset = 0
				m.viewport.SetContent(m.buildContent())
				return m, nil
			}
			m.viewport.GotoTop()
			m.atBottom = false
			return m, nil

		case "end":
			if m.viewMode != viewMulti {
				maxOff := m.currentMaxWidth() - m.width
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

		case "h", "?":
			m.mode = modeHelp
			return m, nil

		case "q", "ctrl+c":
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

	case modeHelp:
		// Any key dismisses the help overlay.
		m.mode = modeNormal
		return m, nil
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
	var body string
	if m.mode == modeHelp {
		body = helpView(m.width, m.viewport.Height)
	} else {
		body = m.viewport.View()
	}
	// Black background for the body. Re-inject it after every SGR reset so
	// lipgloss token resets don't restore the transparent default.
	body = strings.ReplaceAll(body, "\033[m", "\033[m"+ansiBlackBg)
	body = strings.ReplaceAll(body, "\033[0m", "\033[0m"+ansiBlackBg)
	var sb strings.Builder
	for _, line := range strings.Split(body, "\n") {
		sb.WriteString(ansiBlackBg)
		sb.WriteString(line)
		sb.WriteString(ansiBlackBg + "\033[K" + ansiReset)
		sb.WriteByte('\n')
	}
	sb.WriteString(m.statusBar())
	return sb.String()
}

// currentMaxWidth returns the max content width relevant to the current view
// (used by horizontal scroll bounds).
func (m *model) currentMaxWidth() int {
	if m.viewMode == viewCompressed {
		return m.maxCompressedWidth
	}
	return m.maxContentWidth
}

// statusText is the right-aligned part: mode, scroll state, match count.
func (m model) statusText() string {
	var label string
	switch m.viewMode {
	case viewCompressed:
		label = "compressed"
	case viewLine:
		label = "line"
	case viewMulti:
		label = "multi"
	}
	prefix := ""
	if m.viewMode != viewMulti && m.hOffset > 0 {
		prefix = fmt.Sprintf("[%s +%d] ", label, m.hOffset)
	} else {
		prefix = fmt.Sprintf("[%s] ", label)
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
	switch m.viewMode {
	case viewCompressed:
		return m.buildContentCompressed()
	case viewLine:
		return m.buildContentLine()
	default:
		return m.buildContentMulti()
	}
}

func (m *model) buildContentCompressed() string {
	n := len(m.lines)
	timePad := strings.Repeat(" ", timeColWidth)
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
		// Time column (fixed width).
		var tcol string
		if rl.hasTime {
			tcol = styleTime.Render(relTimeStrFixed(m.now.Sub(rl.logTime)))
		} else {
			tcol = timePad
		}
		line := tcol + " " + rl.renderedCompressed
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
	if m.viewMode == viewMulti {
		// Each log expands to multiple display lines.
		for i := 0; i < sourceIdx && i < len(m.lines); i++ {
			if m.lines[i].isSeparator {
				displayOffset++
				continue
			}
			displayOffset += strings.Count(m.lines[i].rendered, "\n") + 1
		}
	} else {
		displayOffset = sourceIdx
	}
	target := displayOffset - m.viewport.Height/3
	if target < 0 {
		target = 0
	}
	m.viewport.SetYOffset(target)
	m.atBottom = false
}
