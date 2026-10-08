package requestlog

import (
	"fmt"
	"math"
	"sort"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"gpt-load/internal/execution"
	"gpt-load/internal/protocol"
	"gpt-load/internal/storage/models"
)

const usageModelSpeedLimit = 5

var usageModelSpeedProtocols = []protocol.Protocol{
	protocol.OpenAICompletions,
	protocol.OpenAIResponses,
	protocol.Anthropic,
	protocol.Gemini,
}

type usageModelSpeedRank struct {
	Model        string
	OutputTokens int64
	DurationMS   int64
}

type usageModelSpeedRow struct {
	BucketStartMS int64 `gorm:"column:speed_bucket_ms"`
	Model         string
	RequestCount  int64
	OutputTokens  int64
	DurationMS    int64
}

type usageModelSpeedPointKey struct {
	BucketStartMS int64
	Model         string
}

type usageModelSpeedStatKey struct {
	BucketStartMS int64
	AccessKeyID   uint
	ChannelID     string
	GroupID       uint
	CredentialID  uint
	Model         string
}

type usageModelSpeedDelta struct {
	RequestCount int64
	OutputTokens int64
	DurationMS   int64
}

func usageModelSpeedEligible(row models.RequestLog) bool {
	if row.AttemptCount <= 0 || row.Operation == string(execution.OperationWebSearch) ||
		row.UpstreamModel == "" || row.StatusCode < 200 || row.StatusCode >= 300 ||
		(row.UsageState != "complete" && row.UsageState != "partial") ||
		row.OutputTokens <= 0 || row.DurationMs <= 0 {
		return false
	}
	for _, candidate := range usageModelSpeedProtocols {
		if row.Protocol == string(candidate) {
			return true
		}
	}
	return false
}

func applyUsageModelSpeedJournals(
	tx *gorm.DB,
	journals []models.UsageAggregationJournal,
) error {
	deltas, err := buildUsageModelSpeedJournalDeltas(journals)
	if err != nil || len(deltas) == 0 {
		return err
	}
	keys := sortedUsageModelSpeedStatKeys(deltas)
	existing, err := queryExistingUsageModelSpeedStats(tx, keys)
	if err != nil {
		return err
	}
	for _, key := range keys {
		row := existing[key]
		row.BucketStartMS = key.BucketStartMS
		row.AccessKeyID = key.AccessKeyID
		row.ChannelID = key.ChannelID
		row.GroupID = key.GroupID
		row.CredentialID = key.CredentialID
		row.Model = key.Model
		delta := deltas[key]
		if err := checkedInt64Add(&row.RequestCount, delta.RequestCount, "speed request_count"); err != nil {
			return err
		}
		if err := checkedInt64Add(&row.OutputTokens, delta.OutputTokens, "speed output_tokens"); err != nil {
			return err
		}
		if err := checkedInt64Add(&row.DurationMS, delta.DurationMS, "speed duration_ms"); err != nil {
			return err
		}
		if err := tx.Clauses(usageModelSpeedStatUpsertClause()).Create(&row).Error; err != nil {
			return fmt.Errorf("upsert usage model speed stat: %w", err)
		}
	}
	return nil
}

func buildUsageModelSpeedJournalDeltas(
	journals []models.UsageAggregationJournal,
) (map[usageModelSpeedStatKey]usageModelSpeedDelta, error) {
	deltas := make(map[usageModelSpeedStatKey]usageModelSpeedDelta)
	for _, journal := range journals {
		if journal.SpeedRequestCount == 0 {
			if journal.SpeedOutputTokens != 0 || journal.SpeedDurationMS != 0 {
				return nil, fmt.Errorf("aggregate usage model speed journal %q: invalid empty values", journal.RequestID)
			}
			continue
		}
		if journal.SpeedRequestCount != 1 || journal.SpeedOutputTokens <= 0 ||
			journal.SpeedDurationMS <= 0 || journal.Model == "" ||
			journal.SpeedBucketStartMS < 0 ||
			journal.SpeedBucketStartMS%UsageFiveMinuteBucketMS != 0 {
			return nil, fmt.Errorf("aggregate usage model speed journal %q: invalid values", journal.RequestID)
		}
		key := usageModelSpeedStatKey{
			BucketStartMS: journal.SpeedBucketStartMS,
			AccessKeyID:   journal.AccessKeyID,
			ChannelID:     journal.ChannelID,
			GroupID:       journal.GroupID,
			CredentialID:  journal.CredentialID,
			Model:         journal.Model,
		}
		delta := deltas[key]
		if err := checkedInt64Add(&delta.RequestCount, journal.SpeedRequestCount, "speed request_count"); err != nil {
			return nil, err
		}
		if err := checkedInt64Add(&delta.OutputTokens, journal.SpeedOutputTokens, "speed output_tokens"); err != nil {
			return nil, err
		}
		if err := checkedInt64Add(&delta.DurationMS, journal.SpeedDurationMS, "speed duration_ms"); err != nil {
			return nil, err
		}
		deltas[key] = delta
	}
	return deltas, nil
}

func sortedUsageModelSpeedStatKeys(
	deltas map[usageModelSpeedStatKey]usageModelSpeedDelta,
) []usageModelSpeedStatKey {
	keys := make([]usageModelSpeedStatKey, 0, len(deltas))
	for key := range deltas {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(left, right int) bool {
		a, b := keys[left], keys[right]
		if a.BucketStartMS != b.BucketStartMS {
			return a.BucketStartMS < b.BucketStartMS
		}
		if a.AccessKeyID != b.AccessKeyID {
			return a.AccessKeyID < b.AccessKeyID
		}
		if a.ChannelID != b.ChannelID {
			return a.ChannelID < b.ChannelID
		}
		if a.GroupID != b.GroupID {
			return a.GroupID < b.GroupID
		}
		if a.CredentialID != b.CredentialID {
			return a.CredentialID < b.CredentialID
		}
		return a.Model < b.Model
	})
	return keys
}

func queryExistingUsageModelSpeedStats(
	tx *gorm.DB,
	keys []usageModelSpeedStatKey,
) (map[usageModelSpeedStatKey]models.UsageModelSpeedStat, error) {
	query := tx.Model(&models.UsageModelSpeedStat{})
	for index, key := range keys {
		condition := "bucket_start_ms = ? AND access_key_id = ? AND channel_id = ? AND group_id = ? AND credential_id = ? AND model = ?"
		arguments := []any{key.BucketStartMS, key.AccessKeyID, key.ChannelID, key.GroupID, key.CredentialID, key.Model}
		if index == 0 {
			query = query.Where(condition, arguments...)
		} else {
			query = query.Or(condition, arguments...)
		}
	}
	var rows []models.UsageModelSpeedStat
	if err := query.Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("query existing usage model speed stats: %w", err)
	}
	result := make(map[usageModelSpeedStatKey]models.UsageModelSpeedStat, len(rows))
	for _, row := range rows {
		if row.RequestCount <= 0 || row.OutputTokens <= 0 || row.DurationMS <= 0 ||
			row.BucketStartMS < 0 || row.BucketStartMS%UsageFiveMinuteBucketMS != 0 || row.Model == "" {
			return nil, fmt.Errorf("query existing usage model speed stats: corrupt row")
		}
		key := usageModelSpeedStatKey{
			BucketStartMS: row.BucketStartMS, AccessKeyID: row.AccessKeyID,
			ChannelID: row.ChannelID, GroupID: row.GroupID,
			CredentialID: row.CredentialID, Model: row.Model,
		}
		result[key] = row
	}
	return result, nil
}

func usageModelSpeedStatUpsertClause() clause.OnConflict {
	return clause.OnConflict{
		Columns: []clause.Column{
			{Name: "bucket_start_ms"}, {Name: "access_key_id"}, {Name: "channel_id"},
			{Name: "group_id"}, {Name: "credential_id"}, {Name: "model"},
		},
		DoUpdates: clause.AssignmentColumns([]string{"request_count", "output_tokens", "duration_ms"}),
	}
}

func queryUsageModelSpeed(
	db *gorm.DB,
	input UsageQuery,
	bucketWidthMS int64,
) ([]UsageModelSpeedSeries, error) {
	rows, err := queryUsageModelSpeedRows(db, input, bucketWidthMS)
	if err != nil {
		return nil, err
	}
	merged := make(map[usageModelSpeedPointKey]usageModelSpeedDelta, len(rows))
	totals := make(map[string]usageModelSpeedRank)
	for _, row := range rows {
		if row.Model == "" || row.RequestCount <= 0 || row.OutputTokens <= 0 || row.DurationMS <= 0 ||
			row.BucketStartMS < 0 || row.BucketStartMS%bucketWidthMS != 0 {
			return nil, fmt.Errorf("query usage model speed: invalid aggregate")
		}
		key := usageModelSpeedPointKey{BucketStartMS: row.BucketStartMS, Model: row.Model}
		delta := merged[key]
		if err := checkedInt64Add(&delta.RequestCount, row.RequestCount, "query speed request_count"); err != nil {
			return nil, err
		}
		if err := checkedInt64Add(&delta.OutputTokens, row.OutputTokens, "query speed output_tokens"); err != nil {
			return nil, err
		}
		if err := checkedInt64Add(&delta.DurationMS, row.DurationMS, "query speed duration_ms"); err != nil {
			return nil, err
		}
		merged[key] = delta

		rank := totals[row.Model]
		rank.Model = row.Model
		if err := checkedInt64Add(&rank.OutputTokens, row.OutputTokens, "rank speed output_tokens"); err != nil {
			return nil, err
		}
		if err := checkedInt64Add(&rank.DurationMS, row.DurationMS, "rank speed duration_ms"); err != nil {
			return nil, err
		}
		totals[row.Model] = rank
	}
	ranks := make([]usageModelSpeedRank, 0, len(totals))
	for _, rank := range totals {
		ranks = append(ranks, rank)
	}
	sort.Slice(ranks, func(left, right int) bool {
		if ranks[left].OutputTokens != ranks[right].OutputTokens {
			return ranks[left].OutputTokens > ranks[right].OutputTokens
		}
		if ranks[left].DurationMS != ranks[right].DurationMS {
			return ranks[left].DurationMS > ranks[right].DurationMS
		}
		return ranks[left].Model < ranks[right].Model
	})
	if len(ranks) > usageModelSpeedLimit {
		ranks = ranks[:usageModelSpeedLimit]
	}
	result := make([]UsageModelSpeedSeries, len(ranks))
	seriesByModel := make(map[string]*UsageModelSpeedSeries, len(ranks))
	for index, rank := range ranks {
		result[index] = UsageModelSpeedSeries{Model: rank.Model, Points: []UsageModelSpeedPoint{}}
		seriesByModel[rank.Model] = &result[index]
	}
	for key, delta := range merged {
		series := seriesByModel[key.Model]
		if series == nil {
			continue
		}
		startMS := max(key.BucketStartMS, input.FromMS)
		endMS := min(key.BucketStartMS+bucketWidthMS, input.ToMS)
		if endMS <= startMS {
			return nil, fmt.Errorf("query usage model speed: invalid bucket")
		}
		series.Points = append(series.Points, UsageModelSpeedPoint{
			BucketStartMS: startMS, BucketEndMS: endMS,
			RequestCount: delta.RequestCount, OutputTokens: delta.OutputTokens,
			DurationMS: delta.DurationMS,
		})
	}
	for index := range result {
		sort.Slice(result[index].Points, func(left, right int) bool {
			return result[index].Points[left].BucketStartMS < result[index].Points[right].BucketStartMS
		})
	}
	return result, nil
}

func queryUsageModelSpeedRows(
	db *gorm.DB,
	input UsageQuery,
	bucketWidthMS int64,
) ([]usageModelSpeedRow, error) {
	fullFromMS, fullToMS, hasFullBuckets, err := usageModelSpeedInterior(input.FromMS, input.ToMS)
	if err != nil {
		return nil, err
	}
	rows := make([]usageModelSpeedRow, 0)
	if hasFullBuckets {
		var aggregateRows []usageModelSpeedRow
		if err := usageModelSpeedStatScope(db, input).
			Where("bucket_start_ms >= ? AND bucket_start_ms < ?", fullFromMS, fullToMS).
			Select(`bucket_start_ms - bucket_start_ms % ? AS speed_bucket_ms,
				model, SUM(request_count) AS request_count,
				SUM(output_tokens) AS output_tokens, SUM(duration_ms) AS duration_ms`, bucketWidthMS).
			Group("speed_bucket_ms, model").
			Scan(&aggregateRows).Error; err != nil {
			return nil, fmt.Errorf("query usage model speed aggregates: %w", err)
		}
		rows = append(rows, aggregateRows...)
	}

	rawScope := usageModelSpeedRawScope(db, input)
	if hasFullBuckets {
		rawScope = rawScope.Where(
			"(completed_at_ms >= ? AND completed_at_ms < ?) OR (completed_at_ms >= ? AND completed_at_ms < ?)",
			input.FromMS, fullFromMS, fullToMS, input.ToMS,
		)
	}
	if !hasFullBuckets || input.FromMS < fullFromMS || fullToMS < input.ToMS {
		var rawRows []usageModelSpeedRow
		if err := rawScope.
			Select(`completed_at_ms - completed_at_ms % ? AS speed_bucket_ms,
				upstream_model AS model, COUNT(*) AS request_count,
				SUM(output_tokens) AS output_tokens, SUM(duration_ms) AS duration_ms`, bucketWidthMS).
			Group("speed_bucket_ms, upstream_model").
			Scan(&rawRows).Error; err != nil {
			return nil, fmt.Errorf("query usage model speed edge logs: %w", err)
		}
		rows = append(rows, rawRows...)
	}
	return rows, nil
}

func usageModelSpeedInterior(fromMS, toMS int64) (int64, int64, bool, error) {
	if fromMS < 0 || toMS <= fromMS {
		return 0, 0, false, fmt.Errorf("query usage model speed: invalid range")
	}
	fullFromMS := fromMS
	if remainder := fromMS % UsageFiveMinuteBucketMS; remainder != 0 {
		delta := UsageFiveMinuteBucketMS - remainder
		if fromMS > math.MaxInt64-delta {
			return 0, 0, false, fmt.Errorf("query usage model speed: range overflow")
		}
		fullFromMS += delta
	}
	fullToMS := toMS - toMS%UsageFiveMinuteBucketMS
	return fullFromMS, fullToMS, fullFromMS < fullToMS, nil
}

func usageModelSpeedStatScope(db *gorm.DB, input UsageQuery) *gorm.DB {
	scope := db.Session(&gorm.Session{NewDB: true}).Model(&models.UsageModelSpeedStat{})
	if input.GroupID != nil {
		scope = scope.Where("group_id = ?", *input.GroupID)
	}
	if input.ChannelID != "" {
		scope = scope.Where("channel_id = ?", input.ChannelID)
	}
	if input.CredentialID != nil {
		scope = scope.Where("credential_id = ?", *input.CredentialID)
	}
	if input.AccessKeyID != nil {
		scope = scope.Where("access_key_id = ?", *input.AccessKeyID)
	}
	if input.UpstreamModel != "" {
		scope = scope.Where("model = ?", input.UpstreamModel)
	}
	return scope
}

func usageModelSpeedRawScope(db *gorm.DB, input UsageQuery) *gorm.DB {
	return usageRequestLogBaseScope(db, input).
		Where("upstream_model <> ''").
		Where("status_code >= ? AND status_code < ?", 200, 300).
		Where("protocol IN ?", usageModelSpeedProtocols).
		Where("usage_state IN ?", []string{"complete", "partial"}).
		Where("output_tokens > 0").
		Where("duration_ms > 0")
}
