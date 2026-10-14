package protocol

import "time"

// Credential is current inventory metadata. It contains no token or token hash.
type Credential struct {
	CredentialID string    `json:"credential_id"`
	PrincipalID  string    `json:"principal_id"`
	Role         string    `json:"role"`
	CreatedAt    time.Time `json:"created_at"`
	ExpiresAt    time.Time `json:"expires_at"`
	Revoked      bool      `json:"revoked"`
}
type CredentialPage struct {
	Total  int          `json:"total"`
	Offset int          `json:"offset"`
	Limit  int          `json:"limit"`
	Items  []Credential `json:"items"`
}
type IssueCredentialRequest struct {
	Role       string `json:"role"`
	TTLSeconds int64  `json:"ttl_seconds"`
}
type RotateCredentialRequest struct {
	TTLSeconds int64 `json:"ttl_seconds"`
}
type IssuedCredential struct {
	Credential Credential `json:"credential"`
	Token      string     `json:"token"`
}
type CreatePairingRequest struct {
	WorkerID             string `json:"worker_id,omitempty"`
	TTLSeconds           int64  `json:"ttl_seconds"`
	CredentialTTLSeconds int64  `json:"credential_ttl_seconds"`
}
type PairingCode struct {
	Code      string    `json:"code"`
	ExpiresAt time.Time `json:"expires_at"`
	WorkerID  string    `json:"worker_id"`
}
type PairRequest struct {
	Code string `json:"code"`
}
