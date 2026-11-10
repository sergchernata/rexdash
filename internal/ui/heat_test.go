package ui

import (
	"testing"

	"github.com/gdamore/tcell/v2"
)

func withTheme(t *testing.T, th *theme) {
	t.Helper()
	themeMu.Lock()
	old, oldLoaded := current, themeLoaded
	current, themeLoaded = th, true
	themeMu.Unlock()
	t.Cleanup(func() {
		themeMu.Lock()
		current, themeLoaded = old, oldLoaded
		themeMu.Unlock()
	})
}

// Fullness shades smoothly: green to half full, then yellow, orange and red near the top.
func TestHeatGradient(t *testing.T) {
	th := &theme{}
	th.ansi[1] = rgb{200, 0, 0}   // red
	th.ansi[2] = rgb{0, 200, 0}   // green
	th.ansi[3] = rgb{200, 200, 0} // yellow
	withTheme(t, th)
	orange := tc(rgb{200, 100, 0})
	for _, c := range []struct {
		pct  float64
		want Color
	}{
		{0, Green()}, {50, Green()}, {60, tc(rgb{100, 200, 0})}, {70, Yellow()},
		{85, orange}, {95, Red()}, {120, Red()},
	} {
		if got := Heat(c.pct); got != c.want {
			t.Errorf("%v%%: %v, want %v", c.pct, got, c.want)
		}
	}
	// Each step up is at least as red and no greener.
	prevR, prevG := int32(-1), int32(256)
	for pct := 50.0; pct <= 95; pct++ {
		r, g, _ := Heat(pct).RGB()
		if r < prevR || g > prevG {
			t.Fatalf("%v%%: rgb(%d,%d) goes backwards", pct, r, g)
		}
		prevR, prevG = r, g
	}
}

// Without a Rex theme the terminal's named colours are used in steps.
func TestHeatWithoutTheme(t *testing.T) {
	withTheme(t, nil)
	for _, c := range []struct {
		pct  float64
		want Color
	}{{10, Green()}, {69, Green()}, {70, Yellow()}, {85, tcell.ColorDarkOrange}, {95, Red()}} {
		if got := Heat(c.pct); got != c.want {
			t.Errorf("%v%%: %v, want %v", c.pct, got, c.want)
		}
	}
}
