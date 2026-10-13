package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gkhntpbs/dumb/internal/protocol"
	"github.com/gkhntpbs/dumb/internal/update/updatetest"
)

const goos, goarch = "linux", "amd64"

// fakeRelease is one release served by fakeGitHub.
type fakeRelease struct {
	tag        string
	prerelease bool
	binary     []byte             // the dumb-worker inside the archive
	signer     *updatetest.Signer // signs checksums.txt
	tamper     bool               // serve an archive that differs from its checksum
	noSig      bool
}

func makeArchive(t *testing.T, name, bin string, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	if strings.HasSuffix(name, ".zip") {
		zw := zip.NewWriter(&buf)
		w, _ := zw.Create("LICENSE")
		w.Write([]byte("MIT"))
		w, _ = zw.Create(bin)
		w.Write(content)
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		return buf.Bytes()
	}
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, f := range []struct {
		name string
		data []byte
	}{{"LICENSE", []byte("MIT")}, {bin, content}} {
		tw.WriteHeader(&tar.Header{Name: f.name, Mode: 0o755, Size: int64(len(f.data)), Typeflag: tar.TypeReg})
		tw.Write(f.data)
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

// fakeGitHub serves /repos/gkhntpbs/dumb/releases and the release assets.
func fakeGitHub(t *testing.T, rels ...fakeRelease) *httptest.Server {
	t.Helper()
	assets := map[string][]byte{}
	type asset struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	}
	type release struct {
		Tag        string  `json:"tag_name"`
		Draft      bool    `json:"draft"`
		Prerelease bool    `json:"prerelease"`
		Assets     []asset `json:"assets"`
	}
	ts := httptest.NewUnstartedServer(nil)
	ts.Start()
	t.Cleanup(ts.Close)
	var list []release
	for _, r := range rels {
		ver := strings.TrimPrefix(r.tag, "v")
		rel := release{Tag: r.tag, Prerelease: r.prerelease}
		files := map[string][]byte{}
		var sums strings.Builder
		for _, os := range []string{"linux", "windows"} {
			name := ArchiveName(ver, os, goarch)
			data := makeArchive(t, name, BinaryName(os), r.binary)
			fmt.Fprintf(&sums, "%x  %s\n", sha256.Sum256(data), name)
			if r.tamper {
				data = makeArchive(t, name, BinaryName(os), append([]byte("EVIL"), r.binary...))
			}
			files[name] = data
		}
		files[ChecksumsName] = []byte(sums.String())
		if !r.noSig {
			files[SignatureName] = r.signer.Sign(files[ChecksumsName], "file:checksums.txt")
		}
		for name, data := range files {
			p := "/download/" + r.tag + "/" + name
			assets[p] = data
			rel.Assets = append(rel.Assets, asset{name, ts.URL + p})
		}
		list = append(list, rel)
	}
	list = append(list, release{Tag: "v9.9.9", Draft: true}, release{Tag: "not-a-version"})
	ts.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/gkhntpbs/dumb/releases" {
			json.NewEncoder(w).Encode(list)
			return
		}
		data, ok := assets[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(data)
	})
	return ts
}

// setup writes the "running" binary and returns an updater for it.
func setup(t *testing.T, signer *updatetest.Signer, ts *httptest.Server) (*Updater, []byte) {
	t.Helper()
	exe := filepath.Join(t.TempDir(), "dumb-worker")
	old := []byte("#!old binary 0.1.0")
	if err := os.WriteFile(exe, old, 0o755); err != nil {
		t.Fatal(err)
	}
	return &Updater{
		Exe: exe, Version: "0.1.0", Key: signer.PublicKey(), GOOS: goos, GOARCH: goarch,
		Source: &Source{BaseURL: ts.URL},
	}, old
}

// assertUntouched checks that exe is byte-identical to want and nothing was staged.
func assertUntouched(t *testing.T, exe string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(exe)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("%s changed: %q, %v", exe, got, err)
	}
	for _, suffix := range []string{".new", ".prev", ".update.json"} {
		if _, err := os.Stat(exe + suffix); err == nil {
			t.Errorf("%s%s exists", exe, suffix)
		}
	}
}

func mustRead(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestUpdateCommit(t *testing.T) {
	s := updatetest.NewSigner()
	newBin := []byte("#!new binary 0.2.0")
	ts := fakeGitHub(t, fakeRelease{tag: "v0.2.0", binary: newBin, signer: s})
	u, old := setup(t, s, ts)

	v, err := u.Update(context.Background(), "0.2.0")
	if err != nil || v.String() != "0.2.0" {
		t.Fatalf("Update: %v, %v", v, err)
	}
	if got := mustRead(t, u.Exe); !bytes.Equal(got, newBin) {
		t.Fatalf("exe = %q", got)
	}
	if got := mustRead(t, u.Exe+".prev"); !bytes.Equal(got, old) {
		t.Fatalf(".prev = %q", got)
	}
	if fi, _ := os.Stat(u.Exe); runtime.GOOS != "windows" && fi.Mode().Perm()&0o100 == 0 {
		t.Errorf("exe mode %v is not executable", fi.Mode())
	}
	var st state
	json.Unmarshal(mustRead(t, statePath(u.Exe)), &st)
	if st.From != "0.1.0" || st.To != "0.2.0" || st.Attempts != 0 || st.StartedAt.IsZero() {
		t.Fatalf("state = %+v", st)
	}
	// No second update while the first is on probation.
	if _, err := u.Update(context.Background(), ""); !errors.Is(err, ErrPending) {
		t.Errorf("second Update: %v, want ErrPending", err)
	}

	// The restarted binary (0.2.0) says hello: the update is committed.
	p, err := Startup(u.Exe, "0.2.0", time.Minute, DefaultMaxAttempts, nil)
	if err != nil || p == nil || p.Attempt != 1 {
		t.Fatalf("Startup: %+v, %v", p, err)
	}
	ok := make(chan struct{})
	close(ok)
	if err := p.Watch(context.Background(), ok); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{".prev", ".update.json", ".new"} {
		if _, err := os.Stat(u.Exe + suffix); err == nil {
			t.Errorf("%s left after commit", suffix)
		}
	}
	if got := mustRead(t, u.Exe); !bytes.Equal(got, newBin) {
		t.Fatalf("exe after commit = %q", got)
	}
	// Nothing pending any more.
	if p, err := Startup(u.Exe, "0.2.0", time.Minute, DefaultMaxAttempts, nil); p != nil || err != nil {
		t.Errorf("Startup after commit: %+v, %v", p, err)
	}
}

func TestUpdateZipOnWindows(t *testing.T) {
	s := updatetest.NewSigner()
	ts := fakeGitHub(t, fakeRelease{tag: "v0.2.0", binary: []byte("MZ new"), signer: s})
	u, _ := setup(t, s, ts)
	u.GOOS = "windows"
	if _, err := u.Update(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	if got := mustRead(t, u.Exe); string(got) != "MZ new" {
		t.Fatalf("exe = %q", got)
	}
}

func TestTamperedArchiveNeverApplied(t *testing.T) {
	s := updatetest.NewSigner()
	ts := fakeGitHub(t, fakeRelease{tag: "v0.2.0", binary: []byte("new"), signer: s, tamper: true})
	u, old := setup(t, s, ts)
	_, err := u.Update(context.Background(), "")
	if err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("Update: %v, want a checksum mismatch", err)
	}
	assertUntouched(t, u.Exe, old)
}

func TestBadSignatureNeverApplied(t *testing.T) {
	s := updatetest.NewSigner()
	for name, rel := range map[string]fakeRelease{
		"signed by another key": {tag: "v0.2.0", binary: []byte("new"), signer: updatetest.NewSigner()},
		"no signature":          {tag: "v0.2.0", binary: []byte("new"), signer: s, noSig: true},
	} {
		t.Run(name, func(t *testing.T) {
			u, old := setup(t, s, fakeGitHub(t, rel))
			if _, err := u.Update(context.Background(), ""); err == nil {
				t.Fatal("Update succeeded")
			}
			assertUntouched(t, u.Exe, old)
		})
	}
}

func TestDisabledWithoutKey(t *testing.T) {
	s := updatetest.NewSigner()
	u, old := setup(t, s, fakeGitHub(t, fakeRelease{tag: "v0.2.0", binary: []byte("new"), signer: s}))
	u.Key = ""
	if _, err := u.Update(context.Background(), ""); !errors.Is(err, ErrDisabled) {
		t.Fatalf("got %v, want ErrDisabled", err)
	}
	assertUntouched(t, u.Exe, old)
}

func TestSwapFailureRestoresBinary(t *testing.T) {
	s := updatetest.NewSigner()
	u, old := setup(t, s, fakeGitHub(t, fakeRelease{tag: "v0.2.0", binary: []byte("new"), signer: s}))
	calls := 0
	rename = func(a, b string) error {
		calls++
		if calls == 2 { // <exe>.new → <exe>
			return errors.New("injected rename failure")
		}
		return os.Rename(a, b)
	}
	t.Cleanup(func() { rename = os.Rename })
	if _, err := u.Update(context.Background(), ""); err == nil {
		t.Fatal("Update succeeded")
	}
	assertUntouched(t, u.Exe, old)
}

func TestRollbackWhenServerUnreachable(t *testing.T) {
	s := updatetest.NewSigner()
	u, old := setup(t, s, fakeGitHub(t, fakeRelease{tag: "v0.2.0", binary: []byte("broken 0.2.0"), signer: s}))
	if _, err := u.Update(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	p, err := Startup(u.Exe, "0.2.0", 20*time.Millisecond, DefaultMaxAttempts, nil)
	if err != nil || p == nil {
		t.Fatalf("Startup: %v", err)
	}
	// Hello never succeeds.
	if err := p.Watch(context.Background(), make(chan struct{})); !errors.Is(err, ErrRestart) {
		t.Fatalf("Watch: %v, want ErrRestart", err)
	}
	assertUntouched(t, u.Exe, old)
	if skip := readSkip(u.Exe); !slices.Equal(skip, []string{"0.2.0"}) {
		t.Fatalf("skip = %v", skip)
	}
	// The old binary is back and does not retry the bad version.
	if _, err := u.Update(context.Background(), ""); !errors.Is(err, ErrNoUpdate) {
		t.Fatalf("Update after rollback: %v, want ErrNoUpdate", err)
	}
	assertUntouched(t, u.Exe, old)
}

func TestCrashLoopRollback(t *testing.T) {
	s := updatetest.NewSigner()
	u, old := setup(t, s, fakeGitHub(t, fakeRelease{tag: "v0.2.0", binary: []byte("crashy 0.2.0"), signer: s}))
	if _, err := u.Update(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	// Three starts that die before hello (no Watch outcome).
	for i := 1; i <= DefaultMaxAttempts; i++ {
		p, err := Startup(u.Exe, "0.2.0", time.Minute, DefaultMaxAttempts, nil)
		if err != nil || p == nil || p.Attempt != i {
			t.Fatalf("start %d: %+v, %v", i, p, err)
		}
	}
	p, err := Startup(u.Exe, "0.2.0", time.Minute, DefaultMaxAttempts, nil)
	if p != nil || !errors.Is(err, ErrRestart) {
		t.Fatalf("start 4: %+v, %v, want ErrRestart", p, err)
	}
	assertUntouched(t, u.Exe, old)
	if !slices.Contains(readSkip(u.Exe), "0.2.0") {
		t.Fatal("0.2.0 not skipped")
	}
}

func TestSkipListLetsNewerVersionThrough(t *testing.T) {
	s := updatetest.NewSigner()
	ts := fakeGitHub(t,
		fakeRelease{tag: "v0.2.0", binary: []byte("0.2.0"), signer: s},
		fakeRelease{tag: "v0.2.1", binary: []byte("0.2.1"), signer: s})
	u, _ := setup(t, s, ts)
	if err := addSkip(u.Exe, "0.2.1"); err != nil {
		t.Fatal(err)
	}
	v, err := u.Update(context.Background(), "")
	if err != nil || v.String() != "0.2.0" {
		t.Fatalf("got %v, %v; want 0.2.0 (0.2.1 is skipped)", v, err)
	}
}

func TestChannels(t *testing.T) {
	s := updatetest.NewSigner()
	ts := fakeGitHub(t,
		fakeRelease{tag: "v0.2.0", binary: []byte("stable 0.2.0"), signer: s},
		fakeRelease{tag: "v0.3.0-beta.1", prerelease: true, binary: []byte("beta 0.3.0-beta.1"), signer: s})
	for _, c := range []struct{ channel, want, bin string }{
		{"", "0.2.0", "stable 0.2.0"},
		{ChannelStable, "0.2.0", "stable 0.2.0"},
		{ChannelBeta, "0.3.0-beta.1", "beta 0.3.0-beta.1"},
	} {
		u, _ := setup(t, s, ts)
		u.Channel = c.channel
		v, err := u.Update(context.Background(), "")
		if err != nil || v.String() != c.want {
			t.Errorf("channel %q: got %v, %v; want %s", c.channel, v, err, c.want)
			continue
		}
		if got := mustRead(t, u.Exe); string(got) != c.bin {
			t.Errorf("channel %q: exe = %q", c.channel, got)
		}
	}
}

func TestPick(t *testing.T) {
	v := func(s string) protocol.ReleaseVersion {
		r, err := protocol.ParseReleaseVersion(s)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	rels := []Release{
		{Tag: "v0.1.0", Version: v("0.1.0")},
		{Tag: "v0.2.0", Version: v("0.2.0")},
		{Tag: "v0.3.0-beta.1", Version: v("0.3.0-beta.1"), Prerelease: true},
		{Tag: "v0.2.1-beta.2", Version: v("0.2.1-beta.2")}, // pre-release by tag even if not flagged
	}
	min := v("0.3.0-beta.1")
	cases := []struct {
		name, channel, current string
		min                    *protocol.ReleaseVersion
		skip                   []string
		want                   string // "" = ErrNoUpdate
	}{
		{"stable newest", ChannelStable, "0.1.0", nil, nil, "0.2.0"},
		{"beta newest", ChannelBeta, "0.1.0", nil, nil, "0.3.0-beta.1"},
		{"up to date", ChannelStable, "0.2.0", nil, nil, ""},
		{"never downgrade", ChannelStable, "0.5.0", nil, nil, ""},
		{"stable cannot meet a beta minimum", ChannelStable, "0.1.0", &min, nil, ""},
		{"beta meets it", ChannelBeta, "0.1.0", &min, nil, "0.3.0-beta.1"},
		{"skip", ChannelBeta, "0.1.0", nil, []string{"0.3.0-beta.1"}, "0.2.1-beta.2"},
		{"beta worker gets the release", ChannelBeta, "0.2.0-beta.3", nil, []string{"0.3.0-beta.1", "0.2.1-beta.2"}, "0.2.0"},
	}
	for _, c := range cases {
		got, err := Pick(rels, c.channel, v(c.current), c.min, c.skip)
		switch {
		case c.want == "" && !errors.Is(err, ErrNoUpdate):
			t.Errorf("%s: got %v, %v; want ErrNoUpdate", c.name, got.Version, err)
		case c.want != "" && (err != nil || got.Version.String() != c.want):
			t.Errorf("%s: got %v, %v; want %s", c.name, got.Version, err, c.want)
		}
	}
}

func TestStartupDiscardsForeignState(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "dumb-worker")
	os.WriteFile(exe, []byte("x"), 0o755)
	if err := writeState(exe, state{From: "0.1.0", To: "0.2.0"}); err != nil {
		t.Fatal(err)
	}
	if p, err := Startup(exe, "0.1.0", time.Minute, 3, nil); p != nil || err != nil {
		t.Fatalf("got %+v, %v", p, err)
	}
	if _, err := os.Stat(statePath(exe)); err == nil {
		t.Fatal("state file kept")
	}
}

func TestRejectsPlainHTTP(t *testing.T) {
	src := &Source{BaseURL: "http://example.com"}
	if _, err := src.Releases(context.Background()); err == nil || !strings.Contains(err.Error(), "https") {
		t.Fatalf("got %v", err)
	}
}

func TestStartupCleansExitedRollbackImage(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "worker")
	if err := os.WriteFile(exe+".bad", []byte("exited rollback image"), 0600); err != nil {
		t.Fatal(err)
	}
	probation, err := Startup(exe, "0.1.0", DefaultWindow, DefaultMaxAttempts, nil)
	if err != nil || probation != nil {
		t.Fatalf("fresh startup: probation=%v error=%v", probation, err)
	}
	if _, err := os.Stat(exe + ".bad"); !os.IsNotExist(err) {
		t.Fatalf("stale rollback image remains: %v", err)
	}
}
