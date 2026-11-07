package control

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gpt-load/internal/channel"
	"gpt-load/internal/platform/config"
	app_errors "gpt-load/internal/platform/errors"
	"gpt-load/internal/storage/models"
)

func newNexusTokenResetFixture(t *testing.T, override func(http.ResponseWriter, *http.Request) bool) (serviceFixture, uint, models.Credential, *atomic.Int32) {
	t.Helper()
	fixture := newServiceFixture(t)
	calls := &atomic.Int32{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if (auth != "Bearer website-token" && auth != "Bearer website-rotated" && r.URL.Path != "/api/v1/auth/refresh") || r.Header.Get("X-User-UI-Request") != "1" {
			t.Error("reset must use the website session, never the inference key")
		}
		if strings.HasSuffix(r.URL.Path, "/openai-quota/reset") {
			calls.Add(1)
			body, _ := io.ReadAll(r.Body)
			if r.Method != http.MethodPost || len(body) != 0 {
				t.Errorf("reset request = %s %q, want POST without a body", r.Method, body)
			}
		}
		if override != nil && override(w, r) {
			return
		}
		switch r.URL.Path {
		case "/api/v1/carpool/my/assets":
			fmt.Fprint(w, `{"code":0,"data":[{"round_id":7,"active_seat_ids":[9],"product_name":"ChatGPT Pro 200","status":"active"}]}`)
		case "/api/v1/carpool/my/api-key-bindings":
			fmt.Fprint(w, `{"code":0,"data":[{"api_key":{"id":33},"seat_id":9}]}`)
		case "/api/v1/carpool/my/seats/9/usage":
			used := 100
			if calls.Load() > 0 {
				used = 0
			}
			fmt.Fprintf(w, `{"code":0,"data":{"seat_percent_7d":%d,"account_percent_7d":%d}}`, used, used)
		case "/api/v1/carpool/my/seats/9/openai-quota/refresh":
			fmt.Fprintf(w, `{"code":0,"data":{"rate_limit_reset_credits":{"available_count":%d}}}`, 2-calls.Load())
		case "/api/v1/carpool/my/seats/9/openai-quota/reset":
			fmt.Fprint(w, `{"code":0,"data":{"windows_reset":2}}`)
		default:
			t.Errorf("unexpected upstream request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)
	encrypted, err := fixture.encryption.Encrypt(`{"access_token":"website-token"}`)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Create(&models.NexusTokenSession{
		ID: "website-session", BaseURL: upstream.URL, UserID: 1, EncryptedPayload: encrypted,
	}).Error; err != nil {
		t.Fatal(err)
	}
	created, err := fixture.service.CreateGroup(t.Context(), GroupCreateRequest{
		Name: stringPointer("NexusToken reset"), ChannelID: channel.NexusToken, ConnectionType: models.ConnectionTypeAPIKey,
		Params:      json.RawMessage(`{"base_url":"` + upstream.URL + `/v1"}`),
		Credentials: `{"api_key":"sk-inference-only","session_id":"website-session","seat_id":"9","key_id":"33","email":"driver@example.com"}`,
		Models:      optionalGroupModels{Set: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	var credential models.Credential
	if err := fixture.db.Take(&credential, "group_id = ?", created.GroupID).Error; err != nil {
		t.Fatal(err)
	}
	return fixture, created.GroupID, credential, calls
}

func TestNexusTokenResetHTTPRefreshesQuotaAndReplaysWithoutConsuming(t *testing.T) {
	initControlI18n(t)
	fixture, groupID, credential, calls := newNexusTokenResetFixture(t, nil)
	if _, err := fixture.service.RefreshCredentialObservation(t.Context(), groupID, credential.ID); err != nil {
		t.Fatal(err)
	}
	ref, _ := fixture.registry.CredentialRef(credential.ID)
	fixture.registry.SetModelCooldown(ref, "gpt-test", time.Now().Add(time.Hour), time.Now())
	router := gin.New()
	NewServer(&config.Config{AuthKey: "test-auth-key"}, fixture.service).RegisterRoutes(router)
	for attempt := 0; attempt < 2; attempt++ {
		request := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/groups/%d/credentials/%d/reset-credits/consume", groupID, credential.ID), strings.NewReader(`{}`))
		request.Header.Set("Authorization", "Bearer test-auth-key")
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Idempotency-Key", resetCreditTestKey)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		var envelope struct {
			Data ResetCreditConsumeResponse `json:"data"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		result := envelope.Data
		if response.Code != http.StatusOK || result.Status != "succeeded" || result.WindowsReset != 2 || result.Replayed != (attempt > 0) || result.ObservationPending || result.Observation == nil || result.Observation.State != "fresh" {
			t.Fatalf("reset response = %d %s", response.Code, response.Body)
		}
		snapshot := result.Observation.Snapshot
		if snapshot == nil || snapshot.ResetCreditsAvailable == nil || *snapshot.ResetCreditsAvailable != 1 || len(snapshot.QuotaWindows) != 2 || *snapshot.QuotaWindows[0].Used != 0 {
			t.Fatalf("reset did not refresh cards and usage: %#v", snapshot)
		}
		if len(fixture.registry.ModelCooldowns(credential.ID, time.Now())) != attempt {
			t.Fatal("reset or replay incorrectly cleared model cooldowns")
		}
		ref, _ = fixture.registry.CredentialRef(credential.ID)
		fixture.registry.SetModelCooldown(ref, "gpt-test", time.Now().Add(time.Hour), time.Now())
		for _, secret := range []string{"website-token", "website-session", "sk-inference-only"} {
			if strings.Contains(response.Body.String(), secret) {
				t.Fatal("reset response leaked a credential")
			}
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("reset calls = %d, want 1", calls.Load())
	}
}

func TestNexusTokenResetErrorsNeverResendUncertainOperations(t *testing.T) {
	for _, test := range []struct {
		name, body, code string
		status           int
		unknown          bool
	}{
		{"ineligible", "", app_errors.ErrResetCreditRejected.Code, 400, false},
		{"expired session", "", app_errors.ErrCredentialReauthorizationRequired.Code, 401, false},
		{"forbidden", "", app_errors.ErrCredentialReauthorizationRequired.Code, 403, false},
		{"missing card", "", app_errors.ErrResetCreditUnavailable.Code, 409, false},
		{"cooldown", "", app_errors.ErrResetCreditRejected.Code, 429, false},
		{"upstream failure", "", app_errors.ErrResetCreditOutcomeUnknown.Code, 503, true},
		{"malformed", "{", app_errors.ErrResetCreditOutcomeUnknown.Code, 200, true},
		{"missing result", `{"code":0,"data":{}}`, app_errors.ErrResetCreditOutcomeUnknown.Code, 200, true},
		{"negative result", `{"code":0,"data":{"windows_reset":-1}}`, app_errors.ErrResetCreditOutcomeUnknown.Code, 200, true},
		{"invalid result type", `{"code":0,"data":{"windows_reset":"2"}}`, app_errors.ErrResetCreditOutcomeUnknown.Code, 200, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture, groupID, credential, calls := newNexusTokenResetFixture(t, func(w http.ResponseWriter, r *http.Request) bool {
				if !strings.HasSuffix(r.URL.Path, "/openai-quota/reset") {
					return false
				}
				w.WriteHeader(test.status)
				fmt.Fprint(w, test.body)
				return true
			})
			_, err := fixture.service.ConsumeCredentialResetCredit(t.Context(), groupID, credential.ID, resetCreditTestKey)
			assertAPIErrorCode(t, err, test.code)
			_, err = fixture.service.ConsumeCredentialResetCredit(t.Context(), groupID, credential.ID, resetCreditTestKey)
			assertAPIErrorCode(t, err, test.code)
			if test.unknown {
				_, err = fixture.service.ConsumeCredentialResetCredit(t.Context(), groupID, credential.ID, "afe32eb9-d82b-4798-a65b-a6c158f7c54d")
				assertAPIErrorCode(t, err, app_errors.ErrResetCreditOutcomeUnknown.Code)
				if err := fixture.db.Model(&models.CredentialResetOperation{}).Where("credential_id = ?", credential.ID).Updates(map[string]any{
					"state": models.CredentialResetOperationPrepared, "updated_at_ms": time.Now().Add(-time.Hour).UnixMilli(),
				}).Error; err != nil {
					t.Fatal(err)
				}
				_, err = fixture.service.ConsumeCredentialResetCredit(t.Context(), groupID, credential.ID, resetCreditTestKey)
				assertAPIErrorCode(t, err, app_errors.ErrResetCreditOutcomeUnknown.Code)
			}
			if calls.Load() != 1 {
				t.Fatalf("uncertain or rejected operation was resent %d times", calls.Load())
			}
		})
	}
}

func TestNexusTokenResetChecksWebsiteOwnershipAndKeyBinding(t *testing.T) {
	for _, path := range []string{"assets", "api-key-bindings"} {
		t.Run(path, func(t *testing.T) {
			var denied atomic.Bool
			denied.Store(true)
			fixture, groupID, credential, calls := newNexusTokenResetFixture(t, func(w http.ResponseWriter, r *http.Request) bool {
				if denied.Load() && r.URL.Path == "/api/v1/carpool/my/"+path {
					fmt.Fprint(w, `{"code":0,"data":[]}`)
					return true
				}
				return false
			})
			_, err := fixture.service.ConsumeCredentialResetCredit(t.Context(), groupID, credential.ID, resetCreditTestKey)
			assertAPIErrorCode(t, err, app_errors.ErrCredentialReauthorizationRequired.Code)
			if calls.Load() != 0 {
				t.Fatal("reset dispatched with an invalid website binding")
			}
			denied.Store(false)
			if _, err := fixture.service.ConsumeCredentialResetCredit(t.Context(), groupID, credential.ID, "afe32eb9-d82b-4798-a65b-a6c158f7c54d"); err != nil || calls.Load() != 1 {
				t.Fatalf("new operation after fixing binding = %v, calls=%d", err, calls.Load())
			}
		})
	}
}

func TestNexusTokenResetKeepsSuccessfulConsumeWhenQuotaRefreshFails(t *testing.T) {
	var failUsage atomic.Bool
	fixture, groupID, credential, calls := newNexusTokenResetFixture(t, func(w http.ResponseWriter, r *http.Request) bool {
		if failUsage.Load() && strings.HasSuffix(r.URL.Path, "/usage") {
			w.WriteHeader(http.StatusServiceUnavailable)
			return true
		}
		return false
	})
	if _, err := fixture.service.RefreshCredentialObservation(t.Context(), groupID, credential.ID); err != nil {
		t.Fatal(err)
	}
	failUsage.Store(true)
	for attempt := 0; attempt < 2; attempt++ {
		result, err := fixture.service.ConsumeCredentialResetCredit(t.Context(), groupID, credential.ID, resetCreditTestKey)
		if err != nil || result.Status != "succeeded" || !result.ObservationPending || result.Observation == nil || result.Observation.State == "fresh" {
			t.Fatalf("failed follow-up must retain successful consume and stale observation: %#v, %v", result, err)
		}
	}
	if calls.Load() != 1 {
		t.Fatal("failed quota refresh repeated a successful reset")
	}
}

func TestNexusTokenResetBlocksConcurrentConsume(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	fixture, groupID, credential, calls := newNexusTokenResetFixture(t, func(w http.ResponseWriter, r *http.Request) bool {
		if strings.HasSuffix(r.URL.Path, "/openai-quota/reset") {
			close(started)
			<-release
		}
		return false
	})
	t.Cleanup(func() { close(release) })
	done := make(chan error, 1)
	go func() {
		_, err := fixture.service.ConsumeCredentialResetCredit(t.Context(), groupID, credential.ID, resetCreditTestKey)
		done <- err
	}()
	awaitSignal(t, started)
	for _, key := range []string{resetCreditTestKey, "afe32eb9-d82b-4798-a65b-a6c158f7c54d"} {
		_, err := fixture.service.ConsumeCredentialResetCredit(t.Context(), groupID, credential.ID, key)
		assertAPIErrorCode(t, err, app_errors.ErrResetCreditOutcomeUnknown.Code)
	}
	// Release the response before waiting for the first call to finish.
	release <- struct{}{}
	if err := awaitValue(t, done); err != nil || calls.Load() != 1 {
		t.Fatalf("concurrent reset = %v, calls=%d", err, calls.Load())
	}
}

func TestNexusTokenResetReplaysOnlyForOriginalWebsiteBinding(t *testing.T) {
	fixture, groupID, credential, calls := newNexusTokenResetFixture(t, nil)
	if _, err := fixture.service.ConsumeCredentialResetCredit(t.Context(), groupID, credential.ID, resetCreditTestKey); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.ImportGroupCredentials(t.Context(), groupID, CredentialImportRequest{
		Credentials: `{"api_key":"sk-inference-only","session_id":"new-account","seat_id":"10","key_id":"44","email":"new@example.com"}`,
	}); err != nil {
		t.Fatal(err)
	}
	_, err := fixture.service.ConsumeCredentialResetCredit(t.Context(), groupID, credential.ID, resetCreditTestKey)
	assertAPIErrorCode(t, err, app_errors.ErrIdempotencyKeyReused.Code)
	if calls.Load() != 1 {
		t.Fatal("old operation dispatched to a relinked website account")
	}
}

func TestNexusTokenResetDoesNotRestoreRelinkedCredential(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	fixture, groupID, credential, calls := newNexusTokenResetFixture(t, func(w http.ResponseWriter, r *http.Request) bool {
		if strings.HasSuffix(r.URL.Path, "/openai-quota/reset") {
			close(started)
			<-release
		}
		return false
	})
	t.Cleanup(func() { close(release) })
	type resetResult struct {
		response ResetCreditConsumeResponse
		err      error
	}
	done := make(chan resetResult, 1)
	go func() {
		response, err := fixture.service.ConsumeCredentialResetCredit(t.Context(), groupID, credential.ID, resetCreditTestKey)
		done <- resetResult{response, err}
	}()
	awaitSignal(t, started)
	if _, err := fixture.service.ImportGroupCredentials(t.Context(), groupID, CredentialImportRequest{
		Credentials: `{"api_key":"sk-inference-only","session_id":"new-account","seat_id":"10","key_id":"44","email":"new@example.com"}`,
	}); err != nil {
		t.Fatal(err)
	}
	ref, _ := fixture.registry.CredentialRef(credential.ID)
	fixture.registry.SetModelCooldown(ref, "gpt-test", time.Now().Add(time.Hour), time.Now())
	release <- struct{}{}
	result := awaitValue(t, done)
	if result.err != nil || result.response.Status != "succeeded" || !result.response.ObservationPending || calls.Load() != 1 {
		t.Fatalf("reset during relink = %#v, calls=%d", result, calls.Load())
	}
	if len(fixture.registry.ModelCooldowns(credential.ID, time.Now())) != 1 {
		t.Fatal("old website reset cleared the new binding's cooldown")
	}
}

func TestNexusTokenResetRotatesExpiredWebsiteSessionBeforeConsume(t *testing.T) {
	var rotations atomic.Int32
	fixture, groupID, credential, calls := newNexusTokenResetFixture(t, func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path == "/api/v1/auth/refresh" {
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["refresh_token"] != "rotation-token" || r.Header.Get("Authorization") != "" {
				t.Error("invalid website token rotation")
			}
			rotations.Add(1)
			fmt.Fprint(w, `{"code":0,"data":{"access_token":"website-rotated","refresh_token":"rotated-refresh","expires_in":3600}}`)
			return true
		}
		if r.Header.Get("Authorization") != "Bearer website-rotated" {
			t.Error("reset used an expired website token")
		}
		return false
	})
	encrypted, err := fixture.encryption.Encrypt(`{"access_token":"website-token","refresh_token":"rotation-token","expires_at_ms":1}`)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&models.NexusTokenSession{}).Where("id = ?", "website-session").Update("encrypted_payload", encrypted).Error; err != nil {
		t.Fatal(err)
	}
	result, err := fixture.service.ConsumeCredentialResetCredit(t.Context(), groupID, credential.ID, resetCreditTestKey)
	if err != nil || result.Status != "succeeded" || result.ObservationPending || rotations.Load() != 1 || calls.Load() != 1 {
		t.Fatalf("rotated reset = %#v, error=%v, rotations=%d, calls=%d", result, err, rotations.Load(), calls.Load())
	}
}
