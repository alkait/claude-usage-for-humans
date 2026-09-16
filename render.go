package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// Catppuccin Mocha, the palette the old panel used.
var (
	cBG     = lipgloss.Color("#1e1e2e")
	cText   = lipgloss.Color("#cdd6f4")
	cDim    = lipgloss.Color("#7f849c")
	cTrack  = lipgloss.Color("#45475a")
	cMark   = lipgloss.Color("#f5e0dc")
	cBorder = lipgloss.Color("#585b70")
	cTime   = lipgloss.Color("#9399b2")
	cOrange = lipgloss.Color("#e8956b")
	cGreen  = lipgloss.Color("#a6e3a1")
	cBlue   = lipgloss.Color("#89b4fa")
	cPeach  = lipgloss.Color("#fab387")
	cRed    = lipgloss.Color("#f38ba8")
)

const panelMaxWidth = 80

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

// styles derive from one base so the panel background is continuous.
type styles struct {
	base, text, bold, dim, track, mark, orange, red lipgloss.Style
}

func newStyles(paintBG bool) styles {
	base := lipgloss.NewStyle()
	if paintBG && lipgloss.ColorProfile() != termenv.Ascii {
		base = base.Background(cBG)
	}
	return styles{
		base:   base,
		text:   base.Foreground(cText),
		bold:   base.Foreground(cText).Bold(true),
		dim:    base.Foreground(cDim),
		track:  base.Foreground(cTrack),
		mark:   base.Foreground(cMark).Bold(true),
		orange: base.Foreground(cOrange).Bold(true),
		red:    base.Foreground(cRed),
	}
}

// View is everything a renderer needs.
type View struct {
	State    *State
	Plan     string
	As       []Assessment
	Binding  *Assessment
	Headline string
	Now      time.Time
	Width    int
	Error    string // current problem, if any (numbers shown are cached)
	PaintBG  bool
	Height   int // terminal rows, 0 if unknown; the panel drops spacer lines to fit
}

func (s styles) twoCols(left, right string, width int) string {
	gap := width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		gap = 1
	}
	return left + s.base.Render(strings.Repeat(" ", gap)) + right
}

func (s styles) divider(width int) string {
	return s.track.Render(strings.Repeat("─", width))
}

// shade blends a colour toward the panel background; used for the faint
// projection segment on the quota bar.
func shade(c lipgloss.Color, f float64) lipgloss.Color {
	hex := func(h string) (int, int, int) {
		var r, g, b int
		fmt.Sscanf(strings.TrimPrefix(h, "#"), "%02x%02x%02x", &r, &g, &b)
		return r, g, b
	}
	r, g, b := hex(string(c))
	br, bg, bb := hex(string(cBG))
	mix := func(a, z int) int { return z + int(float64(a-z)*f) }
	return lipgloss.Color(fmt.Sprintf("#%02x%02x%02x", mix(r, br), mix(g, bg), mix(b, bb)))
}

const (
	meterLevels = 10 // one level per 10%
	colHalf     = 10 // width of each half column (time | quota)
	colWidth    = colHalf*2 + 2
	axisWidth   = 6
	glyphFull   = "▬▬"
	glyphEmpty  = "╌╌"
	glyphLand   = "╍╍"
)

func center(t string, w int) string {
	n := lipgloss.Width(t)
	if n >= w {
		return t
	}
	left := (w - n) / 2
	return strings.Repeat(" ", left) + t + strings.Repeat(" ", w-n-left)
}

func levelsOf(frac float64) int {
	if frac < 0 {
		return 0
	}
	return int(min(frac, 1)*meterLevels + 0.5)
}

// meterCell draws one level of a meter: full, landing (faint), or empty.
func (s styles) meterCell(level, full, land int, col lipgloss.Color) string {
	switch {
	case level <= full:
		return s.base.Foreground(col).Render(glyphFull)
	case level <= land:
		return s.base.Foreground(shade(col, 0.45)).Render(glyphLand)
	}
	return s.track.Render(glyphEmpty)
}

// positionCaption is the two-line caption under the time meter.
func positionCaption(a Assessment) (string, string) {
	if a.ResetsAt == nil || a.Window == 0 {
		return "no", "window"
	}
	elapsed := a.Window - a.ResetIn
	if a.Group == "weekly" {
		day := int(elapsed.Hours()/24) + 1
		if day > 7 {
			day = 7
		}
		return fmt.Sprintf("day %d", day), "of 7"
	}
	return strings.ReplaceAll(fmtDur(elapsed), " ", ""), "of 5h"
}

// judgment is the compact verdict text under a column.
func (s styles) judgment(a Assessment) string {
	vc := s.base.Foreground(verdictColor(a.Verdict))
	var text string
	var st lipgloss.Style
	switch {
	case a.ResetsAt == nil:
		return ""
	case a.Percent >= 100:
		text, st = "used up", vc.Bold(true)
	case a.EmptyIn > 0:
		text, st = "runs out "+fmtDur(a.EmptyIn), vc.Bold(true)
	case a.Projected < 0:
		text, st = "no pace yet", s.dim
	default:
		text, st = fmt.Sprintf("on course: %d%%", int(a.Projected+0.5)), vc
	}
	out := st.Render(text)
	if a.VsUsual >= 1.5 || (a.VsUsual > 0 && a.VsUsual <= 0.5) {
		out += s.dim.Render(fmt.Sprintf(" · %.1f×", a.VsUsual))
	}
	return out
}

// meters lays the limits out as columns of two vertical meters each.
func (s styles) meters(as []Assessment, now time.Time, inner int) []string {
	perRow := max(1, (inner-axisWidth)/colWidth)
	var lines []string
	for start := 0; start < len(as); start += perRow {
		row := as[start:min(start+perRow, len(as))]
		if start > 0 {
			lines = append(lines, "")
		}
		pad := strings.Repeat(" ", axisWidth)
		l1, l2 := pad, pad
		for _, a := range row {
			l1 += s.bold.Render(center(a.Name, colWidth))
			l2 += s.dim.Render(center("time", colHalf) + "  " + center("quota", colHalf))
		}
		lines = append(lines, l1, l2)
		for level := meterLevels; level >= 1; level-- {
			axis := "    "
			switch level {
			case meterLevels:
				axis = "100%"
			case meterLevels / 2:
				axis = " 50%"
			case 1:
				axis = "  0%"
			}
			line := s.dim.Render(axis) + "  "
			for _, a := range row {
				tfull := levelsOf(a.Elapsed)
				qfull := levelsOf(a.Percent / 100)
				qland := qfull
				if a.Projected >= 0 && a.Percent < 100 {
					qland = levelsOf(a.Projected / 100)
				}
				line += center(s.meterCell(level, tfull, 0, cTime), colHalf) + "  " +
					center(s.meterCell(level, qfull, qland, verdictColor(a.Verdict)), colHalf)
			}
			lines = append(lines, line)
		}
		c1, c2, c3, c4 := pad, pad, pad, pad
		for _, a := range row {
			p1, p2 := positionCaption(a)
			vc := s.base.Foreground(verdictColor(a.Verdict)).Bold(true)
			c1 += s.text.Render(center(p1, colHalf)) + "  " + vc.Render(center(fmt.Sprintf("%d%%", int(a.Percent+0.5)), colHalf))
			c2 += s.dim.Render(center(p2, colHalf)) + "  " + s.dim.Render(center("used", colHalf))
			c3 += center(s.judgment(a), colWidth)
			c4 += s.dim.Render(center(resetClock(a, now), colWidth))
		}
		lines = append(lines, c1, c2, c3, c4)
	}
	return lines
}

// resetClock renders the reset as a clock time in the local zone.
func resetClock(a Assessment, now time.Time) string {
	if a.ResetsAt == nil {
		return "no window"
	}
	t := a.ResetsAt.In(time.Local)
	if t.YearDay() == now.In(time.Local).YearDay() && t.Year() == now.In(time.Local).Year() {
		return "resets at " + t.Format("15:04")
	}
	return "resets " + t.Format("Mon 15:04")
}

// renderPanel is the main view: a bordered card with its own background.
func renderPanel(v View) string {
	s := newStyles(v.PaintBG)
	outer := min(v.Width, panelMaxWidth)
	inner := outer - 6 // border 2 + padding 4
	if inner < 36 {
		inner = 36
	}
	// Spacer lines are marked so they can be dropped when the terminal is short.
	const spacer = "\x00"
	var L []string
	add := func(line string) { L = append(L, line) }

	add(s.orange.Render("✱ ") + s.bold.Render("Claude usage") + s.dim.Render("  ·  "+v.Plan))
	add(s.divider(inner))
	add(spacer)

	vd := VUnknown
	if v.Binding != nil {
		vd = v.Binding.Verdict
	}
	badge := lipgloss.NewStyle().Background(verdictColor(vd)).Foreground(cBG).Bold(true).Padding(0, 1).Render(vd.Word())
	boxInner := inner - 4 // rounded border 2 + padding 2
	verdictBox := s.base.Border(lipgloss.RoundedBorder()).Padding(0, 1).Width(inner - 2)
	if lipgloss.ColorProfile() != termenv.Ascii {
		verdictBox = verdictBox.BorderForeground(verdictColor(vd))
		if v.PaintBG {
			verdictBox = verdictBox.BorderBackground(cBG)
		}
	}
	boxBody := s.base.Render(vd.Glyph()+" ") + badge + "\n" + s.text.Width(boxInner).Render(v.Headline)
	add(verdictBox.Render(boxBody))
	add(spacer)
	add(spacer)

	for _, line := range s.meters(v.As, v.Now, inner) {
		add(line)
	}
	add(spacer)

	if sp := spendOf(v.State); sp != nil && sp.Limit.AmountMinor > 0 {
		state := "off"
		if sp.Enabled {
			state = "on"
		}
		add(s.divider(inner))
		add(s.twoCols(s.dim.Render("EXTRA USAGE"), s.text.Render(money(sp.Used)+" of "+money(sp.Limit))+s.dim.Render(" · "+state), inner))
	}

	add(s.divider(inner))
	stamp := "◷ no data yet"
	if !v.State.FetchedAt.IsZero() {
		stamp = "◷ updated " + fmtAgo(v.Now.Sub(v.State.FetchedAt))
		if v.Error != "" {
			stamp += " (cached)"
		}
		stamp += " · via " + v.State.Via
	}
	add(s.dim.Render(stamp))
	if v.Error != "" {
		add(s.red.Width(inner).Render("! " + v.Error))
	}

	// Drop spacer lines when the panel would not fit the terminal (border and
	// padding add 4 rows). Otherwise turn them into blank lines.
	needed := 0
	for _, line := range L {
		needed += strings.Count(line, "\n") + 1
	}
	overflow := 0
	if v.Height > 0 {
		overflow = needed + 4 - v.Height
	}
	// remove spacers from the bottom up, only as many as needed
	for i := len(L) - 1; i >= 0 && overflow > 0; i-- {
		if L[i] == spacer {
			L = append(L[:i], L[i+1:]...)
			overflow--
		}
	}
	for i, line := range L {
		if line == spacer {
			L[i] = ""
		}
	}

	// pad every line to the inner width so the background is a solid block
	for i, line := range L {
		if w := lipgloss.Width(line); w < inner {
			L[i] = line + s.base.Render(strings.Repeat(" ", inner-w))
		}
	}
	panel := s.base.Border(lipgloss.RoundedBorder()).Padding(1, 2).Width(inner + 4)
	if lipgloss.ColorProfile() != termenv.Ascii {
		panel = panel.BorderForeground(cBorder)
		if v.PaintBG {
			panel = panel.BorderBackground(cBG)
		}
	}
	return panel.Render(strings.Join(L, "\n"))
}

// renderShort is the single line for status lines, tmux, and prompts:
// the call to action first, its reason, then the raw percentages.
func renderShort(v View) string {
	s := newStyles(false)
	vd := VUnknown
	if v.Binding != nil {
		vd = v.Binding.Verdict
	}
	vc := s.base.Foreground(verdictColor(vd))
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
		line += s.dim.Render("  │  " + strings.Join(tail, " "))
	}
	if m := v.marker(); m != "" {
		line += s.red.Render("  ⚠ " + m)
	}
	return line
}

// marker is the short reason the status line should not be trusted blindly:
// the server is down, the numbers are old, or this machine failed to sample.
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

func money(m Money) string {
	sym := m.Currency + " "
	if m.Currency == "USD" {
		sym = "$"
	}
	return fmt.Sprintf("%s%.2f", sym, m.Float())
}

func spendOf(st *State) *Spend {
	if st == nil || st.Usage == nil {
		return nil
	}
	return st.Usage.Spend
}
