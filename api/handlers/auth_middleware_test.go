package handlers_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/malbeclabs/lake/api/handlers"
	"github.com/stretchr/testify/assert"
)

func TestRequireInternalDomainOrAPIToken(t *testing.T) {
	const token = "kJ8x-service-token-for-tests"

	tests := []struct {
		name     string
		envToken string
		auth     string
		account  *handlers.Account
		wantCode int
	}{
		{"configured token passes", token, "Bearer " + token, nil, http.StatusOK},
		{"wrong token is forbidden", token, "Bearer not-the-token", nil, http.StatusForbidden},
		{"no credential is forbidden", token, "", nil, http.StatusForbidden},
		// An unconfigured token must grant nothing: an empty secret equals an empty
		// presentation, so every deployment without one would accept a bare "Bearer ".
		{"unset token grants nothing", "", "Bearer ", nil, http.StatusForbidden},
		{"internal account still passes", token, "", &handlers.Account{IsInternalUser: true}, http.StatusOK},
		{"non-internal account is forbidden", token, "", &handlers.Account{IsInternalUser: false}, http.StatusForbidden},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("AUTH_INTERNAL_API_TOKEN", tt.envToken)

			var reached bool
			h := handlers.RequireInternalDomainOrAPIToken(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				reached = true
			}))

			req := httptest.NewRequest(http.MethodGet, "/api/dz/hyperliquid/scoreboard", nil)
			if tt.auth != "" {
				req.Header.Set("Authorization", tt.auth)
			}
			if tt.account != nil {
				req = withAccount(req, tt.account)
			}

			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, req)

			assert.Equal(t, tt.wantCode, rr.Code)
			assert.Equal(t, tt.wantCode == http.StatusOK, reached, "handler reached")
		})
	}
}

// A token caller stays anonymous, so a handler that widens its payload for an internal
// account (shred client IPs, ops tickets) keeps treating the token as an outsider.
func TestRequireInternalDomainOrAPIToken_TokenCallerStaysAnonymous(t *testing.T) {
	const token = "kJ8x-service-token-for-tests"
	t.Setenv("AUTH_INTERNAL_API_TOKEN", token)

	var account *handlers.Account
	h := handlers.RequireInternalDomainOrAPIToken(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		account = handlers.GetAccountFromContext(r.Context())
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/dz/hyperliquid/scoreboard", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	h.ServeHTTP(httptest.NewRecorder(), req)

	assert.Nil(t, account)
}
