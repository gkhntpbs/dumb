//go:build darwin || linux

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPairRejectsSubstitutableAncestor(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ancestor := filepath.Join(root, "shared")
	parent := filepath.Join(ancestor, "private")
	if err := os.Mkdir(ancestor, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(parent, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(ancestor, 0777); err != nil {
		t.Fatal(err)
	}
	if err := pairTarget(filepath.Join(parent, "token")); err == nil {
		t.Fatal("shared-writable ancestor accepted")
	}
	if err := os.Chmod(ancestor, os.ModeSticky|0777); err != nil {
		t.Fatal(err)
	}
	if err := pairTarget(filepath.Join(parent, "token")); err != nil {
		t.Fatal("sticky trusted ancestor rejected", err)
	}
}
func TestPairDirectoryOwnershipPolicy(t *testing.T) {
	uid := uint32(os.Geteuid())
	other := uid + 1
	if pairDirectoryPolicy(0700, other, uid, true) {
		t.Fatal("other owner accepted for private parent")
	}
	if pairDirectoryPolicy(0755, other, uid, false) {
		t.Fatal("untrusted ancestor owner can replace child")
	}
	if !pairDirectoryPolicy(0755, 0, uid, false) {
		t.Fatal("root-owned protected ancestor rejected")
	}
	if pairDirectoryPolicy(0777, uid, uid, true) {
		t.Fatal("shared parent accepted")
	}
	if pairDirectoryPolicy(os.ModeSticky|0777, uid, uid, true) {
		t.Fatal("sticky selected parent accepted")
	}
}
