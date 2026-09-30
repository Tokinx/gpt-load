package migrations

import (
	"fmt"
	"strings"

	"gorm.io/gorm"
)

const ID0024 = "0024_priority_manual"

type groupPriorityManual0024 struct {
	PriorityManual *int `gorm:"column:priority_manual"`
}

func (groupPriorityManual0024) TableName() string { return "groups" }

// Up0024 adds group priority without changing existing rows. Legacy credential
// priority columns, if present, are left untouched and no longer used.
func Up0024(db *gorm.DB) error {
	model := &groupPriorityManual0024{}
	if db.Migrator().HasColumn(model, "priority_manual") {
		return nil
	}
	if err := db.Migrator().AddColumn(model, "PriorityManual"); err != nil {
		return fmt.Errorf("add groups.priority_manual: %w", err)
	}
	return nil
}

func ValidateRecoverable0024(db *gorm.DB) error {
	model := &groupPriorityManual0024{}
	if !db.Migrator().HasTable(model) {
		return fmt.Errorf("validate recoverable group priority: groups table is missing")
	}
	if db.Migrator().HasColumn(model, "priority_manual") {
		return validatePriorityManualColumn0024(db, "groups")
	}
	return nil
}

func Validate0024(db *gorm.DB) error {
	return validatePriorityManualColumn0024(db, "groups")
}

func validatePriorityManualColumn0024(db *gorm.DB, table string) error {
	columns, err := db.Migrator().ColumnTypes(table)
	if err != nil {
		return fmt.Errorf("inspect %s.priority_manual: %w", table, err)
	}
	for _, column := range columns {
		if !strings.EqualFold(column.Name(), "priority_manual") {
			continue
		}
		typeName := strings.ToLower(column.DatabaseTypeName())
		if !strings.Contains(typeName, "int") {
			return fmt.Errorf("validate priority manual: column %q.priority_manual is not integer", table)
		}
		if nullable, known := column.Nullable(); known && !nullable {
			return fmt.Errorf("validate priority manual: column %q.priority_manual is not nullable", table)
		}
		return nil
	}
	return fmt.Errorf("validate priority manual: column %q.priority_manual is missing", table)
}
