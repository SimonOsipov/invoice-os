package erp

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

var errNotImplemented = errors.New("erp: not implemented")

// ErrInvalidLookup is a lookup body the save refuses; its message names the entry's kind, field and list, never the value.
var ErrInvalidLookup = errors.New("erp: invalid lookup")

type FieldView struct {
	Target      string  `json:"target"`
	Label       string  `json:"label"`
	Line        bool    `json:"line"`
	SourcePath  *string `json:"source_path"`
	DefaultPath *string `json:"default_path"`
	Origin      string  `json:"origin"` // client, default or none
}

type MappingView struct {
	System string      `json:"system"`
	Fields []FieldView `json:"fields"`
}

// FieldChange names one target; a nil SourcePath reverts it to the connector default.
type FieldChange struct {
	Target     string  `json:"target"`
	SourcePath *string `json:"source_path"`
}

type LookupsView struct {
	System      string   `json:"system"`
	ExternalKey string   `json:"external_key"`
	Lookups     []Lookup `json:"lookups"`
}

func (s *Store) Mapping(ctx context.Context, entityID, system string) (MappingView, error) {
	return MappingView{}, errNotImplemented
}

func (s *Store) SaveMapping(ctx context.Context, entityID, system string, changes []FieldChange) (MappingView, error) {
	return MappingView{}, errNotImplemented
}

func (s *Store) Lookups(ctx context.Context, entityID, system, externalKey string) (LookupsView, error) {
	return LookupsView{}, errNotImplemented
}

func (s *Store) SaveLookups(ctx context.Context, entityID, system, externalKey string, entries []Lookup) (LookupsView, error) {
	return LookupsView{}, errNotImplemented
}

// readOverrides reads the client's saved overrides of one system.
func readOverrides(ctx context.Context, tx pgx.Tx, entityID, system string) (Mapping, error) {
	return nil, errNotImplemented
}
