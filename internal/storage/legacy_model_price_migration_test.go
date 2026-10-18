package storage

import (
	"fmt"
	"reflect"
	"testing"

	migrationfiles "gpt-load/internal/storage/migrations"
)

func TestLocalMigrationsUpgradeWithUpstreamModelPriceReferences(t *testing.T) {
	for count := 31; count <= 34; count++ {
		for _, resume := range []bool{false, true} {
			t.Run(fmt.Sprintf("%04d/resume=%t", count, resume), func(t *testing.T) {
				db := openInternalMigrationTestDatabase(t)
				legacy := append([]migration(nil), migrations[:30]...)
				for index, id := range []string{
					"0031_request_log_processing", "0032_usage_model_speed_stats",
					"0033_nexustoken_sessions", "0034_access_key_calendar_day",
				} {
					entry := migrations[index+31]
					entry.ID, entry.Legacy = id, nil
					legacy = append(legacy, entry)
				}
				legacy = legacy[:count]
				if err := applyMigrationRegistry(db, legacy); err != nil {
					t.Fatalf("apply previous local migrations: %v", err)
				}
				retainedCount := count
				if resume {
					lastID := legacy[count-1].ID
					if err := db.Model(&schemaMigration{}).Where("id = ?", lastID).
						Update("id", migrationResumeMarker(lastID)).Error; err != nil {
						t.Fatal(err)
					}
					retainedCount--
				}
				preserveSpeed := retainedCount >= 32
				if preserveSpeed {
					if err := db.Table("usage_model_speed_stats").Create(map[string]any{
						"bucket_start_ms": 0, "access_key_id": 1, "group_id": 1,
						"model": "retained-model", "request_count": 1,
						"output_tokens": 10, "duration_ms": 1000,
					}).Error; err != nil {
						t.Fatal(err)
					}
				}
				for range 2 {
					if err := AutoMigrate(db); err != nil {
						t.Fatalf("upgrade previous local migrations: %v", err)
					}
				}
				if err := migrationfiles.Validate0031(db); err != nil {
					t.Fatalf("upstream model price references missing: %v", err)
				}
				wantIDs := make([]string, len(migrations))
				for index, entry := range migrations {
					wantIDs[index] = entry.ID
					if index < retainedCount {
						wantIDs[index] = legacy[index].ID
					}
				}
				var ids []string
				if err := db.Table(migrationLedgerTable).Order("id").Pluck("id", &ids).Error; err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(ids, wantIDs) {
					t.Fatalf("migration ledger = %v, want %v", ids, wantIDs)
				}
				if preserveSpeed {
					var rows int64
					if err := db.Table("usage_model_speed_stats").Where("model = ?", "retained-model").Count(&rows).Error; err != nil || rows != 1 {
						t.Fatalf("historical speed aggregate was lost: count=%d, err=%v", rows, err)
					}
				}
			})
		}
	}
}
