package auth

import (
	"strings"
	"testing"
)

func TestStaticTokens(t *testing.T) {
	s := NewStaticTokens()
	s.Add("w1", RoleWorker, "secret-one")

	if p, err := s.Authenticate("secret-one"); err != nil || p.ID != "w1" || p.Role != RoleWorker {
		t.Fatalf("valid token: got %+v, %v", p, err)
	}
	for name, tok := range map[string]string{"missing": "", "wrong": "secret-two"} {
		if _, err := s.Authenticate(tok); err != ErrUnauthorized {
			t.Errorf("%s: got %v, want ErrUnauthorized", name, err)
		}
	}
	if !s.Revoke("w1") {
		t.Fatal("revoke of known id returned false")
	}
	if _, err := s.Authenticate("secret-one"); err != ErrUnauthorized {
		t.Errorf("revoked: got %v, want ErrUnauthorized", err)
	}
	if s.Revoke("nobody") {
		t.Error("revoke of unknown id returned true")
	}
}

func TestOnlyHashStored(t *testing.T) {
	s := NewStaticTokens()
	s.Add("w1", RoleWorker, "plain-token-value")
	for h := range s.byHash {
		if strings.Contains(h, "plain-token-value") || h != HashToken("plain-token-value") {
			t.Fatalf("stored key %q is not the token hash", h)
		}
	}
}

func TestReAddReplacesToken(t *testing.T) {
	s := NewStaticTokens()
	s.Add("w1", RoleWorker, "old")
	s.Add("w1", RoleWorker, "new")
	if _, err := s.Authenticate("old"); err != ErrUnauthorized {
		t.Errorf("old token still valid: %v", err)
	}
	if _, err := s.Authenticate("new"); err != nil {
		t.Errorf("new token rejected: %v", err)
	}
}
