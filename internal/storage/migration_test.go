package storage

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"gorm.io/gorm"

	migrationfiles "gpt-load/internal/storage/migrations"
)

func TestMigrationRegistryContainsOrderedMigrations(t *testing.T) {
	wantIDs := []string{
		migrationfiles.ID0001,
		migrationfiles.ID0002,
		migrationfiles.ID0003,
		migrationfiles.ID0004,
		migrationfiles.ID0005,
		migrationfiles.ID0006,
		migrationfiles.ID0007,
		migrationfiles.ID0008,
		migrationfiles.ID0009,
		migrationfiles.ID0010,
		migrationfiles.ID0011,
		migrationfiles.ID0012,
		migrationfiles.ID0013,
		migrationfiles.ID0014,
		migrationfiles.ID0015,
		migrationfiles.ID0016,
		migrationfiles.ID0017,
		migrationfiles.ID0018,
		migrationfiles.ID0019,
		migrationfiles.ID0020,
		migrationfiles.ID0021,
		migrationfiles.ID0022,
		migrationfiles.ID0023,
		migrationfiles.ID0024,
		migrationfiles.ID0025,
		migrationfiles.ID0026,
		migrationfiles.ID0027,
		migrationfiles.ID0028,
		migrationfiles.ID0029,
		migrationfiles.ID0030,
		migrationfiles.ID0031,
		migrationfiles.ID0032,
		migrationfiles.ID0033,
		migrationfiles.ID0034,
	}
	if len(migrations) != len(wantIDs) {
		t.Fatalf("migration registry length = %d, want %d", len(migrations), len(wantIDs))
	}
	for index, entry := range migrations {
		if entry.ID != wantIDs[index] || entry.Up == nil ||
			entry.Validate == nil || entry.ValidateRecoverable == nil {
			t.Fatalf("migration registry entry %d = %#v", index, entry)
		}
	}
}

func TestLegacyRequestLogProcessing0024UpgradesWithoutRenumbering(t *testing.T) {
	db := openInternalMigrationTestDatabase(t)
	legacy := append([]migration(nil), migrations[:23]...)
	legacy = append(legacy, migration{
		ID:                  "0024_request_log_processing",
		Up:                  migrationfiles.Up0031,
		Validate:            migrationfiles.Validate0031,
		ValidateRecoverable: migrationfiles.ValidateRecoverable0031,
	})
	if err := applyMigrationRegistry(db, legacy); err != nil {
		t.Fatalf("apply local 0024: %v", err)
	}
	if err := db.Table("request_logs").Create(map[string]any{
		"id": "legacy-processing", "started_at_ms": 1000, "completed_at_ms": 1000,
		"access_key_id": 1, "protocol": "openai-responses", "client_model": "test", "upstream_model": "test",
		"status": "processing", "status_code": 0, "duration_ms": 0, "error_summary": "",
	}).Error; err != nil {
		t.Fatalf("seed local processing log: %v", err)
	}
	for range 2 {
		if err := AutoMigrate(db); err != nil {
			t.Fatalf("upgrade local 0024: %v", err)
		}
	}
	var ids []string
	if err := db.Table(migrationLedgerTable).Order("id").Pluck("id", &ids).Error; err != nil {
		t.Fatal(err)
	}
	if len(ids) != len(migrations) || ids[23] != "0024_request_log_processing" || ids[29] != migrationfiles.ID0030 || ids[30] != migrationfiles.ID0031 {
		t.Fatalf("upgraded migration ledger = %v", ids)
	}
	var row struct {
		StartedAtMS int64
		Status      string
	}
	if err := db.Table("request_logs").Where("id = ?", "legacy-processing").Take(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.StartedAtMS != 1000 || row.Status != "processing" {
		t.Fatalf("legacy processing log changed: %+v", row)
	}
}

func TestLegacyRequestLogProcessing0024ResumeMarker(t *testing.T) {
	db := openInternalMigrationTestDatabase(t)
	if err := applyMigrationRegistry(db, migrations[:23]); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("ALTER TABLE request_logs ADD COLUMN started_at_ms BIGINT NOT NULL DEFAULT 0").Error; err != nil {
		t.Fatal(err)
	}
	marker := migrationResumeMarker("0024_request_log_processing")
	if err := db.Create(&schemaMigration{ID: marker}).Error; err != nil {
		t.Fatal(err)
	}
	if err := AutoMigrate(db); err != nil {
		t.Fatalf("resume legacy 0024: %v", err)
	}
	if err := migrationfiles.Validate0031(db); err != nil {
		t.Fatalf("processing schema after resume: %v", err)
	}
	var count int64
	if err := db.Model(&schemaMigration{}).Where("id = ?", marker).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("legacy resume marker remains")
	}
}

func TestLegacyRequestLogMigrationsUpgradeWithoutRenumbering(t *testing.T) {
	for _, scenario := range []struct {
		count  int
		resume bool
	}{
		{30, false}, {30, true}, {31, false}, {31, true}, {32, false}, {32, true},
	} {
		resume := scenario.resume
		name := fmt.Sprintf("%04d/completed", scenario.count)
		if resume {
			name = fmt.Sprintf("%04d/resume_marker", scenario.count)
		}
		t.Run(name, func(t *testing.T) {
			db := openInternalMigrationTestDatabase(t)
			const legacyID = "0030_request_log_processing"
			legacy := append([]migration(nil), migrations[:29]...)
			legacy = append(legacy, migration{
				ID: legacyID, Up: migrationfiles.Up0031,
				Validate: migrationfiles.Validate0031, ValidateRecoverable: migrationfiles.ValidateRecoverable0031,
			}, migration{
				ID: "0031_usage_model_speed_stats", Up: migrationfiles.Up0032,
				Validate: migrationfiles.Validate0032, ValidateRecoverable: migrationfiles.ValidateRecoverable0032,
			}, migration{
				ID: "0032_nexustoken_sessions", Up: migrationfiles.Up0033,
				Validate: migrationfiles.Validate0033, ValidateRecoverable: migrationfiles.ValidateRecoverable0033,
			})
			legacy = legacy[:scenario.count]
			if err := applyMigrationRegistry(db, legacy); err != nil {
				t.Fatalf("apply local migrations: %v", err)
			}
			if resume {
				lastID := legacy[len(legacy)-1].ID
				if err := db.Model(&schemaMigration{}).Where("id = ?", lastID).
					Update("id", migrationResumeMarker(lastID)).Error; err != nil {
					t.Fatal(err)
				}
			}
			if err := db.Table("request_logs").Create(map[string]any{
				"id": "legacy-processing", "started_at_ms": 1000, "completed_at_ms": 1000,
				"access_key_id": 1, "protocol": "openai-responses", "client_model": "test", "upstream_model": "test",
				"status": "processing", "status_code": 0, "duration_ms": 0, "error_summary": "",
			}).Error; err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if err := AutoMigrate(db); err != nil {
					t.Fatalf("upgrade local migrations: %v", err)
				}
			}
			if err := migrationfiles.Validate0030(db); err != nil {
				t.Fatalf("combined request body and processing schema: %v", err)
			}
			var ids []string
			if err := db.Table(migrationLedgerTable).Order("id").Pluck("id", &ids).Error; err != nil {
				t.Fatal(err)
			}
			wantIDs := make([]string, len(migrations))
			for index, entry := range migrations {
				wantIDs[index] = entry.ID
			}
			retainedCount := scenario.count
			if resume {
				retainedCount--
			}
			for index := 29; index < retainedCount; index++ {
				wantIDs[index] = legacy[index].ID
			}
			if !reflect.DeepEqual(ids, wantIDs) {
				t.Fatalf("upgraded migration ledger = %v", ids)
			}
			var row struct {
				StartedAtMS int64
				Status      string
			}
			if err := db.Table("request_logs").Where("id = ?", "legacy-processing").Take(&row).Error; err != nil {
				t.Fatal(err)
			}
			if row.StartedAtMS != 1000 || row.Status != "processing" {
				t.Fatalf("legacy processing log changed: %+v", row)
			}
		})
	}
}

func TestMigrationRegistryUsesOneOrderedChainForFreshAndExistingDatabases(t *testing.T) {
	entries, calls := testMigrationRegistry()

	fresh := openInternalMigrationTestDatabase(t)
	if err := applyMigrationRegistry(fresh, entries); err != nil {
		t.Fatalf("migrate fresh database: %v", err)
	}
	if !reflect.DeepEqual(*calls, []string{"0001_test", "0002_test"}) {
		t.Fatalf("fresh migration calls = %v, want [0001_test 0002_test]", *calls)
	}

	existing := openInternalMigrationTestDatabase(t)
	if err := existing.AutoMigrate(&schemaMigration{}); err != nil {
		t.Fatalf("create existing migration ledger: %v", err)
	}
	if err := existing.Create(&schemaMigration{ID: entries[0].ID}).Error; err != nil {
		t.Fatalf("record existing migration: %v", err)
	}
	*calls = nil
	if err := applyMigrationRegistry(existing, entries); err != nil {
		t.Fatalf("migrate existing database: %v", err)
	}
	if !reflect.DeepEqual(*calls, []string{"0002_test"}) {
		t.Fatalf("existing migration calls = %v, want [0002_test]", *calls)
	}
}

func TestApplyMigrationRegistryRejectsOutOfOrderEntries(t *testing.T) {
	entries, _ := testMigrationRegistry()
	entries[0], entries[1] = entries[1], entries[0]

	err := applyMigrationRegistry(openInternalMigrationTestDatabase(t), entries)
	if err == nil || !strings.Contains(err.Error(), "migration registry entry 1") {
		t.Fatalf("applyMigrationRegistry() error = %v, want out-of-order registry rejection", err)
	}
}

func testMigrationRegistry() ([]migration, *[]string) {
	calls := make([]string, 0, 2)
	entry := func(id string) migration {
		return migration{
			ID: id,
			Up: func(*gorm.DB) error {
				calls = append(calls, id)
				return nil
			},
			Validate:            func(*gorm.DB) error { return nil },
			ValidateRecoverable: func(*gorm.DB) error { return nil },
		}
	}
	return []migration{entry("0001_test"), entry("0002_test")}, &calls
}
