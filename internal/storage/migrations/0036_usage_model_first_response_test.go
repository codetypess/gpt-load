package migrations_test

import (
	"testing"

	"gpt-load/internal/storage/migrations"
	"gpt-load/internal/storage/models"
)

func TestUsageFirstResponseMigrationBackfillsMeasurementsAndPreservesSpeed(t *testing.T) {
	db := openInitialTestDatabase(t)
	if err := migrations.Up0001(db); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		id      string
		stream  bool
		firstMS *int64
		applied bool
	}{
		{"fast", true, new(int64(200)), true},
		{"slow", true, new(int64(800)), true},
		{"missing", true, nil, true},
		{"zero", true, new(int64(0)), true},
		{"nonstream", false, new(int64(5_000)), true},
		{"pending", true, new(int64(600)), false},
	} {
		if err := db.Table("request_logs").Create(map[string]any{
			"id": test.id, "completed_at_ms": int64(310_000), "access_key_id": 1,
			"group_id": 7, "channel_id": "openai", "credential_id": 11,
			"protocol": "openai-completions", "operation": "chat_completion",
			"client_model": "client", "upstream_model": "response-model",
			"status": "success", "status_code": 200, "duration_ms": 2_000,
			"attempt_count": 1, "error_summary": "", "output_tokens": 40,
			"usage_state": "complete", "cost_state": "unpriced",
			"pricing_completeness": "unavailable", "stream": test.stream, "first_response_ms": test.firstMS,
		}).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Table("usage_aggregation_journal").Create(map[string]any{
			"request_id": test.id, "bucket_start_ms": int64(0), "access_key_id": 1,
			"group_id": 7, "channel_id": "openai", "credential_id": 11,
			"model": "response-model", "request_count": 1, "success_count": 1,
			"failure_count": 0, "uncached_input_tokens": 0, "output_tokens": 40,
			"cache_read_tokens": 0, "cache_write_5m_tokens": 0,
			"cache_write_1h_tokens": 0, "cache_write_unknown_tokens": 0,
			"estimated_cost_nano_usd": 0, "usage_missing_count": 0,
			"partial_count": 0, "unpriced_request_count": 1,
			"pricing_partial_count": 0, "applied": test.applied,
		}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := migrations.Up0033(db); err != nil {
		t.Fatal(err)
	}
	// This historical bucket has no retained log and must keep its speed sources.
	if err := db.Table("usage_model_speed_stats").Create(map[string]any{
		"bucket_start_ms": int64(0), "access_key_id": 1, "group_id": 7,
		"model": "expired-model", "request_count": 3, "output_tokens": 120, "duration_ms": 6_000,
	}).Error; err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := migrations.Up0036(db); err != nil {
			t.Fatal(err)
		}
	}
	var stat models.UsageModelSpeedStat
	if err := db.Where("model = ?", "response-model").Take(&stat).Error; err != nil {
		t.Fatal(err)
	}
	if stat.RequestCount != 5 || stat.OutputTokens != 200 || stat.DurationMS != 10_000 ||
		stat.FirstResponseCount != 2 || stat.FirstResponseTotalMS != 1_000 {
		t.Fatalf("backfilled first response statistics = %#v", stat)
	}
	var expired models.UsageModelSpeedStat
	if err := db.Where("model = ?", "expired-model").Take(&expired).Error; err != nil {
		t.Fatal(err)
	}
	if expired.RequestCount != 3 || expired.OutputTokens != 120 || expired.DurationMS != 6_000 ||
		expired.FirstResponseCount != 0 || expired.FirstResponseTotalMS != 0 {
		t.Fatalf("historical speed sources changed = %#v", expired)
	}
	var pending models.UsageAggregationJournal
	if err := db.Where("request_id = ?", "pending").Take(&pending).Error; err != nil {
		t.Fatal(err)
	}
	if pending.Applied || pending.SpeedFirstResponseCount != 1 || pending.SpeedFirstResponseTotalMS != 600 {
		t.Fatalf("pending first response journal = %#v", pending)
	}
}
