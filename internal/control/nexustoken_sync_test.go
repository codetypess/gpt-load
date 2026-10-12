package control

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"gpt-load/internal/channel"
	"gpt-load/internal/storage/models"
)

type controlledNexusTokenSync struct {
	calls    chan struct{}
	release  chan struct{}
	returned chan struct{}
}

func (syncer *controlledNexusTokenSync) SyncNexusTokenObservations(ctx context.Context) {
	syncer.calls <- struct{}{}
	select {
	case <-syncer.release:
	case <-ctx.Done():
	}
	syncer.returned <- struct{}{}
}

func TestRuntimeSyncsNexusTokenImmediatelyAndEveryFiveMinutesWithoutOverlap(t *testing.T) {
	t.Parallel()
	syncer := &controlledNexusTokenSync{
		calls: make(chan struct{}, 8), release: make(chan struct{}), returned: make(chan struct{}, 8),
	}
	validationTicker, syncTicker := newFakeRuntimeTicker(), newFakeRuntimeTicker()
	created := make(chan time.Duration, 2)
	runtime := newTestRuntime(newFakeValidationSweep(false), validationTicker, created, time.Now)
	runtime.nexusTokenObservations = syncer
	runtime.newTicker = func(interval time.Duration) runtimeTicker {
		created <- interval
		if interval == 32*time.Minute {
			return validationTicker
		}
		return syncTicker
	}
	cancel, done := startRuntime(t, runtime)
	if interval := awaitValue(t, created); interval != 32*time.Minute {
		t.Fatalf("validation interval = %v", interval)
	}
	if interval := awaitValue(t, created); interval != 5*time.Minute {
		t.Fatalf("NexusToken sync interval = %v, want 5m", interval)
	}
	awaitSignal(t, syncer.calls)
	syncTicker.ticks <- time.Now().Add(5 * time.Minute)
	select {
	case <-syncer.calls:
		t.Fatal("automatic sync overlapped the previous sweep")
	case <-time.After(25 * time.Millisecond):
	}
	syncer.release <- struct{}{}
	awaitSignal(t, syncer.returned)
	awaitSignal(t, syncer.calls)
	cancel()
	awaitSignal(t, syncer.returned)
	awaitSignal(t, done)
	awaitSignal(t, syncTicker.stopped)
	awaitSignal(t, validationTicker.stopped)
	select {
	case <-syncer.calls:
		t.Fatal("automatic sync continued after shutdown")
	default:
	}
}

func TestNexusTokenAutomaticSyncFiltersTargetsAndRecoversAfterFailure(t *testing.T) {
	fixture := newServiceFixture(t)
	clock := &fakeRuntimeClock{now: time.Date(2026, time.October, 15, 1, 0, 0, 0, time.UTC)}
	fixture.service.now = clock.current
	var usageRequests atomic.Int32
	var failFirst atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer website-token" {
			t.Error("automatic sync did not use the website session")
		}
		switch r.URL.Path {
		case "/api/v1/carpool/my/assets":
			fmt.Fprint(w, `{"code":0,"data":[{"round_id":7,"active_seat_ids":[9,10],"status":"active","product_name":"Pro"}]}`)
		case "/api/v1/carpool/my/seats/9/usage", "/api/v1/carpool/my/seats/10/usage":
			usageRequests.Add(1)
			if r.URL.Path == "/api/v1/carpool/my/seats/9/usage" && failFirst.Load() {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			fmt.Fprint(w, `{"code":0,"data":{"seat_percent_7d":24,"account_percent_7d":80,"seat_cost_7d":68.80}}`)
		case "/api/v1/carpool/my/api-key-bindings":
			fmt.Fprint(w, `{"code":0,"data":[{"api_key":{"id":33},"seat_id":9},{"api_key":{"id":34},"seat_id":10}]}`)
		case "/api/v1/carpool/my/seats/9/openai-quota/refresh", "/api/v1/carpool/my/seats/10/openai-quota/refresh":
			if r.Method != http.MethodPost {
				t.Error("incorrect quota request method")
			}
			fmt.Fprint(w, `{"code":0,"data":{"credits":{"balance":"62500"},"rate_limit_reset_credits":{"available_count":2}}}`)
		default:
			t.Errorf("unexpected automatic request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	encrypted, err := fixture.encryption.Encrypt(`{"access_token":"website-token"}`)
	if err != nil {
		t.Fatal(err)
	}
	session := models.NexusTokenSession{ID: "website-session", BaseURL: upstream.URL, UserID: 1, EncryptedPayload: encrypted, UpdatedAtMS: clock.current().UnixMilli()}
	if err := fixture.db.Create(&session).Error; err != nil {
		t.Fatal(err)
	}
	create := func(name string, channelID channel.ID, seatID, keyID int) (uint, models.Credential) {
		t.Helper()
		fields := map[string]string{"api_key": "sk-" + name}
		if seatID != 0 {
			fields["session_id"] = session.ID
			fields["seat_id"], fields["key_id"] = fmt.Sprint(seatID), fmt.Sprint(keyID)
		}
		payload, _ := json.Marshal(fields)
		created, err := fixture.service.CreateGroup(t.Context(), GroupCreateRequest{
			Name: stringPointer(name), ChannelID: channelID, ConnectionType: models.ConnectionTypeAPIKey,
			Params: json.RawMessage(`{"base_url":"` + upstream.URL + `"}`), Credentials: string(payload), Models: optionalGroupModels{Set: true},
			ConfirmSameTarget: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		var credential models.Credential
		if err := fixture.db.Where("group_id = ?", created.GroupID).Take(&credential).Error; err != nil {
			t.Fatal(err)
		}
		return created.GroupID, credential
	}
	_, first := create("first", channel.NexusToken, 9, 33)
	_, second := create("second", channel.NexusToken, 10, 34)
	create("unbound", channel.NexusToken, 0, 0)
	create("invalid-seat", channel.NexusToken, -1, 33)
	create("other-channel", channel.OpenAI, 0, 0)
	disabledGroup, _ := create("disabled-group", channel.NexusToken, 9, 33)
	if err := fixture.db.Model(&models.Group{}).Where("id = ?", disabledGroup).Update("enabled", false).Error; err != nil {
		t.Fatal(err)
	}
	_, disabled := create("disabled-credential", channel.NexusToken, 9, 33)
	if err := fixture.db.Model(&disabled).Update("status", models.CredentialStatusDisabled).Error; err != nil {
		t.Fatal(err)
	}
	_, reauthorize := create("reauthorize", channel.NexusToken, 9, 33)
	if err := fixture.db.Model(&reauthorize).Update("auth_state", models.CredentialAuthStateReauthorizationRequired).Error; err != nil {
		t.Fatal(err)
	}
	read := func(id uint) models.CredentialObservation {
		t.Helper()
		var row models.CredentialObservation
		if err := fixture.db.Take(&row, "credential_id = ?", id).Error; err != nil {
			t.Fatal(err)
		}
		return row
	}
	fixture.service.SyncNexusTokenObservations(t.Context())
	if got := usageRequests.Load(); got != 2 {
		t.Fatalf("usage requests = %d, want only the two active bound keys", got)
	}
	before := read(first.ID)
	var snapshot CredentialObservationSnapshot
	if err := json.Unmarshal(before.SnapshotJSON, &snapshot); err != nil || len(snapshot.QuotaWindows) != 2 || snapshot.Credits == nil || snapshot.ResetCreditsAvailable == nil {
		t.Fatalf("automatic sync did not persist quotas, points and reset cards: %#v, %v", snapshot, err)
	}
	failFirst.Store(true)
	clock.set(clock.current().Add(5 * time.Minute))
	fixture.service.SyncNexusTokenObservations(t.Context())
	afterFailure, healthy := read(first.ID), read(second.ID)
	if string(afterFailure.SnapshotJSON) != string(before.SnapshotJSON) || afterFailure.LastErrorCode == "" || *afterFailure.LastAttemptAtMS != clock.current().UnixMilli() {
		t.Fatalf("failed automatic sync lost cached data or attempt metadata: %#v", afterFailure)
	}
	if healthy.ObservationVersion != 2 || *healthy.ObservedAtMS != clock.current().UnixMilli() {
		t.Fatalf("one failed account blocked the next account: %#v", healthy)
	}
	failFirst.Store(false)
	clock.set(clock.current().Add(5 * time.Minute))
	fixture.service.SyncNexusTokenObservations(t.Context())
	recovered := read(first.ID)
	if recovered.State != models.CredentialObservationFresh || recovered.LastErrorCode != "" || *recovered.ObservedAtMS != clock.current().UnixMilli() {
		t.Fatalf("automatic retry did not recover: %#v", recovered)
	}
	var count int64
	if err := fixture.db.Model(&models.CredentialObservation{}).Count(&count).Error; err != nil || count != 2 {
		t.Fatalf("unexpected observations for skipped keys: %d, %v", count, err)
	}
	if got := usageRequests.Load(); got != 6 {
		t.Fatalf("usage requests after three syncs = %d, want 6", got)
	}
}
