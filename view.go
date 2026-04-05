package main

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

var (
	// Matches any .log section header, captures the filename stem.
	logSectionRe = regexp.MustCompile(`/([^/]+)\.log last \d+ lines:`)
	// Matches PM2 log lines: "7|service     | content"
	logLineRe = regexp.MustCompile(`^\d+\|([^|]+?)\s*\| (.*)$`)
	// ISO 8601 timestamp at the start of content, e.g. 2026-03-01T01:10:00.076Z
	// Also matches timezone offsets like +0100.
	logTsRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{4})?`)
	// Strips ANSI escape codes for plain-text matching.
	ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*m`)
)

func stripANSI(s string) string { return ansiRe.ReplaceAllString(s, "") }

// filterLines returns only lines whose plain-text content contains query (case-insensitive).
func filterLines(content, query string) string {
	lower := strings.ToLower(query)
	var out []string
	for line := range strings.SplitSeq(content, "\n") {
		if strings.Contains(strings.ToLower(stripANSI(line)), lower) {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

// parsedLog holds a parsed log line with its timestamp for sorting.
type parsedLog struct {
	ts      string // raw ISO 8601 timestamp (lexicographically sortable)
	order   int    // original position, for stable sort of lines without timestamps
	content string // content after timestamp
	name    string // service name
	stderr  bool
}

func processLogs(raw string, showService, showTS, showStderr bool) string {
	lines := strings.Split(raw, "\n")
	parsed := make([]parsedLog, 0, len(lines))
	isStderr := false
	isSkip := true // skip until we're inside a known service log section
	order := 0

	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		// Section header — determine whether we're in stdout/stderr/other
		if m := logSectionRe.FindStringSubmatch(line); m != nil {
			stem := m[1]
			switch {
			case strings.HasSuffix(stem, "-out"):
				isSkip, isStderr = false, false
			case strings.HasSuffix(stem, "-error"):
				isSkip, isStderr = false, true
			default:
				isSkip = true
			}
			continue
		}
		if isSkip {
			continue
		}
		// Parse PM2 log line "7|name     | content"
		match := logLineRe.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		if isStderr && !showStderr {
			continue
		}
		name := match[1]
		content := match[2]

		// Strip ANSI before timestamp extraction so colour codes don't interfere.
		plain := stripANSI(content)
		var ts string
		if loc := logTsRe.FindStringIndex(plain); loc != nil {
			ts = plain[:loc[1]]
			content = strings.TrimLeft(content[loc[1]:], "\t ")
		}

		// Continuation line (no timestamp) — append to the previous entry
		// so multi-line log messages stay together.
		if ts == "" && len(parsed) > 0 {
			parsed[len(parsed)-1].content += "\n" + content
			continue
		}

		parsed = append(parsed, parsedLog{
			ts:      ts,
			order:   order,
			content: content,
			name:    name,
			stderr:  isStderr,
		})
		order++
	}

	// Sort by timestamp so stdout and stderr are interleaved chronologically.
	// Lines with identical (or empty) timestamps keep their original order.
	sort.SliceStable(parsed, func(i, j int) bool {
		if parsed[i].ts == parsed[j].ts {
			return parsed[i].order < parsed[j].order
		}
		return parsed[i].ts < parsed[j].ts
	})

	out := make([]string, 0, len(parsed))
	for _, p := range parsed {
		var prefix string
		if showService {
			prefix = lipgloss.NewStyle().Foreground(serviceColour(p.name)).Render(p.name) + " "
		}
		if showTS && p.ts != "" {
			prefix += sDim.Render(p.ts) + " "
		}
		out = append(out, prefix+p.content)
	}
	return strings.Join(out, "\n")
}

func (m model) View() string {
	if m.w == 0 {
		return "Initializing…"
	}
	return strings.Join([]string{
		m.viewBody(),
		m.viewFooter(),
	}, "\n")
}

func (m model) viewBody() string {
	if m.splitActive {
		return m.viewSplitPanels()
	}
	if m.zoomed {
		return m.viewLogsPanel(m.w)
	}
	return lipgloss.JoinHorizontal(lipgloss.Top,
		m.viewListPanel(),
		m.viewLogsPanel(m.w-listOuterW),
	)
}

func (m model) viewListPanel() string {
	innerH := m.panelInnerH()
	sepLine := sSep.Render(strings.Repeat("─", listInnerW))

	listAreaH := max(innerH-detailLines-1, 3)

	inner := m.renderList(listAreaH) + "\n" + sepLine + "\n" + m.renderDetail()

	bs := sBorderIdle
	if m.focus == listFocus {
		bs = sBorderFocus
	}
	return bs.Width(listInnerW).Height(innerH).Render(inner)
}

func (m model) renderList(areaH int) string {
	hdr := lipgloss.NewStyle().Bold(true).Foreground(cCyan).Width(listInnerW).Render(
		pad("", 4) + pad("NAME", 18) + pad("STATUS", 9) + "H",
	)
	subSep := sDim.Render(strings.Repeat("─", listInnerW))

	itemsH := max(areaH-2, 0)

	start := m.listOffset
	if m.searchQuery != "" {
		start = 0 // always scan from top when filtering
	}
	var rows []string
	for i := start; i < len(m.procs) && len(rows) < itemsH; i++ {
		if !m.matchesSearch(i) {
			continue
		}
		rows = append(rows, m.renderListRow(i))
	}
	for len(rows) < itemsH {
		rows = append(rows, "")
	}

	return strings.Join(append([]string{hdr, subSep}, rows...), "\n")
}

func (m model) renderListRow(i int) string {
	p := m.procs[i]

	cursor := "  "
	if i == m.selIdx {
		cursor = "▶ "
	}
	icon := statusStyle(p.PM2Env.Status).Render(p.StatusIcon())
	name := truncate(p.Name, 17)
	st := truncate(p.PM2Env.Status, 8)
	h := healthStr(p)

	row := cursor +
		icon + " " +
		pad(name, 17) + " " +
		pad(statusStyle(p.PM2Env.Status).Render(st), 8) + " " +
		h

	if i == m.selIdx {
		row = sSel.Width(listInnerW).Render(row)
	}
	return row
}

func (m model) renderDetail() string {
	if len(m.procs) == 0 || m.selIdx >= len(m.procs) {
		lines := make([]string, detailLines)
		lines[1] = "  " + sDim.Render("no processes")
		return strings.Join(lines, "\n")
	}

	p := m.procs[m.selIdx]

	lbl := func(k, v string) string {
		return "  " + sDim.Render(k) + " " + sWhite.Render(v)
	}

	var hStr string
	switch {
	case !p.HealthKnown:
		hStr = sDim.Render("checking…")
	case p.Healthy:
		hStr = lipgloss.NewStyle().Foreground(cGreen).Render("● healthy")
	default:
		hStr = lipgloss.NewStyle().Foreground(cRed).Render("● unhealthy")
	}

	var hcSrcStr string
	if p.HCPort != "" {
		hcSrcStr = lipgloss.NewStyle().Foreground(cCyan).Render(":" + p.HCPort)
	} else if p.HealthKnown {
		hcSrcStr = sDim.Render("pm2 status only")
	} else {
		hcSrcStr = sDim.Render("scanning…")
	}

	lines := []string{
		"  " + lipgloss.NewStyle().Bold(true).Foreground(cWhite).Render(truncate(p.Name, listInnerW-2)),
		lbl("status :", statusStyle(p.PM2Env.Status).Render(p.StatusIcon()+" "+p.PM2Env.Status)),
		lbl("pid    :", fmt.Sprintf("%d", p.PID)),
		lbl("cpu    :", fmt.Sprintf("%.1f%%", p.Monit.CPU)),
		lbl("memory :", p.MemStr()),
		lbl("uptime :", p.Uptime()),
		lbl("restart:", fmt.Sprintf("%d", p.PM2Env.RestartTime)),
		"  " + sDim.Render("health :") + " " + hStr,
		"  " + sDim.Render("hc via :") + " " + hcSrcStr,
	}

	return strings.Join(lines, "\n")
}

func (m model) viewLogsPanel(outerW int) string {
	if outerW < 12 {
		outerW = 12
	}

	var innerW, innerH int
	if m.zoomed {
		innerW = outerW
		innerH = m.panelH()
	} else {
		innerW = outerW - 2
		innerH = m.panelInnerH()
	}

	m.vp.Width = innerW
	m.vp.Height = innerH - 2 // title + separator consume 2 lines

	var titleLine string
	if m.mode == modeAll {
		titleLine = lipgloss.NewStyle().Bold(true).Foreground(cCyan).Render("Logs") +
			sDim.Render(": ") +
			lipgloss.NewStyle().Foreground(cYellow).Bold(true).Render("all services")
	} else {
		var procName string
		if len(m.procs) > 0 && m.selIdx < len(m.procs) {
			procName = m.procs[m.selIdx].Name
		}
		titleLine = lipgloss.NewStyle().Bold(true).Foreground(cCyan).Render("Logs") +
			sDim.Render(": ") +
			lipgloss.NewStyle().Foreground(cWhite).Bold(true).Render(procName)
	}

	if !m.autoScroll {
		titleLine += sDim.Render("  scroll mode · G to follow")
	}

	sepLine := sDim.Render(strings.Repeat("─", innerW))
	inner := titleLine + "\n" + sepLine + "\n" + m.vp.View()

	if m.zoomed {
		return lipgloss.NewStyle().Width(innerW).Height(innerH).Render(inner)
	}
	bs := sBorderIdle
	if m.focus == logsFocus {
		bs = sBorderFocus
	}
	return bs.Width(innerW).Height(innerH).Render(inner)
}

func (m model) viewSplitPanels() string {
	if m.splitZoomed {
		return m.viewSplitPanel(m.splitPane, m.w)
	}
	leftW := m.w / 2
	rightW := m.w - leftW
	return lipgloss.JoinHorizontal(lipgloss.Top,
		m.viewSplitPanel(0, leftW),
		m.viewSplitPanel(1, rightW),
	)
}

func (m model) viewSplitPanel(panel, outerW int) string {
	if outerW < 12 {
		outerW = 12
	}

	zoomed := m.splitZoomed && panel == m.splitPane
	var innerW, innerH int
	if zoomed {
		innerW = outerW
		innerH = m.panelH()
	} else {
		innerW = outerW - 2
		innerH = m.panelInnerH()
	}

	var procName string
	var vpView string
	if panel == 0 {
		m.vp.Width = innerW
		m.vp.Height = innerH - 2
		if len(m.procs) > 0 && m.selIdx < len(m.procs) {
			procName = m.procs[m.selIdx].Name
		}
		vpView = m.vp.View()
	} else {
		m.vp2.Width = innerW
		m.vp2.Height = innerH - 2
		if len(m.procs) > 0 && m.splitIdx < len(m.procs) {
			procName = m.procs[m.splitIdx].Name
		}
		vpView = m.vp2.View()
	}

	label := fmt.Sprintf("[%d] ", panel+1)
	titleLine := sDim.Render(label) +
		lipgloss.NewStyle().Bold(true).Foreground(cCyan).Render("Logs") +
		sDim.Render(": ") +
		lipgloss.NewStyle().Foreground(cWhite).Bold(true).Render(procName)

	sepLine := sDim.Render(strings.Repeat("─", innerW))
	inner := titleLine + "\n" + sepLine + "\n" + vpView

	if zoomed {
		return lipgloss.NewStyle().Width(innerW).Height(innerH).Render(inner)
	}
	bs := sBorderIdle
	if panel == m.splitPane {
		bs = sBorderFocus
	}
	return bs.Width(innerW).Height(innerH).Render(inner)
}

type hint struct {
	k, d   string
	active bool
}

func renderHints(hints []hint) string {
	sep := sSep.Render("  ·  ")
	parts := make([]string, len(hints))
	for i, h := range hints {
		if h.active {
			parts[i] = sKeyOn.Render(h.k) + " " + sDescOn.Render(h.d)
		} else {
			parts[i] = sKey.Render(h.k) + " " + sDesc.Render(h.d)
		}
	}
	return strings.Join(parts, sep)
}

func statusSuffix(status string) string {
	if status == "" {
		return ""
	}
	c := cYellow
	if strings.HasPrefix(status, "✓") {
		c = cGreen
	} else if strings.HasPrefix(status, "✕") {
		c = cRed
	}
	return "    " + lipgloss.NewStyle().Foreground(c).Render(status)
}

func (m model) viewFooter() string {
	if m.searchMode {
		cursor := lipgloss.NewStyle().Foreground(cCyan).Render("█")
		bar := sKey.Render("/") + " " + sWhite.Render(m.searchQuery) + cursor +
			"  " + sDim.Render("esc to clear  enter to lock")
		return sFooter.Width(m.w).Render(bar)
	}

	var hints []hint
	if m.splitActive {
		hints = []hint{
			{"Tab", "switch pane", false},
			{"↑↓/jk", "scroll", false},
			{"S-↑↓", "change svc", false},
			{"r", "restart", false},
			{"s", "stop/start", false},
			{"n", "svc name", m.showService},
			{"t", "timestamp", m.showTS},
			{"e", "stderr", m.showStderr},
			{"z", "zoom", m.splitZoomed},
			{"V", "exit split", false},
		}
	} else if m.focus == logsFocus {
		hints = []hint{
			{"↑↓/jk", "scroll", false},
			{"G/g", "bottom/top", false},
			{"a", "all logs", m.mode == modeAll},
			{"/", "search", m.searchQuery != ""},
			{"n", "svc name", m.showService},
			{"t", "timestamp", m.showTS},
			{"e", "stderr", m.showStderr},
			{"z", "zoom", m.zoomed},
			{"V", "split", false},
			{"Tab", "list", false},
		}
	} else {
		hints = []hint{
			{"↑↓/jk", "navigate", false},
			{"r", "restart", false},
			{"s", "stop/start", false},
			{"a", "all logs", m.mode == modeAll},
			{"/", "search", m.searchQuery != ""},
			{"Tab", "logs", false},
			{"n", "svc name", m.showService},
			{"t", "timestamp", m.showTS},
			{"e", "stderr", m.showStderr},
			{"V", "split", false},
			{"R", "refresh", false},
			{"?", "debug", m.debug},
			{"q", "quit", false},
		}
	}

	bar := renderHints(hints) + statusSuffix(m.status)
	return sFooter.Width(m.w).Render(bar)
}
