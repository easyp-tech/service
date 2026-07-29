// Package auth resolves the credentials carried by a request into the actor
// they belong to. It knows nothing about which methods require credentials —
// that decision belongs to the interceptor in internal/api.
package auth

import (
	"context"
	"errors"

	"google.golang.org/grpc/metadata"
)

// Kinds of actor. More will appear once tokens are issued by an identity
// service rather than listed in configuration.
const (
	// KindToken is a caller identified by a static token from the configuration.
	KindToken = "token"
)

var (
	// ErrNoCredentials is returned when the request carries no usable
	// authorization metadata.
	ErrNoCredentials = errors.New("auth: no credentials presented")
	// ErrUnknownToken is returned when credentials were presented but match
	// nothing configured.
	ErrUnknownToken = errors.New("auth: token not recognised")
)

// Actor is who a request is attributed to.
//
// Only the fields that can actually be filled today are present. When identity
// moves to a dedicated service, this grows UserID, OrgID and Scopes — the
// interceptor and the audit trail read Actor either way, so nothing above this
// type has to change.
type Actor struct {
	// Name identifies the caller in logs and in the audit log. For a static
	// token it is the label from the configuration, e.g. "ci".
	Name string
	// Kind says where the identity came from.
	Kind string
}

// Authenticator turns request metadata into an Actor.
//
// Deliberately an interface with one implementation today: the second one
// verifies JWTs issued by the identity service, and it has to be able to sit
// beside this one rather than replace it — static tokens stay as the path that
// keeps working when that service is unreachable.
type Authenticator interface {
	Authenticate(ctx context.Context, md metadata.MD) (Actor, error)
}
