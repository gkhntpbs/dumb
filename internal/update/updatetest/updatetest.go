// Package updatetest produces throwaway minisign keys and signatures for tests
// and local end-to-end runs. Keys are generated at run time and never stored in
// the repository; production releases are signed with the minisign tool.
package updatetest

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"fmt"

	"golang.org/x/crypto/blake2b"
)

// Signer is a minisign key pair.
type Signer struct {
	ID   [8]byte
	Priv ed25519.PrivateKey
}

// NewSigner generates a fresh key pair.
func NewSigner() *Signer {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		panic(err)
	}
	s := &Signer{Priv: priv}
	rand.Read(s.ID[:])
	return s
}

// FromSeed rebuilds a signer from an id and a 32-byte Ed25519 seed.
func FromSeed(id [8]byte, seed []byte) *Signer {
	return &Signer{ID: id, Priv: ed25519.NewKeyFromSeed(seed)}
}

// PublicKey is the base64 key line of a minisign .pub file.
func (s *Signer) PublicKey() string {
	b := append([]byte("Ed"), s.ID[:]...)
	b = append(b, s.Priv.Public().(ed25519.PublicKey)...)
	return base64.StdEncoding.EncodeToString(b)
}

// Sign returns a prehashed ("ED") minisign signature file for msg.
func (s *Signer) Sign(msg []byte, trustedComment string) []byte {
	h := blake2b.Sum512(msg)
	return s.SignRaw("ED", h[:], trustedComment)
}

// SignLegacy returns a legacy ("Ed", not prehashed) signature file.
func (s *Signer) SignLegacy(msg []byte, trustedComment string) []byte {
	return s.SignRaw("Ed", msg, trustedComment)
}

// SignRaw signs payload and labels the signature with alg.
func (s *Signer) SignRaw(alg string, payload []byte, trustedComment string) []byte {
	sig := ed25519.Sign(s.Priv, payload)
	line := append([]byte(alg), s.ID[:]...)
	line = append(line, sig...)
	global := ed25519.Sign(s.Priv, append(append([]byte{}, sig...), trustedComment...))
	return fmt.Appendf(nil, "untrusted comment: signature from updatetest key\n%s\ntrusted comment: %s\n%s\n",
		base64.StdEncoding.EncodeToString(line), trustedComment, base64.StdEncoding.EncodeToString(global))
}
