package migrations_test

import (
	"testing"

	"gpt-load/internal/storage/migrations"
	"gpt-load/internal/storage/models"
)

func TestUsageModelSpeedStatisticsMigrationBackfillsAppliedAndPendingRows(t *testing.T) {
	db := openInitialTestDatabase(t)
	if err := migrations.Up0001(db); err != nil {
		t.Fatal(err)
	}
	insertLog := func(id string, completedAtMS int64, outputTokens, durationMS int64) {
		t.Helper()
		if err := db.Table("request_logs").Create(map[string]any{
			"id": id, "completed_at_ms": completedAtMS, "access_key_id": 1,
			"group_id": 7, "channel_id": "openai", "credential_id": 11,
			"protocol": "openai-completions", "operation": "chat_completion",
			"client_model": "client-model", "upstream_model": "speed-model",
			"status": "success", "status_code": 200, "duration_ms": durationMS,
			"attempt_count": 1, "error_summary": "", "output_tokens": outputTokens,
			"usage_state": "complete", "cost_state": "unpriced",
			"pricing_completeness": "unavailable",
		}).Error; err != nil {
			t.Fatal(err)
		}
	}
	insertJournal := func(id string, applied bool) {
		t.Helper()
		if err := db.Table("usage_aggregation_journal").Create(map[string]any{
			"request_id": id, "bucket_start_ms": int64(0), "access_key_id": 1,
			"group_id": 7, "channel_id": "openai", "credential_id": 11,
			"model": "speed-model", "request_count": 1, "success_count": 1,
			"failure_count": 0, "uncached_input_tokens": 0, "output_tokens": 10,
			"cache_read_tokens": 0, "cache_write_5m_tokens": 0,
			"cache_write_1h_tokens": 0, "cache_write_unknown_tokens": 0,
			"estimated_cost_nano_usd": 0, "usage_missing_count": 0,
			"partial_count": 0, "unpriced_request_count": 1,
			"pricing_partial_count": 0, "applied": applied,
		}).Error; err != nil {
			t.Fatal(err)
		}
	}
	insertLog("speed-applied", 310_000, 40, 2_000)
	insertJournal("speed-applied", true)
	insertLog("speed-pending", 620_000, 60, 3_000)
	insertJournal("speed-pending", false)

	for range 2 {
		if err := migrations.Up0031(db); err != nil {
			t.Fatal(err)
		}
		if err := migrations.Validate0031(db); err != nil {
			t.Fatal(err)
		}
	}
	var stats []models.UsageModelSpeedStat
	if err := db.Find(&stats).Error; err != nil {
		t.Fatal(err)
	}
	if len(stats) != 1 || stats[0].BucketStartMS != 300_000 || stats[0].RequestCount != 1 ||
		stats[0].OutputTokens != 40 || stats[0].DurationMS != 2_000 {
		t.Fatalf("backfilled speed stats = %#v", stats)
	}
	var pending models.UsageAggregationJournal
	if err := db.Where("request_id = ?", "speed-pending").Take(&pending).Error; err != nil {
		t.Fatal(err)
	}
	if pending.SpeedBucketStartMS != 600_000 || pending.SpeedRequestCount != 1 ||
		pending.SpeedOutputTokens != 60 || pending.SpeedDurationMS != 3_000 {
		t.Fatalf("backfilled pending journal = %#v", pending)
	}
}
