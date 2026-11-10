package ui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
	"howett.net/plist"
)

func ansiHex(base int) []string {
	var out []string
	for i := 0; i < 16; i++ {
		out = append(out, "#"+hex2(base+i)+"0000")
	}
	return out
}

func hex2(n int) string { const d = "0123456789abcdef"; return string([]byte{d[n/16%16], d[n%16]}) }

func writeThemes(t *testing.T, v any) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "themes.json")
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func themeFixture() []any {
	return []any{
		map[string]any{"id": "one", "source": map[string]any{
			"ansi": ansiHex(0x10), "background": "#000000", "foreground": "#ffffff", "cursor": "#00ff00", "cursorText": "#111111"}},
		map[string]any{"id": "two", "source": map[string]any{
			"ansi": ansiHex(0x40), "background": "#ffffff", "foreground": "#000000"}},
		map[string]any{"id": "short", "source": map[string]any{"ansi": []string{"#000000"}, "background": "#000000", "foreground": "#ffffff"}},
		map[string]any{"id": "badhex", "source": map[string]any{"ansi": append(ansiHex(0)[:15], "nope"), "background": "#000000", "foreground": "#ffffff"}},
		map[string]any{"id": "nobg", "source": map[string]any{"ansi": ansiHex(0), "foreground": "#ffffff"}},
		map[string]any{"id": "nofg", "source": map[string]any{"ansi": ansiHex(0), "background": "#000000"}},
	}
}

// useTheme switches the package theme for one test and puts the terminal theme back after.
func useTheme(t *testing.T, path, id string) bool {
	t.Helper()
	t.Setenv("REX_THEMES", path)
	t.Setenv("REXDASH_THEME", id)
	return ReloadTheme()
}

func restoreTheme(t *testing.T) {
	t.Cleanup(func() { os.Setenv("REXDASH_THEME", "terminal"); ReloadTheme() })
}

func TestThemeSwitch(t *testing.T) {
	restoreTheme(t)
	path := writeThemes(t, themeFixture())

	if !useTheme(t, path, "one") {
		t.Fatal("switching to theme one reported no change")
	}
	if Red() != tcell.NewRGBColor(0x11, 0, 0) || Muted() != tcell.NewRGBColor(0x18, 0, 0) {
		t.Errorf("ansi colours: red %v muted %v", Red(), Muted())
	}
	if Bg() != tcell.NewRGBColor(0, 0, 0) || FgC() != tcell.NewRGBColor(255, 255, 255) {
		t.Errorf("bg/fg %v %v", Bg(), FgC())
	}
	if Accent() != tcell.NewRGBColor(0, 255, 0) || OnAccent() != tcell.NewRGBColor(0x11, 0x11, 0x11) {
		t.Errorf("accent %v on %v", Accent(), OnAccent())
	}
	if Border() != tcell.NewRGBColor(51, 51, 51) || Surface() != tcell.NewRGBColor(36, 36, 36) {
		t.Errorf("border %v surface %v", Border(), Surface())
	}
	if ReloadTheme() {
		t.Error("reload of the same theme reported a change")
	}

	// Theme two: no cursor colours, so accent falls back to ansi blue and text on it to white.
	if !useTheme(t, path, "two") {
		t.Fatal("switch to two reported no change")
	}
	if Red() != tcell.NewRGBColor(0x41, 0, 0) {
		t.Errorf("red after switch %v", Red())
	}
	if Accent() != tcell.NewRGBColor(0x44, 0, 0) || OnAccent() != tcell.NewRGBColor(255, 255, 255) {
		t.Errorf("fallback accent %v on %v", Accent(), OnAccent())
	}
	for _, c := range []Color{Green(), Yellow(), Blue(), Magenta(), Cyan(), SelBg(), AddBg(), DelBg()} {
		if c&tcell.ColorIsRGB == 0 {
			t.Errorf("colour %v not from the theme", c)
		}
	}

	// "terminal" switches to the ANSI fallbacks.
	if !useTheme(t, path, "terminal") {
		t.Fatal("switch to terminal reported no change")
	}
	want := map[string][2]Color{
		"red": {Red(), tcell.ColorMaroon}, "green": {Green(), tcell.ColorGreen}, "yellow": {Yellow(), tcell.ColorOlive},
		"blue": {Blue(), tcell.ColorNavy}, "magenta": {Magenta(), tcell.ColorPurple}, "cyan": {Cyan(), tcell.ColorTeal},
		"muted": {Muted(), tcell.ColorGray}, "fg": {FgC(), tcell.ColorReset}, "bg": {Bg(), tcell.ColorBlack},
		"accent": {Accent(), tcell.ColorTeal}, "onaccent": {OnAccent(), tcell.ColorBlack},
		"border": {Border(), tcell.PaletteColor(239)}, "sel": {SelBg(), tcell.PaletteColor(236)},
		"surface": {Surface(), tcell.PaletteColor(238)}, "add": {AddBg(), tcell.PaletteColor(22)}, "del": {DelBg(), tcell.PaletteColor(52)},
	}
	for name, c := range want {
		if c[0] != c[1] {
			t.Errorf("terminal %s = %v, want %v", name, c[0], c[1])
		}
	}

	// Broken or unknown themes fall back too.
	for _, id := range []string{"short", "badhex", "nobg", "nofg", "missing"} {
		useTheme(t, path, "one")
		if !useTheme(t, path, id) || Red() != tcell.ColorMaroon {
			t.Errorf("theme %q: want terminal fallback, red %v", id, Red())
		}
	}
	for _, p := range []string{filepath.Join(t.TempDir(), "absent.json"), writeThemes(t, "not a list")} {
		useTheme(t, path, "one")
		if useTheme(t, p, "one"); Red() != tcell.ColorMaroon {
			t.Errorf("themes file %s: want fallback", p)
		}
	}
	bad := filepath.Join(t.TempDir(), "bad.json")
	os.WriteFile(bad, []byte("{"), 0o644)
	if useTheme(t, bad, "one"); Red() != tcell.ColorMaroon {
		t.Error("invalid JSON: want fallback")
	}

	// themes.json may also be an object keyed by id.
	obj := map[string]any{"one": themeFixture()[0]}
	if useTheme(t, writeThemes(t, obj), "one"); Red() != tcell.NewRGBColor(0x11, 0, 0) {
		t.Errorf("object themes.json: red %v", Red())
	}
}

func writePlist(t *testing.T, path string, v map[string]any) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := plist.NewBinaryEncoder(f).Encode(v); err != nil {
		t.Fatal(err)
	}
}

// fakeHome makes $HOME/Library/Preferences with Rex's theme ids and the macOS appearance.
func fakeHome(t *testing.T, dark bool, darkID string) string {
	t.Helper()
	home := t.TempDir()
	if p, err := filepath.EvalSymlinks(home); err == nil {
		home = p // FSEvents reports /private/var/…
	}
	prefs := filepath.Join(home, "Library", "Preferences")
	if err := os.MkdirAll(prefs, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	setPrefs(t, prefs, dark, darkID)
	return prefs
}

func setPrefs(t *testing.T, prefs string, dark bool, darkID string) {
	g := map[string]any{}
	if dark {
		g["AppleInterfaceStyle"] = "Dark"
	}
	writePlist(t, filepath.Join(prefs, ".GlobalPreferences.plist"), g)
	writePlist(t, filepath.Join(prefs, "com.superlogical.rex.plist"), map[string]any{
		"Workspace.darkThemeID": darkID, "Workspace.lightThemeID": "one"})
}

func TestThemeFromRexPrefs(t *testing.T) {
	restoreTheme(t)
	prefs := fakeHome(t, true, "two")
	t.Setenv("REX_THEMES", writeThemes(t, themeFixture()))
	t.Setenv("REXDASH_THEME", "")
	ReloadTheme()
	if Red() != tcell.NewRGBColor(0x41, 0, 0) {
		t.Errorf("dark appearance: red %v, want theme two", Red())
	}
	setPrefs(t, prefs, false, "two")
	if !ReloadTheme() || Red() != tcell.NewRGBColor(0x11, 0, 0) {
		t.Errorf("light appearance: red %v, want theme one", Red())
	}
	// No Rex preferences: terminal colours.
	os.Remove(filepath.Join(prefs, "com.superlogical.rex.plist"))
	if !ReloadTheme() || Red() != tcell.ColorMaroon {
		t.Errorf("no prefs: red %v", Red())
	}
}

func TestWatchTheme(t *testing.T) {
	restoreTheme(t)
	prefs := fakeHome(t, true, "one")
	t.Setenv("REX_THEMES", writeThemes(t, themeFixture()))
	t.Setenv("REXDASH_THEME", "")
	ReloadTheme()
	ch := make(chan struct{}, 1)
	WatchTheme(ch)
	time.Sleep(100 * time.Millisecond) // let the stream start before changing the file

	setPrefs(t, prefs, true, "two")
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("no signal after Rex's theme changed")
	}
	if Red() != tcell.NewRGBColor(0x41, 0, 0) {
		t.Errorf("after watch: red %v, want theme two", Red())
	}
}
