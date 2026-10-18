package storage

import (
	"fmt"
	"testing"

	"gorm.io/gorm"

	"gpt-load/internal/storage/models"
)

func TestCalendarDayMigrationPreservesRulesAndCheckpoints(t *testing.T) {
	for _, interrupted := range []bool{false, true} {
		t.Run(fmt.Sprint(interrupted), func(t *testing.T) {
			db := openInternalMigrationTestDatabase(t)
			if err := applyMigrationRegistry(db, migrations[:33]); err != nil {
				t.Fatal(err)
			}
			key := models.AccessKey{Name: "calendar-upgrade", KeyValue: "cipher", KeyHash: "calendar-hash", KeySuffix: "test", Filters: models.JSON(`{}`)}
			if err := db.Create(&key).Error; err != nil {
				t.Fatal(err)
			}
			rule := models.AccessKeyCostLimitRule{AccessKeyID: key.ID, Kind: models.AccessKeyCostLimitKindPeriodic, LimitNanoUSD: 20, PeriodSeconds: 86400, RuleRevision: 1}
			if err := db.Omit("PeriodMode", "Timezone").Create(&rule).Error; err != nil {
				t.Fatal(err)
			}
			state := models.AccessKeyCostLimitState{RuleID: rule.ID, RuleRevision: 1, UsedNanoUSD: 7, SnapshotVersion: 3}
			if err := db.Create(&state).Error; err != nil {
				t.Fatal(err)
			}
			if interrupted {
				entry := migrations[33]
				up := entry.Up
				entry.Up = func(tx *gorm.DB) error {
					if err := up(tx); err != nil {
						return err
					}
					return fmt.Errorf("interrupt calendar migration")
				}
				if err := applyMigrationRegistry(db, append(append([]migration(nil), migrations[:33]...), entry)); err == nil {
					t.Fatal("expected interruption")
				}
			}
			for range 2 {
				if err := AutoMigrate(db); err != nil {
					t.Fatal(err)
				}
			}
			var preserved models.AccessKeyCostLimitState
			if err := db.First(&preserved, rule.ID).Error; err != nil {
				t.Fatal(err)
			}
			if preserved.UsedNanoUSD != 7 || preserved.SnapshotVersion != 3 {
				t.Fatalf("checkpoint changed: %#v", preserved)
			}
			day := models.AccessKeyCostLimitRule{AccessKeyID: key.ID, Kind: models.AccessKeyCostLimitKindPeriodic, LimitNanoUSD: 20, PeriodSeconds: 86400, RuleRevision: 1, PeriodMode: "calendar_day", Timezone: "Asia/Shanghai"}
			if err := db.Create(&day).Error; err != nil {
				t.Fatal("calendar day must coexist with 24 hours:", err)
			}
			duplicate := day
			duplicate.ID = 0
			duplicate.Timezone = "UTC"
			if err := db.Create(&duplicate).Error; err == nil {
				t.Fatal("duplicate calendar day was accepted")
			}
			if err := db.Model(&day).Update("period_seconds", 5*86400).Error; err == nil {
				t.Fatal("multiple calendar days were accepted")
			}
			if err := db.Model(&day).Update("period_mode", "").Error; err == nil {
				t.Fatal("duration with timezone was accepted")
			}
			if err := db.Delete(&key).Error; err != nil {
				t.Fatal(err)
			}
			var count int64
			if err := db.Model(&models.AccessKeyCostLimitState{}).Count(&count).Error; err != nil || count != 0 {
				t.Fatalf("cascade lost after migration: %d, %v", count, err)
			}
		})
	}
}
