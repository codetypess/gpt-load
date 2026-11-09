package migrations

import (
	"fmt"

	"gorm.io/gorm"
)

const ID0036 = "0036_usage_model_first_response"

type usageFirstResponseJournal0036 struct {
	SpeedFirstResponseCount   int64 `gorm:"not null;default:0"`
	SpeedFirstResponseTotalMS int64 `gorm:"column:speed_first_response_total_ms;not null;default:0"`
}

func (usageFirstResponseJournal0036) TableName() string { return "usage_aggregation_journal" }

type usageFirstResponseStat0036 struct {
	FirstResponseCount   int64 `gorm:"not null;default:0"`
	FirstResponseTotalMS int64 `gorm:"column:first_response_total_ms;not null;default:0"`
}

func (usageFirstResponseStat0036) TableName() string { return "usage_model_speed_stats" }

func Up0036(db *gorm.DB) error {
	if err := ValidateRecoverable0036(db); err != nil {
		return err
	}
	for _, target := range []struct {
		model  any
		fields []string
	}{
		{&usageFirstResponseJournal0036{}, []string{"SpeedFirstResponseCount", "SpeedFirstResponseTotalMS"}},
		{&usageFirstResponseStat0036{}, []string{"FirstResponseCount", "FirstResponseTotalMS"}},
	} {
		for _, field := range target.fields {
			if !db.Migrator().HasColumn(target.model, field) {
				if err := db.Migrator().AddColumn(target.model, field); err != nil {
					return fmt.Errorf("add usage first response column %s: %w", field, err)
				}
			}
		}
	}

	// Keep historical speed sources intact, including buckets whose logs expired.
	// Only retained, measured streaming requests can supply first-response data.
	scope := func() *gorm.DB {
		return eligibleUsageModelSpeedLogs0033(db).
			Where("r.stream = ? AND r.first_response_ms > 0", true)
	}
	var pending []struct {
		RequestID       string
		FirstResponseMS int64
	}
	if err := scope().Where("j.applied = ?", false).
		Select("r.id AS request_id, r.first_response_ms").Scan(&pending).Error; err != nil {
		return fmt.Errorf("query pending first response journals: %w", err)
	}
	for _, row := range pending {
		if err := db.Model(&usageFirstResponseJournal0036{}).
			Where("request_id = ? AND applied = ?", row.RequestID, false).
			Updates(map[string]any{
				"speed_first_response_count": 1, "speed_first_response_total_ms": row.FirstResponseMS,
			}).Error; err != nil {
			return fmt.Errorf("backfill pending first response journal: %w", err)
		}
	}
	var stats []struct {
		BucketStartMS        int64
		AccessKeyID          uint
		ChannelID            string
		GroupID              uint
		CredentialID         uint
		Model                string
		FirstResponseCount   int64
		FirstResponseTotalMS int64
	}
	if err := scope().Where("j.applied = ?", true).
		Select(`r.completed_at_ms - r.completed_at_ms % 300000 AS bucket_start_ms,
			j.access_key_id, j.channel_id, j.group_id, j.credential_id, j.model,
			COUNT(*) AS first_response_count, SUM(r.first_response_ms) AS first_response_total_ms`).
		Group(`r.completed_at_ms - r.completed_at_ms % 300000,
			j.access_key_id, j.channel_id, j.group_id, j.credential_id, j.model`).Scan(&stats).Error; err != nil {
		return fmt.Errorf("query usage first response backfill: %w", err)
	}
	for _, row := range stats {
		if err := db.Model(&usageFirstResponseStat0036{}).
			Where("bucket_start_ms = ? AND access_key_id = ? AND channel_id = ? AND group_id = ? AND credential_id = ? AND model = ?",
				row.BucketStartMS, row.AccessKeyID, row.ChannelID, row.GroupID, row.CredentialID, row.Model).
			Updates(map[string]any{
				"first_response_count": row.FirstResponseCount, "first_response_total_ms": row.FirstResponseTotalMS,
			}).Error; err != nil {
			return fmt.Errorf("backfill usage first response statistics: %w", err)
		}
	}
	return Validate0036(db)
}

func ValidateRecoverable0036(db *gorm.DB) error {
	for _, table := range []string{"request_logs", "usage_aggregation_journal", "usage_model_speed_stats"} {
		if !db.Migrator().HasTable(table) {
			return fmt.Errorf("usage first response: %s table is missing", table)
		}
	}
	return nil
}

func Validate0036(db *gorm.DB) error {
	if err := ValidateRecoverable0036(db); err != nil {
		return err
	}
	for _, target := range []struct {
		model   any
		columns []string
	}{
		{&usageFirstResponseJournal0036{}, []string{"speed_first_response_count", "speed_first_response_total_ms"}},
		{&usageFirstResponseStat0036{}, []string{"first_response_count", "first_response_total_ms"}},
	} {
		for _, column := range target.columns {
			if !db.Migrator().HasColumn(target.model, column) {
				return fmt.Errorf("usage first response column %s is missing", column)
			}
		}
	}
	return nil
}
