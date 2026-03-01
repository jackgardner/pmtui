package main

import (
	"hash/fnv"

	"github.com/charmbracelet/lipgloss"
)

// servicePalette is a set of distinct, readable colours for service name labels.
var servicePalette = []lipgloss.Color{
	"#61afef", // blue
	"#98c379", // green
	"#e5c07b", // yellow
	"#c678dd", // purple
	"#e06c75", // red
	"#56b6c2", // cyan
	"#d19a66", // orange
	"#be5046", // dark red
	"#528bff", // bright blue
	"#7ed6df", // teal
}

// serviceColour returns a consistent colour for a given service name.
func serviceColour(name string) lipgloss.Color {
	h := fnv.New32a()
	h.Write([]byte(name))
	return servicePalette[h.Sum32()%uint32(len(servicePalette))]
}

var (
	cGreen  = lipgloss.Color("#4CAF50")
	cRed    = lipgloss.Color("#F44336")
	cYellow = lipgloss.Color("#FF9800")
	cBlue   = lipgloss.Color("#42A5F5")
	cCyan   = lipgloss.Color("#26C6DA")
	cGray   = lipgloss.Color("#757575")
	cDim    = lipgloss.Color("#424242")
	cWhite  = lipgloss.Color("#ECEFF4")
	cSelBg  = lipgloss.Color("#1C2A3A")

	sDim   = lipgloss.NewStyle().Foreground(cGray)
	sWhite = lipgloss.NewStyle().Foreground(cWhite)

	sBorderIdle  = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(cDim)
	sBorderFocus = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(cBlue)

	sFooter = lipgloss.NewStyle().Background(lipgloss.Color("#161B22")).Foreground(cGray).Padding(0, 1)
	sSel    = lipgloss.NewStyle().Background(cSelBg).Foreground(cWhite)
	sKey     = lipgloss.NewStyle().Foreground(cCyan).Bold(true)
	sKeyOn   = lipgloss.NewStyle().Foreground(cGreen).Bold(true)
	sDesc    = lipgloss.NewStyle().Foreground(cGray)
	sDescOn  = lipgloss.NewStyle().Foreground(cGreen)
	sSep     = lipgloss.NewStyle().Foreground(cDim)
)

func statusStyle(status string) lipgloss.Style {
	switch status {
	case "online":
		return lipgloss.NewStyle().Foreground(cGreen)
	case "stopped", "stopping":
		return lipgloss.NewStyle().Foreground(cYellow)
	case "errored":
		return lipgloss.NewStyle().Foreground(cRed)
	default:
		return lipgloss.NewStyle().Foreground(cGray)
	}
}

func healthStr(p PM2Process) string {
	if !p.HealthKnown {
		return sDim.Render("·")
	}
	if p.Healthy {
		return lipgloss.NewStyle().Foreground(cGreen).Render("✓")
	}
	return lipgloss.NewStyle().Foreground(cRed).Render("✗")
}

// truncate rune-safely to n visible chars, appending "…" if cut.
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return string(r[:n])
	}
	return string(r[:n-1]) + "…"
}

// pad pads s to exactly n visible chars using lipgloss.
func pad(s string, n int) string {
	return lipgloss.NewStyle().Width(n).Render(s)
}
