package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// Mid-tone colours that stay readable on light and dark status bars.
var (
	cDim   = lipgloss.Color("#7c7f93")
	cGreen = lipgloss.Color("#40a02b")
	cBlue  = lipgloss.Color("#1e66f5")
	cPeach = lipgloss.Color("#fe640b")
	cRed   = lipgloss.Color("#d20f39")
)

func verdictColor(v Verdict) lipgloss.Color {
	switch v {
	case VPush:
		return cGreen
	case VHold:
		return cBlue
	case VEase:
		return cPeach
	case VStop, VWait:
		return cRed
	}
	return cDim
}

// setupColors picks the colour profile from the environment instead of probing
// the terminal, so output piped into a status line keeps its colours.
func setupColors(noColor bool) {
	ct := strings.ToLower(os.Getenv("COLORTERM"))
	switch {
	case noColor || os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb":
		lipgloss.SetColorProfile(termenv.Ascii)
	case ct == "truecolor" || ct == "24bit":
		lipgloss.SetColorProfile(termenv.TrueColor)
	default:
		lipgloss.SetColorProfile(termenv.ANSI256)
	}
	lipgloss.SetHasDarkBackground(true)
}

// View is everything the renderers need.
type View struct {
	State    *State
	As       []Assessment
	Binding  *Assessment
	Headline string
	Now      time.Time
	Error    string // current problem, if any (numbers shown are cached)
}

// renderLine is the one line for status lines and prompts: the call to action,
// its reason, the raw percentages, and a warning when the numbers are not live.
func renderLine(v View) string {
	vd := VUnknown
	if v.Binding != nil {
		vd = v.Binding.Verdict
	}
	vc := lipgloss.NewStyle().Foreground(verdictColor(vd))
	line := vc.Bold(true).Render(vd.Glyph()+" "+vd.Word()) + " " + vc.Render(shortReason(v.Binding))
	var tail []string
	for _, a := range v.As {
		name := a.Name
		switch a.Key {
		case "session":
			name = "S"
		case "weekly_all":
			name = "W"
		}
		tail = append(tail, fmt.Sprintf("%s %d%%", name, int(a.Percent+0.5)))
	}
	if len(tail) > 0 {
		line += lipgloss.NewStyle().Foreground(cDim).Render("  │  " + strings.Join(tail, " "))
	}
	if m := v.marker(); m != "" {
		line += lipgloss.NewStyle().Foreground(cRed).Render("  ⚠ " + m)
	}
	return line
}

// marker is the short reason the line should not be trusted blindly: the
// server is down, the numbers are old, or this machine failed to sample.
func (v View) marker() string {
	age := time.Duration(0)
	if !v.State.FetchedAt.IsZero() {
		age = v.Now.Sub(v.State.FetchedAt)
	}
	switch {
	case v.State.Offline && age > 0:
		return "server offline · last update " + fmtAgo(age)
	case v.State.Offline:
		return "server offline"
	case age >= staleAfter:
		return "last update " + fmtAgo(age)
	case v.Error != "":
		return "sampling failed"
	}
	return ""
}
