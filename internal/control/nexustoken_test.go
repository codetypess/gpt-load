package control

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gpt-load/internal/channel"
	"gpt-load/internal/outboundproxy"
	"gpt-load/internal/storage/models"
)

func TestNexusTokenLoginImportRefreshAndFailurePreserveInference(t *testing.T) {
	fixture := newServiceFixture(t)
	var rotations atomic.Int32
	var failUsage atomic.Bool
	var quotaUnavailable atomic.Bool
	var bindingChanged atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1/auth/login" {
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["email"] != "driver@example.com" || body["password"] != "website-password" {
				t.Error("incorrect login")
			}
			fmt.Fprint(w, `{"code":0,"data":{"access_token":"website-old","refresh_token":"refresh-old","expires_in":1,"user":{"id":12,"email":"driver@example.com"}}}`)
			return
		}
		if r.URL.Path == "/api/v1/auth/refresh" {
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["refresh_token"] != "refresh-old" {
				t.Error("wrong refresh token")
			}
			rotations.Add(1)
			fmt.Fprint(w, `{"code":0,"data":{"access_token":"website-new","refresh_token":"refresh-new","expires_in":3600}}`)
			return
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer website-old" && auth != "Bearer website-new" {
			t.Error("inference key authorized website request")
		}
		switch r.URL.Path {
		case "/api/v1/carpool/my/assets":
			fmt.Fprint(w, `{"code":0,"data":[{"round_id":7,"active_seat_ids":[9],"status":"active","product_name":"Pro 20x"}]}`)
		case "/api/v1/carpool/my/api-key-bindings":
			if bindingChanged.Load() {
				fmt.Fprint(w, `{"code":0,"data":[]}`)
				return
			}
			fmt.Fprint(w, `{"code":0,"data":[{"api_key":{"id":33,"name":"carpool-key"},"seat_id":9,"seat":{"round_id":7}}]}`)
		case "/api/v1/keys/33":
			fmt.Fprint(w, `{"code":0,"data":{"id":33,"scope":"carpool","key":"sk-inference-only"}}`)
		case "/api/v1/carpool/my/seats/9/usage":
			if failUsage.Load() {
				w.WriteHeader(503)
				return
			}
			fmt.Fprint(w, `{"code":0,"data":{"seat_percent_7d":24,"account_percent_7d":80,"seat_cost_7d":68.80,"account_cost_7d":256.44,"reset_7d_at":"2026-10-18T00:00:00Z"}}`)
		case "/api/v1/carpool/my/seats/9/openai-quota/refresh":
			if r.Method != http.MethodPost {
				t.Error("wrong quota query method")
			}
			if quotaUnavailable.Load() {
				w.WriteHeader(503)
				return
			}
			fmt.Fprint(w, `{"code":0,"data":{"credits":{"balance":"62500"},"rate_limit_reset_credits":{"available_count":2,"credits":[{"expires_at":"2026-10-30T00:00:00Z"}]}}}`)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	login, err := fixture.service.loginNexusToken(t.Context(), nexusTokenLoginRequest{BaseURL: upstream.URL + "/v1", Email: "driver@example.com", Password: "website-password", Proxy: &outboundproxy.Config{Mode: outboundproxy.ModeDirect}})
	if err != nil || len(login.Choices) != 1 {
		t.Fatalf("login = %#v, %v", login, err)
	}
	choice := login.Choices[0]
	for _, secret := range []string{"website-old", "refresh-old", "website-password"} {
		if strings.Contains(choice.Credential, secret) {
			t.Fatal("website secret leaked into inference credential")
		}
	}
	var session models.NexusTokenSession
	if err := fixture.db.Take(&session).Error; err != nil {
		t.Fatal(err)
	}
	if strings.Contains(session.EncryptedPayload, "website-old") {
		t.Fatal("website session not encrypted")
	}
	created, err := fixture.service.CreateGroup(t.Context(), GroupCreateRequest{
		Name: stringPointer("carpool"), ChannelID: channel.NexusToken, ConnectionType: models.ConnectionTypeAPIKey,
		Params: json.RawMessage(`{"base_url":"` + upstream.URL + `/v1"}`), Credentials: choice.Credential,
		Models: optionalGroupModels{Set: true, Values: []GroupModel{{ID: "gpt-test"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var credential models.Credential
	if err := fixture.db.Where("group_id = ?", created.GroupID).Take(&credential).Error; err != nil {
		t.Fatal(err)
	}
	observation, err := fixture.service.RefreshCredentialObservation(t.Context(), created.GroupID, credential.ID)
	if err != nil || observation.State != "fresh" || observation.Snapshot == nil || len(observation.Snapshot.QuotaWindows) != 2 {
		t.Fatalf("observation = %#v, %v", observation, err)
	}
	if rotations.Load() != 1 {
		t.Fatalf("token rotations = %d", rotations.Load())
	}
	var saved models.NexusTokenSession
	_ = fixture.db.Take(&saved).Error
	decoded, _ := fixture.encryption.Decrypt(saved.EncryptedPayload)
	if !strings.Contains(decoded, "refresh-new") {
		t.Fatal("rotated refresh token not persisted")
	}
	collection, err := fixture.service.ListGroupCredentials(t.Context(), created.GroupID, CredentialCollectionQuery{Page: 1, PageSize: 20})
	if err != nil || len(collection.Items) != 1 || collection.Items[0].Observation == nil || collection.Items[0].Account.Email != "driver@example.com" {
		t.Fatalf("collection = %#v, %v", collection, err)
	}
	detail, err := fixture.service.GetCredentialDetail(t.Context(), created.GroupID, credential.ID)
	if err != nil || detail.Observation.Snapshot == nil {
		t.Fatalf("detail = %#v, %v", detail, err)
	}
	for _, snapshot := range []*CredentialObservationSnapshot{observation.Snapshot, collection.Items[0].Observation.Snapshot, detail.Observation.Snapshot} {
		for i, cost := range []float64{68.80, 256.44} {
			if snapshot.QuotaWindows[i].UsedCostUSD == nil || *snapshot.QuotaWindows[i].UsedCostUSD != cost {
				t.Fatalf("upstream costs lost in refresh, list or detail response: %#v", snapshot.QuotaWindows)
			}
		}
	}
	quotaUnavailable.Store(true)
	observation, err = fixture.service.RefreshCredentialObservation(t.Context(), created.GroupID, credential.ID)
	if err != nil || observation.State != "stale" || observation.LastErrorCode != "observation_partial" || observation.Snapshot.Credits == nil || observation.Snapshot.Credits.Balance != "62500" || observation.Snapshot.ResetCreditsAvailable == nil {
		t.Fatalf("partial = %#v, %v", observation, err)
	}
	failUsage.Store(true)
	_, err = fixture.service.RefreshCredentialObservation(t.Context(), created.GroupID, credential.ID)
	if err == nil {
		t.Fatal("failed query was accepted")
	}
	cached, err := fixture.service.GetCredentialObservation(t.Context(), created.GroupID, credential.ID)
	if err != nil || cached.Snapshot == nil || len(cached.Snapshot.QuotaWindows) != 2 {
		t.Fatal("last known quota lost")
	}
	if cached.Snapshot.QuotaWindows[0].UsedCostUSD == nil || *cached.Snapshot.QuotaWindows[0].UsedCostUSD != 68.80 {
		t.Fatal("last known upstream cost lost")
	}
	failUsage.Store(false)
	bindingChanged.Store(true)
	_, err = fixture.service.RefreshCredentialObservation(t.Context(), created.GroupID, credential.ID)
	if err == nil {
		t.Fatal("unbound key still queried old carpool")
	}
	var after models.Credential
	_ = fixture.db.Take(&after, credential.ID).Error
	if after.Data != credential.Data || after.Status != credential.Status || after.AuthState != credential.AuthState || after.SecretVersion != credential.SecretVersion {
		t.Fatal("observation failure changed inference credential")
	}
}

func TestNexusTokenUnauthorizedRotatesOnceAndSharesSession(t *testing.T) {
	fixture := newServiceFixture(t)
	var rotations atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/auth/refresh" {
			rotations.Add(1)
			fmt.Fprint(w, `{"code":0,"data":{"access_token":"new","refresh_token":"rotated","expires_in":3600}}`)
			return
		}
		if r.Header.Get("Authorization") != "Bearer new" {
			w.WriteHeader(401)
			return
		}
		fmt.Fprint(w, `{"code":0,"data":[]}`)
	}))
	defer upstream.Close()
	var tokens struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	tokens.AccessToken = "old"
	tokens.RefreshToken = "old-refresh"
	encoded, _ := json.Marshal(tokens)
	encrypted, _ := fixture.encryption.Encrypt(string(encoded))
	session := models.NexusTokenSession{ID: "shared-session", BaseURL: upstream.URL, UserID: 1, Email: "a@example.com", EncryptedPayload: encrypted, UpdatedAtMS: time.Now().UnixMilli()}
	if err := fixture.db.Create(&session).Error; err != nil {
		t.Fatal(err)
	}
	for range 2 {
		var output []any
		if err := fixture.service.nexusTokenRequest(t.Context(), session.ID, upstream.URL, http.MethodGet, "/carpool/my/assets", &output); err != nil {
			t.Fatal(err)
		}
	}
	if rotations.Load() != 1 {
		t.Fatalf("rotations = %d", rotations.Load())
	}
	var output []any
	if err := fixture.service.nexusTokenRequest(t.Context(), session.ID, "https://other.example", http.MethodGet, "/carpool/my/assets", &output); err == nil {
		t.Fatal("session reused for other origin")
	}
}

func TestNexusTokenAccountImportLinksExistingAPIKey(t *testing.T) {
	fixture := newServiceFixture(t)
	created, err := fixture.service.CreateGroup(t.Context(), GroupCreateRequest{
		Name: stringPointer("link carpool"), ChannelID: channel.NexusToken, ConnectionType: models.ConnectionTypeAPIKey,
		Params: json.RawMessage(`{"base_url":"https://nexus.example"}`), Credentials: "sk-same-key",
		Models: optionalGroupModels{Set: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	var before models.Credential
	if err := fixture.db.Where("group_id = ?", created.GroupID).Take(&before).Error; err != nil {
		t.Fatal(err)
	}
	result, err := fixture.service.ImportGroupCredentials(t.Context(), created.GroupID, CredentialImportRequest{
		Credentials: `{"api_key":"sk-same-key","session_id":"website-account","key_id":"33","seat_id":"9","email":"driver@example.com"}`,
	})
	if err != nil || result.CredentialsAdded != 0 || result.CredentialsDuplicated != 1 {
		t.Fatalf("import = %#v, %v", result, err)
	}
	var after models.Credential
	if err := fixture.db.Take(&after, before.ID).Error; err != nil {
		t.Fatal(err)
	}
	canonical, _, err := fixture.service.decodeCredential(models.Group{ChannelID: string(channel.NexusToken), ConnectionType: models.ConnectionTypeAPIKey}, after)
	if err != nil || !strings.Contains(string(canonical), "website-account") || before.IdentityFingerprint != after.IdentityFingerprint || after.SecretVersion != before.SecretVersion+1 {
		t.Fatalf("binding not updated: %s, %v", canonical, err)
	}
	_, err = fixture.service.ImportGroupCredentials(t.Context(), created.GroupID, CredentialImportRequest{Credentials: "sk-same-key"})
	if err != nil {
		t.Fatal(err)
	}
	var latest models.Credential
	_ = fixture.db.Take(&latest, before.ID).Error
	if latest.Data != after.Data {
		t.Fatal("manual API key import erased account binding")
	}
}

func TestNexusTokenRelinkingDiscardsInFlightObservation(t *testing.T) {
	fixture := newServiceFixture(t)
	started := make(chan struct{})
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/carpool/my/assets":
			fmt.Fprint(w, `{"code":0,"data":[{"round_id":7,"active_seat_ids":[9],"status":"active"}]}`)
		case "/api/v1/carpool/my/seats/9/usage":
			close(started)
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
			fmt.Fprint(w, `{"code":0,"data":{"seat_percent_7d":24}}`)
		case "/api/v1/carpool/my/api-key-bindings":
			fmt.Fprint(w, `{"code":0,"data":[{"api_key":{"id":33},"seat_id":9}]}`)
		case "/api/v1/carpool/my/seats/9/openai-quota/refresh":
			fmt.Fprint(w, `{"code":0,"data":{}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	defer close(release)
	encrypted, _ := fixture.encryption.Encrypt(`{"access_token":"website-token"}`)
	session := models.NexusTokenSession{ID: "old-account", BaseURL: upstream.URL, UserID: 1, Email: "old@example.com", EncryptedPayload: encrypted, UpdatedAtMS: time.Now().UnixMilli()}
	if err := fixture.db.Create(&session).Error; err != nil {
		t.Fatal(err)
	}
	created, err := fixture.service.CreateGroup(t.Context(), GroupCreateRequest{
		Name: stringPointer("relink carpool"), ChannelID: channel.NexusToken, ConnectionType: models.ConnectionTypeAPIKey,
		Params:      json.RawMessage(`{"base_url":"` + upstream.URL + `"}`),
		Credentials: `{"api_key":"sk-same-key","session_id":"old-account","seat_id":"9","key_id":"33","email":"old@example.com"}`,
		Models:      optionalGroupModels{Set: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	var credential models.Credential
	if err := fixture.db.Where("group_id = ?", created.GroupID).Take(&credential).Error; err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		result, err := fixture.service.RefreshCredentialObservation(t.Context(), created.GroupID, credential.ID)
		if err == nil && result.Snapshot != nil {
			err = fmt.Errorf("old binding published a snapshot")
		}
		done <- err
	}()
	select {
	case <-started:
	case err := <-done:
		t.Fatalf("refresh returned before usage request: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("usage request did not start")
	}
	if _, err := fixture.service.ImportGroupCredentials(t.Context(), created.GroupID, CredentialImportRequest{
		Credentials: `{"api_key":"sk-same-key","session_id":"new-account","seat_id":"10","key_id":"44","email":"new@example.com"}`,
	}); err != nil {
		t.Fatal(err)
	}
	release <- struct{}{}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("refresh did not finish")
	}
	var count int64
	if err := fixture.db.Model(&models.CredentialObservation{}).Where("credential_id = ?", credential.ID).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("old observation persisted: count=%d, error=%v", count, err)
	}
}
