package update

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/gkhntpbs/dumb/internal/update/updatetest"
)

func TestMinisignVerify(t *testing.T) {
	s := updatetest.NewSigner()
	msg := []byte("abc123  dumb-worker_0.2.0_darwin_arm64.tar.gz\n")
	sig := s.Sign(msg, "timestamp:1 file:checksums.txt")
	key, err := ParseKey(s.PublicKey())
	if err != nil {
		t.Fatal(err)
	}
	if tc, err := key.Verify(msg, sig); err != nil || tc != "timestamp:1 file:checksums.txt" {
		t.Fatalf("valid signature: %q, %v", tc, err)
	}
	// A whole .pub file parses too.
	if k, err := ParseKey("untrusted comment: minisign public key\n" + s.PublicKey() + "\n"); err != nil || k.ID != key.ID {
		t.Fatalf(".pub file: %v", err)
	}

	other := updatetest.NewSigner()
	sameID := updatetest.NewSigner()
	sameID.ID = s.ID
	wrongKey, _ := ParseKey(sameID.PublicKey())
	otherKey, _ := ParseKey(other.PublicKey())

	lines := strings.Split(string(sig), "\n")
	tamperedComment := []byte(strings.Join([]string{lines[0], lines[1], "trusted comment: timestamp:2 file:checksums.txt", lines[3], ""}, "\n"))
	badB64 := []byte(strings.Join([]string{lines[0], "!!!not base64!!!", lines[2], lines[3], ""}, "\n"))
	short := []byte(strings.Join([]string{lines[0], base64.StdEncoding.EncodeToString([]byte("EDshort")), lines[2], lines[3], ""}, "\n"))

	cases := []struct {
		name string
		key  Key
		msg  []byte
		sig  []byte
	}{
		{"tampered file", key, append(bytes.Clone(msg), 'x'), sig},
		{"wrong key id", otherKey, msg, sig},
		{"wrong key, same id", wrongKey, msg, sig},
		{"tampered trusted comment", key, msg, tamperedComment},
		{"legacy algorithm", key, msg, s.SignLegacy(msg, "c")},
		{"unknown algorithm", key, msg, s.SignRaw("XX", msg, "c")},
		{"malformed base64", key, msg, badB64},
		{"short signature", key, msg, short},
		{"empty", key, msg, nil},
		{"three lines", key, msg, []byte(strings.Join(lines[:3], "\n"))},
	}
	for _, c := range cases {
		if _, err := c.key.Verify(c.msg, c.sig); err == nil {
			t.Errorf("%s: verified", c.name)
		}
	}
}

func TestParseKeyRejects(t *testing.T) {
	for _, bad := range []string{"", "!!!", base64.StdEncoding.EncodeToString([]byte("Ed1234567")),
		base64.StdEncoding.EncodeToString(append([]byte("XX12345678"), make([]byte, 32)...))} {
		if _, err := ParseKey(bad); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
}
