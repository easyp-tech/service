# Authentication roadmap: from write tokens to SSO

Design sketch, not an implementation plan for today. Its purpose is to record
where identity is heading so that the choices already made — the write tokens
described in [AUTH.md](../AUTH.md) — stay compatible with it.

## The decision that shapes everything

Identity will live in a **dedicated authorization service**, not here.

That single choice removes most of what a naive plan would have added to this
codebase. This service becomes a resource server: it accepts a token, verifies
the signature, reads the claims. Who the user is, how they logged in, whether
their company uses Okta — invisible here, and deliberately so.

**Consequence: no `users`, `organizations`, `user_org_roles` or `sessions`
tables in this database.** The only column identity will ever add is
`plugins.org_id`, and that is a multi-tenancy decision rather than an
authentication one.

**Consequence: SSO is a feature of the authorization service.** It integrates
with the customer's IdP and issues a token; the same JWT arrives here whether the
human authenticated with a password or through SAML. The enterprise complexity —
SAML, SCIM, group mapping, session policy — stays in one place instead of
spreading across product services.

## How a request will look

**Human.** `easyp login` opens a browser → authorization service → OIDC
Authorization Code + PKCE against the customer's IdP (Enterprise) or a local
login (all tiers). The service returns a short-lived access JWT (5–15 min) plus a
refresh token; the CLI stores them in the OS keyring. `easyp generate` then sends
`Authorization: Bearer <jwt>` — the same header the static token already uses, so
no client-side plumbing changes. On `Unauthenticated` the CLI refreshes and
retries.

**Machine.** CI either performs a `client_credentials` grant against the
authorization service, or keeps using a static write token.

**This service verifies:** signature against the issuer's JWKS (cached, keyed by
`kid`), `iss` matches configuration, `aud` contains this service, `exp`/`nbf`,
and that the caller holds the scope the method requires.

## What changes in this codebase

| Piece | Today | With JWT |
|---|---|---|
| `auth.Authenticator` | `StaticTokenAuthenticator` | plus `JWTAuthenticator` |
| selection | — | `MultiAuthenticator`, dispatching on token shape |
| `auth.Actor` | `Name`, `Kind` | plus `UserID`, `OrgID`, `Scopes` |
| method policy | public allow-list | plus `method → required scope` |
| config | `auth.write_tokens` | plus `auth.jwt.{issuer,jwks_url,audience}` |

`api.AuthInterceptor` does not change at all. That is precisely why
`Authenticator` was introduced as an interface with a single implementation: the
cost was one file, and it buys the second mechanism arriving *beside* the first
rather than replacing it.

Dispatch needs no heuristics: a JWT is three base64 segments separated by dots, a
static token is 64 hex characters. `Actor` gains its extra fields only when
something can fill them; empty fields today would be noise.

New dependency when the time comes: `github.com/lestrrat-go/jwx/v3`, for its
JWKS cache with background refresh.

## Two decisions worth making consciously

**JWT or introspection.** Local verification means no network hop per request and
no hard dependency on the authorization service being reachable in the hot path.
The price is that revocation takes effect when the token expires rather than
immediately. Introspection (RFC 7662) buys instant revocation at the cost of a
call per request and an outage in the authorization service becoming an outage
here. Recommendation: JWT with a 5–15 minute TTL; if `DeletePlugin` ever needs
instant revocation, add introspection for the mutating methods only.

**Static tokens are permanent, not transitional.** When the authorization
service is unreachable you still have to be able to register a plugin and repair
production. That is why the target is `MultiAuthenticator` rather than a
replacement.

## Order of work

1. Write tokens — done; closes the "anyone can call DeletePlugin" gap.
2. Authorization service: JWT issuance, local accounts, JWKS endpoint.
3. `JWTAuthenticator` here, plus per-method scopes.
4. Multi-tenancy: `plugins.org_id`, filtering by `OrgID` from the claims.
5. SSO (OIDC/SAML to the customer's IdP) — **in the authorization service**, as
   an Enterprise feature.
6. SCIM provisioning — same place.

Steps 1–3 do not need multi-tenancy. Step 5 needs no change here at all.

## Licensing boundary

Static tokens and plain JWT verification are available in every tier. Enterprise
sells the IdP integration, i.e. steps 5–6, and that lives in the authorization
service. The only thing gated here is multi-tenancy —
`core.FeatureMultiTenancy` is already declared and unused.

The basic lock is never sold separately. If authentication required a licence,
a Community installation would ship with `DeletePlugin` open to the internet,
which is the exact hole this work closed.

## Explicitly not decided

Refresh token format and storage; more than one IdP per installation;
impersonation and acting-on-behalf-of; quotas and billing per `OrgID`; moving
static tokens into the database (unnecessary while the subjects are one human and
one pipeline).
