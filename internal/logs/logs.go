// Package logs is the CloudWatch logs pane: live tail with filter, errors-only, wrap, and scroll-back.
package logs

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/smithy-go"
	"github.com/gdamore/tcell/v2"

	"rexdash/internal/awsx"
	"rexdash/internal/ui"
)

const keep = 10_000

type level int

const (
	info level = iota
	warn
	errLevel
)

func (l level) String() string { return [...]string{"info", "warn", "error"}[l] }

type entry struct {
	ts    int64 // ms
	msg   string
	level level
}

type Logs struct {
	ui.Base
	group string
	// stream limits to streams starting with this (e.g. production vs staging); "" = all.
	stream    string
	cw        *cloudwatchlogs.Client
	startMs   int64
	seen      map[string]bool
	seenOrder []string
	hide      []string
	lines     []entry
	filter    string
	typing    bool
	errsOnly  bool
	wrap      bool
	// offset is logical lines scrolled up from the bottom; 0 = following.
	offset int
	unseen int
	page   int
}

func New(group, stream, since string, hide []string) (ui.Pane, error) {
	cfg, err := awsx.Config(context.Background())
	if err != nil {
		return nil, err
	}
	return &Logs{
		group:   group,
		stream:  stream,
		cw:      cloudwatchlogs.NewFromConfig(cfg),
		startMs: (time.Now().Unix() - sinceSecs(since)) * 1000,
		seen:    map[string]bool{},
		hide:    hide,
		page:    20,
	}, nil
}

func lower(c byte) byte {
	if 'A' <= c && c <= 'Z' {
		return c + 'a' - 'A'
	}
	return c
}

// asciiLower lowercases ASCII only, so byte offsets stay aligned with the original.
func asciiLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		b[i] = lower(c)
	}
	return string(b)
}

// containsCI is an ASCII case-insensitive substring test without allocating (runs over every line on each redraw).
func containsCI(hay, needle string) bool {
	n := len(needle)
outer:
	for i := 0; i+n <= len(hay); i++ {
		for j := 0; j < n; j++ {
			if lower(hay[i+j]) != lower(needle[j]) {
				continue outer
			}
		}
		return true
	}
	return n == 0
}

// levelOf is the severity from Elixir/Phoenix-style log text.
func levelOf(msg string) level {
	switch {
	case strings.Contains(msg, "[error]") || strings.Contains(msg, "** (") || strings.Contains(msg, "[critical]"):
		return errLevel
	case strings.Contains(msg, "[warning]") || strings.Contains(msg, "[warn]"):
		return warn
	}
	return info
}

// sinceSecs reads "15m", "2h", "30s", "1d"; a bare number means minutes.
func sinceSecs(s string) int64 {
	s = strings.TrimSpace(s)
	n, unit := s, "m"
	if r, size := utf8.DecodeLastRuneInString(s); s != "" && !('0' <= r && r <= '9') {
		n, unit = s[:len(s)-size], s[len(s)-size:]
	}
	v, err := strconv.ParseInt(n, 10, 64)
	if err != nil {
		v = 15
	}
	switch unit {
	case "s":
		return v
	case "h":
		return v * 3600
	case "d":
		return v * 86400
	}
	return v * 60
}

func (l *Logs) visible(e *entry) bool {
	return (!l.errsOnly || e.level != info) && containsCI(e.msg, l.filter)
}

func (l *Logs) renderEntry(e *entry) []ui.Span {
	var base ui.Style
	gutter := ui.T("  ")
	switch e.level {
	case errLevel:
		base, gutter = ui.Fg(ui.Red()), ui.C(ui.FAIL.S()+" ", ui.Red())
	case warn:
		base, gutter = ui.Fg(ui.Yellow()), ui.C(ui.WARN.S()+" ", ui.Yellow())
	}
	segs := []ui.Span{gutter, ui.C(ui.Local(e.ts/1000, "15:04:05")+" ", ui.Blue())}
	if l.filter == "" {
		return append(segs, ui.S(e.msg, base))
	}
	// Highlight filter matches (ASCII case-insensitive keeps byte offsets aligned).
	hay, needle := asciiLower(e.msg), asciiLower(l.filter)
	mark := ui.Style{Fg: ui.Bg(), Bg: ui.Yellow()}
	i := 0
	for {
		p := strings.Index(hay[i:], needle)
		if p < 0 {
			break
		}
		s := i + p
		segs = append(segs, ui.S(e.msg[i:s], base), ui.S(e.msg[s:s+len(needle)], mark))
		i = s + len(needle)
	}
	return append(segs, ui.S(e.msg[i:], base))
}

// layout cuts styled segments into lines of at most width chars (one line if !wrap).
func layout(segs []ui.Span, width int, wrap bool) []ui.Line {
	var out []ui.Line
	var cur []ui.Span
	used := 0
	for _, seg := range segs {
		var buf strings.Builder
		for _, c := range seg.Text {
			if used == width {
				cur = append(cur, ui.S(buf.String(), seg.Style))
				out = append(out, ui.L(cur...))
				if !wrap {
					return out
				}
				cur = nil
				buf.Reset()
				buf.WriteString("  ")
				used = 2
			}
			buf.WriteRune(c)
			used++
		}
		cur = append(cur, ui.S(buf.String(), seg.Style))
	}
	return append(out, ui.L(cur...))
}

func (l *Logs) recentErrors() int {
	cutoff := (time.Now().Unix() - 900) * 1000
	n := 0
	for i := len(l.lines) - 1; i >= 0 && l.lines[i].ts >= cutoff; i-- {
		if l.lines[i].level == errLevel {
			n++
		}
	}
	return n
}

func (l *Logs) total() int {
	n := 0
	for i := range l.lines {
		if l.visible(&l.lines[i]) {
			n++
		}
	}
	return n
}

func (l *Logs) Name() string         { return "Logs" }
func (l *Logs) Every() time.Duration { return 2 * time.Second }
func (l *Logs) Typing() bool         { return l.typing }

func (l *Logs) Title() ui.Line {
	t := l.group
	if l.stream != "" {
		t = fmt.Sprintf("%s · %s*", l.group, l.stream)
	}
	return ui.L(ui.C(t, ui.Muted()))
}

func (l *Logs) Hints() []ui.Hint {
	if l.typing {
		return []ui.Hint{{Key: "⏎", Desc: "keep"}, {Key: "esc", Desc: "clear"}}
	}
	return []ui.Hint{{Key: "/", Desc: "filter"}, {Key: "e", Desc: "errors"}, {Key: "w", Desc: "wrap"}, {Key: "↑↓", Desc: "scroll"}, {Key: "G", Desc: "live"}, {Key: "c", Desc: "clear"}}
}

func (l *Logs) Status() ui.Status {
	msg := l.group + " · live, no errors in 15m"
	if errs := l.recentErrors(); errs > 0 {
		msg = fmt.Sprintf("%s · %d errors in 15m", l.group, errs)
	}
	return ui.Status{State: "idle", Msg: msg}
}

func (l *Logs) Refresh() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	in := &cloudwatchlogs.FilterLogEventsInput{LogGroupName: aws.String(l.group), StartTime: aws.Int64(l.startMs)}
	if l.stream != "" {
		in.LogStreamNamePrefix = aws.String(l.stream)
	}
	newest, added := l.startMs, 0
	for {
		resp, err := l.cw.FilterLogEvents(ctx, in)
		if err != nil {
			var ae smithy.APIError
			if errors.As(err, &ae) {
				return fmt.Errorf("%s: %s", ae.ErrorCode(), ae.ErrorMessage())
			}
			return err
		}
		for _, ev := range resp.Events {
			id := aws.ToString(ev.EventId)
			if l.seen[id] {
				continue
			}
			l.seen[id] = true
			l.seenOrder = append(l.seenOrder, id)
			if len(l.seenOrder) > keep*2 {
				delete(l.seen, l.seenOrder[0])
				l.seenOrder = l.seenOrder[1:]
			}
			ts := aws.ToInt64(ev.Timestamp)
			newest = max(newest, ts)
			msg := strings.ReplaceAll(strings.TrimRightFunc(aws.ToString(ev.Message), unicode.IsSpace), "\t", "    ")
			if l.hidden(msg) {
				continue
			}
			e := entry{ts, msg, levelOf(msg)}
			if l.visible(&e) {
				added++
			}
			l.lines = append(l.lines, e)
			if len(l.lines) > keep {
				l.lines = l.lines[1:]
			}
		}
		if resp.NextToken == nil {
			break
		}
		in.NextToken = resp.NextToken
	}
	if l.offset > 0 {
		l.offset += added // hold the view still while paused
		l.unseen += added
	}
	// Re-read the last few seconds next time: events can land late; dedup handles repeats.
	l.startMs = max(l.startMs, newest-5_000)
	return nil
}

func (l *Logs) hidden(msg string) bool {
	for _, h := range l.hide {
		if strings.Contains(msg, h) {
			return true
		}
	}
	return false
}

func (l *Logs) Key(ev *tcell.EventKey) error {
	if l.typing {
		switch ev.Key() {
		case tcell.KeyEnter:
			l.typing = false
		case tcell.KeyEscape:
			l.typing, l.filter = false, ""
		case tcell.KeyBackspace, tcell.KeyBackspace2:
			if _, size := utf8.DecodeLastRuneInString(l.filter); size > 0 {
				l.filter = l.filter[:len(l.filter)-size]
			}
		case tcell.KeyRune:
			l.filter += string(ev.Rune())
		}
		l.offset, l.unseen = 0, 0
		return nil
	}
	maxOff := max(l.total()-1, 0)
	key, r := ev.Key(), rune(0)
	if key == tcell.KeyRune {
		r = ev.Rune()
	}
	switch {
	case r == '/':
		l.typing = true
	case key == tcell.KeyEscape && l.filter != "":
		l.filter = ""
	case r == 'e':
		l.errsOnly = !l.errsOnly
	case r == 'w':
		l.wrap = !l.wrap
	case r == 'c':
		l.lines, l.offset = nil, 0
	case key == tcell.KeyUp || r == 'k':
		l.offset = min(l.offset+1, maxOff)
	case key == tcell.KeyDown || r == 'j':
		l.offset = max(l.offset-1, 0)
	case key == tcell.KeyPgUp || r == 'u' || r == 'b':
		l.offset = min(l.offset+l.page, maxOff)
	case key == tcell.KeyPgDn || r == 'd' || r == ' ':
		l.offset = max(l.offset-l.page, 0)
	case key == tcell.KeyHome || r == 'g':
		l.offset = maxOff
	case key == tcell.KeyEnd || r == 'G':
		l.offset = 0
	}
	if l.offset == 0 {
		l.unseen = 0
	}
	return nil
}

func (l *Logs) Draw(b *ui.Buffer, area ui.Rect) {
	showInput := l.typing || l.filter != ""
	inputH := 0
	if showInput {
		inputH = 3
	}
	parts := ui.Vertical(area, 0, ui.Min(3), ui.Len(inputH))
	la, ia := parts[0], parts[1]

	// Coloured icon, plain text: the same look as the other panes' panel titles.
	badge := func(icon string, c ui.Color, text string) []ui.Span {
		return []ui.Span{ui.C(icon+" ", c), ui.S(text+" ", ui.Style{Fg: ui.FgC()})}
	}
	title := ui.L(ui.T(" "))
	if l.offset > 0 {
		title.Spans = append(title.Spans, badge(ui.PAUSE.S(), ui.Yellow(), "Paused")...)
		if l.unseen > 0 {
			title.Spans = append(title.Spans, ui.C(fmt.Sprintf("+%d new ", l.unseen), ui.Yellow()))
		}
	} else {
		title.Spans = append(title.Spans, badge(ui.DOT.S(), ui.Green(), "Live")...)
	}
	if l.errsOnly {
		title.Spans = append(title.Spans, ui.C("· ", ui.Muted()))
		title.Spans = append(title.Spans, badge(ui.BUG.S(), ui.Red(), "errors only")...)
	}
	if l.wrap {
		title.Spans = append(title.Spans, ui.C(" wrap", ui.Muted()))
	}
	title.Spans = append(title.Spans, ui.T(" "))
	errs := l.recentErrors()
	errC := ui.Muted()
	if errs > 0 {
		errC = ui.Red()
	}
	right := ui.L(
		ui.C(fmt.Sprintf(" %d lines ", len(l.lines)), ui.Muted()),
		ui.C(fmt.Sprintf("%s %d errors/15m ", ui.BUG.S(), errs), errC),
	)
	inner := ui.Panel{Title: title, Right: right, NoPad: true}.Draw(b, la)

	w, h := max(inner.W-1, 0), inner.H
	l.page = max(h-2, 1)
	total := l.total()
	var rows []ui.Line // newest last, built back to front
	skip := l.offset
	for i := len(l.lines) - 1; i >= 0 && len(rows) < h; i-- {
		e := &l.lines[i]
		if !l.visible(e) {
			continue
		}
		if skip > 0 {
			skip--
			continue
		}
		rows = append(layout(l.renderEntry(e), max(w, 10), l.wrap), rows...)
	}
	if len(rows) > h {
		rows = rows[len(rows)-h:]
	}
	switch {
	case len(rows) > 0:
		ui.Paragraph(b, inner, rows, false)
	case len(l.lines) == 0:
		ui.EmptyState(b, inner, ui.LOGS.S(), ui.Accent(), "Waiting for log events…", l.group)
	default:
		ui.EmptyState(b, inner, ui.FILTER.S(), ui.Yellow(), "No lines match", "esc clears the filter")
	}
	ui.Scrollbar(b, la, total, max(total-(l.offset+h), 0), h)

	if showInput {
		border := ui.Border()
		if l.typing {
			border = ui.Accent()
		}
		in := ui.Panel{Title: ui.L(ui.T(fmt.Sprintf(" %s Filter · %d matches ", ui.FILTER.S(), total))), BorderColor: border}.Draw(b, ia)
		line := ui.L(ui.C(ui.FILTER.S()+" ", ui.Accent()), ui.T(l.filter))
		if l.typing {
			line.Spans = append(line.Spans, ui.C("▏", ui.Accent()))
		}
		b.SetLine(in, line)
	}
}
