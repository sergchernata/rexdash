package ui

// Colours from Rex's active theme, so panes match the app. Reads the theme id from Rex's
// preferences (dark or light, following macOS), looks it up in the app's themes.json and
// derives surface colours by blending; falls back to the terminal's ANSI colours.
// WatchTheme reloads it when Rex's theme or the macOS appearance changes.

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsevents"
	"github.com/gdamore/tcell/v2"
	"howett.net/plist"
)

type rgb [3]uint8

type theme struct {
	bg, fg, accent, onAccent rgb
	ansi                     [16]rgb
}

const themesPath = "/Applications/Rex Beta.app/Contents/Resources/SuperUI_SuperDesignTokens.bundle/Contents/Resources/themes.json"

var (
	themeMu     sync.RWMutex
	themeLoaded bool
	current     *theme
)

func hex(s string) (rgb, bool) {
	if !strings.HasPrefix(s, "#") || len(s) < 7 {
		return rgb{}, false
	}
	var out rgb
	for i := 0; i < 3; i++ {
		v, err := strconv.ParseUint(s[1+2*i:3+2*i], 16, 8)
		if err != nil {
			return rgb{}, false
		}
		out[i] = uint8(v)
	}
	return out, true
}

func pref(path, key string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	var m map[string]any
	if plist.NewDecoder(f).Decode(&m) != nil {
		return ""
	}
	s, _ := m[key].(string)
	return s
}

func loadTheme() *theme {
	if os.Getenv("REXDASH_THEME") == "terminal" {
		return nil
	}
	home := os.Getenv("HOME")
	dark := pref(home+"/Library/Preferences/.GlobalPreferences.plist", "AppleInterfaceStyle") == "Dark"
	id := os.Getenv("REXDASH_THEME")
	if id == "" {
		key := "Workspace.lightThemeID"
		if dark {
			key = "Workspace.darkThemeID"
		}
		id = pref(home+"/Library/Preferences/com.superlogical.rex.plist", key)
	}
	if id == "" {
		return nil
	}
	path := os.Getenv("REX_THEMES")
	if path == "" {
		path = themesPath
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var raw any
	if json.Unmarshal(data, &raw) != nil {
		return nil
	}
	var list []any
	switch v := raw.(type) {
	case []any:
		list = v
	case map[string]any:
		for _, x := range v {
			list = append(list, x)
		}
	}
	for _, x := range list {
		t, _ := x.(map[string]any)
		if t == nil || t["id"] != id {
			continue
		}
		src, _ := t["source"].(map[string]any)
		ansi, _ := src["ansi"].([]any)
		if len(ansi) < 16 {
			return nil
		}
		var th theme
		for i := 0; i < 16; i++ {
			s, _ := ansi[i].(string)
			c, ok := hex(s)
			if !ok {
				return nil
			}
			th.ansi[i] = c
		}
		str := func(k string) string { s, _ := src[k].(string); return s }
		var ok bool
		if th.bg, ok = hex(str("background")); !ok {
			return nil
		}
		if th.fg, ok = hex(str("foreground")); !ok {
			return nil
		}
		if th.accent, ok = hex(str("cursor")); !ok {
			th.accent = th.ansi[4]
		}
		if th.onAccent, ok = hex(str("cursorText")); !ok {
			th.onAccent = rgb{255, 255, 255}
		}
		return &th
	}
	return nil
}

func getTheme() *theme {
	themeMu.RLock()
	if themeLoaded {
		t := current
		themeMu.RUnlock()
		return t
	}
	themeMu.RUnlock()
	t := loadTheme()
	themeMu.Lock()
	current, themeLoaded = t, true
	themeMu.Unlock()
	return t
}

// ReloadTheme re-reads the theme; true if it changed.
func ReloadTheme() bool {
	t := loadTheme()
	themeMu.Lock()
	defer themeMu.Unlock()
	changed := !themeLoaded || (t == nil) != (current == nil) || (t != nil && *t != *current)
	current, themeLoaded = t, true
	return changed
}

// WatchTheme signals ch when Rex's theme or the macOS appearance changes.
func WatchTheme(ch chan<- struct{}) {
	dir := os.Getenv("HOME") + "/Library/Preferences"
	names := map[string]bool{"com.superlogical.rex.plist": true, ".GlobalPreferences.plist": true}
	es := &fsevents.EventStream{Paths: []string{dir}, Latency: 300 * time.Millisecond, Flags: fsevents.FileEvents | fsevents.NoDefer}
	if es.Start() != nil {
		return
	}
	go func() {
		for evs := range es.Events {
			hit := false
			for _, e := range evs {
				if names[filepath.Base(e.Path)] {
					hit = true
				}
			}
			if hit && ReloadTheme() {
				select {
				case ch <- struct{}{}:
				default:
				}
			}
		}
	}()
}

func tc(c rgb) Color { return tcell.NewRGBColor(int32(c[0]), int32(c[1]), int32(c[2])) }

func mixRGB(a, b rgb, t float64) rgb {
	m := func(x, y uint8) uint8 { return uint8(math.Round(float64(x) + (float64(y)-float64(x))*t)) }
	return rgb{m(a[0], b[0]), m(a[1], b[1]), m(a[2], b[2])}
}

func ansi(i int, fallback Color) Color {
	if t := getTheme(); t != nil {
		return tc(t.ansi[i])
	}
	return fallback
}

func Red() Color     { return ansi(1, tcell.ColorMaroon) }
func Green() Color   { return ansi(2, tcell.ColorGreen) }
func Yellow() Color  { return ansi(3, tcell.ColorOlive) }
func Blue() Color    { return ansi(4, tcell.ColorNavy) }
func Magenta() Color { return ansi(5, tcell.ColorPurple) }
func Cyan() Color    { return ansi(6, tcell.ColorTeal) }
func Muted() Color   { return ansi(8, tcell.ColorGray) }

func FgC() Color {
	if t := getTheme(); t != nil {
		return tc(t.fg)
	}
	return tcell.ColorReset
}

func Bg() Color {
	if t := getTheme(); t != nil {
		return tc(t.bg)
	}
	return tcell.ColorBlack
}

func Accent() Color {
	if t := getTheme(); t != nil {
		return tc(t.accent)
	}
	return tcell.ColorTeal
}

func OnAccent() Color {
	if t := getTheme(); t != nil {
		return tc(t.onAccent)
	}
	return tcell.ColorBlack
}

func blend(f func(t *theme) rgb, fallback Color) Color {
	if t := getTheme(); t != nil {
		return tc(f(t))
	}
	return fallback
}

// Border is for panel borders.
func Border() Color {
	return blend(func(t *theme) rgb { return mixRGB(t.bg, t.fg, 0.20) }, tcell.PaletteColor(239))
}

// SelBg is the selected row's background.
func SelBg() Color {
	return blend(func(t *theme) rgb { return mixRGB(t.bg, t.accent, 0.20) }, tcell.PaletteColor(236))
}

// Surface is for keycaps and neutral segments.
func Surface() Color {
	return blend(func(t *theme) rgb { return mixRGB(t.bg, t.fg, 0.14) }, tcell.PaletteColor(238))
}

func AddBg() Color {
	return blend(func(t *theme) rgb { return mixRGB(t.bg, t.ansi[2], 0.18) }, tcell.PaletteColor(22))
}

func DelBg() Color {
	return blend(func(t *theme) rgb { return mixRGB(t.bg, t.ansi[1], 0.18) }, tcell.PaletteColor(52))
}

// Heat is the colour for how full something is, in percent: green up to half full, then
// shading through yellow (70%) and orange (85%) to red (95% and over). Without a Rex theme
// it steps through the terminal's named colours instead.
func Heat(pct float64) Color {
	t := getTheme()
	if t == nil {
		switch {
		case pct >= 95:
			return Red()
		case pct >= 85:
			return tcell.ColorDarkOrange
		case pct >= 70:
			return Yellow()
		}
		return Green()
	}
	green, yellow, red := t.ansi[2], t.ansi[3], t.ansi[1]
	stops := []struct {
		at float64
		c  rgb
	}{{50, green}, {70, yellow}, {85, mixRGB(yellow, red, 0.5)}, {95, red}}
	if pct <= stops[0].at {
		return tc(green)
	}
	for i := 1; i < len(stops); i++ {
		if a, b := stops[i-1], stops[i]; pct <= b.at {
			return tc(mixRGB(a.c, b.c, (pct-a.at)/(b.at-a.at)))
		}
	}
	return tc(red)
}
