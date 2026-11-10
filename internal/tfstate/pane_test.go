package tfstate

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	"github.com/gdamore/tcell/v2"

	"rexdash/internal/ui"
)

const (
	bucket = "example-state"
	key    = "env/terraform.tfstate"
)

// fakeS3 serves a versioned state object and an optional lock file from memory.
type fakeS3 struct {
	mu       sync.Mutex
	versions []s3types.ObjectVersion
	objects  map[string][]byte // version id → state
	lock     []byte            // nil: no lock file
	listErr  error
	getErr   map[string]error // by version id ("" for the lock file)
	lockErr  error            // returned for a missing lock (NoSuchKey or NotFound)

	lists int
	reads map[string]int // version id → GetObject calls ("" for the lock file)
}

func newFake() *fakeS3 {
	return &fakeS3{objects: map[string][]byte{}, getErr: map[string]error{}, reads: map[string]int{}, lockErr: &s3types.NoSuchKey{}}
}

func (f *fakeS3) ListObjectVersions(_ context.Context, in *s3.ListObjectVersionsInput, _ ...func(*s3.Options)) (*s3.ListObjectVersionsOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lists++
	if aws.ToString(in.Bucket) != bucket || aws.ToString(in.Prefix) != key || aws.ToInt32(in.MaxKeys) != 100 {
		return nil, fmt.Errorf("unexpected list %+v", in)
	}
	if f.listErr != nil {
		return nil, f.listErr
	}
	return &s3.ListObjectVersionsOutput{Versions: f.versions}, nil
}

func (f *fakeS3) GetObject(_ context.Context, in *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := aws.ToString(in.VersionId)
	f.reads[id]++
	if aws.ToString(in.Bucket) != bucket {
		return nil, fmt.Errorf("bucket %q", aws.ToString(in.Bucket))
	}
	if err := f.getErr[id]; err != nil {
		return nil, err
	}
	var b []byte
	switch k := aws.ToString(in.Key); {
	case k == key+".tflock" && id == "":
		if f.lock == nil {
			return nil, f.lockErr
		}
		b = f.lock
	case k == key && id != "":
		var ok bool
		if b, ok = f.objects[id]; !ok {
			return nil, &s3types.NoSuchKey{}
		}
	default:
		return nil, fmt.Errorf("unexpected get %s@%s", k, id)
	}
	return &s3.GetObjectOutput{Body: io.NopCloser(bytes.NewReader(b))}, nil
}

func (f *fakeS3) versionReads() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for id, c := range f.reads {
		if id != "" {
			n += c
		}
	}
	return n
}

// put adds a state version written `ago` before now.
func (f *fakeS3) put(id string, ago time.Duration, st map[string]any) {
	b, _ := json.Marshal(st)
	f.objects[id] = b
	f.versions = append(f.versions, s3types.ObjectVersion{
		Key: aws.String(key), VersionId: aws.String(id), LastModified: aws.Time(time.Now().Add(-ago)),
	})
}

func newPane(f *fakeS3) *Terraform {
	return &Terraform{every: time.Minute, bucket: bucket, key: key, region: "us-east-1", s3: f, snaps: map[string]snap{}, st: ui.NewTableState()}
}

func res(typ, name string, attrs map[string]any) map[string]any {
	return map[string]any{"mode": "managed", "type": typ, "name": name, "instances": []any{map[string]any{"attributes": attrs}}}
}

func tfState(serial int, rs ...map[string]any) map[string]any {
	list := []any{
		map[string]any{"mode": "managed", "type": "aws_ecs_service", "name": "svc", "module": "module.app", "instances": []any{
			map[string]any{"index_key": 0, "attributes": map[string]any{"n": 1}},
			map[string]any{"index_key": "web", "attributes": map[string]any{"n": 2}},
		}},
		map[string]any{"mode": "data", "type": "aws_caller_identity", "name": "me", "instances": []any{map[string]any{"attributes": map[string]any{"id": serial}}}},
	}
	for _, r := range rs {
		list = append(list, r)
	}
	return map[string]any{"serial": serial, "terraform_version": "1.17.2", "resources": list}
}

// history is four writes, listed out of order:
//
//	v1 (4h ago)  bucket(private)                 base
//	v2 (3h ago)  bucket(private) + queue          +1
//	v3 (2h ago)  bucket(public) + role            +1 ~1 −1
//	v4 (1h ago)  same as v3, data source refresh  no change
func history() *fakeS3 {
	f := newFake()
	private := res("aws_s3_bucket", "b", map[string]any{"acl": "private"})
	public := res("aws_s3_bucket", "b", map[string]any{"acl": "public-read"})
	queue := res("aws_sqs_queue", "q", map[string]any{})
	role := res("aws_iam_role", "r", map[string]any{"name": "app"})
	f.put("v3", 2*time.Hour, tfState(3, public, role))
	f.put("v1", 4*time.Hour, tfState(1, private))
	f.put("v4", time.Hour, tfState(4, public, role))
	f.put("v2", 3*time.Hour, tfState(2, private, queue))
	// Neither another key under the prefix nor an entry without a time is a state write.
	f.versions = append(f.versions,
		s3types.ObjectVersion{Key: aws.String(key + ".backup"), VersionId: aws.String("bk"), LastModified: aws.Time(time.Now())},
		s3types.ObjectVersion{Key: aws.String(key), VersionId: aws.String("nt")},
	)
	return f
}

func draw(t *testing.T, p *Terraform) (*ui.Buffer, string) {
	t.Helper()
	b := ui.NewBuffer(110, 24)
	p.Draw(b, b.Area())
	return b, b.Text()
}

func line(b *ui.Buffer, text string) string {
	_, y, ok := b.Find(text)
	if !ok {
		return ""
	}
	return strings.Split(b.Text(), "\n")[y]
}

func TestHistoryDiffsBetweenWrites(t *testing.T) {
	f := history()
	p := newPane(f)
	b, text := draw(t, p)
	if !strings.Contains(text, "Reading state history") || p.Status().Msg != "loading" {
		t.Errorf("before refresh:\n%s", text)
	}
	if err := p.Refresh(); err != nil {
		t.Fatal(err)
	}
	if f.reads["bk"]+f.reads["nt"] != 0 {
		t.Error("read a version that isn't a state write")
	}

	var serials []int64
	for _, v := range p.vers {
		serials = append(serials, v.serial)
	}
	if fmt.Sprint(serials) != "[4 3 2 1]" {
		t.Fatalf("serials newest first: %v", serials)
	}
	if p.vers[3].diff != nil {
		t.Error("oldest write has a diff without a base")
	}
	if d := p.vers[1].diff; fmt.Sprint(d.added, d.changed, d.removed) != "[aws_iam_role.r] [aws_s3_bucket.b] [aws_sqs_queue.q]" {
		t.Errorf("v3 diff %+v", d)
	}
	if v := p.vers[0]; v.total != 4 || v.tf != "1.17.2" || !v.diff.empty() {
		t.Errorf("v4 %+v", v)
	}

	b, text = draw(t, p)
	for s, want := range map[string][]string{
		"unlocked": {"unlocked · serial 4 · TF 1.17.2 · 4 resources"},
		"1h ":      {"  4  no change"},
		"2h ":      {"  3  +1 ~1 −1 ", "aws_iam_role.r, aws_s3_bucket.b, aws_sqs_queue.q"},
		"3h ":      {"  2  +1 ", "aws_sqs_queue.q"},
		"4h ":      {"  1  —"},
		"Written":  {"Serial", "Resources"},
	} {
		l := line(b, s)
		for _, w := range want {
			if !strings.Contains(l, w) {
				t.Errorf("line with %q is %q, want %q in\n%s", s, l, w, text)
			}
		}
	}
	if l := line(b, "3h "); strings.Contains(l, "~") || strings.Contains(l, "−") {
		t.Errorf("v2 only added: %q", l)
	}
	if st := p.Status(); st.State != "idle" || st.Msg != "serial 4 · last write 1h ago" {
		t.Errorf("status %+v", st)
	}
	if got := p.Title().String(); got != "s3://"+bucket+"/"+key {
		t.Errorf("title %q", got)
	}
}

func TestVersionsAreReadOnceAndForgotten(t *testing.T) {
	f := newFake()
	for i := range 20 { // v0 oldest … v19 newest
		f.put(fmt.Sprintf("v%d", i), time.Duration(20-i)*time.Hour, tfState(i+1, res("aws_sqs_queue", fmt.Sprint("q", i), map[string]any{})))
	}
	p := newPane(f)
	if err := p.Refresh(); err != nil {
		t.Fatal(err)
	}
	// 15 shown plus one base, newest first; the four oldest never read.
	if len(p.vers) != show || p.vers[0].serial != 20 || p.vers[show-1].serial != 6 {
		t.Fatalf("shown %d, %d…%d", len(p.vers), p.vers[0].serial, p.vers[len(p.vers)-1].serial)
	}
	if p.vers[show-1].diff == nil {
		t.Error("the oldest shown write lost its base")
	}
	if n := f.versionReads(); n != show+1 || f.reads["v3"] != 0 || f.reads["v4"] != 1 {
		t.Errorf("first refresh read %d versions (%v)", n, f.reads)
	}
	if len(p.snaps) != show+1 {
		t.Errorf("%d snapshots", len(p.snaps))
	}

	// Same versions: nothing is read again (only the lock file is checked).
	if err := p.Refresh(); err != nil {
		t.Fatal(err)
	}
	if n := f.versionReads(); n != show+1 {
		t.Errorf("second refresh read %d more versions", n-(show+1))
	}
	if f.reads[""] != 2 {
		t.Errorf("lock read %d times", f.reads[""])
	}

	// Two new writes: only they are read, and the two that dropped out are forgotten.
	f.put("v20", 30*time.Minute, tfState(21))
	f.put("v21", 10*time.Minute, tfState(22))
	if err := p.Refresh(); err != nil {
		t.Fatal(err)
	}
	if n := f.versionReads(); n != show+3 || f.reads["v20"] != 1 || f.reads["v21"] != 1 {
		t.Errorf("third refresh reads %v", f.reads)
	}
	if _, ok := p.snaps["v4"]; ok {
		t.Error("v4 dropped out but is still kept")
	}
	if _, ok := p.snaps["v5"]; ok {
		t.Error("v5 dropped out but is still kept")
	}
	if len(p.snaps) != show+1 || p.vers[0].serial != 22 {
		t.Errorf("%d snapshots, newest %d", len(p.snaps), p.vers[0].serial)
	}
	// v21 drops every queue v20 had: one removed, nothing else.
	if d := p.vers[0].diff; len(d.added)+len(d.changed) != 0 || len(d.removed) != 0 {
		t.Errorf("v21 diff %+v", d)
	}
	if d := p.vers[1].diff; fmt.Sprint(d.removed) != "[aws_sqs_queue.q19]" {
		t.Errorf("v20 diff %+v", d)
	}
}

func TestLockFile(t *testing.T) {
	f := history()
	created := time.Now().Add(-5 * time.Minute).UTC().Format(time.RFC3339Nano)
	f.lock = []byte(`{"ID":"00000000-0000-0000-0000-000000000000","Operation":"OperationTypeApply","Who":"dev@laptop","Version":"1.17.2","Created":"` + created + `","Info":""}`)
	p := newPane(f)
	if err := p.Refresh(); err != nil {
		t.Fatal(err)
	}
	if st := p.Status(); st.State != "working" || st.Msg != "locked · apply · dev@laptop" {
		t.Errorf("status %+v", st)
	}
	b, text := draw(t, p)
	if l := line(b, "LOCKED"); !strings.Contains(l, "apply · dev@laptop · 5m") || !strings.Contains(l, "serial 4") {
		t.Errorf("lock line %q in\n%s", l, text)
	}

	// Lock released; S3 says NotFound (HEAD-style) this time.
	f.lock = nil
	f.lockErr = &smithy.GenericAPIError{Code: "NotFound", Message: "Not Found"}
	if err := p.Refresh(); err != nil {
		t.Fatal(err)
	}
	if p.lock != nil || p.Status().State != "idle" {
		t.Errorf("still locked: %+v", p.Status())
	}
	if _, text := draw(t, p); strings.Contains(text, "LOCKED") || !strings.Contains(text, "unlocked") {
		t.Errorf("after unlock:\n%s", text)
	}

	// A lock with no owner still shows.
	f.lock = []byte(`{"Operation":"OperationTypePlan"}`)
	if err := p.Refresh(); err != nil {
		t.Fatal(err)
	}
	if st := p.Status(); st.Msg != "locked · plan · ?" {
		t.Errorf("status %+v", st)
	}
}

func TestErrorsSurface(t *testing.T) {
	denied := &smithy.GenericAPIError{Code: "AccessDenied", Message: "Access Denied"}
	for name, c := range map[string]struct {
		set  func(f *fakeS3)
		want string
	}{
		"list":      {func(f *fakeS3) { f.listErr = denied }, "AccessDenied: Access Denied"},
		"version":   {func(f *fakeS3) { f.getErr["v2"] = denied }, "AccessDenied: Access Denied"},
		"lock":      {func(f *fakeS3) { f.getErr[""] = denied }, "AccessDenied: Access Denied"},
		"bad state": {func(f *fakeS3) { f.objects["v3"] = []byte("{not json") }, "invalid character"},
	} {
		t.Run(name, func(t *testing.T) {
			f := history()
			c.set(f)
			p := newPane(f)
			err := p.Refresh()
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err %v, want %q", err, c.want)
			}
			if p.loaded {
				t.Error("loaded after an error")
			}
		})
	}
}

func TestVanishedVersionIsSkipped(t *testing.T) {
	f := history()
	delete(f.objects, "v2") // deleted between list and read
	p := newPane(f)
	if err := p.Refresh(); err != nil {
		t.Fatal(err)
	}
	if len(p.vers) != 3 || p.vers[2].serial != 1 || p.vers[1].diff != nil {
		t.Errorf("vers %+v", p.vers)
	}
}

func press(k tcell.Key) *tcell.EventKey { return tcell.NewEventKey(k, 0, 0) }
func rn(r rune) *tcell.EventKey         { return tcell.NewEventKey(tcell.KeyRune, r, 0) }

func TestViews(t *testing.T) {
	t.Setenv("REX_SESSION", "")
	p := newPane(history())
	if err := p.Refresh(); err != nil {
		t.Fatal(err)
	}
	table := len(p.Hints())

	// Enter on the newest write (no managed changes).
	_ = p.Key(press(tcell.KeyEnter))
	if _, text := draw(t, p); !strings.Contains(text, "serial 4") || !strings.Contains(text, "No managed resources changed") {
		t.Errorf("no-change view:\n%s", text)
	}
	if len(p.Hints()) == table {
		t.Error("view hints")
	}
	_ = p.Key(press(tcell.KeyEscape))
	if p.view != nil {
		t.Fatal("esc didn't go back")
	}

	// Down, Enter: the write that added, changed and removed.
	_ = p.Key(rn('j'))
	_ = p.Key(press(tcell.KeyEnter))
	_, text := draw(t, p)
	for _, s := range []string{"serial 3", "Added (1)", "+ aws_iam_role.r", "Changed (1)", "~ aws_s3_bucket.b", "Removed (1)", "- aws_sqs_queue.q"} {
		if !strings.Contains(text, s) {
			t.Errorf("missing %q in\n%s", s, text)
		}
	}
	_ = p.Key(press(tcell.KeyDown)) // scrolls the view, doesn't leave it
	if p.view == nil {
		t.Fatal("left the view on ↓")
	}
	_ = p.Key(press(tcell.KeyEscape))

	// The oldest write has nothing to compare with: Enter does nothing.
	_ = p.Key(press(tcell.KeyEnd))
	for range 5 {
		_ = p.Key(press(tcell.KeyDown))
	}
	if p.st.Sel != 3 {
		t.Fatalf("sel %d", p.st.Sel)
	}
	_ = p.Key(press(tcell.KeyEnter))
	if p.view != nil {
		t.Error("opened a diff for the oldest write")
	}

	// t: resources by type in the newest state, most first.
	_ = p.Key(rn('t'))
	b, text := draw(t, p)
	if !strings.Contains(text, "resources by type") {
		t.Fatalf("types view:\n%s", text)
	}
	_, ecs, _ := b.Find("aws_ecs_service")
	_, iam, _ := b.Find("aws_iam_role")
	_, s3b, _ := b.Find("aws_s3_bucket")
	if !(ecs < iam && iam < s3b) || !strings.Contains(line(b, "aws_ecs_service"), "2  aws_ecs_service") {
		t.Errorf("type order:\n%s", text)
	}
	_ = p.Key(press(tcell.KeyEscape))
	if _, text := draw(t, p); !strings.Contains(text, "State writes") {
		t.Errorf("back to the table:\n%s", text)
	}
	_ = p.Key(rn('o')) // opens nothing under test
	if p.Name() != "Terraform" || p.Every() != time.Minute {
		t.Error("pane basics")
	}
}

func TestNewFindsBackend(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config")
	if err := os.WriteFile(cfg, []byte("[default]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", dir)
	t.Setenv("AWS_CONFIG_FILE", cfg)
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(dir, "credentials"))
	t.Setenv("AWS_PROFILE", "")
	t.Setenv("AWS_REGION", "")
	t.Setenv("AWS_DEFAULT_REGION", "")
	t.Chdir(dir)

	if _, err := New("", "", time.Minute); err == nil || !strings.Contains(err.Error(), `no backend "s3"`) {
		t.Errorf("no .tf: %v", err)
	}
	tf := "terraform {\n  backend \"s3\" {\n    bucket = \"example-state\"\n    key    = \"env/terraform.tfstate\"\n  }\n}\n"
	if err := os.WriteFile(filepath.Join(dir, "main.tf"), []byte(tf), 0o644); err != nil {
		t.Fatal(err)
	}
	pane, err := New("", "", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	p := pane.(*Terraform)
	if p.bucket != bucket || p.key != key || p.region != "us-east-1" {
		t.Errorf("from main.tf: %q %q %q", p.bucket, p.key, p.region)
	}
	if _, ok := p.s3.(*s3.Client); !ok {
		t.Error("not a real client")
	}
	pane, err = New("other-bucket", "", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if p := pane.(*Terraform); p.bucket != "other-bucket" || p.key != "terraform.tfstate" {
		t.Errorf("explicit bucket: %q %q", p.bucket, p.key)
	}
}
