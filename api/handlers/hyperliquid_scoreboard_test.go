package handlers_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/malbeclabs/lake/api/handlers"
	apitesting "github.com/malbeclabs/lake/api/testing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// createFeedsTable creates the hyperliquid_bbo_feed_race_summary table in the feeds DB.
func createFeedsTable(t *testing.T, api *handlers.API) {
	t.Helper()
	ctx := t.Context()
	db := "`" + api.FeedsDB + "`"
	require.NoError(t, api.DB.Exec(ctx, fmt.Sprintf("CREATE DATABASE IF NOT EXISTS %s", db)))
	require.NoError(t, api.DB.Exec(ctx, fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s.hyperliquid_bbo_feed_race_summary (
			event_ts DateTime64(9),
			ingested_at DateTime64(9) DEFAULT now64(9),
			capture_run_id String,
			measurement_node_id String,
			host String,
			location_code LowCardinality(String),
			feed_type LowCardinality(String) DEFAULT 'bbo',
			symbol LowCardinality(String),
			source_ts_ms UInt64,
			bbo_hash UInt64,
			feed LowCardinality(String),
			loser_feed LowCardinality(String) DEFAULT '',
			total_events UInt64,
			events_won UInt64,
			lead_time_p50_ms Float64 DEFAULT 0,
			lead_time_p95_ms Float64 DEFAULT 0,
			send_lead_time_p50_ms Nullable(Float64) DEFAULT NULL,
			send_lead_time_p95_ms Nullable(Float64) DEFAULT NULL
		) ENGINE = ReplacingMergeTree(ingested_at)
		PARTITION BY toDate(event_ts)
		ORDER BY (measurement_node_id, symbol, source_ts_ms, bbo_hash, feed, loser_feed)
	`, db)))
}

// pairwiseRow inserts one pairwise race row (winner feed beat loser_feed by leadMs).
func insertPairwise(t *testing.T, api *handlers.API, node, loc, symbol string, srcTs, hash uint64, winner, loser string, leadMs float64) {
	t.Helper()
	ctx := t.Context()
	db := "`" + api.FeedsDB + "`"
	require.NoError(t, api.DB.Exec(ctx, fmt.Sprintf(`
		INSERT INTO %s.hyperliquid_bbo_feed_race_summary
		(event_ts, capture_run_id, measurement_node_id, host, location_code, symbol, source_ts_ms, bbo_hash, feed, loser_feed, total_events, events_won, lead_time_p50_ms, lead_time_p95_ms)
		VALUES (now64(9), 'run1', '%s', '%s', '%s', '%s', %d, %d, '%s', '%s', 1, 1, %f, %f)
	`, db, node, node, loc, symbol, srcTs, hash, winner, loser, leadMs, leadMs)))
}

func TestGetHyperliquidScoreboard_Empty(t *testing.T) {
	api := apitesting.NewTestAPIBare(t, testChDB)
	createFeedsTable(t, api) // empty table -> empty-but-valid response

	req := httptest.NewRequest(http.MethodGet, "/api/dz/hyperliquid/scoreboard", nil)
	rr := httptest.NewRecorder()
	api.GetHyperliquidScoreboard(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	var resp handlers.HyperliquidScoreboardResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	assert.Equal(t, "1h", resp.Window)
	assert.Equal(t, "MISS", rr.Header().Get("X-Cache"))
}

func TestGetHyperliquidScoreboard_MissingTable(t *testing.T) {
	api := apitesting.NewTestAPIBare(t, testChDB)
	// Do NOT create the table -> handler must degrade to empty 200, not 500.

	req := httptest.NewRequest(http.MethodGet, "/api/dz/hyperliquid/scoreboard", nil)
	rr := httptest.NewRecorder()
	api.GetHyperliquidScoreboard(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	var resp handlers.HyperliquidScoreboardResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	assert.Empty(t, resp.Competitors)
}

func TestFetchHyperliquidScoreboardData_MissingTable(t *testing.T) {
	api := apitesting.NewTestAPIBare(t, testChDB)
	// Do NOT create the table -> FetchHyperliquidScoreboardData must return an
	// empty-but-valid response (nil error, non-nil resp, empty slices).

	resp, err := api.FetchHyperliquidScoreboardData(t.Context(), "24h", "")
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, "24h", resp.Window)
	assert.Empty(t, resp.Competitors)
	assert.Empty(t, resp.Nodes)
	assert.Empty(t, resp.RecentRaces)
}

func TestHyperliquidScoreboard_PerNode(t *testing.T) {
	api := apitesting.NewTestAPIBare(t, testChDB)
	createFeedsTable(t, api)

	// tyo: DZ wins both vs QuickNode. nyc: DZ wins 1, loses 1 vs QuickNode.
	insertPairwise(t, api, "tyo-rec1", "tyo", "ETH", 10, 1, "tob_gcp_tyo_hl_mainnet1", "quicknode_l2book_bbo", 2.0)
	insertPairwise(t, api, "tyo-rec1", "tyo", "ETH", 20, 2, "tob_gcp_tyo_hl_mainnet1", "quicknode_l2book_bbo", 2.0)
	insertPairwise(t, api, "nyc-rec1", "nyc", "ETH", 30, 3, "tob_aws_galaxy1", "quicknode_l2book_bbo", 1.0)
	insertPairwise(t, api, "nyc-rec1", "nyc", "ETH", 40, 4, "quicknode_l2book_bbo", "tob_aws_galaxy1", 1.0)

	resp, err := api.FetchHyperliquidScoreboardData(t.Context(), "24h", "")
	require.NoError(t, err)
	require.Len(t, resp.Nodes, 2)

	byNode := map[string]handlers.HyperliquidNode{}
	for _, n := range resp.Nodes {
		byNode[n.MeasurementNodeID] = n
	}
	assert.InDelta(t, 100.0, byNode["tyo-rec1"].DZWinSharePct, 0.1)
	assert.InDelta(t, 50.0, byNode["nyc-rec1"].DZWinSharePct, 0.1)
}

func TestHyperliquidScoreboard_RecentRaces(t *testing.T) {
	api := apitesting.NewTestAPIBare(t, testChDB)
	createFeedsTable(t, api)

	// A DZ-won race (BTC) and a competitor-won race (ETH), both pairwise.
	insertPairwise(t, api, "tyo-rec1", "tyo", "BTC", 100, 1, "tob_gcp_tyo_hl_mainnet1", "hydromancer_bbo", 1.5)
	insertPairwise(t, api, "tyo-rec1", "tyo", "ETH", 200, 2, "quicknode_l2book_bbo", "tob_gcp_tyo_hl_mainnet1", 0.7)

	races, err := api.FetchHyperliquidScoreboardData(t.Context(), "24h", "")
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(races.RecentRaces), 2)

	bySym := map[string]handlers.HyperliquidRace{}
	for _, r := range races.RecentRaces {
		bySym[r.Symbol] = r
	}
	assert.True(t, bySym["BTC"].IsDZ)
	assert.Equal(t, "Hydromancer", bySym["BTC"].RunnerUpLabel)
	assert.InDelta(t, 1.5, bySym["BTC"].LeadMs, 0.001)
	assert.False(t, bySym["ETH"].IsDZ)
}

// A cell where DoubleZero won zero races (competitor swept it) makes the lead-time
// quantile aggregate over an empty predicate set, which ClickHouse returns as NaN. If that
// NaN reaches the float64 fields, json encoding of the whole response fails — breaking the
// scoreboard for everyone and poisoning the page cache. The percentiles must coalesce to 0.
func TestHyperliquidScoreboard_ZeroDZWins_Encodable(t *testing.T) {
	api := apitesting.NewTestAPIBare(t, testChDB)
	createFeedsTable(t, api)

	// Only a competitor-won race: DZ has zero wins vs Hydromancer in this window.
	insertPairwise(t, api, "nyc-rec1", "nyc", "ETH", 10, 1, "hydromancer_bbo", "tob_aws_galaxy1", 3.0)

	resp, err := api.FetchHyperliquidScoreboardData(t.Context(), "24h", "")
	require.NoError(t, err)

	// The entire response must be JSON-encodable — a single NaN anywhere fails encoding.
	_, err = json.Marshal(resp)
	require.NoError(t, err, "response must not contain NaN percentiles")

	var hydro *handlers.HyperliquidCompetitor
	for i := range resp.Competitors {
		if resp.Competitors[i].Feed == "hydromancer_bbo" {
			hydro = &resp.Competitors[i]
		}
	}
	require.NotNil(t, hydro)
	assert.InDelta(t, 0.0, hydro.DZWinPct, 0.001)
	assert.InDelta(t, 0.0, hydro.LeadP50Ms, 0.001)
	assert.InDelta(t, 0.0, hydro.LeadP95Ms, 0.001)
}

func TestHyperliquidScoreboard_HeadlineAndCompetitors(t *testing.T) {
	api := apitesting.NewTestAPIBare(t, testChDB)
	createFeedsTable(t, api)

	// 4 races at node tyo: DZ (tob_*) beats Hydromancer three times, loses once.
	insertPairwise(t, api, "tyo-rec1", "tyo", "BTC", 1000, 1, "tob_gcp_tyo_hl_mainnet1", "hydromancer_bbo", 1.0)
	insertPairwise(t, api, "tyo-rec1", "tyo", "BTC", 2000, 2, "tob_gcp_tyo_hl_mainnet1", "hydromancer_bbo", 2.0)
	insertPairwise(t, api, "tyo-rec1", "tyo", "BTC", 3000, 3, "tob_gcp_tyo_hl_mainnet1", "hydromancer_bbo", 3.0)
	insertPairwise(t, api, "tyo-rec1", "tyo", "BTC", 4000, 4, "hydromancer_bbo", "tob_gcp_tyo_hl_mainnet1", 0.5)

	resp, err := api.FetchHyperliquidScoreboardData(t.Context(), "24h", "")
	require.NoError(t, err)

	// DZ won 3 of 4 comparable races = 75%.
	assert.InDelta(t, 75.0, resp.DZWinSharePct, 0.1)
	assert.EqualValues(t, 4, resp.TotalRaces)

	var hydro *handlers.HyperliquidCompetitor
	for i := range resp.Competitors {
		if resp.Competitors[i].Feed == "hydromancer_bbo" {
			hydro = &resp.Competitors[i]
		}
	}
	require.NotNil(t, hydro)
	assert.Equal(t, "Hydromancer", hydro.Label)
	assert.InDelta(t, 75.0, hydro.DZWinPct, 0.1)
	assert.EqualValues(t, 4, hydro.Races)
	// Lead p50 over the 3 DZ wins (1.0, 2.0, 3.0) = 2.0 (quantileTDigest(0.5), exact at this size).
	assert.InDelta(t, 2.0, hydro.LeadP50Ms, 0.001)
}

// The scoreboard is an internal-only venue and the handler itself serves anyone who reaches
// it — the middleware api/main.go mounts it behind is the whole of the enforcement. These
// cases wrap the handler exactly as that route does, so a change on either side of the pair
// lands here. What they cannot see is the mount itself: main.go builds its router inline
// inside main(), so nothing here fails if that line stops naming this middleware.
func TestGetHyperliquidScoreboard_Auth(t *testing.T) {
	const token = "hyperliquid-scoreboard-test-token"

	api := apitesting.NewTestAPIBare(t, testChDB)
	createFeedsTable(t, api)
	guarded := handlers.RequireInternalDomainOrAPIToken(http.HandlerFunc(api.GetHyperliquidScoreboard))

	tests := []struct {
		name     string
		envToken string
		auth     string
		account  *handlers.Account
		wantCode int
	}{
		{"service token is admitted", token, "Bearer " + token, nil, http.StatusOK},
		{"internal google user is admitted", token, "", &handlers.Account{AccountType: "domain", IsInternalUser: true}, http.StatusOK},
		{"anonymous caller is refused", token, "", nil, http.StatusForbidden},
		{"wrong token is refused", token, "Bearer not-the-token", nil, http.StatusForbidden},
		{"wallet user is refused", token, "", &handlers.Account{AccountType: "wallet"}, http.StatusForbidden},
		// With no token configured the venue stays Google-only: a blank secret must not
		// admit a blank presentation.
		{"unconfigured token admits nobody", "", "Bearer ", nil, http.StatusForbidden},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("AUTH_INTERNAL_API_TOKEN", tt.envToken)

			req := httptest.NewRequest(http.MethodGet, "/api/dz/hyperliquid/scoreboard", nil)
			if tt.auth != "" {
				req.Header.Set("Authorization", tt.auth)
			}
			if tt.account != nil {
				req = withAccount(req, tt.account)
			}
			rr := httptest.NewRecorder()
			guarded.ServeHTTP(rr, req)

			require.Equal(t, tt.wantCode, rr.Code)
			if tt.wantCode == http.StatusOK {
				var resp handlers.HyperliquidScoreboardResponse
				require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
				assert.Equal(t, "1h", resp.Window)
				return
			}
			// A refusal must not leak the payload it was guarding.
			assert.NotContains(t, rr.Body.String(), "dz_win_share_pct")
		})
	}
}

// The token caller reaches the handler with no account in context, so the two ways in would
// diverge the moment this endpoint started varying its payload by account. They must not:
// the token is a way in, not a narrower view of the scoreboard.
func TestGetHyperliquidScoreboard_TokenAndLoginSeeSamePayload(t *testing.T) {
	const token = "hyperliquid-scoreboard-test-token"
	t.Setenv("AUTH_INTERNAL_API_TOKEN", token)

	api := apitesting.NewTestAPIBare(t, testChDB)
	createFeedsTable(t, api)
	insertPairwise(t, api, "tyo-rec1", "tyo", "BTC", 1000, 1, "tob_gcp_tyo_hl_mainnet1", "hydromancer_bbo", 1.0)
	guarded := handlers.RequireInternalDomainOrAPIToken(http.HandlerFunc(api.GetHyperliquidScoreboard))

	call := func(prepare func(*http.Request) *http.Request) handlers.HyperliquidScoreboardResponse {
		t.Helper()
		req := prepare(httptest.NewRequest(http.MethodGet, "/api/dz/hyperliquid/scoreboard?window=24h", nil))
		rr := httptest.NewRecorder()
		guarded.ServeHTTP(rr, req)
		require.Equal(t, http.StatusOK, rr.Code)

		var resp handlers.HyperliquidScoreboardResponse
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
		resp.GeneratedAt = time.Time{} // wall clock, not a property of the caller
		return resp
	}

	viaToken := call(func(r *http.Request) *http.Request {
		r.Header.Set("Authorization", "Bearer "+token)
		return r
	})
	viaLogin := call(func(r *http.Request) *http.Request {
		return withAccount(r, &handlers.Account{AccountType: "domain", IsInternalUser: true})
	})

	assert.Equal(t, viaLogin, viaToken)
	assert.EqualValues(t, 1, viaToken.TotalRaces)
}
