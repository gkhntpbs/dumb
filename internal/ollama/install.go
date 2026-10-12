package ollama

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const MaxInstallerBytes int64 = 2 << 30
const MaxExtractedBytes uint64 = 8 << 30

type InstallFileSystem interface {
	Lstat(string) (fs.FileInfo, error)
	Stat(string) (fs.FileInfo, error)
	MkdirAll(string, fs.FileMode) error
	MkdirTemp(string, string) (string, error)
	RemoveAll(string) error
	Remove(string) error
	Symlink(string, string) error
}

type OSInstallFileSystem struct{}

func (OSInstallFileSystem) Lstat(p string) (fs.FileInfo, error)    { return os.Lstat(p) }
func (OSInstallFileSystem) Stat(p string) (fs.FileInfo, error)     { return os.Stat(p) }
func (OSInstallFileSystem) MkdirAll(p string, m fs.FileMode) error { return os.MkdirAll(p, m) }
func (OSInstallFileSystem) MkdirTemp(p, prefix string) (string, error) {
	return os.MkdirTemp(p, prefix)
}
func (OSInstallFileSystem) RemoveAll(p string) error       { return os.RemoveAll(p) }
func (OSInstallFileSystem) Remove(p string) error          { return os.Remove(p) }
func (OSInstallFileSystem) Symlink(target, p string) error { return os.Symlink(target, p) }

func contained(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func ValidateMacArchive(p string) error {
	archive, err := zip.OpenReader(p)
	if err != nil {
		return fmt.Errorf("ollama: invalid installer archive: %w", err)
	}
	defer archive.Close()
	var total uint64
	seen := map[string]bool{}
	symlinks := map[string]string{}
	for _, entry := range archive.File {
		clean := path.Clean(entry.Name)
		for _, part := range strings.Split(entry.Name, "/") {
			if part == ".." {
				return errors.New("ollama: unsafe installer parent traversal")
			}
		}
		if seen[clean] {
			return errors.New("ollama: duplicate installer archive path")
		}
		seen[clean] = true
		if strings.Contains(entry.Name, "\\") || path.IsAbs(entry.Name) || clean == ".." || strings.HasPrefix(clean, "../") || (clean != "Ollama.app" && !strings.HasPrefix(clean, "Ollama.app/") && clean != "__MACOSX" && !strings.HasPrefix(clean, "__MACOSX/")) {
			return errors.New("ollama: unsafe installer archive path")
		}
		if entry.UncompressedSize64 > MaxExtractedBytes-total {
			return errors.New("ollama: installer archive exceeds extraction limit")
		}
		total += entry.UncompressedSize64
		if entry.Mode()&fs.ModeSymlink != 0 {
			if entry.UncompressedSize64 > 4096 {
				return errors.New("ollama: unsafe archive symlink")
			}
			stream, err := entry.Open()
			if err != nil {
				return err
			}
			target, readErr := io.ReadAll(io.LimitReader(stream, 4097))
			stream.Close()
			if readErr != nil {
				return readErr
			}
			for _, part := range strings.Split(string(target), "/") {
				if part == ".." {
					return errors.New("ollama: unsafe archive symlink parent traversal")
				}
			}
			resolved := path.Clean(path.Join(path.Dir(clean), string(target)))
			symlinks[clean] = resolved
			if path.IsAbs(string(target)) || strings.Contains(string(target), "\\") || !strings.HasPrefix(resolved, "Ollama.app/") {
				return errors.New("ollama: archive symlink escapes bundle")
			}
		}
	}
	for name := range seen {
		for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
			if _, ok := symlinks[parent]; ok {
				return errors.New("ollama: installer writes through archive symlink")
			}
		}
	}
	return nil
}

func (s *System) installMac(ctx context.Context) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !filepath.IsAbs(s.Home) {
		return errors.New("ollama: absolute home directory required")
	}
	if s.FS == nil || s.ValidateArchive == nil {
		return errors.New("ollama: macOS install filesystem is not configured")
	}
	root := filepath.Join(s.Home, "Applications")
	target := filepath.Join(root, "Ollama.app")
	if _, e := s.FS.Lstat(target); e == nil {
		return fs.ErrExist
	} else if !errors.Is(e, fs.ErrNotExist) {
		return e
	}
	if e := s.FS.MkdirAll(root, 0755); e != nil {
		return e
	}
	stage, e := s.FS.MkdirTemp(root, ".dumb-ollama-install-")
	if e != nil {
		return e
	}
	if !contained(root, stage) || filepath.Clean(root) == filepath.Clean(stage) {
		return errors.New("ollama: staging directory is outside install root")
	}
	published := false
	defer func() {
		if !published {
			err = errors.Join(err, s.FS.RemoveAll(stage))
		}
	}()
	archive := filepath.Join(stage, "Ollama-darwin.zip")
	if e := s.Download(ctx, MacOSDownloadURL, archive); e != nil {
		return e
	}
	if e := s.ValidateArchive(archive); e != nil {
		return e
	}
	if e := s.Runner.Run(ctx, "ditto", "-xk", archive, stage); e != nil {
		return e
	}
	bundle := filepath.Join(stage, "Ollama.app")
	binary := filepath.Join(bundle, "Contents", "Resources", "ollama")
	info, e := s.FS.Stat(binary)
	if e != nil {
		return e
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return errors.New("ollama: installer has no executable CLI")
	}
	if e := ctx.Err(); e != nil {
		return e
	}
	relative, e := filepath.Rel(root, bundle)
	if e != nil {
		return e
	}
	if e := s.FS.Symlink(relative, target); e != nil {
		return e
	}
	published = true
	if e := s.FS.Remove(archive); e != nil {
		return fmt.Errorf("ollama: installed, but archive cleanup failed: %w", e)
	}
	return nil
}
