// Package adminserver exposes the server embedding boundary without exposing
// production authentication, password storage or deployment configuration.
package adminserver

import (
	"github.com/gkhntpbs/dumb/internal/auth"
	"github.com/gkhntpbs/dumb/internal/protocol"
	"github.com/gkhntpbs/dumb/internal/queue"
	"github.com/gkhntpbs/dumb/internal/server"
	"net"
)

type Principal = auth.Principal
type Role = auth.Role
type Authenticator = auth.Authenticator
type SessionProvider = server.AdminSessionProvider
type QueueLimits = queue.Limits
type Config = server.Config
type Server = server.Server

const (
	RoleAdminRead   = auth.RoleAdminRead
	RoleAdminManage = auth.RoleAdminManage
	RoleWorker      = auth.RoleWorker
	RoleCaller      = auth.RoleCaller
)

var ErrUnauthorized = auth.ErrUnauthorized

type denyAuth struct{}

func (denyAuth) Authenticate(string) (Principal, error) { return Principal{}, ErrUnauthorized }

// New builds an embeddable queue server. Browser sessions never grant worker or
// caller privileges; configure Auth separately to enable those protocol APIs.
func New(cfg Config) (*Server, error) {
	if cfg.Auth == nil {
		cfg.Auth = denyAuth{}
	}
	return server.New(cfg)
}

// ListenAdmin requires a numeric loopback address for the private listener.
func ListenAdmin(address string) (net.Listener, error) { return server.ListenAdmin(address) }

// Credential management DTOs are public; storage and authentication stay private.
type CredentialProvider = server.CredentialProvider
type Credential = protocol.Credential
type CredentialPage = protocol.CredentialPage
type IssueCredentialRequest = protocol.IssueCredentialRequest
type RotateCredentialRequest = protocol.RotateCredentialRequest
type IssuedCredential = protocol.IssuedCredential
type CreatePairingRequest = protocol.CreatePairingRequest
type PairingCode = protocol.PairingCode
type PairRequest = protocol.PairRequest

var (
	ErrCredentialInvalid     = server.ErrCredentialInvalid
	ErrCredentialNotFound    = server.ErrCredentialNotFound
	ErrCredentialCapacity    = server.ErrCredentialCapacity
	ErrCredentialUnavailable = server.ErrCredentialUnavailable
	ErrPairingDenied         = server.ErrPairingDenied
)
