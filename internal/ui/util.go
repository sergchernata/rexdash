package ui

import (
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/gdamore/tcell/v2"
)

// ---------- keys ----------

// IsRune is true for a plain key press of r.
func IsRune(ev *tcell.EventKey, r rune) bool {
	return ev.Key() == tcell.KeyRune && ev.Rune() == r
}

// IsBack: esc, q, ←, h, backspace.
func IsBack(ev *tcell.EventKey) bool {
	switch ev.Key() {
	case tcell.KeyEscape, tcell.KeyLeft, tcell.KeyBackspace, tcell.KeyBackspace2:
		return true
	}
	return IsRune(ev, 'q') || IsRune(ev, 'h')
}

// IsEnter: ⏎, →, l.
func IsEnter(ev *tcell.EventKey) bool {
	return ev.Key() == tcell.KeyEnter || ev.Key() == tcell.KeyRight || IsRune(ev, 'l')
}

// Nav moves a selection (-1 = none) with the usual keys; true if the key was used.
func Nav(sel *int, n int, ev *tcell.EventKey, page int) bool {
	if n == 0 {
		*sel = -1
		return ev.Key() == tcell.KeyUp || ev.Key() == tcell.KeyDown || IsRune(ev, 'j') || IsRune(ev, 'k')
	}
	i := min(max(*sel, 0), n-1)
	switch {
	case ev.Key() == tcell.KeyDown || IsRune(ev, 'j'):
		i = min(i+1, n-1)
	case ev.Key() == tcell.KeyUp || IsRune(ev, 'k'):
		i = max(i-1, 0)
	case ev.Key() == tcell.KeyPgDn || IsRune(ev, 'd'):
		i = min(i+page, n-1)
	case ev.Key() == tcell.KeyPgUp || IsRune(ev, 'u'):
		i = max(i-page, 0)
	case ev.Key() == tcell.KeyHome || IsRune(ev, 'g'):
		i = 0
	case ev.Key() == tcell.KeyEnd || IsRune(ev, 'G'):
		i = n - 1
	default:
		return false
	}
	*sel = i
	return true
}

// Clamp keeps a selection inside a list that changed length.
func Clamp(sel *int, n int) {
	if n == 0 {
		*sel = -1
		return
	}
	*sel = min(max(*sel, 0), n-1)
}

// ---------- formatters ----------

// EveryLabel is "30s", "5m", "6h" for the refresh interval.
func EveryLabel(d time.Duration) string {
	s := int64(d / time.Second)
	switch {
	case s < 60 || s%60 != 0:
		return fmt.Sprintf("%ds", s)
	case s < 3600 || s%3600 != 0:
		return fmt.Sprintf("%dm", s/60)
	}
	return fmt.Sprintf("%dh", s/3600)
}

// Now is the current Unix time.
func Now() int64 { return time.Now().Unix() }

// Ago is "now", "5m", "3h", "2d" since a Unix time.
func Ago(secs int64) string {
	d := Now() - secs
	switch {
	case d < 60:
		return "now"
	case d < 3600:
		return fmt.Sprintf("%dm", d/60)
	case d < 86400:
		return fmt.Sprintf("%dh", d/3600)
	}
	return fmt.Sprintf("%dd", d/86400)
}

// Dur is "45s", "3m07s", "2h05m".
func Dur(secs int64) string {
	switch {
	case secs < 0:
		return ""
	case secs < 60:
		return fmt.Sprintf("%ds", secs)
	case secs < 3600:
		return fmt.Sprintf("%dm%02ds", secs/60, secs%60)
	}
	return fmt.Sprintf("%dh%02dm", secs/3600, secs%3600/60)
}

// Local formats a Unix time in local time with a Go layout ("Jan 02 15:04").
func Local(secs int64, layout string) string {
	if secs == 0 {
		return ""
	}
	return time.Unix(secs, 0).Local().Format(layout)
}

// ParseTime reads an RFC 3339 time as Unix seconds (0 if missing or invalid).
func ParseTime(s string) int64 {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return 0
	}
	return t.Unix()
}

// ---------- tones ----------

type Tone int

const (
	ToneIdle Tone = iota
	ToneOk
	ToneBad
	ToneBusy
)

func (t Tone) String() string {
	return [...]string{"idle", "ok", "bad", "busy"}[t]
}

// ToneOf maps AWS and GitHub status names to a tone.
func ToneOf(s string) Tone {
	switch s {
	case "Succeeded", "SUCCESS", "COMPLETED", "APPROVED":
		return ToneOk
	case "Failed", "Stopped", "Cancelled", "Abandoned", "FAILURE", "ERROR", "TIMED_OUT", "CANCELLED", "ACTION_REQUIRED", "STARTUP_FAILURE", "CHANGES_REQUESTED":
		return ToneBad
	case "InProgress", "Stopping", "PENDING", "IN_PROGRESS", "QUEUED", "EXPECTED", "WAITING", "REVIEW_REQUIRED":
		return ToneBusy
	}
	return ToneIdle
}

func ToneColor(t Tone) Color {
	switch t {
	case ToneOk:
		return Green()
	case ToneBad:
		return Red()
	case ToneBusy:
		return Yellow()
	}
	return Muted()
}

func ToneIcon(t Tone) string {
	switch t {
	case ToneOk:
		return OK.S()
	case ToneBad:
		return FAIL.S()
	case ToneBusy:
		return RUNNING.S()
	}
	return PENDING.S()
}

func StatusColor(s string) Color { return ToneColor(ToneOf(s)) }

// StatusIcon is the bold, coloured icon for a status name.
func StatusIcon(s string) Span {
	return S(ToneIcon(ToneOf(s)), Style{Fg: StatusColor(s), Bold: true})
}

// ---------- misc ----------

// Noisy paths never affect what a pane shows (build output, dependencies, git objects).
func Noisy(path string) bool {
	for _, c := range strings.Split(filepath.ToSlash(path), "/") {
		switch c {
		case "node_modules", "_build", "deps", "target", "objects", ".expo", "dist":
			return true
		}
	}
	return false
}

// B64 is s without control characters, cut to 180 bytes on a character boundary, base64-encoded.
func B64(s string) string {
	clean := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	for len(clean) > 180 {
		cut := 180
		for cut > 0 && !utf8.RuneStart(clean[cut]) {
			cut--
		}
		clean = clean[:cut]
	}
	return base64.StdEncoding.EncodeToString([]byte(clean))
}

// RexZoom toggles (nil) or sets Rex's zoom on the pane this program runs in.
func RexZoom(on *bool) {
	s, b := getenv("REX_SESSION"), getenv("REX_BLOCK")
	if s == "" || b == "" {
		return
	}
	args := []string{"zoom", "-s", s, b}
	if on != nil {
		if *on {
			args = append(args, "--on")
		} else {
			args = append(args, "--off")
		}
	}
	quiet("rex", args...)
}

// Zoom sets Rex's zoom on or off.
func Zoom(on bool) { RexZoom(&on) }

// OpenURL opens a URL in the default browser.
func OpenURL(url string) {
	if url != "" {
		quiet("open", url)
	}
}

// underTest is true inside `go test` binaries (named *.test), checked without linking the
// testing package into rexdash.
var underTest = strings.HasSuffix(os.Args[0], ".test")

// quiet starts a helper (rex, open) in the background; never during tests, so they can't
// zoom the developer's own pane or open a browser.
func quiet(name string, args ...string) {
	if underTest {
		return
	}
	cmd := exec.Command(name, args...)
	if cmd.Start() == nil {
		go cmd.Wait()
	}
}
