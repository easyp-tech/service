package core

import "context"

const unknownValue = "unknown"

type callerIPKey struct{}

// WithCallerIP returns a new context with the caller IP address stored.
func WithCallerIP(ctx context.Context, ip string) context.Context {
	return context.WithValue(ctx, callerIPKey{}, ip)
}

// CallerIPFromContext extracts the caller IP address from the context.
// Returns "unknown" if not set.
func CallerIPFromContext(ctx context.Context) string {
	if ip, ok := ctx.Value(callerIPKey{}).(string); ok && ip != "" {
		return ip
	}

	return unknownValue
}

type actorKey struct{}

// WithActor returns a new context carrying the name of the authenticated
// caller. Anonymous methods leave it unset.
func WithActor(ctx context.Context, actor string) context.Context {
	return context.WithValue(ctx, actorKey{}, actor)
}

// ActorFromContext extracts the authenticated caller's name.
// Returns "unknown" if the request was not authenticated.
func ActorFromContext(ctx context.Context) string {
	if actor, ok := ctx.Value(actorKey{}).(string); ok && actor != "" {
		return actor
	}

	return unknownValue
}
