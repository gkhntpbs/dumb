package main

import (
	"context"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/gkhntpbs/dumb/internal/protocol"
	"github.com/gkhntpbs/dumb/internal/server"
)

// The preview pairing provider mints contract-shaped throwaway codes and can
// never hand out a credential.
func TestFakePairingIsInert(t *testing.T) {
	var f fakePairing
	code := regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)
	got, err := f.CreatePairing(context.Background(), protocol.CreatePairingRequest{TTLSeconds: 600, CredentialTTLSeconds: 31536000})
	if err != nil || !code.MatchString(got.Code) || !code.MatchString(got.WorkerID) || time.Until(got.ExpiresAt) > 600*time.Second || time.Until(got.ExpiresAt) < 590*time.Second {
		t.Fatalf("new worker: %+v %v", got, err)
	}
	again, _ := f.CreatePairing(context.Background(), protocol.CreatePairingRequest{TTLSeconds: 600, CredentialTTLSeconds: 1})
	if again.Code == got.Code || again.WorkerID == got.WorkerID {
		t.Fatal("codes and ids must be random")
	}
	repair, _ := f.CreatePairing(context.Background(), protocol.CreatePairingRequest{WorkerID: "existing-worker", TTLSeconds: 120, CredentialTTLSeconds: 1})
	if repair.WorkerID != "existing-worker" {
		t.Fatalf("re-pair changed the identity: %q", repair.WorkerID)
	}
	if _, err := f.ExchangePairing(context.Background(), got.Code); !errors.Is(err, server.ErrPairingDenied) {
		t.Fatalf("exchange: %v", err)
	}
	if _, err := f.Issue(context.Background(), protocol.IssueCredentialRequest{}); !errors.Is(err, server.ErrCredentialUnavailable) {
		t.Fatalf("issue: %v", err)
	}
}
