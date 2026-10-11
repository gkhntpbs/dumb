package update

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/blake2b"
)

// Minisign (https://jedisct1.github.io/minisign/) key and signature handling.
// Only the prehashed "ED" algorithm (Ed25519 over BLAKE2b-512 of the file) is
// accepted; legacy "Ed" signatures over the raw file are refused.

const (
	algKey       = "Ed" // public keys always carry this tag
	algPrehashed = "ED" // signatures we accept
	algLegacy    = "Ed" // signatures we refuse
	keyIDLen     = 8
)

// Key is a minisign public key.
type Key struct {
	ID  [keyIDLen]byte
	Key ed25519.PublicKey
}

// ParseKey accepts the base64 key line ("RW...") or a whole minisign .pub file.
func ParseKey(s string) (Key, error) {
	line := ""
	for _, l := range strings.Split(strings.TrimSpace(s), "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "untrusted comment:") {
			line = l
			break
		}
	}
	b, err := base64.StdEncoding.DecodeString(line)
	if err != nil {
		return Key{}, fmt.Errorf("update: public key: %w", err)
	}
	if len(b) != 2+keyIDLen+ed25519.PublicKeySize || string(b[:2]) != algKey {
		return Key{}, errors.New("update: public key: not a minisign Ed25519 key")
	}
	var k Key
	copy(k.ID[:], b[2:2+keyIDLen])
	k.Key = ed25519.PublicKey(bytes.Clone(b[2+keyIDLen:]))
	return k, nil
}

// signature is a parsed .minisig file.
type signature struct {
	alg            string
	keyID          [keyIDLen]byte
	sig            []byte // over BLAKE2b-512(file)
	trustedComment string
	globalSig      []byte // over sig || trustedComment
}

func parseSignature(data []byte) (signature, error) {
	var s signature
	lines := strings.Split(strings.ReplaceAll(strings.TrimRight(string(data), "\r\n"), "\r\n", "\n"), "\n")
	if len(lines) != 4 {
		return s, fmt.Errorf("update: signature: want 4 lines, got %d", len(lines))
	}
	if !strings.HasPrefix(lines[0], "untrusted comment:") {
		return s, errors.New("update: signature: missing untrusted comment")
	}
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(lines[1]))
	if err != nil {
		return s, fmt.Errorf("update: signature: %w", err)
	}
	if len(b) != 2+keyIDLen+ed25519.SignatureSize {
		return s, errors.New("update: signature: bad length")
	}
	s.alg = string(b[:2])
	copy(s.keyID[:], b[2:2+keyIDLen])
	s.sig = b[2+keyIDLen:]
	tc, ok := strings.CutPrefix(lines[2], "trusted comment: ")
	if !ok {
		return s, errors.New("update: signature: missing trusted comment")
	}
	s.trustedComment = tc
	if s.globalSig, err = base64.StdEncoding.DecodeString(strings.TrimSpace(lines[3])); err != nil {
		return s, fmt.Errorf("update: signature: global signature: %w", err)
	}
	if len(s.globalSig) != ed25519.SignatureSize {
		return s, errors.New("update: signature: global signature: bad length")
	}
	return s, nil
}

// Verify checks a minisign signature file over msg and returns its trusted
// comment. The key id, the signature over BLAKE2b-512(msg) and the global
// signature over the trusted comment must all verify.
func (k Key) Verify(msg, sigFile []byte) (string, error) {
	s, err := parseSignature(sigFile)
	if err != nil {
		return "", err
	}
	switch s.alg {
	case algPrehashed:
	case algLegacy:
		return "", errors.New("update: signature: legacy non-prehashed signatures are not accepted")
	default:
		return "", fmt.Errorf("update: signature: unknown algorithm %q", s.alg)
	}
	if s.keyID != k.ID {
		return "", fmt.Errorf("update: signature: made with key %X, want %X", s.keyID, k.ID)
	}
	h := blake2b.Sum512(msg)
	if !ed25519.Verify(k.Key, h[:], s.sig) {
		return "", errors.New("update: signature: does not verify")
	}
	if !ed25519.Verify(k.Key, append(bytes.Clone(s.sig), s.trustedComment...), s.globalSig) {
		return "", errors.New("update: signature: trusted comment does not verify")
	}
	return s.trustedComment, nil
}
