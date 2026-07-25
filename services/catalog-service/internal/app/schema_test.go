package app

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestValidateCatalogSchemaAcceptsFreshLaunchSchema(t *testing.T) {
	err := validateCatalogSchema(context.Background(), stubCatalogSchemaQuerier{row: stubCatalogSchemaRow{
		values: []bool{true, true, true, true, true, true, true, true},
	}})
	if err != nil {
		t.Fatalf("validateCatalogSchema() error = %v", err)
	}
}

func TestValidateCatalogSchemaRejectsStaleSchema(t *testing.T) {
	err := validateCatalogSchema(context.Background(), stubCatalogSchemaQuerier{row: stubCatalogSchemaRow{
		values: []bool{true, true, false, true, true, true, true, true},
	}})
	if !errors.Is(err, errStaleCatalogSchema) {
		t.Fatalf("validateCatalogSchema() error = %v, want stale schema rejection", err)
	}
}

type stubCatalogSchemaQuerier struct {
	row stubCatalogSchemaRow
}

func (s stubCatalogSchemaQuerier) QueryRow(context.Context, string, ...any) pgx.Row {
	return s.row
}

type stubCatalogSchemaRow struct {
	values []bool
}

func (r stubCatalogSchemaRow) Scan(dest ...any) error {
	if len(dest) != len(r.values) {
		return errors.New("unexpected scan destination count")
	}
	for index, value := range r.values {
		ptr, ok := dest[index].(*bool)
		if !ok {
			return errors.New("unexpected scan destination type")
		}
		*ptr = value
	}
	return nil
}
