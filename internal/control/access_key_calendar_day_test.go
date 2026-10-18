package control

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"gpt-load/internal/accessquota"
	"gpt-load/internal/storage/models"
)

func TestCalendarDayCostLimitsRoundTripAndModeSwap(t *testing.T) {
	t.Parallel()
	fixture, engine := newAccessKeyCostLimitHTTPFixture(t)
	fixture.service.accessKeyQuotaTimezone = "America/New_York"
	body := `{"name":"calendar","cost_limit_rules":[
		{"kind":"periodic","limit_usd":"20","period_seconds":86400,"period_mode":"calendar_day"},
		{"kind":"periodic","limit_usd":"100","period_seconds":86400}]}`
	const operation = "00000000-0000-4000-8000-000000009801"
	created := serveAccessKeyCostLimitRequest(t, engine, http.MethodPost, "/api/access-keys", body, operation)
	if created.Code != http.StatusOK {
		t.Fatalf("create: %d %s", created.Code, created.Body.String())
	}
	var envelope struct {
		Data AccessKeyCreateResult `json:"data"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	key := envelope.Data
	if len(key.CostLimitRules) != 2 || key.CostLimitRules[0].PeriodMode != "" || key.CostLimitRules[1].PeriodMode != accessquota.PeriodModeCalendarDay || key.CostLimitRules[1].Timezone != "America/New_York" {
		t.Fatalf("round trip: %#v", key.CostLimitRules)
	}
	if replay := serveAccessKeyCostLimitRequest(t, engine, http.MethodPost, "/api/access-keys", body, operation); replay.Code != http.StatusOK {
		t.Fatalf("replay: %s", replay.Body.String())
	}
	collection, err := fixture.service.ListAccessKeyCollection(t.Context(), AccessKeyCollectionQuery{})
	if err != nil || collection.QuotaTimezone != "America/New_York" {
		t.Fatalf("collection timezone: %#v, %v", collection, err)
	}
	rules := loadAccessKeyCostLimitRules(t, fixture, key.ID)
	if err := fixture.db.Model(&models.AccessKeyCostLimitState{}).Where("rule_id IN ?", []uint{rules[0].ID, rules[1].ID}).Update("used_nano_usd", 7).Error; err != nil {
		t.Fatal(err)
	}
	// Swap the identities without changing their common 86400-second display length.
	updated := serveAccessKeyCostLimitRequest(t, engine, http.MethodPut, fmt.Sprintf("/api/access-keys/%d", key.ID), fmt.Sprintf(`{"cost_limit_rules":[
		{"id":%d,"kind":"periodic","limit_usd":"100","period_seconds":86400,"period_mode":"calendar_day","timezone":"Asia/Shanghai"},
		{"id":%d,"kind":"periodic","limit_usd":"20","period_seconds":86400}]}`, rules[0].ID, rules[1].ID), "")
	if updated.Code != http.StatusOK {
		t.Fatalf("mode swap: %d %s", updated.Code, updated.Body.String())
	}
	for _, original := range rules {
		var current models.AccessKeyCostLimitRule
		var state models.AccessKeyCostLimitState
		if err := fixture.db.First(&current, original.ID).Error; err != nil {
			t.Fatal(err)
		}
		if err := fixture.db.First(&state, original.ID).Error; err != nil {
			t.Fatal(err)
		}
		if current.RuleRevision != original.RuleRevision+1 || state.RuleRevision != current.RuleRevision || state.UsedNanoUSD != 0 {
			t.Fatalf("mode change did not reset state: %#v %#v", current, state)
		}
	}
	// A timezone-only change is also a new window definition.
	listed, err := fixture.service.ListAccessKeyCollection(t.Context(), AccessKeyCollectionQuery{})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range listed.Items {
		if item.ID == key.ID && item.CostLimitRules[0].PeriodMode != accessquota.PeriodModeDuration {
			t.Fatalf("collection definitions must use runtime ordering after mode swap: %#v", item.CostLimitRules)
		}
	}
	current := loadAccessKeyCostLimitRules(t, fixture, key.ID)
	for _, rule := range current {
		if rule.PeriodMode != "calendar_day" {
			continue
		}
		if _, err := fixture.service.UpdateAccessKey(t.Context(), key.ID, AccessKeyUpdateRequest{CostLimitRules: OptionalAccessKeyCostLimitRules{Set: true, Values: []AccessKeyCostLimitRuleRequest{{ID: rule.ID, Kind: accessquota.KindPeriodic, LimitUSD: "100", PeriodSeconds: 86400, PeriodMode: accessquota.PeriodModeCalendarDay, Timezone: "UTC"}}}}); err != nil {
			t.Fatal(err)
		}
		var changed models.AccessKeyCostLimitRule
		if err := fixture.db.First(&changed, rule.ID).Error; err != nil {
			t.Fatal(err)
		}
		if changed.RuleRevision != rule.RuleRevision+1 || changed.Timezone != "UTC" {
			t.Fatalf("timezone-only edit: %#v", changed)
		}
	}
}

func TestCalendarDayCostLimitsRejectInvalidInput(t *testing.T) {
	t.Parallel()
	for index, rules := range []string{
		`[{"kind":"periodic","limit_usd":"20","period_seconds":432000,"period_mode":"calendar_day"}]`,
		`[{"kind":"periodic","limit_usd":"20","period_seconds":86400,"period_mode":"calendar_day","timezone":"Local"}]`,
		`[{"kind":"periodic","limit_usd":"20","period_seconds":86400,"period_mode":"calendar_day","timezone":"Invalid/Zone"}]`,
		`[{"kind":"periodic","limit_usd":"20","period_seconds":86400,"timezone":"UTC"}]`,
		`[{"kind":"total","limit_usd":"20","period_mode":"calendar_day"}]`,
		`[{"kind":"periodic","limit_usd":"20","period_seconds":86400,"period_mode":"calendar_day"},{"kind":"periodic","limit_usd":"30","period_seconds":86400,"period_mode":"calendar_day","timezone":"UTC"}]`,
	} {
		t.Run(fmt.Sprint(index), func(t *testing.T) {
			_, engine := newAccessKeyCostLimitHTTPFixture(t)
			result := serveAccessKeyCostLimitRequest(t, engine, http.MethodPost, "/api/access-keys", `{"name":"invalid","cost_limit_rules":`+rules+`}`, fmt.Sprintf("00000000-0000-4000-8000-%012d", index+9900))
			if result.Code != http.StatusBadRequest {
				t.Fatalf("invalid rules accepted: %d %s", result.Code, result.Body.String())
			}
		})
	}
}
