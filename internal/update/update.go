// Package update is dumb-worker's self-updater: it finds a release on GitHub,
// verifies its minisign-signed checksums, swaps the binary atomically and rolls
// back when the new version cannot reach the server. See docs/release.md.
//
// Only dumb-worker imports this package; dumb-server must not (it would pull
// golang.org/x/crypto into the server).
package update

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path"
	"strings"
	"time"

	"github.com/gkhntpbs/dumb/internal/protocol"
)

// PublicKey is the minisign public key releases are verified against, set at
// build time:
//
//	-ldflags "-X github.com/gkhntpbs/dumb/internal/update.PublicKey=RW..."
//
// Empty means self-update is disabled.
var PublicKey = ""

// ErrDisabled means no release key is embedded.
var ErrDisabled = errors.New("update: no release key embedded; self-update disabled")

// ErrPending means an applied update is still on probation; no new update starts.
var ErrPending = errors.New("update: a previous update is still on probation")

// Asset limits.
const (
	maxArchive   = 256 << 20
	maxChecksums = 1 << 20
	maxBinary    = 512 << 20
)

// Updater checks for and applies releases of dumb-worker.
type Updater struct {
	Exe     string // path of the running binary (symlinks resolved)
	Version string // the running version
	Channel string // ChannelStable (default) or ChannelBeta
	Key     string // minisign public key; "" disables updates
	Source  *Source
	GOOS    string
	GOARCH  string
	Logger  *log.Logger
	Now     func() time.Time
}

func (u *Updater) logf(format string, args ...any) {
	if u.Logger != nil {
		u.Logger.Printf(format, args...)
	}
}

func (u *Updater) now() time.Time {
	if u.Now != nil {
		return u.Now()
	}
	return time.Now()
}

// BinaryName is the executable inside the archive.
func BinaryName(goos string) string {
	if goos == "windows" {
		return "dumb-worker.exe"
	}
	return "dumb-worker"
}

// ArchiveName is the goreleaser archive for one platform (see .goreleaser.yaml).
func ArchiveName(version, goos, goarch string) string {
	ext := ".tar.gz"
	if goos == "windows" {
		ext = ".zip"
	}
	return "dumb-worker_" + version + "_" + goos + "_" + goarch + ext
}

// Checksum file names.
const (
	ChecksumsName = "checksums.txt"
	SignatureName = "checksums.txt.minisig"
)

// Update applies the newest qualifying release. min, when non-empty, is the
// server's minimum version: the release must be at least that. On success the
// new binary is in place and the caller must exit with the restart code; on
// any error nothing has changed on disk except leftover temporary files.
func (u *Updater) Update(ctx context.Context, min string) (protocol.ReleaseVersion, error) {
	var none protocol.ReleaseVersion
	if u.Key == "" {
		return none, ErrDisabled
	}
	key, err := ParseKey(u.Key)
	if err != nil {
		return none, err
	}
	if _, err := os.Stat(statePath(u.Exe)); err == nil {
		return none, ErrPending
	}
	cur, err := protocol.ParseReleaseVersion(u.Version)
	if err != nil {
		return none, err
	}
	var minV *protocol.ReleaseVersion
	if min != "" {
		m, err := protocol.ParseReleaseVersion(min)
		if err != nil {
			return none, err
		}
		minV = &m
	}
	rels, err := u.Source.Releases(ctx)
	if err != nil {
		return none, err
	}
	channel := u.Channel
	if channel == "" {
		channel = ChannelStable
	}
	rel, err := Pick(rels, channel, cur, minV, readSkip(u.Exe))
	if err != nil {
		return none, err
	}
	u.logf("update: %s → %s (%s channel)", cur, rel.Version, channel)
	bin, err := u.fetch(ctx, key, rel)
	if err != nil {
		return none, fmt.Errorf("update %s: %w", rel.Version, err)
	}
	if err := u.swap(bin, cur.String(), rel.Version.String()); err != nil {
		return none, fmt.Errorf("update %s: %w", rel.Version, err)
	}
	u.logf("update: %s installed; restarting", rel.Version)
	return rel.Version, nil
}

// fetch downloads and verifies a release and returns the worker binary. The
// signature is checked first, then the archive's SHA-256 against the signed
// checksums; only then is the archive opened.
func (u *Updater) fetch(ctx context.Context, key Key, rel Release) ([]byte, error) {
	asset := func(name string) (string, error) {
		if url, ok := rel.Assets[name]; ok {
			return url, nil
		}
		return "", fmt.Errorf("release %s has no asset %s", rel.Tag, name)
	}
	archive := ArchiveName(rel.Version.String(), u.GOOS, u.GOARCH)
	sumURL, err := asset(ChecksumsName)
	if err != nil {
		return nil, err
	}
	sigURL, err := asset(SignatureName)
	if err != nil {
		return nil, err
	}
	arcURL, err := asset(archive)
	if err != nil {
		return nil, err
	}
	sums, err := u.Source.get(ctx, sumURL, maxChecksums)
	if err != nil {
		return nil, err
	}
	sig, err := u.Source.get(ctx, sigURL, 64<<10)
	if err != nil {
		return nil, err
	}
	if _, err := key.Verify(sums, sig); err != nil {
		return nil, err
	}
	want, err := checksumFor(sums, archive)
	if err != nil {
		return nil, err
	}
	data, err := u.Source.get(ctx, arcURL, maxArchive)
	if err != nil {
		return nil, err
	}
	if got := sha256.Sum256(data); !bytes.Equal(got[:], want) {
		return nil, fmt.Errorf("%s: SHA-256 %x does not match the signed checksum %x", archive, got, want)
	}
	return extract(data, archive, BinaryName(u.GOOS))
}

// checksumFor finds name in a sha256sum-style file ("<hex>  <name>").
func checksumFor(sums []byte, name string) ([]byte, error) {
	sc := bufio.NewScanner(bytes.NewReader(sums))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == name {
			b, err := hex.DecodeString(f[0])
			if err != nil || len(b) != sha256.Size {
				return nil, fmt.Errorf("checksums: bad entry for %s", name)
			}
			return b, nil
		}
	}
	return nil, fmt.Errorf("checksums: no entry for %s", name)
}

// extract returns the regular file called bin from a .tar.gz or .zip archive.
func extract(data []byte, archive, bin string) ([]byte, error) {
	if strings.HasSuffix(archive, ".zip") {
		zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			return nil, err
		}
		for _, f := range zr.File {
			if path.Base(f.Name) == bin && f.Mode().IsRegular() {
				rc, err := f.Open()
				if err != nil {
					return nil, err
				}
				defer rc.Close()
				return readLimited(rc)
			}
		}
		return nil, fmt.Errorf("%s: no %s inside", archive, bin)
	}
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil, fmt.Errorf("%s: no %s inside", archive, bin)
		}
		if err != nil {
			return nil, err
		}
		if path.Base(h.Name) == bin && h.Typeflag == tar.TypeReg {
			return readLimited(tr)
		}
	}
}

func readLimited(r io.Reader) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, maxBinary+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxBinary || len(b) == 0 {
		return nil, errors.New("binary is empty or too large")
	}
	return b, nil
}

// swap installs bin as u.Exe: <exe>.new is written and synced, the running
// binary is renamed to <exe>.prev, <exe>.new is renamed into place and the
// state file records the probation.
func (u *Updater) swap(bin []byte, from, to string) error {
	exe := u.Exe
	newPath, prev := exe+".new", exe+".prev"
	if err := writeSynced(newPath, bin, 0o755); err != nil {
		os.Remove(newPath)
		return err
	}
	if err := os.Chmod(newPath, 0o755); err != nil {
		os.Remove(newPath)
		return err
	}
	os.Remove(prev) // a leftover from a committed update
	if err := rename(exe, prev); err != nil {
		os.Remove(newPath)
		return err
	}
	if err := rename(newPath, exe); err != nil {
		if rerr := rename(prev, exe); rerr != nil {
			return fmt.Errorf("%w (and restoring %s failed: %v)", err, exe, rerr)
		}
		os.Remove(newPath)
		return err
	}
	st := state{From: from, To: to, StartedAt: u.now().UTC()}
	if err := writeState(exe, st); err != nil {
		// Without a state file there is no probation: put the old binary back.
		rename(exe, newPath)
		rename(prev, exe)
		os.Remove(newPath)
		return err
	}
	return nil
}

// rename is os.Rename; tests replace it to inject failures.
var rename = os.Rename

func writeSynced(name string, data []byte, mode os.FileMode) error {
	f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
