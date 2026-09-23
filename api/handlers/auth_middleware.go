package handlers

import (
	"context"
	"crypto/subtle"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
)

// Context keys for auth
type contextKey string

const (
	accountContextKey contextKey = "account"
	ipContextKey      contextKey = "client_ip"
)

// OptionalAuth middleware attaches user to context if authenticated, allows anonymous
func (a *API) OptionalAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		// Extract and store IP address
		ip := GetIPFromRequest(r)
		ctx = context.WithValue(ctx, ipContextKey, ip)

		// Try to authenticate
		token := extractBearerToken(r)
		if token != "" {
			account, err := a.GetAccountByToken(ctx, token)
			if err == nil && account != nil {
				ctx = context.WithValue(ctx, accountContextKey, account)
			} else if err != nil {
				slog.Debug("OptionalAuth: failed to get account by token", "error", err)
			}
		}

		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequireAuth middleware returns 401 if not authenticated
func (a *API) RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		// Extract and store IP address
		ip := GetIPFromRequest(r)
		ctx = context.WithValue(ctx, ipContextKey, ip)

		// Try to authenticate
		token := extractBearerToken(r)
		if token == "" {
			http.Error(w, "Authentication required", http.StatusUnauthorized)
			return
		}

		account, err := a.GetAccountByToken(ctx, token)
		if err != nil || account == nil {
			http.Error(w, "Invalid or expired token", http.StatusUnauthorized)
			return
		}

		ctx = context.WithValue(ctx, accountContextKey, account)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequireInternalDomain middleware returns 403 unless the user is authenticated
// with an email from an allowed domain (AUTH_ALLOWED_DOMAINS).
func RequireInternalDomain(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		account := GetAccountFromContext(r.Context())
		if account == nil || !account.IsInternalUser {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// internalAPIToken returns the static service token that stands in for an internal-domain
// login, or "" when none is configured.
func internalAPIToken() string {
	return strings.TrimSpace(os.Getenv("AUTH_INTERNAL_API_TOKEN"))
}

// RequireInternalDomainOrAPIToken is RequireInternalDomain with a second way in: the static
// bearer token in AUTH_INTERNAL_API_TOKEN, for service callers that have no Google login to
// make. It is scoped by where it is mounted — the token opens exactly the routes it wraps and
// not every internal-domain route, so a caller that needs one endpoint is not handed the rest.
//
// Two properties are load-bearing. An unset or blank token grants nothing: without that guard
// every deployment that never configured one would accept a bare "Authorization: Bearer ",
// since an empty secret matches an empty presentation. And a token caller is deliberately left
// anonymous in the request context — the handlers that widen a payload for an internal account
// (shred client IPs, ops tickets) go on treating it as an outsider, so mounting this on one of
// them cannot disclose more than the route itself.
//
// The token rides the same Authorization: Bearer slot as a session token, so OptionalAuth runs
// its session lookup against it first and misses: one indexed Postgres read per request, which
// the per-IP query rate limit already bounds.
func RequireInternalDomainOrAPIToken(next http.Handler) http.Handler {
	domainOnly := RequireInternalDomain(next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if want := internalAPIToken(); want != "" {
			// ConstantTimeCompare returns 0 on a length mismatch without comparing, so a
			// presented token of any length is safe to pass in.
			if subtle.ConstantTimeCompare([]byte(extractBearerToken(r)), []byte(want)) == 1 {
				next.ServeHTTP(w, r)
				return
			}
		}
		domainOnly.ServeHTTP(w, r)
	})
}

// GetAccountFromContext returns the account from context, or nil if not authenticated
func GetAccountFromContext(ctx context.Context) *Account {
	account, ok := ctx.Value(accountContextKey).(*Account)
	if !ok {
		return nil
	}
	return account
}

// GetIPFromContext returns the IP from context
func GetIPFromContext(ctx context.Context) string {
	ip, ok := ctx.Value(ipContextKey).(string)
	if !ok {
		return ""
	}
	return ip
}

// GetIPFromRequest extracts the client IP from request
func GetIPFromRequest(r *http.Request) string {
	// Check X-Forwarded-For header (from proxies/load balancers)
	xff := r.Header.Get("X-Forwarded-For")
	if xff != "" {
		// Take the first IP in the list
		parts := strings.Split(xff, ",")
		if len(parts) > 0 {
			ip := strings.TrimSpace(parts[0])
			if ip != "" {
				return ip
			}
		}
	}

	// Check X-Real-IP header
	xri := r.Header.Get("X-Real-IP")
	if xri != "" {
		return xri
	}

	// Fall back to RemoteAddr
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return ip
}

// SetQuotaHeaders sets the rate limit headers on response
func SetQuotaHeaders(w http.ResponseWriter, quota *QuotaInfo) {
	if quota == nil {
		return
	}

	if quota.Limit != nil {
		w.Header().Set("X-RateLimit-Limit", itoa(*quota.Limit))
	}

	if quota.Remaining != nil {
		w.Header().Set("X-RateLimit-Remaining", itoa(*quota.Remaining))
	}

	// Parse ResetsAt and convert to Unix timestamp
	if quota.ResetsAt != "" {
		w.Header().Set("X-RateLimit-Reset", quota.ResetsAt)
	}
}

func itoa(i int) string {
	return fmt.Sprintf("%d", i)
}

// SetAccountInContext is a test helper that adds an account to the context.
// This is used for testing handlers without going through the auth middleware.
func SetAccountInContext(ctx context.Context, account *Account) context.Context {
	return context.WithValue(ctx, accountContextKey, account)
}
