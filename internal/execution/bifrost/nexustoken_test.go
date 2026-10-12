package bifrost

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"gpt-load/internal/catalog"
	"gpt-load/internal/channel"
	"gpt-load/internal/execution"
	"gpt-load/internal/protocol"
	"gpt-load/internal/provideradapter"
)

func TestNexusTokenInferenceUsesOnlyAPIKey(t *testing.T) {
	registry := channel.NewRegistry()
	credential, err := registry.ValidateCredential(channel.NexusToken, json.RawMessage(`{"api_key":"inference-key","session_id":"website-session","seat_id":"9","key_id":"33","email":"driver@example.com","plan_name":"Pro 20x"}`))
	if err != nil {
		t.Fatal(err)
	}
	key, _, err := directKeyForAttempt(execution.AttemptSpec{}, channel.ProviderMultiProtocolGateway, nil, credential)
	if err != nil || key.Value.GetValue() != "inference-key" {
		t.Fatalf("key = %#v, %v", key, err)
	}
	for _, forbidden := range []string{"access_token", "refresh_token", "password"} {
		if _, err := registry.ValidateCredential(channel.NexusToken, json.RawMessage(`{"api_key":"inference-key","`+forbidden+`":"website-secret"}`)); err == nil {
			t.Fatalf("website secret field %s accepted in inference credential", forbidden)
		}
	}
}

func TestNexusTokenModelDiscoveryForwardsClientVersion(t *testing.T) {
	t.Parallel()

	for _, suffix := range []string{"", "/gateway"} {
		t.Run("base_url"+suffix, func(t *testing.T) {
			t.Parallel()
			const response = `{"models":[{"slug":"gpt-6.1-sol"},{"slug":"gpt-6-astra"}]}`
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != suffix+"/v1/models" {
					t.Errorf("model discovery target = %s %s", r.Method, r.URL.Path)
				}
				if r.URL.Query().Get("client_version") != catalog.CodexModelCatalogVersion {
					t.Error("model discovery omitted the pinned Codex client version")
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				if r.Header.Get("Authorization") != "Bearer "+testAPIKey {
					t.Error("model discovery used the wrong API key")
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, response)
			}))
			defer server.Close()

			registry := channel.NewRegistry()
			resolved, err := registry.Resolve(channel.NexusToken, json.RawMessage(`{"base_url":"`+server.URL+suffix+`"}`))
			if err != nil {
				t.Fatal(err)
			}
			manager, err := newRuntimeManager(runtimeOptions{allowPrivateNetwork: true}, registry)
			if err != nil {
				t.Fatal(err)
			}
			if err := manager.Start(t.Context()); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(manager.Shutdown)
			if err := manager.Reconcile([]provideradapter.RuntimeTarget{{Target: resolved}}); err != nil {
				t.Fatal(err)
			}
			spec := utilitySpec(channel.NexusToken, protocol.OpenAICompletions, execution.OperationListModels, http.MethodGet, "/v1/models", nil)
			spec.TargetConfig = resolved.TargetConfig
			spec.Query.Set("client_version", catalog.CodexModelCatalogVersion)
			result := manager.Execute(context.Background(), freezeTestAttempt(spec))
			if err := result.Validate(); err != nil || result.Error != nil || result.StatusCode != http.StatusOK || !bytes.Equal(result.Body, []byte(response)) {
				t.Fatalf("model discovery = %+v, validation=%v, body=%s", result, err, result.Body)
			}
		})
	}
}
