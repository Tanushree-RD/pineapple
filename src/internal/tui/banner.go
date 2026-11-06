package tui

import "strings"

// A 5x5 block font for the banners. Only the letters the two tools need.
var glyphs = map[rune][5]string{
	'H': {"█   █", "█   █", "█████", "█   █", "█   █"},
	'Y': {"█   █", " █ █ ", "  █  ", "  █  ", "  █  "},
	'D': {"████ ", "█   █", "█   █", "█   █", "████ "},
	'R': {"████ ", "█   █", "████ ", "█  █ ", "█   █"},
	'A': {" ███ ", "█   █", "█████", "█   █", "█   █"},
	'L': {"█    ", "█    ", "█    ", "█    ", "█████"},
	'E': {"█████", "█    ", "████ ", "█    ", "█████"},
	'P': {"████ ", "█   █", "████ ", "█    ", "█    "},
	'F': {"█████", "█    ", "████ ", "█    ", "█    "},
	'O': {" ███ ", "█   █", "█   █", "█   █", " ███ "},
	'G': {" ████", "█    ", "█  ██", "█   █", " ███ "},
	'-': {"     ", "     ", " ███ ", "     ", "     "},
	' ': {"  ", "  ", "  ", "  ", "  "},
}

// Banner renders text in the block font with a vertical gradient between two 256-color codes.
func Banner(text string, from, to int) []string {
	rows := make([]string, 5)
	for _, r := range strings.ToUpper(text) {
		g, ok := glyphs[r]
		if !ok {
			g = glyphs[' ']
		}
		for i := 0; i < 5; i++ {
			rows[i] += g[i] + " "
		}
	}
	out := make([]string, 5)
	for i, row := range rows {
		c := from + (to-from)*i/4
		out[i] = C(c, row)
	}
	return out
}
