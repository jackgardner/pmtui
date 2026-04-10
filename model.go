package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
)

type procsMsg struct {
	procs []PM2Process
	err   error
}

type logsMsg struct {
	name string // empty string = all-logs mode
	logs string
}

type healthMsg struct {
	idx     int
	healthy bool
	hcPort  string
	hcDebug string
}

type splitLogsMsg struct {
	name string
	logs string
}

type actionMsg struct {
	name string
	verb string
	err  error
}

type tickMsg time.Time

type viewMode int

const (
	modeSingle viewMode = iota // show one service + its logs
	modeAll                    // show service list + combined logs from all services
)

type pane int

const (
	listFocus pane = iota
	logsFocus
)

const (
	listOuterW  = 40 // left panel rendered width including border
	listInnerW  = listOuterW - 2
	detailLines = 10 // fixed lines used by process detail section
)

type model struct {
	procs       []PM2Process
	selIdx      int
	listOffset  int
	rawLogs     string
	vp          viewport.Model
	w, h        int
	focus       pane
	mode        viewMode
	autoScroll  bool
	loading     bool
	debug       bool
	showService bool
	showTS      bool
	showStderr  bool
	err         error
	status      string
	statusTTL   int
	searchQuery  string
	searchMode   bool
	splitActive  bool
	splitIdx     int
	rawLogs2     string
	vp2          viewport.Model
	splitPane    int // 0=left, 1=right
	splitZoomed  bool
	zoomed       bool
	autoScroll2  bool
}

func newModel() model {
	return model{
		loading:     true,
		autoScroll:  true,
		autoScroll2: true,
		showService: true,
		showTS:      true,
		showStderr:  false,
		mode:        modeSingle,
		vp:          viewport.New(80, 20),
		vp2:         viewport.New(80, 20),
	}
}

func (m model) panelH() int {
	h := m.h - 2
	if h < 6 {
		return 6
	}
	return h
}

func (m model) panelInnerH() int { return m.panelH() - 2 }

func (m model) vpWidth() int {
	var w int
	if m.zoomed {
		w = m.w
	} else {
		w = m.w - listOuterW - 2
	}
	if w < 10 {
		return 10
	}
	return w
}

func (m model) vpHeight() int {
	h := m.panelInnerH() - 2
	if h < 1 {
		return 1
	}
	return h
}

func (m model) listItemsVisible() int {
	n := m.panelInnerH() - detailLines - 1 - 2
	if n < 1 {
		return 1
	}
	return n
}

func (m model) Init() tea.Cmd {
	return tea.Batch(cmdLoadProcs(), cmdTick())
}

func cmdLoadProcs() tea.Cmd {
	return func() tea.Msg {
		procs, err := pm2List()
		return procsMsg{procs: procs, err: err}
	}
}

// applyLogs processes rawLogs and pushes the result into the viewport.
func (m *model) applyLogs() {
	content := processLogs(m.rawLogs, m.showService, m.showTS, m.showStderr)
	if m.searchQuery != "" {
		content = filterLines(content, m.searchQuery)
	}
	m.vp.SetContent(content)
}

// matchesSearch reports whether procs[i].Name matches the current search query.
func (m *model) matchesSearch(i int) bool {
	if m.searchQuery == "" {
		return true
	}
	return strings.Contains(strings.ToLower(m.procs[i].Name), strings.ToLower(m.searchQuery))
}

func (m *model) applySplitLogs() {
	content := processLogs(m.rawLogs2, m.showService, m.showTS, m.showStderr)
	m.vp2.SetContent(content)
}

func cmdLoadSplitLogs(name string) tea.Cmd {
	return func() tea.Msg {
		return splitLogsMsg{name: name, logs: pm2Logs(name)}
	}
}

func cmdLoadLogs(name string) tea.Cmd {
	return func() tea.Msg {
		return logsMsg{name: name, logs: pm2Logs(name)}
	}
}

func cmdLoadAllLogs() tea.Cmd {
	return func() tea.Msg {
		return logsMsg{name: "", logs: pm2AllLogs()}
	}
}

func cmdHealth(idx int, p PM2Process) tea.Cmd {
	return func() tea.Msg {
		healthy, hcPort, hcDebug := pm2HealthCheck(p)
		return healthMsg{idx: idx, healthy: healthy, hcPort: hcPort, hcDebug: hcDebug}
	}
}

func cmdAction(verb, name string, fn func(string) error) tea.Cmd {
	return func() tea.Msg {
		return actionMsg{verb: verb, name: name, err: fn(name)}
	}
}

func cmdTick() tea.Cmd {
	return tea.Tick(5*time.Second, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		m.vp.Width = m.vpWidth()
		m.vp.Height = m.vpHeight()
		if m.rawLogs != "" {
			m.applyLogs()
			if m.autoScroll {
				m.vp.GotoBottom()
			}
		}
		if m.splitActive && m.rawLogs2 != "" {
			m.applySplitLogs()
			if m.autoScroll2 {
				m.vp2.GotoBottom()
			}
		}

	case procsMsg:
		m.loading = false
		if msg.err != nil {
			m.err = msg.err
			return m, cmdTick()
		}
		m.err = nil

		type healthState struct {
			pid         int
			ok, known   bool
			hcDebug     string
		}
		saved := make(map[string]healthState)
		for _, p := range m.procs {
			saved[p.Name] = healthState{p.PID, p.Healthy, p.HealthKnown, p.HCDebug}
		}
		m.procs = msg.procs
		for i, p := range m.procs {
			if s, ok := saved[p.Name]; ok && s.pid == p.PID {
				// Same PID — preserve cached health state.
				m.procs[i].Healthy = s.ok
				m.procs[i].HealthKnown = s.known
				m.procs[i].HCDebug = s.hcDebug
			}
			// If PID changed (restart), HealthKnown stays false → re-checked below.
		}
		if m.selIdx >= len(m.procs) && len(m.procs) > 0 {
			m.selIdx = len(m.procs) - 1
		}
		if m.splitIdx >= len(m.procs) && len(m.procs) > 0 {
			m.splitIdx = len(m.procs) - 1
		}

		for i, p := range m.procs {
			if !p.HealthKnown {
				cmds = append(cmds, cmdHealth(i, p))
			}
		}

		if len(m.procs) > 0 {
			if m.mode == modeAll {
				cmds = append(cmds, cmdLoadAllLogs())
			} else {
				cmds = append(cmds, cmdLoadLogs(m.procs[m.selIdx].Name))
			}
			if m.splitActive && m.splitIdx < len(m.procs) {
				cmds = append(cmds, cmdLoadSplitLogs(m.procs[m.splitIdx].Name))
			}
		}

	case logsMsg:
		isAllMode := msg.name == ""
		isCurrentProc := len(m.procs) > 0 && msg.name == m.procs[m.selIdx].Name
		if isAllMode || isCurrentProc {
			m.rawLogs = msg.logs
			m.applyLogs()
			if m.autoScroll {
				m.vp.GotoBottom()
			}
		}

	case splitLogsMsg:
		if m.splitActive && len(m.procs) > 0 && msg.name == m.procs[m.splitIdx].Name {
			m.rawLogs2 = msg.logs
			m.applySplitLogs()
			if m.autoScroll2 {
				m.vp2.GotoBottom()
			}
		}

	case healthMsg:
		if msg.idx < len(m.procs) {
			m.procs[msg.idx].Healthy = msg.healthy
			m.procs[msg.idx].HealthKnown = true
			m.procs[msg.idx].HCPort = msg.hcPort
			m.procs[msg.idx].HCDebug = msg.hcDebug
		}

	case actionMsg:
		if msg.err != nil {
			m.status = fmt.Sprintf("✕ error: %v", msg.err)
		} else {
			m.status = fmt.Sprintf("✓ %s %sed", msg.name, msg.verb)
		}
		m.statusTTL = 5
		cmds = append(cmds, cmdLoadProcs())

	case tickMsg:
		cmds = append(cmds, cmdLoadProcs(), cmdTick())
		if m.statusTTL > 0 {
			m.statusTTL--
			if m.statusTTL == 0 {
				m.status = ""
			}
		}

	case tea.KeyMsg:
		if cmd := m.handleKey(msg); cmd != nil {
			cmds = append(cmds, cmd)
		}
	}

	return m, tea.Batch(cmds...)
}

func (m *model) handleKey(msg tea.KeyMsg) tea.Cmd {
	if m.searchMode {
		return m.handleSearchKey(msg)
	}
	if m.splitActive {
		return m.handleSplitKey(msg)
	}
	switch m.focus {
	case listFocus:
		return m.handleListKey(msg)
	case logsFocus:
		return m.handleLogsKey(msg)
	}
	return nil
}

func (m *model) handleSearchKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.Type {
	case tea.KeyEsc:
		m.searchMode = false
		m.searchQuery = ""
		m.applyLogs()
	case tea.KeyEnter:
		m.searchMode = false
	case tea.KeyBackspace, tea.KeyCtrlH:
		r := []rune(m.searchQuery)
		if len(r) > 0 {
			m.searchQuery = string(r[:len(r)-1])
			if m.focus == logsFocus {
				m.applyLogs()
			}
		}
	case tea.KeyCtrlC:
		return tea.Quit
	case tea.KeyRunes:
		m.searchQuery += string(msg.Runes)
		if m.focus == logsFocus {
			m.applyLogs()
		}
	}
	return nil
}

func (m *model) handleSplitKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "ctrl+c":
		return tea.Quit
	case "q", "esc", "V":
		m.splitActive = false
		m.splitZoomed = false
		m.focus = listFocus
		m.vp.Width = m.vpWidth()
		m.vp.Height = m.vpHeight()
		m.applyLogs()

	case "tab":
		m.splitPane = 1 - m.splitPane

	case "z":
		m.splitZoomed = !m.splitZoomed

	case "shift+up":
		if m.splitPane == 0 {
			if m.selIdx > 0 {
				m.selIdx--
				m.autoScroll = true
				return cmdLoadLogs(m.procs[m.selIdx].Name)
			}
		} else {
			if m.splitIdx > 0 {
				m.splitIdx--
				m.autoScroll2 = true
				return cmdLoadSplitLogs(m.procs[m.splitIdx].Name)
			}
		}

	case "shift+down":
		if m.splitPane == 0 {
			if m.selIdx < len(m.procs)-1 {
				m.selIdx++
				m.autoScroll = true
				return cmdLoadLogs(m.procs[m.selIdx].Name)
			}
		} else {
			if m.splitIdx < len(m.procs)-1 {
				m.splitIdx++
				m.autoScroll2 = true
				return cmdLoadSplitLogs(m.procs[m.splitIdx].Name)
			}
		}

	case "G":
		if m.splitPane == 0 {
			m.autoScroll = true
			m.vp.GotoBottom()
		} else {
			m.autoScroll2 = true
			m.vp2.GotoBottom()
		}

	case "g":
		if m.splitPane == 0 {
			m.autoScroll = false
			m.vp.GotoTop()
		} else {
			m.autoScroll2 = false
			m.vp2.GotoTop()
		}

	case "r":
		if len(m.procs) > 0 {
			idx := m.selIdx
			if m.splitPane == 1 {
				idx = m.splitIdx
			}
			name := m.procs[idx].Name
			m.status = "restarting " + name + "..."
			return cmdAction("restart", name, pm2Restart)
		}

	case "s":
		if len(m.procs) > 0 {
			idx := m.selIdx
			if m.splitPane == 1 {
				idx = m.splitIdx
			}
			p := m.procs[idx]
			if p.PM2Env.Status == "online" {
				m.status = "stopping " + p.Name + "..."
				return cmdAction("stop", p.Name, pm2Stop)
			} else {
				m.status = "starting " + p.Name + "..."
				return cmdAction("start", p.Name, pm2Start)
			}
		}

	case "n":
		m.showService = !m.showService
		m.applyLogs()
		m.applySplitLogs()

	case "t":
		m.showTS = !m.showTS
		m.applyLogs()
		m.applySplitLogs()

	case "e":
		m.showStderr = !m.showStderr
		m.applyLogs()
		m.applySplitLogs()
		if m.autoScroll {
			m.vp.GotoBottom()
		}
		if m.autoScroll2 {
			m.vp2.GotoBottom()
		}

	default:
		if m.splitPane == 0 {
			var cmd tea.Cmd
			m.vp, cmd = m.vp.Update(msg)
			if !m.vp.AtBottom() {
				m.autoScroll = false
			}
			return cmd
		}
		var cmd tea.Cmd
		m.vp2, cmd = m.vp2.Update(msg)
		if !m.vp2.AtBottom() {
			m.autoScroll2 = false
		}
		return cmd
	}
	return nil
}

func (m *model) handleListKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "ctrl+c":
		return tea.Quit
	case "q":
		if m.searchQuery != "" {
			m.searchQuery = ""
			return nil
		}
		return tea.Quit

	case "up", "k":
		for i := m.selIdx - 1; i >= 0; i-- {
			if m.matchesSearch(i) {
				m.selIdx = i
				m.autoScroll = true
				m.adjustOffset()
				if len(m.procs) > 0 && m.mode == modeSingle {
					return cmdLoadLogs(m.procs[m.selIdx].Name)
				}
				break
			}
		}

	case "down", "j":
		for i := m.selIdx + 1; i < len(m.procs); i++ {
			if m.matchesSearch(i) {
				m.selIdx = i
				m.autoScroll = true
				m.adjustOffset()
				if len(m.procs) > 0 && m.mode == modeSingle {
					return cmdLoadLogs(m.procs[m.selIdx].Name)
				}
				break
			}
		}

	case "r":
		if len(m.procs) > 0 {
			name := m.procs[m.selIdx].Name
			m.status = "restarting " + name + "..."
			return cmdAction("restart", name, pm2Restart)
		}

	case "s":
		if len(m.procs) > 0 {
			p := m.procs[m.selIdx]
			if p.PM2Env.Status == "online" {
				m.status = "stopping " + p.Name + "..."
				return cmdAction("stop", p.Name, pm2Stop)
			} else {
				m.status = "starting " + p.Name + "..."
				return cmdAction("start", p.Name, pm2Start)
			}
		}

	case "a":
		return m.toggleAllLogs()

	case "V":
		return m.enterSplit()

	case "/":
		m.searchMode = true

	case "tab", "l", "enter":
		m.focus = logsFocus
		m.searchMode = false
		m.searchQuery = ""
		m.applyLogs()
		m.vp.Width = m.vpWidth()
		m.vp.Height = m.vpHeight()

	case "R":
		m.loading = true
		return cmdLoadProcs()

	case "n":
		m.showService = !m.showService
		m.applyLogs()

	case "t":
		m.showTS = !m.showTS
		m.applyLogs()

	case "e":
		m.showStderr = !m.showStderr
		m.applyLogs()
		if m.autoScroll {
			m.vp.GotoBottom()
		}

	case "?":
		m.debug = !m.debug
	}
	return nil
}

func (m *model) handleLogsKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "ctrl+c":
		return tea.Quit

	case "V":
		return m.enterSplit()

	case "/":
		m.searchMode = true
		m.applyLogs()

	case "z":
		m.zoomed = !m.zoomed
		m.vp.Width = m.vpWidth()
		m.vp.Height = m.vpHeight()

	case "q", "tab", "esc", "h":
		m.focus = listFocus
		m.zoomed = false
		m.searchMode = false
		m.searchQuery = ""
		m.applyLogs()
		m.vp.Width = m.vpWidth()
		m.vp.Height = m.vpHeight()

	case "G":
		m.autoScroll = true
		m.vp.GotoBottom()

	case "g":
		m.autoScroll = false
		m.vp.GotoTop()

	case "a":
		return m.toggleAllLogs()

	case "n":
		m.showService = !m.showService
		m.applyLogs()

	case "t":
		m.showTS = !m.showTS
		m.applyLogs()

	case "e":
		m.showStderr = !m.showStderr
		m.applyLogs()
		if m.autoScroll {
			m.vp.GotoBottom()
		}

	default:
		var cmd tea.Cmd
		m.vp, cmd = m.vp.Update(msg)
		if !m.vp.AtBottom() {
			m.autoScroll = false
		}
		return cmd
	}
	return nil
}

func (m *model) enterSplit() tea.Cmd {
	if len(m.procs) <= 1 {
		return nil
	}
	m.splitActive = true
	m.splitPane = 0
	m.splitIdx = (m.selIdx + 1) % len(m.procs)
	m.autoScroll2 = true
	return cmdLoadSplitLogs(m.procs[m.splitIdx].Name)
}

func (m *model) toggleAllLogs() tea.Cmd {
	m.autoScroll = true
	if m.mode == modeAll {
		m.mode = modeSingle
		if len(m.procs) > 0 {
			return cmdLoadLogs(m.procs[m.selIdx].Name)
		}
	} else {
		m.mode = modeAll
		if len(m.procs) > 0 {
			return cmdLoadAllLogs()
		}
	}
	return nil
}

func (m *model) adjustOffset() {
	visible := m.listItemsVisible()
	if visible <= 0 {
		return
	}
	if m.selIdx >= m.listOffset+visible {
		m.listOffset = m.selIdx - visible + 1
	}
	if m.selIdx < m.listOffset {
		m.listOffset = m.selIdx
	}
}
