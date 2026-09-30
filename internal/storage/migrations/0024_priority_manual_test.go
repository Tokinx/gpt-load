package migrations_test

import (
	"testing"

	"gpt-load/internal/storage/migrations"
)

func TestPriorityManualMigrationAddsNullableColumnsAndPreservesRows(t *testing.T) {
	t.Parallel()
	db := openInitialTestDatabase(t)
	if err := migrations.Up0001(db); err != nil {
		t.Fatalf("Up0001() error = %v", err)
	}

	if err := db.Exec(`INSERT INTO groups (
		id, name, channel_id, connection_type, params, models, enabled, created_at_ms, updated_at_ms
	) VALUES (1, 'priority migration', 'openai', 'api_key', '{}', '[]', true, 1, 1)`).Error; err != nil {
		t.Fatalf("create group: %v", err)
	}
	if err := db.Exec(`INSERT INTO credentials (
		id, group_id, data, fingerprint, identity_fingerprint, secret_version,
		auth_state, auth_error_code, status, created_at_ms, updated_at_ms
	) VALUES (1, 1, 'credential-cipher', 'fingerprint', 'identity', 1,
		'ready', '', 'active', 1, 1)`).Error; err != nil {
		t.Fatalf("create credential: %v", err)
	}

	if err := migrations.Up0024(db); err != nil {
		t.Fatalf("Up0024() error = %v", err)
	}
	if err := migrations.Validate0024(db); err != nil {
		t.Fatalf("Validate0024() error = %v", err)
	}
	if !db.Migrator().HasColumn("groups", "priority_manual") {
		t.Fatal("groups.priority_manual is missing")
	}
	if db.Migrator().HasColumn("credentials", "priority_manual") {
		t.Fatal("group-only migration must not add credential priority")
	}

	type priorityRow struct {
		ID             uint
		PriorityManual *int
	}
	var groupRow priorityRow
	if err := db.Table("groups").Select("id", "priority_manual").Where("id = ?", 1).Take(&groupRow).Error; err != nil {
		t.Fatalf("read group priority_manual: %v", err)
	}
	if groupRow.ID != 1 || groupRow.PriorityManual != nil {
		t.Fatalf("existing group changed: %#v", groupRow)
	}
	// Existing credential rows are untouched, and group values survive replay.
	if err := db.Exec("UPDATE groups SET priority_manual = 90 WHERE id = 1").Error; err != nil {
		t.Fatal(err)
	}

	if err := migrations.Up0024(db); err != nil {
		t.Fatalf("second Up0024() error = %v", err)
	}
	if err := db.Table("groups").Select("id", "priority_manual").Where("id = ?", 1).Take(&groupRow).Error; err != nil {
		t.Fatal(err)
	}
	if groupRow.PriorityManual == nil || *groupRow.PriorityManual != 90 {
		t.Fatal("priority value lost on replay")
	}
}

func TestPriorityManualMigrationRecoverableValidationAcceptsPartialColumns(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name      string
		statement string
	}{
		{name: "no_columns"},
		{name: "groups_only", statement: "ALTER TABLE groups ADD COLUMN priority_manual integer NULL"},
		{name: "credentials_only", statement: "ALTER TABLE credentials ADD COLUMN priority_manual integer NULL"},
		{name: "both_columns", statement: "ALTER TABLE groups ADD COLUMN priority_manual integer NULL; ALTER TABLE credentials ADD COLUMN priority_manual integer NULL"},
	} {
		t.Run(test.name, func(t *testing.T) {
			db := openInitialTestDatabase(t)
			if err := migrations.Up0001(db); err != nil {
				t.Fatalf("Up0001() error = %v", err)
			}
			if test.statement != "" {
				if err := db.Exec(test.statement).Error; err != nil {
					t.Fatalf("prepare partial schema: %v", err)
				}
			}
			if err := migrations.ValidateRecoverable0024(db); err != nil {
				t.Fatalf("ValidateRecoverable0024() error = %v", err)
			}
			if err := migrations.Up0024(db); err != nil {
				t.Fatalf("Up0024() after partial schema error = %v", err)
			}
			if err := migrations.Validate0024(db); err != nil {
				t.Fatalf("Validate0024() after recovery error = %v", err)
			}
		})
	}
}

func TestGroupPriorityMigrationPreservesLegacyCredentialPriority(t *testing.T) {
	t.Parallel()
	db := openInitialTestDatabase(t)
	if err := migrations.Up0001(db); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("ALTER TABLE credentials ADD COLUMN priority_manual integer NULL").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO groups (
 id,name,channel_id,connection_type,params,models,enabled,created_at_ms,updated_at_ms
 ) VALUES (1,'legacy','openai','api_key','{}','[]',true,1,1)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO credentials (
 id,group_id,data,fingerprint,identity_fingerprint,secret_version,auth_state,auth_error_code,status,created_at_ms,updated_at_ms,priority_manual
 ) VALUES (1,1,'cipher','fingerprint','identity',1,'ready','','active',1,1,90)`).Error; err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := migrations.Up0024(db); err != nil {
			t.Fatal(err)
		}
		if err := migrations.Validate0024(db); err != nil {
			t.Fatal(err)
		}
	}
	var row struct {
		PriorityManual int
		Data           string
	}
	if err := db.Table("credentials").Select("priority_manual,data").Where("id = 1").Take(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.PriorityManual != 90 || row.Data != "cipher" {
		t.Fatalf("legacy credential data changed: %#v", row)
	}
}
