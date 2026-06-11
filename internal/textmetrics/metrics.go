// Package textmetrics approximates bpmn-js label rendering (Arial 12px) well
// enough to compute label and annotation bounds without a font engine.
package textmetrics

import "strings"

// Standard Helvetica/Arial advance widths in 1/1000 em for ASCII 32..126.
var widths = [95]int{
	278, 278, 355, 556, 556, 889, 667, 191, 333, 333, 389, 584, 278, 333, 278, 278,
	556, 556, 556, 556, 556, 556, 556, 556, 556, 556, 278, 278, 584, 584, 584, 556,
	1015, 667, 667, 722, 722, 667, 611, 778, 722, 278, 500, 667, 556, 833, 722, 778,
	667, 778, 722, 667, 611, 722, 667, 944, 667, 667, 611, 278, 278, 278, 469, 556,
	333, 556, 556, 500, 556, 556, 278, 556, 556, 222, 222, 500, 222, 833, 556, 556,
	556, 556, 333, 500, 278, 556, 500, 722, 500, 500, 500, 334, 260, 334, 584,
}

const (
	fontSize   = 12.0
	LineHeight = 15.0 // bpmn-js: fontSize 12, lineHeight 1.2, rounded up
)

// RuneWidth returns the approximate advance width of one rune in pixels.
func RuneWidth(r rune) float64 {
	if r >= 32 && r <= 126 {
		return float64(widths[r-32]) / 1000 * fontSize
	}
	// Treat everything else (umlauts, dashes, …) like a medium glyph.
	return 556.0 / 1000 * fontSize
}

// Width returns the approximate width of a single line of text in pixels.
func Width(s string) float64 {
	var w float64
	for _, r := range s {
		w += RuneWidth(r)
	}
	return w
}

// Wrap greedily wraps text at maxWidth pixels, honoring explicit newlines.
// Words longer than maxWidth stay on their own line.
func Wrap(s string, maxWidth float64) []string {
	var out []string
	for _, para := range strings.Split(s, "\n") {
		words := strings.Fields(para)
		if len(words) == 0 {
			out = append(out, "")
			continue
		}
		line := words[0]
		for _, w := range words[1:] {
			if Width(line)+RuneWidth(' ')+Width(w) <= maxWidth {
				line += " " + w
			} else {
				out = append(out, line)
				line = w
			}
		}
		out = append(out, line)
	}
	return out
}

// Box returns the bounding box of text wrapped at maxWidth.
func Box(s string, maxWidth float64) (w, h float64) {
	lines := Wrap(s, maxWidth)
	for _, l := range lines {
		if lw := Width(l); lw > w {
			w = lw
		}
	}
	return w, float64(len(lines)) * LineHeight
}
