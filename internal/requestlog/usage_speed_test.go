package requestlog

import (
	"context"
	"testing"
	"time"

	"gpt-load/internal/execution"
	"gpt-load/internal/protocol"
	"gpt-load/internal/storage/models"
	"gpt-load/internal/usage"
)

func TestQueryUsageModelSpeedReturnsWeightedRatesByBucketAndModel(t *testing.T) {
	db := openRequestLogQueryDB(t)
	from := time.Date(2026, time.October, 9, 13, 17, 0, 0, time.UTC)
	to := from.Add(time.Hour)
	row := func(id, model string, at time.Time, outputTokens, durationMS int64) models.RequestLog {
		value := aggregationRow(id, at, 7, model)
		value.OutputTokens = outputTokens
		value.DurationMs = durationMS
		return value
	}
	rows := []models.RequestLog{
		row("speed-a-1", "model-a", from.Add(4*time.Minute), 100, 1_000),
		row("speed-a-2", "model-a", from.Add(4*time.Minute+time.Second), 300, 3_000),
		row("speed-a-3", "model-a", from.Add(9*time.Minute), 50, 2_000),
		row("speed-b", "model-b", from.Add(4*time.Minute), 600, 3_000),
		row("speed-c", "model-c", from.Add(4*time.Minute), 500, 5_000),
		row("speed-d", "model-d", from.Add(4*time.Minute), 300, 1_000),
		row("speed-e", "model-e", from.Add(4*time.Minute), 200, 1_000),
		row("speed-f", "model-f", from.Add(4*time.Minute), 100, 1_000),
	}
	invalid := []models.RequestLog{
		row("speed-status", "invalid-status", from.Add(4*time.Minute), 10_000, 1_000),
		row("speed-usage", "invalid-usage", from.Add(4*time.Minute), 10_000, 1_000),
		row("speed-duration", "invalid-duration", from.Add(4*time.Minute), 10_000, 0),
		row("speed-output", "invalid-output", from.Add(4*time.Minute), 0, 1_000),
		row("speed-protocol", "invalid-protocol", from.Add(4*time.Minute), 10_000, 1_000),
		row("speed-attempt", "invalid-attempt", from.Add(4*time.Minute), 10_000, 1_000),
		row("speed-search", "invalid-search", from.Add(4*time.Minute), 10_000, 1_000),
		row("speed-model", "invalid-model", from.Add(4*time.Minute), 10_000, 1_000),
	}
	invalid[0].StatusCode = 500
	invalid[1].UsageState = string(usage.StateMissing)
	invalid[1].CostState = "unpriced"
	invalid[1].PricingCompleteness = "unavailable"
	invalid[1].EstimatedCostNanoUSD = 0
	invalid[4].Protocol = string(protocol.OpenAIEmbeddings)
	invalid[5].AttemptCount = 0
	invalid[6].Operation = string(execution.OperationWebSearch)
	invalid[7].UpstreamModel = ""
	rows = append(rows, invalid...)
	if err := (&gormBatchWriter{db: db}).WriteBatch(context.Background(), rows); err != nil {
		t.Fatal(err)
	}

	series, err := queryUsageModelSpeed(db, UsageQuery{
		FromMS: from.UnixMilli(), ToMS: to.UnixMilli(),
	}, UsageFiveMinuteBucketMS)
	if err != nil {
		t.Fatal(err)
	}
	if len(series) != usageModelSpeedLimit {
		t.Fatalf("series count = %d, want %d", len(series), usageModelSpeedLimit)
	}
	models := []string{"model-b", "model-c", "model-a", "model-d", "model-e"}
	for index, model := range models {
		if series[index].Model != model {
			t.Fatalf("series[%d].model = %q, want %q", index, series[index].Model, model)
		}
	}
	a := series[2]
	if len(a.Points) != 2 {
		t.Fatalf("model-a points = %#v, want two non-empty buckets", a.Points)
	}
	if point := a.Points[0]; point.RequestCount != 2 || point.OutputTokens != 400 || point.DurationMS != 4_000 {
		t.Fatalf("model-a first point = %#v, want weighted 100 Token/s source", point)
	}
	if point := a.Points[1]; point.RequestCount != 1 || point.OutputTokens != 50 || point.DurationMS != 2_000 {
		t.Fatalf("model-a second point = %#v, want 25 Token/s source", point)
	}
	if a.Points[0].BucketStartMS != from.Add(3*time.Minute).UnixMilli() ||
		a.Points[0].BucketEndMS != from.Add(8*time.Minute).UnixMilli() ||
		a.Points[1].BucketStartMS != from.Add(8*time.Minute).UnixMilli() ||
		a.Points[1].BucketEndMS != from.Add(13*time.Minute).UnixMilli() {
		t.Fatalf("model-a buckets = %#v, want completion-time five-minute buckets", a.Points)
	}
}

func TestQueryUsageModelSpeedSubtractsFirstResponseForNewLogs(t *testing.T) {
	db := openRequestLogQueryDB(t)
	from := time.Date(2026, time.October, 9, 13, 0, 0, 0, time.UTC)
	row := aggregationRow("speed-generation-window", from.Add(time.Minute), 7, "generation-model")
	row.OutputTokens = 300
	row.DurationMs = 4_000
	firstResponseMS := int64(1_000)
	row.FirstResponseMs = &firstResponseMS
	if err := (&gormBatchWriter{db: db}).WriteBatch(t.Context(), []models.RequestLog{row}); err != nil {
		t.Fatal(err)
	}

	series, err := queryUsageModelSpeed(db, UsageQuery{
		FromMS: from.UnixMilli(), ToMS: from.Add(time.Hour).UnixMilli(),
	}, UsageFiveMinuteBucketMS)
	if err != nil {
		t.Fatal(err)
	}
	if len(series) != 1 || len(series[0].Points) != 1 {
		t.Fatalf("generation speed series = %#v", series)
	}
	point := series[0].Points[0]
	if point.OutputTokens != 300 || point.DurationMS != 3_000 {
		t.Fatalf("generation speed point = %#v, want output=300 duration=3000", point)
	}
}

func TestQueryUsageModelSpeedAppliesUsageFilters(t *testing.T) {
	db := openRequestLogQueryDB(t)
	from := time.Date(2026, time.October, 9, 13, 0, 0, 0, time.UTC)
	first := aggregationRow("speed-filter-first", from.Add(time.Minute), 7, "selected")
	first.AccessKeyID, first.ChannelID, first.CredentialID = 41, "openai", 11
	second := aggregationRow("speed-filter-second", from.Add(time.Minute), 8, "other")
	second.AccessKeyID, second.ChannelID, second.CredentialID = 42, "anthropic", 12
	if err := (&gormBatchWriter{db: db}).WriteBatch(t.Context(), []models.RequestLog{first, second}); err != nil {
		t.Fatal(err)
	}
	groupID, accessKeyID, credentialID := uint(7), uint(41), uint(11)
	series, err := queryUsageModelSpeed(db, UsageQuery{
		FromMS: from.UnixMilli(), ToMS: from.Add(time.Hour).UnixMilli(),
		GroupID: &groupID, AccessKeyID: &accessKeyID, ChannelID: "openai",
		CredentialID: &credentialID, UpstreamModel: "selected",
	}, UsageFiveMinuteBucketMS)
	if err != nil {
		t.Fatal(err)
	}
	if len(series) != 1 || series[0].Model != "selected" || len(series[0].Points) != 1 {
		t.Fatalf("filtered speed series = %#v", series)
	}
}

func TestQueryUsageModelSpeedUsesAggregatesAndRawEdgeBuckets(t *testing.T) {
	db := openRequestLogQueryDB(t)
	from := time.Date(2026, time.October, 9, 13, 17, 0, 0, time.UTC)
	row := func(id string, at time.Time, outputTokens, durationMS int64) models.RequestLog {
		value := aggregationRow(id, at, 7, "hybrid-model")
		value.OutputTokens = outputTokens
		value.DurationMs = durationMS
		return value
	}
	rows := []models.RequestLog{
		row("speed-edge-first", from.Add(time.Minute), 30, 1_000),
		row("speed-full", from.Add(4*time.Minute), 70, 2_000),
		row("speed-edge-last", from.Add(59*time.Minute), 50, 1_000),
	}
	if err := (&gormBatchWriter{db: db}).WriteBatch(t.Context(), rows); err != nil {
		t.Fatal(err)
	}
	if err := db.Where("id = ?", "speed-full").Delete(&models.RequestLog{}).Error; err != nil {
		t.Fatal(err)
	}

	series, err := queryUsageModelSpeed(db, UsageQuery{
		FromMS: from.UnixMilli(), ToMS: from.Add(time.Hour).UnixMilli(),
	}, UsageFiveMinuteBucketMS)
	if err != nil {
		t.Fatal(err)
	}
	if len(series) != 1 || series[0].Model != "hybrid-model" || len(series[0].Points) != 3 {
		t.Fatalf("hybrid speed series = %#v", series)
	}
	want := []struct {
		start, end             time.Time
		requests, output, time int64
	}{
		{from, from.Add(3 * time.Minute), 1, 30, 1_000},
		{from.Add(3 * time.Minute), from.Add(8 * time.Minute), 1, 70, 2_000},
		{from.Add(58 * time.Minute), from.Add(time.Hour), 1, 50, 1_000},
	}
	for index, expected := range want {
		point := series[0].Points[index]
		if point.BucketStartMS != expected.start.UnixMilli() || point.BucketEndMS != expected.end.UnixMilli() ||
			point.RequestCount != expected.requests || point.OutputTokens != expected.output || point.DurationMS != expected.time {
			t.Fatalf("hybrid point %d = %#v, want %#v", index, point, expected)
		}
	}
}
