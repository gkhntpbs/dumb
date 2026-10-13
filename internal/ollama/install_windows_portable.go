package ollama

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

func (s *System) windowsRoot() string {
	if s.WindowsDataDir != "" {
		return s.WindowsDataDir
	}
	return filepath.Join(s.LocalAppData, "DUMB")
}
func (s *System) windowsBinary() string {
	return filepath.Join(s.windowsRoot(), "Ollama", "ollama.exe")
}

// ValidateWindowsArchive rejects Windows path aliases as well as traversal and
// archive bombs. No archive entry may write a symlink, device, ADS or reserved name.
func ValidateWindowsArchive(name string) error {
	archive, err := zip.OpenReader(name)
	if err != nil {
		return err
	}
	defer archive.Close()
	seen := map[string]bool{}
	var total uint64
	binary := false
	for _, entry := range archive.File {
		clean := path.Clean(entry.Name)
		if clean == "." || path.IsAbs(entry.Name) || strings.ContainsAny(entry.Name, "\\:") {
			return errors.New("ollama: unsafe Windows archive path")
		}
		for _, part := range strings.Split(entry.Name, "/") {
			if part == ".." || strings.TrimRight(part, " .") != part {
				return errors.New("ollama: unsafe Windows archive path component")
			}
			base := strings.ToUpper(strings.SplitN(part, ".", 2)[0])
			if base == "CON" || base == "PRN" || base == "AUX" || base == "NUL" || (len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && base[3] >= '1' && base[3] <= '9') {
				return errors.New("ollama: reserved Windows archive filename")
			}
		}
		key := strings.ToLower(clean)
		if seen[key] {
			return errors.New("ollama: duplicate Windows archive path")
		}
		seen[key] = true
		if entry.Mode()&fs.ModeType != 0 && !entry.FileInfo().IsDir() {
			return errors.New("ollama: nonregular Windows archive entry")
		}
		if entry.UncompressedSize64 > MaxExtractedBytes-total {
			return errors.New("ollama: Windows archive exceeds extraction limit")
		}
		total += entry.UncompressedSize64
		if clean == "ollama.exe" && !entry.FileInfo().IsDir() {
			binary = true
		}
	}
	if !binary {
		return errors.New("ollama: Windows archive is missing ollama.exe")
	}
	return nil
}

func (s *System) windowsDownload(ctx context.Context, dst string) error {
	if s.WindowsArchive == "" {
		return s.Download(ctx, WindowsDownloadURL, dst)
	}
	expected, err := hex.DecodeString(s.WindowsArchiveSHA256)
	if err != nil || len(expected) != sha256.Size {
		return errors.New("ollama: local official archive requires its trusted SHA256 digest")
	}
	source, err := os.Open(s.WindowsArchive)
	if err != nil {
		return err
	}
	defer source.Close()
	info, err := source.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > MaxInstallerBytes {
		return errors.New("ollama: invalid local official archive")
	}
	output, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	hasher := sha256.New()
	_, copyErr := io.Copy(io.MultiWriter(output, hasher), &contextReader{ctx: ctx, reader: source})
	closeErr := output.Close()
	if copyErr != nil || closeErr != nil {
		return errors.Join(copyErr, closeErr)
	}
	if !strings.EqualFold(hex.EncodeToString(hasher.Sum(nil)), s.WindowsArchiveSHA256) {
		return errors.New("ollama: local official archive SHA256 mismatch")
	}
	return nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

// installWindows publishes only a new, worker-owned portable installation. It
// never runs the GUI installer, modifies PATH, creates services or overwrites an
// existing install/model directory. The original official ZIP is extracted whole.
func (s *System) installWindows(ctx context.Context) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	root := s.windowsRoot()
	if !filepath.IsAbs(root) {
		return errors.New("ollama: absolute Windows data root required")
	}
	target := filepath.Join(root, "Ollama")
	if _, e := os.Lstat(target); e == nil {
		return fs.ErrExist
	} else if !errors.Is(e, fs.ErrNotExist) {
		return e
	}
	if e := os.MkdirAll(root, 0700); e != nil {
		return e
	}
	stage, e := os.MkdirTemp(root, ".ollama-install-")
	if e != nil {
		return e
	}
	defer func() { err = errors.Join(err, os.RemoveAll(stage)) }()
	archivePath := filepath.Join(stage, "official.zip")
	if e := s.windowsDownload(ctx, archivePath); e != nil {
		return e
	}
	if e := ValidateWindowsArchive(archivePath); e != nil {
		return e
	}
	archive, e := zip.OpenReader(archivePath)
	if e != nil {
		return e
	}
	defer archive.Close()
	content := filepath.Join(stage, "content")
	if e := os.Mkdir(content, 0700); e != nil {
		return e
	}
	destination, e := os.OpenRoot(content)
	if e != nil {
		return e
	}
	defer destination.Close()
	for _, entry := range archive.File {
		if e := ctx.Err(); e != nil {
			return e
		}
		name := filepath.FromSlash(path.Clean(entry.Name))
		if entry.FileInfo().IsDir() {
			if e := destination.MkdirAll(name, 0700); e != nil {
				return e
			}
			continue
		}
		if e := destination.MkdirAll(filepath.Dir(name), 0700); e != nil {
			return e
		}
		output, e := destination.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			return e
		}
		source, e := entry.Open()
		if e != nil {
			output.Close()
			return e
		}
		count, copyErr := io.Copy(output, &contextReader{ctx: ctx, reader: source})
		source.Close()
		closeErr := output.Close()
		if copyErr != nil || closeErr != nil {
			return errors.Join(copyErr, closeErr)
		}
		if uint64(count) != entry.UncompressedSize64 {
			return errors.New("ollama: Windows archive size mismatch")
		}
	}
	cli, e := destination.Open("ollama.exe")
	if e != nil {
		return e
	}
	var header [2]byte
	_, e = io.ReadFull(cli, header[:])
	cli.Close()
	if e != nil || string(header[:]) != "MZ" {
		return errors.New("ollama: Windows CLI is not an executable")
	}
	if e := destination.WriteFile(".dumb-owned.json", []byte("{\"schema\":1,\"source\":\"official-windows-standalone\"}\n"), 0600); e != nil {
		return e
	}
	// Close all handles before the directory move: Windows sharing rules apply.
	destination.Close()
	archive.Close()
	if e := ctx.Err(); e != nil {
		return e
	}
	if e := os.Rename(content, target); e != nil {
		return fmt.Errorf("ollama: publish Windows installation: %w", e)
	}
	return nil
}
