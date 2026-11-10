// Package tfstate is the Terraform pane: the S3 state's apply history (resources added,
// changed and removed per state write), current serial and Terraform version, and whether
// an apply holds the lock.
//
// The S3 bucket keeps a version per state write. Each version is read once and reduced to
// resource addresses plus a hash of each resource's attributes; the state itself (which holds
// secrets) is never kept or shown.
package tfstate

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
	"github.com/gdamore/tcell/v2"

	"rexdash/internal/awsx"
	"rexdash/internal/ui"
)

var (
	tfIcon = ui.Ic{Glyph: "", Plain: "tf"}
	lock   = ui.Ic{Glyph: "", Plain: "locked"}
	unlock = ui.Ic{Glyph: "", Plain: ""}
)

// show is how many versions to list; one more is read as the base for the oldest diff.
const show = 15

// snap is what a state version is reduced to.
type snap struct {
	serial  int64
	tf      string
	managed map[string]uint64 // managed resource instance address → attribute hash
	data    int
}

type ver struct {
	at, serial int64
	tf         string
	total      int
	diff       *diff // nil for the oldest shown version (no base to compare with)
}

type diff struct {
	added, changed, removed []string
}

func (d diff) empty() bool { return len(d.added)+len(d.changed)+len(d.removed) == 0 }

type lockInfo struct {
	who, op string
	created int64
}

// findBackend reads bucket and key from a `backend "s3"` block.
func findBackend(text string) (bucket, key string, ok bool) {
	start := strings.Index(text, `backend "s3"`)
	if start < 0 {
		return "", "", false
	}
	block := text[start:]
	if end := strings.Index(block, "}"); end >= 0 {
		block = block[:end]
	}
	val := func(k string) (string, bool) {
		for _, l := range strings.Split(block, "\n") {
			if name, v, ok := strings.Cut(l, "="); ok && strings.TrimSpace(name) == k {
				return strings.Trim(strings.TrimSpace(v), `"`), true
			}
		}
		return "", false
	}
	bucket, ok1 := val("bucket")
	key, ok2 := val("key")
	return bucket, key, ok1 && ok2
}

// state is the part of a state file we look at.
type state struct {
	Serial    int64  `json:"serial"`
	TF        string `json:"terraform_version"`
	Resources []struct {
		Mode, Type, Name, Module string
		Instances                []struct {
			IndexKey   json.RawMessage `json:"index_key"`
			Attributes json.RawMessage `json:"attributes"`
		}
	} `json:"resources"`
}

// snapshot reduces a state file to addresses and attribute hashes.
func snapshot(raw []byte) (snap, error) {
	var st state
	if err := json.Unmarshal(raw, &st); err != nil {
		return snap{}, err
	}
	s := snap{serial: st.Serial, tf: st.TF, managed: map[string]uint64{}}
	for _, r := range st.Resources {
		data := r.Mode == "data"
		base := ""
		if r.Module != "" {
			base = r.Module + "."
		}
		if data {
			base += "data."
		}
		base += r.Type + "." + r.Name
		for _, inst := range r.Instances {
			if data {
				s.data++
				continue
			}
			addr := base
			var k string
			switch key := bytes.TrimSpace(inst.IndexKey); {
			case json.Unmarshal(key, &k) == nil:
				addr += `["` + k + `"]`
			case len(key) > 0 && string(key) != "null":
				addr += "[" + string(key) + "]"
			}
			s.managed[addr] = attrHash(inst.Attributes)
		}
	}
	return s, nil
}

// volatile attributes change on every refresh without anything changing (RDS's point-in-time restore clock).
var volatile = map[string]bool{"latest_restorable_time": true}

// attrHash is an FNV-64 over the sorted, compacted attributes, skipping volatile ones.
func attrHash(raw json.RawMessage) uint64 {
	h := fnv.New64a()
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil || m == nil {
		h.Write(compact(raw))
		return h.Sum64()
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		if !volatile[k] {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	for _, k := range keys {
		h.Write([]byte(k))
		h.Write([]byte{0})
		h.Write(compact(m[k]))
		h.Write([]byte{0})
	}
	return h.Sum64()
}

func compact(raw []byte) []byte {
	var b bytes.Buffer
	if json.Compact(&b, raw) != nil {
		return raw
	}
	return b.Bytes()
}

func diffSnaps(old, new snap) diff {
	d := diff{added: []string{}, changed: []string{}, removed: []string{}}
	for a, h := range new.managed {
		o, ok := old.managed[a]
		switch {
		case !ok:
			d.added = append(d.added, a)
		case o != h:
			d.changed = append(d.changed, a)
		}
	}
	for a := range old.managed {
		if _, ok := new.managed[a]; !ok {
			d.removed = append(d.removed, a)
		}
	}
	slices.Sort(d.added)
	slices.Sort(d.changed)
	slices.Sort(d.removed)
	return d
}

// shortAddr is "aws_ecs_service.flow" from "module.x.aws_ecs_service.flow[0]": short enough for a table cell.
func shortAddr(a string) string {
	a, _, _ = strings.Cut(a, "[")
	parts := strings.Split(a, ".")
	if len(parts) >= 2 {
		return strings.Join(parts[len(parts)-2:], ".")
	}
	return a
}

type Terraform struct {
	ui.Base
	every       time.Duration
	bucket, key string
	region      string
	s3          *s3.Client
	snaps       map[string]snap // by version id
	vers        []ver
	lock        *lockInfo
	types       map[string]int
	st          ui.TableState
	view        *ui.TextView
	loaded      bool
}

// New watches s3://bucket/key; with no bucket, the `backend "s3"` block in ./*.tf names it.
func New(bucket, key string, every time.Duration) (ui.Pane, error) {
	if bucket == "" {
		var text strings.Builder
		files, _ := filepath.Glob("*.tf")
		for _, f := range files {
			b, _ := os.ReadFile(f)
			text.Write(b)
			text.WriteByte('\n')
		}
		var ok bool
		if bucket, key, ok = findBackend(text.String()); !ok {
			return nil, errors.New(`no backend "s3" block in ./*.tf; pass BUCKET [KEY]`)
		}
	} else if key == "" {
		key = "terraform.tfstate"
	}
	cfg, err := awsx.Config(context.Background())
	if err != nil {
		return nil, err
	}
	region := cfg.Region
	if region == "" {
		region = "us-east-1"
	}
	return &Terraform{
		every:  every,
		bucket: bucket,
		key:    key,
		region: region,
		s3:     s3.NewFromConfig(cfg),
		snaps:  map[string]snap{},
		st:     ui.NewTableState(),
	}, nil
}

// awsErr keeps just the service's code and message.
func awsErr(err error) error {
	var ae smithy.APIError
	if errors.As(err, &ae) {
		return fmt.Errorf("%s: %s", ae.ErrorCode(), ae.ErrorMessage())
	}
	return err
}

// read fetches an object (a given version when version != ""); nil when it doesn't exist.
func (t *Terraform) read(key, version string) ([]byte, error) {
	in := &s3.GetObjectInput{Bucket: &t.bucket, Key: &key}
	if version != "" {
		in.VersionId = &version
	}
	out, err := t.s3.GetObject(context.Background(), in)
	if err != nil {
		var ae smithy.APIError
		if errors.As(err, &ae) && (ae.ErrorCode() == "NoSuchKey" || ae.ErrorCode() == "NotFound") {
			return nil, nil
		}
		return nil, awsErr(err)
	}
	defer out.Body.Close()
	return io.ReadAll(out.Body)
}

func (t *Terraform) Name() string { return "Terraform" }

func (t *Terraform) Title() ui.Line {
	return ui.L(ui.C(fmt.Sprintf("s3://%s/%s", t.bucket, t.key), ui.Muted()))
}

func hint(key, desc string) ui.Hint { return ui.Hint{Key: key, Desc: desc} }

func (t *Terraform) Hints() []ui.Hint {
	if t.view != nil {
		return []ui.Hint{hint("↑↓", "scroll"), hint("esc", "back")}
	}
	return []ui.Hint{hint("↑↓", "applies"), hint("⏎", "resources"), hint("t", "types"), hint("o", "S3")}
}

func (t *Terraform) Every() time.Duration { return t.every }

func (t *Terraform) Status() ui.Status {
	if l := t.lock; l != nil {
		return ui.Status{State: "working", Msg: fmt.Sprintf("locked · %s · %s", l.op, l.who)}
	}
	if len(t.vers) == 0 {
		return ui.Status{State: "working", Msg: "loading"}
	}
	v := t.vers[0]
	return ui.Status{State: "idle", Msg: fmt.Sprintf("serial %d · last write %s ago", v.serial, ui.Ago(v.at))}
}

func (t *Terraform) Refresh() error {
	r, err := t.s3.ListObjectVersions(context.Background(), &s3.ListObjectVersionsInput{
		Bucket:  &t.bucket,
		Prefix:  &t.key,
		MaxKeys: aws.Int32(100),
	})
	if err != nil {
		return awsErr(err)
	}
	type item struct {
		id string
		at int64
	}
	var list []item
	for _, v := range r.Versions {
		if aws.ToString(v.Key) == t.key && v.VersionId != nil && v.LastModified != nil {
			list = append(list, item{*v.VersionId, v.LastModified.Unix()})
		}
	}
	slices.SortStableFunc(list, func(a, b item) int { return cmp.Compare(b.at, a.at) })
	list = list[:min(len(list), show+1)]

	// Read new versions a few at a time (each is a few MB).
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		errs []error
		sem  = make(chan struct{}, 4)
	)
	for _, it := range list {
		if _, ok := t.snaps[it.id]; ok {
			continue
		}
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			b, err := t.read(t.key, it.id)
			var s snap
			if err == nil && b != nil {
				s, err = snapshot(b)
			}
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
			} else if b != nil {
				t.snaps[it.id] = s
			}
		})
	}
	wg.Wait()
	if len(errs) > 0 {
		return errs[0]
	}
	keep := map[string]snap{}
	for _, it := range list {
		if s, ok := t.snaps[it.id]; ok {
			keep[it.id] = s
		}
	}
	t.snaps = keep

	t.vers = t.vers[:0]
	for i, it := range list[:min(len(list), show)] {
		s, ok := t.snaps[it.id]
		if !ok {
			continue
		}
		v := ver{at: it.at, serial: s.serial, tf: s.tf, total: len(s.managed)}
		if i+1 < len(list) {
			if base, ok := t.snaps[list[i+1].id]; ok {
				d := diffSnaps(base, s)
				v.diff = &d
			}
		}
		t.vers = append(t.vers, v)
	}

	t.types = map[string]int{}
	if len(list) > 0 {
		for a := range t.snaps[list[0].id].managed {
			typ, _, _ := strings.Cut(shortAddr(a), ".")
			t.types[typ]++
		}
	}

	b, err := t.read(t.key+".tflock", "")
	if err != nil {
		return err
	}
	t.lock = nil
	if b != nil {
		var v struct{ Who, Operation, Created string }
		_ = json.Unmarshal(b, &v)
		if v.Who == "" {
			v.Who = "?"
		}
		t.lock = &lockInfo{
			who:     v.Who,
			op:      strings.ToLower(strings.TrimPrefix(v.Operation, "OperationType")),
			created: ui.ParseTime(v.Created),
		}
	}
	t.loaded = true
	ui.Clamp(&t.st.Sel, len(t.vers))
	return nil
}

func (t *Terraform) openSelected() {
	if t.st.Sel < 0 || t.st.Sel >= len(t.vers) {
		return
	}
	v := t.vers[t.st.Sel]
	if v.diff == nil {
		return
	}
	d := v.diff
	lines := []ui.Line{ui.L(
		ui.S(fmt.Sprintf("serial %d ", v.serial), ui.Style{Bold: true}),
		ui.C(ui.Local(v.at, "Jan 02 15:04"), ui.Muted()),
	)}
	for _, g := range []struct {
		title string
		list  []string
		color ui.Color
		sign  string
	}{{"Added", d.added, ui.Green(), "+"}, {"Changed", d.changed, ui.Yellow(), "~"}, {"Removed", d.removed, ui.Red(), "-"}} {
		if len(g.list) == 0 {
			continue
		}
		lines = append(lines, ui.Line{}, ui.LT(fmt.Sprintf("%s (%d)", g.title, len(g.list)), ui.Style{Fg: g.color, Bold: true}))
		for _, a := range g.list {
			lines = append(lines, ui.LT("  "+g.sign+" "+a, ui.Fg(g.color)))
		}
	}
	if d.empty() {
		lines = append(lines, ui.LT("No managed resources changed (outputs or data sources only).", ui.Fg(ui.Muted())))
	}
	t.view = ui.NewTextView(fmt.Sprintf("serial %d", v.serial), lines).WithIcon(tfIcon)
	ui.Zoom(true)
}

func (t *Terraform) openTypes() {
	names := make([]string, 0, len(t.types))
	for k := range t.types {
		names = append(names, k)
	}
	// Most resources first, then by name.
	slices.SortFunc(names, func(a, b string) int {
		return cmp.Or(cmp.Compare(t.types[b], t.types[a]), cmp.Compare(a, b))
	})
	lines := make([]ui.Line, 0, len(names))
	for _, n := range names {
		lines = append(lines, ui.L(ui.C(fmt.Sprintf("%5d  ", t.types[n]), ui.Accent()), ui.T(n)))
	}
	t.view = ui.NewTextView("resources by type", lines).WithIcon(tfIcon)
	ui.Zoom(true)
}

func (t *Terraform) Key(ev *tcell.EventKey) error {
	if t.view != nil {
		if ui.IsBack(ev) {
			t.view = nil
			ui.Zoom(false)
		} else {
			t.view.Key(ev)
		}
		return nil
	}
	switch {
	case ui.IsRune(ev, 't'):
		t.openTypes()
	case ui.IsRune(ev, 'o'):
		r := t.region
		ui.OpenURL(fmt.Sprintf("https://%s.console.aws.amazon.com/s3/object/%s?region=%s&prefix=%s&tab=versions", r, t.bucket, r, t.key))
	case ui.IsEnter(ev):
		t.openSelected()
	default:
		ui.Nav(&t.st.Sel, len(t.vers), ev, 10)
	}
	return nil
}

// counts is "+2 ~1 −1", or "no change".
func counts(d *diff) ui.Line {
	var l ui.Line
	for _, c := range []struct {
		n     int
		sign  string
		color ui.Color
	}{{len(d.added), "+", ui.Green()}, {len(d.changed), "~", ui.Yellow()}, {len(d.removed), "−", ui.Red()}} {
		if c.n > 0 {
			l.Spans = append(l.Spans, ui.C(fmt.Sprintf("%s%d ", c.sign, c.n), c.color))
		}
	}
	if len(l.Spans) == 0 {
		l.Spans = append(l.Spans, ui.C("no change", ui.Muted()))
	}
	return l
}

func (t *Terraform) Draw(b *ui.Buffer, area ui.Rect) {
	if t.view != nil {
		t.view.Draw(b, area)
		return
	}
	if !t.loaded {
		ui.EmptyState(b, area, tfIcon.S(), ui.Muted(), "Reading state history…", "s3://"+t.bucket)
		return
	}
	parts := ui.Vertical(area, 0, ui.Len(1), ui.Min(3))
	la, ta := parts[0], parts[1]

	// Summary line: lock, serial, version, resource count.
	var sum ui.Line
	if l := t.lock; l != nil {
		sum = ui.L(
			ui.S(" "+lock.S()+" LOCKED ", ui.Style{Fg: ui.Bg(), Bg: ui.Yellow(), Bold: true}),
			ui.C(fmt.Sprintf(" %s · %s · %s ", l.op, l.who, ui.Ago(l.created)), ui.Yellow()),
		)
	} else {
		sum = ui.L(ui.C(" "+unlock.S()+" unlocked ", ui.Green()))
	}
	if len(t.vers) > 0 {
		v := t.vers[0]
		sum.Spans = append(sum.Spans, ui.C(fmt.Sprintf("· serial %d · TF %s · %d resources", v.serial, v.tf, v.total), ui.Muted()))
	}
	b.SetLine(la, sum)

	rows := make([]ui.Row, 0, len(t.vers))
	for _, v := range t.vers {
		changes, what := ui.LT("—", ui.Fg(ui.Muted())), ""
		if d := v.diff; d != nil {
			var first []string
			for _, a := range slices.Concat(d.added, d.changed, d.removed) {
				if len(first) == 3 {
					break
				}
				first = append(first, shortAddr(a))
			}
			changes, what = counts(d), strings.Join(first, ", ")
		}
		rows = append(rows, ui.R(
			ui.LT(ui.Local(v.at, "Jan 02 15:04"), ui.Fg(ui.Muted())),
			ui.LT(ui.Ago(v.at), ui.Fg(ui.Muted())).RightAligned(),
			ui.LT(fmt.Sprint(v.serial), ui.Style{}).RightAligned(),
			changes,
			ui.LT(what, ui.Fg(ui.Muted())),
		))
	}
	ui.Table{
		Cols:      []ui.Constraint{ui.Len(12), ui.Len(4), ui.Len(6), ui.Len(14), ui.Min(10)},
		Header:    []ui.Line{ui.LT("Written", ui.Style{}), {}, ui.LT("Serial", ui.Style{}).RightAligned(), ui.LT("Resources", ui.Style{}), {}},
		Rows:      rows,
		Spacing:   2,
		Highlight: true,
		Block:     &ui.Panel{Title: ui.L(ui.T(" " + ui.HOURGLASS.S() + " State writes "))},
	}.Draw(b, ta, &t.st)
	ui.Scrollbar(b, ta, len(t.vers), max(t.st.Sel, 0), ta.H-3)
}
