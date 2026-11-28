package statusview

import (
	"fmt"
	"time"

	"github.com/mattn/go-runewidth"
)

const (
	maxFlexible = 40
	minFlexible = 6
	minColumn   = 3
)

// fitColumns sizes columns to their content, then squeezes them into avail
// (the width left after cell padding): the flexible columns first, and only
// then the rest.
func fitColumns(headers []string, rows [][]string, avail int) []int {
	widths := make([]int, len(headers))
	for i, h := range headers {
		widths[i] = runewidth.StringWidth(h)
	}
	for _, r := range rows {
		for i, c := range r {
			widths[i] = max(widths[i], runewidth.StringWidth(c))
		}
	}
	for i := range widths {
		if flexible[i] {
			widths[i] = min(widths[i], maxFlexible)
		}
	}
	for sum(widths) > avail {
		i := widest(widths, func(i int) bool { return flexible[i] && widths[i] > minFlexible })
		if i < 0 {
			i = widest(widths, func(i int) bool { return widths[i] > minColumn })
		}
		if i < 0 {
			break
		}
		widths[i]--
	}
	return widths
}

func widest(widths []int, ok func(int) bool) int {
	best := -1
	for i, w := range widths {
		if ok(i) && (best < 0 || w > widths[best]) {
			best = i
		}
	}
	return best
}

func sum(ws []int) int {
	n := 0
	for _, w := range ws {
		n += w
	}
	return n
}

func humanDuration(d time.Duration) string {
	d = max(d, 0).Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}
