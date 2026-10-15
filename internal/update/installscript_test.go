package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/gkhntpbs/dumb/internal/update/updatetest"
)

// installScript is scripts/install.sh, run with POSIX sh against a local
// mirror signed with a throwaway key. A fake uname makes any Unix host look
// like Linux; the "binary" is a shell script that records its arguments.
type installScript struct {
	t        *testing.T
	files    map[string][]byte // served under /download/v<version>/
	version  string
	signer   *updatetest.Signer
	bin, out string
	server   *httptest.Server
	uname    string // uname -m answer
}

const fakeBinary = "#!/bin/sh\nprintf '%s\\n' \"$*\" > \"$FAKE_OUT\"\n"

func tarGz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		tw.Write([]byte(body))
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func newInstallScript(t *testing.T) *installScript {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX sh script")
	}
	out, err := exec.Command("openssl", "version").Output()
	if err != nil || !strings.HasPrefix(string(out), "OpenSSL 3") {
		t.Skipf("needs OpenSSL 3 on PATH (found %q)", strings.TrimSpace(string(out)))
	}
	s := &installScript{t: t, version: "1.2.3", signer: updatetest.NewSigner(), uname: "aarch64"}
	archive := ArchiveName(s.version, "linux", "arm64")
	s.files = map[string][]byte{archive: tarGz(t, map[string]string{"dumb-worker": fakeBinary, "LICENSE": "MIT"})}
	s.resign()
	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/gkhntpbs/dumb/releases/latest" {
			fmt.Fprintf(w, `{"url":"x","tag_name": "v%s","name":"v%s"}`, s.version, s.version)
			return
		}
		name, ok := strings.CutPrefix(r.URL.Path, "/download/v"+s.version+"/")
		if body, found := s.files[name]; ok && found {
			w.Write(body)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(s.server.Close)
	dir := t.TempDir()
	s.bin, s.out = filepath.Join(dir, "bin"), filepath.Join(dir, "installer-args")
	os.Mkdir(s.bin, 0o755)
	return s
}

// resign recomputes checksums.txt for the current archives and signs it.
func (s *installScript) resign() {
	var sums strings.Builder
	for name, body := range s.files {
		if strings.HasSuffix(name, ".tar.gz") {
			h := sha256.Sum256(body)
			fmt.Fprintf(&sums, "%s  %s\n", hex.EncodeToString(h[:]), name)
		}
	}
	s.files[ChecksumsName] = []byte(sums.String())
	s.files[SignatureName] = s.signer.Sign(s.files[ChecksumsName], "timestamp:1 file:checksums.txt")
}

func (s *installScript) run(pubkey string, args ...string) (string, error) {
	s.t.Helper()
	uname := fmt.Sprintf("#!/bin/sh\ncase \"$1\" in -s) echo Linux ;; -m) echo %s ;; *) echo Linux ;; esac\n", s.uname)
	if err := os.WriteFile(filepath.Join(s.bin, "uname"), []byte(uname), 0o755); err != nil {
		s.t.Fatal(err)
	}
	os.Remove(s.out)
	cmd := exec.Command("sh", append([]string{"../../scripts/install.sh"}, args...)...)
	cmd.Env = append(os.Environ(),
		"PATH="+s.bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"DUMB_INSTALL_API="+s.server.URL, "DUMB_INSTALL_DOWNLOAD="+s.server.URL+"/download",
		"DUMB_INSTALL_PUBKEY="+pubkey, "FAKE_OUT="+s.out, "TMPDIR="+s.t.TempDir())
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (s *installScript) installerArgs() string {
	b, err := os.ReadFile(s.out)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func TestInstallScriptVerifiesAndInstalls(t *testing.T) {
	s := newInstallScript(t)
	out, err := s.run(s.signer.PublicKey(), "-user", "-server", "https://dumb.example", "-models", "m")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if got := s.installerArgs(); got != "install -user -server https://dumb.example -models m" {
		t.Fatalf("installer args %q\n%s", got, out)
	}
	if !strings.Contains(out, "signature: ok") || !strings.Contains(out, "dumb-worker 1.2.3 for linux/arm64") {
		t.Fatalf("output:\n%s", out)
	}
	// An explicit --version and --download-only stop before installing.
	dest := filepath.Join(t.TempDir(), "verified")
	out, err = s.run(s.signer.PublicKey(), "--version", "v1.2.3", "--download-only", dest)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if b, _ := os.ReadFile(filepath.Join(dest, "dumb-worker")); string(b) != fakeBinary || s.installerArgs() != "" {
		t.Fatalf("download-only: %q, installer ran: %q", b, s.installerArgs())
	}
}

func TestInstallScriptRefusesTampering(t *testing.T) {
	archive := ArchiveName("1.2.3", "linux", "arm64")
	reasons := map[string]string{
		"archive replaced": "does not match the signed", "checksums edited": "signature does not verify",
		"other key": "another key", "legacy signature": "not prehashed", "trusted comment edited": "trusted comment signature does not verify",
		"same key id, other secret": "signature does not verify", "missing signature": "cannot download checksums.txt.minisig",
		"unsupported arch": "unsupported CPU architecture",
	}
	cases := map[string]func(s *installScript) string{ // returns the public key to use
		"archive replaced": func(s *installScript) string {
			s.files[archive] = tarGz(t, map[string]string{"dumb-worker": "#!/bin/sh\necho evil > \"$FAKE_OUT\"\n"})
			return s.signer.PublicKey()
		},
		"checksums edited": func(s *installScript) string {
			s.files[archive] = tarGz(t, map[string]string{"dumb-worker": "#!/bin/sh\necho evil > \"$FAKE_OUT\"\n"})
			sig := s.files[SignatureName]
			s.resign()
			s.files[SignatureName] = sig // the old signature over the old checksums
			return s.signer.PublicKey()
		},
		"other key": func(s *installScript) string {
			return updatetest.NewSigner().PublicKey()
		},
		"legacy signature": func(s *installScript) string {
			s.files[SignatureName] = s.signer.SignLegacy(s.files[ChecksumsName], "timestamp:1")
			return s.signer.PublicKey()
		},
		"trusted comment edited": func(s *installScript) string {
			s.files[SignatureName] = bytes.Replace(s.files[SignatureName], []byte("timestamp:1"), []byte("timestamp:2"), 1)
			return s.signer.PublicKey()
		},
		"same key id, other secret": func(s *installScript) string {
			forger := updatetest.NewSigner()
			forger.ID = s.signer.ID
			s.files[SignatureName] = forger.Sign(s.files[ChecksumsName], "timestamp:1")
			return s.signer.PublicKey()
		},
		"missing signature": func(s *installScript) string {
			delete(s.files, SignatureName)
			return s.signer.PublicKey()
		},
		"unsupported arch": func(s *installScript) string {
			s.uname = "armv7l"
			return s.signer.PublicKey()
		},
	}
	for name, tamper := range cases {
		t.Run(name, func(t *testing.T) {
			s := newInstallScript(t)
			key := tamper(s)
			out, err := s.run(key, "-user")
			if err == nil || !strings.Contains(out, reasons[name]) {
				t.Fatalf("err %v, want a refusal with %q:\n%s", err, reasons[name], out)
			}
			if got := s.installerArgs(); got != "" {
				t.Fatalf("the downloaded binary ran (%q):\n%s", got, out)
			}
		})
	}
}
