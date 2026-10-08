package migrations

import (
	"fmt"

	"gorm.io/gorm"

	"gpt-load/internal/execution"
	"gpt-load/internal/protocol"
)

const (
	ID0031                         = "0031_usage_model_speed_stats"
	usageModelSpeedBucketWidth0031 = int64(5 * 60 * 1000)
)

type usageAggregationJournalSpeed0031 struct {
	RequestID          string `gorm:"column:request_id;type:varchar(36);primaryKey;not null"`
	SpeedBucketStartMS int64  `gorm:"column:speed_bucket_start_ms;not null;default:0"`
	SpeedRequestCount  int64  `gorm:"column:speed_request_count;not null;default:0"`
	SpeedOutputTokens  int64  `gorm:"column:speed_output_tokens;not null;default:0"`
	SpeedDurationMS    int64  `gorm:"column:speed_duration_ms;not null;default:0"`
}

func (usageAggregationJournalSpeed0031) TableName() string {
	return "usage_aggregation_journal"
}

type usageModelSpeedStat0031 struct {
	ID            uint   `gorm:"primaryKey;autoIncrement"`
	BucketStartMS int64  `gorm:"column:bucket_start_ms;not null;check:chk_usage_model_speed_stat_bucket,bucket_start_ms >= 0;uniqueIndex:idx_usage_model_speed_stats_identity,priority:1;index:idx_usage_model_speed_stats_bucket"`
	AccessKeyID   uint   `gorm:"not null;uniqueIndex:idx_usage_model_speed_stats_identity,priority:2"`
	ChannelID     string `gorm:"type:varchar(64);not null;default:'';uniqueIndex:idx_usage_model_speed_stats_identity,priority:3"`
	GroupID       uint   `gorm:"not null;uniqueIndex:idx_usage_model_speed_stats_identity,priority:4"`
	CredentialID  uint   `gorm:"not null;default:0;uniqueIndex:idx_usage_model_speed_stats_identity,priority:5"`
	Model         string `gorm:"type:varchar(255);not null;uniqueIndex:idx_usage_model_speed_stats_identity,priority:6"`
	RequestCount  int64  `gorm:"not null;default:0;check:chk_usage_model_speed_stat_requests,request_count > 0"`
	OutputTokens  int64  `gorm:"not null;default:0;check:chk_usage_model_speed_stat_output,output_tokens > 0"`
	DurationMS    int64  `gorm:"column:duration_ms;not null;default:0;check:chk_usage_model_speed_stat_duration,duration_ms > 0"`
}

func (usageModelSpeedStat0031) TableName() string { return "usage_model_speed_stats" }

type pendingUsageModelSpeed0031 struct {
	RequestID     string
	CompletedAtMS int64
	OutputTokens  int64
	DurationMS    int64
}

func Up0031(db *gorm.DB) error {
	if err := ValidateRecoverable0031(db); err != nil {
		return err
	}
	journal := &usageAggregationJournalSpeed0031{}
	for _, field := range []string{
		"SpeedBucketStartMS", "SpeedRequestCount", "SpeedOutputTokens", "SpeedDurationMS",
	} {
		if !db.Migrator().HasColumn(journal, field) {
			if err := db.Migrator().AddColumn(journal, field); err != nil {
				return fmt.Errorf("add usage speed journal column %s: %w", field, err)
			}
		}
	}
	if err := db.AutoMigrate(&usageModelSpeedStat0031{}); err != nil {
		return fmt.Errorf("create usage model speed statistics: %w", err)
	}
	if err := db.Model(journal).Where("applied = ?", false).Updates(map[string]any{
		"speed_bucket_start_ms": 0,
		"speed_request_count":   0,
		"speed_output_tokens":   0,
		"speed_duration_ms":     0,
	}).Error; err != nil {
		return fmt.Errorf("reset pending usage speed journals: %w", err)
	}

	var pending []pendingUsageModelSpeed0031
	if err := eligibleUsageModelSpeedLogs0031(db).
		Select(`r.id AS request_id, r.completed_at_ms,
			r.output_tokens, r.duration_ms`).
		Where("j.applied = ?", false).
		Scan(&pending).Error; err != nil {
		return fmt.Errorf("query pending usage speed journals: %w", err)
	}
	for _, row := range pending {
		result := db.Model(journal).Where("request_id = ? AND applied = ?", row.RequestID, false).
			Updates(map[string]any{
				"speed_bucket_start_ms": row.CompletedAtMS - row.CompletedAtMS%usageModelSpeedBucketWidth0031,
				"speed_request_count":   1,
				"speed_output_tokens":   row.OutputTokens,
				"speed_duration_ms":     row.DurationMS,
			})
		if result.Error != nil {
			return fmt.Errorf("backfill pending usage speed journal %q: %w", row.RequestID, result.Error)
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("backfill pending usage speed journal %q: row changed", row.RequestID)
		}
	}

	if err := db.Exec("DELETE FROM usage_model_speed_stats").Error; err != nil {
		return fmt.Errorf("reset usage model speed statistics: %w", err)
	}
	if err := db.Exec(`
		INSERT INTO usage_model_speed_stats (
			bucket_start_ms, access_key_id, channel_id, group_id,
			credential_id, model, request_count, output_tokens, duration_ms
		)
		SELECT
			r.completed_at_ms - r.completed_at_ms % ? AS bucket_start_ms,
			j.access_key_id, j.channel_id, j.group_id, j.credential_id, j.model,
			COUNT(*) AS request_count, SUM(r.output_tokens) AS output_tokens,
			SUM(r.duration_ms) AS duration_ms
		FROM request_logs AS r
		INNER JOIN usage_aggregation_journal AS j ON j.request_id = r.id
		WHERE j.applied = ?
			AND r.attempt_count > 0 AND r.operation <> ? AND r.upstream_model <> ''
			AND r.status_code >= 200 AND r.status_code < 300
			AND r.protocol IN (?, ?, ?, ?)
			AND r.usage_state IN ('complete', 'partial')
			AND r.output_tokens > 0 AND r.duration_ms > 0
		GROUP BY
			r.completed_at_ms - r.completed_at_ms % ?,
			j.access_key_id, j.channel_id, j.group_id, j.credential_id, j.model`,
		usageModelSpeedBucketWidth0031,
		true,
		string(execution.OperationWebSearch),
		string(protocol.OpenAICompletions),
		string(protocol.OpenAIResponses),
		string(protocol.Anthropic),
		string(protocol.Gemini),
		usageModelSpeedBucketWidth0031,
	).Error; err != nil {
		return fmt.Errorf("backfill usage model speed statistics: %w", err)
	}
	return Validate0031(db)
}

func eligibleUsageModelSpeedLogs0031(db *gorm.DB) *gorm.DB {
	return db.Table("request_logs AS r").
		Joins("INNER JOIN usage_aggregation_journal AS j ON j.request_id = r.id").
		Where("r.attempt_count > 0").
		Where("r.operation <> ?", string(execution.OperationWebSearch)).
		Where("r.upstream_model <> ''").
		Where("r.status_code >= ? AND r.status_code < ?", 200, 300).
		Where("r.protocol IN ?", []protocol.Protocol{
			protocol.OpenAICompletions, protocol.OpenAIResponses, protocol.Anthropic, protocol.Gemini,
		}).
		Where("r.usage_state IN ?", []string{"complete", "partial"}).
		Where("r.output_tokens > 0").
		Where("r.duration_ms > 0")
}

func ValidateRecoverable0031(db *gorm.DB) error {
	for _, table := range []string{"request_logs", "usage_aggregation_journal"} {
		if !db.Migrator().HasTable(table) {
			return fmt.Errorf("usage model speed statistics: %s table is missing", table)
		}
	}
	return nil
}

func Validate0031(db *gorm.DB) error {
	if err := ValidateRecoverable0031(db); err != nil {
		return err
	}
	journal := &usageAggregationJournalSpeed0031{}
	for _, column := range []string{
		"speed_bucket_start_ms", "speed_request_count", "speed_output_tokens", "speed_duration_ms",
	} {
		if !db.Migrator().HasColumn(journal, column) {
			return fmt.Errorf("usage model speed statistics: journal column %s is missing", column)
		}
	}
	stat := &usageModelSpeedStat0031{}
	if !db.Migrator().HasTable(stat) {
		return fmt.Errorf("usage model speed statistics table is missing")
	}
	for _, column := range []string{
		"id", "bucket_start_ms", "access_key_id", "channel_id", "group_id",
		"credential_id", "model", "request_count", "output_tokens", "duration_ms",
	} {
		if !db.Migrator().HasColumn(stat, column) {
			return fmt.Errorf("usage model speed statistics column %s is missing", column)
		}
	}
	for _, index := range []string{
		"idx_usage_model_speed_stats_identity", "idx_usage_model_speed_stats_bucket",
	} {
		if !db.Migrator().HasIndex(stat, index) {
			return fmt.Errorf("usage model speed statistics index %s is missing", index)
		}
	}
	return nil
}
