package migrations

import (
	"context"
	"database/sql"
	"fmt"

	goose "github.com/pressly/goose/v3"

	v0 "django-threeport-module/pkg/api/v0"
)

// init registers the migration with goose at startup.
func init() {
	goose.AddMigrationNoTxContext(Up000002, Down000002)
}

// envVarColumns lists the columns added for custom environment variables.
// Migration 000001 only creates tables that do not exist yet, so fields added
// to already-deployed tables need their own migration step.
func envVarColumns() []struct {
	model  interface{}
	column string
	table  string
} {
	return []struct {
		model  interface{}
		column string
		table  string
	}{
		{&v0.DjangoDefinition{}, "Env", "v0_django_definitions"},
		{&v0.DjangoDefinition{}, "SecretEnvVars", "v0_django_definitions"},
		{&v0.DjangoInstance{}, "Env", "v0_django_instances"},
		{&v0.DjangoInstance{}, "SecretEnvVars", "v0_django_instances"},
	}
}

// Up000002 adds the Env and SecretEnvVars columns to the definition and
// instance tables.
func Up000002(ctx context.Context, db *sql.DB) error {
	gormDb, err := getGormDbFromContext(ctx)
	if err != nil {
		return err
	}

	for _, c := range envVarColumns() {
		if gormDb.Migrator().HasColumn(c.model, c.column) {
			continue
		}
		if err := gormDb.Migrator().AddColumn(c.model, c.column); err != nil {
			return fmt.Errorf("failed to add %s column to %s: %w", c.column, c.table, err)
		}
	}

	return nil
}

// Down000002 drops the columns added by Up000002.
func Down000002(ctx context.Context, db *sql.DB) error {
	gormDb, err := getGormDbFromContext(ctx)
	if err != nil {
		return err
	}

	for _, c := range envVarColumns() {
		if !gormDb.Migrator().HasColumn(c.model, c.column) {
			continue
		}
		if err := gormDb.Migrator().DropColumn(c.model, c.column); err != nil {
			return fmt.Errorf("failed to drop %s column from %s: %w", c.column, c.table, err)
		}
	}

	return nil
}
