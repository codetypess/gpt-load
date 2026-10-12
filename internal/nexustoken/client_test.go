package nexustoken

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSnapshotKeepsPersonalVehicleAndResetCardData(t *testing.T) {
	now := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	asset := Asset{ProductName: "Pro 20x", AssignmentName: "Seat A"}
	usage := []byte(`{"seat_percent_7d":24,"account_percent_7d":81,"seat_percent_5h":0,"account_percent_5h":12,"reset_7d_at":"2026-10-18T00:00:00Z","reset_5h_at":"2026-10-12T05:00:00Z","seat_cost_5h":0,"account_cost_5h":12.56,"seat_cost_7d":68.80,"account_cost_7d":256.44}`)
	quota := []byte(`{"credits":{"balance":"62500","has_credits":true,"unlimited":false},"rate_limit_reset_credits":{"available_count":2,"credits":[{"expires_at":"2026-11-01T00:00:00Z"},{"expires_at":"2026-10-30T00:00:00Z"}]}}`)
	snapshot, err := Snapshot(asset, usage, quota, "driver@example.com", now)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.QuotaWindows) != 4 {
		t.Fatalf("windows = %#v", snapshot.QuotaWindows)
	}
	for i, w := range snapshot.QuotaWindows {
		want := []float64{0, 12, 24, 81}[i]
		if w.Used == nil || *w.Used != want || w.Remaining == nil || *w.Remaining != 100-want || w.ResetAtMS == nil {
			t.Fatalf("window = %#v", w)
		}
		if w.IsPrimary != (w.ID == "personal-7d") {
			t.Fatalf("only the personal weekly quota should be primary: %#v", w)
		}
		if w.Unit != "percent" || w.UsedCostUSD == nil || *w.UsedCostUSD != []float64{0, 12.56, 68.80, 256.44}[i] {
			t.Fatalf("upstream cost must remain separate from percentage quota: %#v", w)
		}
	}
	if snapshot.QuotaWindows[2].Scope != "personal" || snapshot.QuotaWindows[3].Scope != "vehicle" || snapshot.Credits.Balance != "62500" || snapshot.ResetCreditsAvailable == nil || *snapshot.ResetCreditsAvailable != 2 || len(snapshot.ResetCredits) != 2 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
}

func TestSnapshotOptionalUpstreamUsageCosts(t *testing.T) {
	for _, raw := range []string{
		`{"seat_percent_7d":7}`,
		`{"seat_percent_7d":7,"seat_cost_7d":null}`,
	} {
		snapshot, err := Snapshot(Asset{}, []byte(raw), nil, "", time.Now())
		if err != nil || len(snapshot.QuotaWindows) != 1 || snapshot.QuotaWindows[0].UsedCostUSD != nil {
			t.Fatalf("missing cost must stay unknown: %#v, %v", snapshot, err)
		}
		encoded, err := json.Marshal(snapshot)
		if err != nil || strings.Contains(string(encoded), "used_cost_usd") {
			t.Fatalf("missing cost must be omitted from the response: %s, %v", encoded, err)
		}
	}
	for _, cost := range []string{`-1`, `"68.80"`, `{}`, `1e400`} {
		t.Run(cost, func(t *testing.T) {
			usage := []byte(`{"seat_percent_7d":7,"seat_cost_7d":` + cost + `}`)
			if _, err := Snapshot(Asset{}, usage, nil, "", time.Now()); err == nil {
				t.Fatal("invalid upstream cost was accepted")
			}
		})
	}
}

func TestSnapshotOverLimitPreservesUsageAndBoundsUtilization(t *testing.T) {
	snapshot, err := Snapshot(Asset{}, []byte(`{"seat_percent_7d":137.5}`), nil, "", time.Now())
	if err != nil || len(snapshot.QuotaWindows) != 1 {
		t.Fatalf("snapshot = %#v, %v", snapshot, err)
	}
	window := snapshot.QuotaWindows[0]
	if window.Used == nil || *window.Used != 137.5 || window.Utilization == nil || *window.Utilization != 1 || window.Remaining == nil || *window.Remaining != 0 || window.State != "exhausted" {
		t.Fatalf("over-limit window = %#v", window)
	}
}

func TestSnapshotMissingDataAndCachedZeroBalance(t *testing.T) {
	var asset Asset
	if err := json.Unmarshal([]byte(`{"openai_quota":{"credits_snapshot":{"credits":{"balance":"0","has_credits":false}},"reset_credit_snapshot":{"available_count":0,"credits":[]}}}`), &asset); err != nil {
		t.Fatal(err)
	}
	snapshot, err := Snapshot(asset, []byte(`{"seat_percent_7d":null}`), nil, "", time.Now())
	if err != nil || len(snapshot.QuotaWindows) != 0 || snapshot.Credits.Balance != "0" || snapshot.ResetCreditsAvailable == nil || *snapshot.ResetCreditsAvailable != 0 {
		t.Fatalf("snapshot = %#v, %v", snapshot, err)
	}
	if _, err := Snapshot(asset, []byte(`{"seat_percent_7d":"24"}`), nil, "", time.Now()); err == nil {
		t.Fatal("malformed quota was accepted")
	}
}

func TestClientWebsiteAuthenticationAndNoRedirect(t *testing.T) {
	redirected := false
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected = true }))
	defer destination.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/redirect" {
			http.Redirect(w, r, destination.URL, http.StatusFound)
			return
		}
		if r.Header.Get("Authorization") != "Bearer website-token" || r.Header.Get("X-User-UI-Request") != "1" {
			t.Error("incorrect website authorization")
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"code":0,"data":[{"round_id":7,"active_seat_ids":[9],"status":"active"}]}`)
	}))
	defer server.Close()
	client := Client{BaseURL: server.URL, HTTP: server.Client()}
	assets, err := client.Assets(t.Context(), "website-token")
	if err != nil || len(assets) != 1 || assets[0].ActiveSeatIDs[0] != 9 {
		t.Fatalf("assets = %#v, %v", assets, err)
	}
	if err := client.Request(t.Context(), http.MethodGet, "/redirect", "website-token", nil, nil); err == nil || redirected {
		t.Fatal("authorization redirect was followed")
	}
}

func TestClientErrorsDoNotExposeSecretsAndLoginSupportsTwoFactor(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/auth/login" {
			fmt.Fprint(w, `{"code":0,"data":{"temp_token":"temporary"}}`)
			return
		}
		if r.URL.Path == "/api/v1/auth/login/2fa" {
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["temp_token"] != "temporary" || body["totp_code"] != "123456" || body["password"] != "" {
				t.Error("invalid 2FA request")
			}
			fmt.Fprint(w, `{"code":0,"data":{"access_token":"website","refresh_token":"rotation"}}`)
			return
		}
		w.WriteHeader(401)
		fmt.Fprint(w, `password=secret api_key=sk-test`)
	}))
	defer server.Close()
	client := Client{BaseURL: server.URL, HTTP: server.Client()}
	challenge, err := client.Login(t.Context(), "a@example.com", "secret", "", "")
	if err != nil || challenge.TempToken != "temporary" {
		t.Fatal(err)
	}
	tokens, err := client.Login(t.Context(), "", "", "temporary", "123456")
	if err != nil || tokens.AccessToken != "website" {
		t.Fatal(err)
	}
	err = client.Request(t.Context(), http.MethodGet, "/private", "", nil, nil)
	if err == nil || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "sk-test") {
		t.Fatalf("unsafe error = %v", err)
	}
}

func TestWebsiteURLPreservesPrefix(t *testing.T) {
	got, err := WebsiteURL("HTTPS://EXAMPLE.COM:443/team-a/v1/")
	if err != nil || got != "https://example.com/team-a" {
		t.Fatalf("url = %s, %v", got, err)
	}
	for _, value := range []string{"https://u:password@example.com", "https://example.com?key=secret", "ftp://example.com"} {
		if _, err := WebsiteURL(value); err == nil {
			t.Fatalf("accepted %s", value)
		}
	}
}
