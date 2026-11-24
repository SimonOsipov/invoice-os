package erp

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/riverqueue/river/rivertype"
)

func TestIngestArgs_KindTenantAndInsertOpts(t *testing.T) {
	args := IngestArgs{
		TenantID: uuid.NewString(), ConnectionID: uuid.NewString(),
		SourceDocID: "doc-1", SourceRevision: "3", Deleted: true,
	}
	if got := args.Kind(); got != "erp.ingest" {
		t.Errorf("Kind() = %q, want erp.ingest", got)
	}
	if got := args.Tenant(); got != args.TenantID || got == "" {
		t.Errorf("Tenant() = %q, want the TenantID %q", got, args.TenantID)
	}

	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	var keys []string
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	want := []string{"connection_id", "deleted", "source_doc_id", "source_revision", "tenant_id"}
	if !slices.Equal(keys, want) {
		t.Errorf("args JSON keys = %v, want %v (no entity_id: the client comes from the connection row)", keys, want)
	}
	if m["tenant_id"] != args.TenantID {
		t.Errorf("tenant_id = %v, want %q", m["tenant_id"], args.TenantID)
	}

	opts := args.InsertOpts()
	if opts.MaxAttempts != 8 {
		t.Errorf("MaxAttempts = %d, want 8", opts.MaxAttempts)
	}
	if !opts.UniqueOpts.ByArgs {
		t.Error("UniqueOpts.ByArgs = false, want true")
	}
}

func TestIngestKey_BoundedStableAndPerRevision(t *testing.T) {
	const connX, connY = "00000000-0000-4000-8000-00000000000a", "00000000-0000-4000-8000-00000000000b"
	base := IngestKey(connX, 0, "a", "1", false)
	if base == "" {
		t.Fatal("IngestKey returned an empty key")
	}
	if again := IngestKey(connX, 0, "a", "1", false); again != base {
		t.Errorf("same inputs gave %q then %q, want a stable key", base, again)
	}

	if long := IngestKey(connX, 0, strings.Repeat("d", 10000), strings.Repeat("r", 10000), false); len(long) == 0 || len(long) > 255 {
		t.Errorf("a 10000-character doc id and revision gave a %d-character key, want 1..255", len(long))
	}

	for name, other := range map[string]string{
		"revision":   IngestKey(connX, 0, "a", "2", false),
		"document":   IngestKey(connX, 0, "b", "1", false),
		"connection": IngestKey(connY, 0, "a", "1", false),
		"epoch":      IngestKey(connX, 1, "a", "1", false),
		"deleted":    IngestKey(connX, 0, "a", "1", true),
	} {
		if other == base {
			t.Errorf("a different %s gave the same key %q", name, base)
		}
	}

	// The doc id and revision must not run together under one separator.
	if IngestKey(connX, 0, "a\x00b", "c", false) == IngestKey(connX, 0, "a", "b\x00c", false) {
		t.Error("(\"a\\x00b\", \"c\") and (\"a\", \"b\\x00c\") share a key")
	}
}

func TestPullArgs_KindTenantAndInsertOpts(t *testing.T) {
	args := PullArgs{TenantID: uuid.NewString(), ConnectionID: uuid.NewString()}
	if got := args.Kind(); got != "erp.pull" {
		t.Errorf("Kind() = %q, want erp.pull", got)
	}
	if got := args.Tenant(); got != args.TenantID || got == "" {
		t.Errorf("Tenant() = %q, want the TenantID %q", got, args.TenantID)
	}

	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	var keys []string
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	if want := []string{"connection_id", "tenant_id"}; !slices.Equal(keys, want) {
		t.Errorf("args JSON keys = %v, want %v", keys, want)
	}
	if m["tenant_id"] != args.TenantID {
		t.Errorf("tenant_id = %v, want %q", m["tenant_id"], args.TenantID)
	}

	opts := args.InsertOpts()
	if opts.MaxAttempts != 8 {
		t.Errorf("MaxAttempts = %d, want 8", opts.MaxAttempts)
	}
	if !opts.UniqueOpts.ByArgs {
		t.Error("UniqueOpts.ByArgs = false, want true")
	}
	// Completed is left out so the next tick can enqueue again.
	got := slices.Clone(opts.UniqueOpts.ByState)
	want := []rivertype.JobState{
		rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRetryable,
		rivertype.JobStateRunning, rivertype.JobStateScheduled,
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("UniqueOpts.ByState = %v, want exactly %v", got, want)
	}
}

func TestWriteBackArgs_KindTenantAndInsertOpts(t *testing.T) {
	args := WriteBackArgs{
		TenantID: uuid.NewString(), InvoiceID: uuid.NewString(), InvoiceNumber: "N-1",
		ResultVersion: 2, Outcome: "accepted", IRN: "IRN-1", Reason: "r",
	}
	if got := args.Kind(); got != "erp.writeback" {
		t.Errorf("Kind() = %q, want erp.writeback", got)
	}
	if (WriteBackArgs{}).Kind() != "erp.writeback" {
		t.Error("zero args have another kind")
	}
	if got := args.Tenant(); got != args.TenantID || got == "" {
		t.Errorf("Tenant() = %q, want the TenantID %q", got, args.TenantID)
	}
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	var keys []string
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	want := []string{"invoice_id", "invoice_number", "irn", "outcome", "reason", "result_version", "tenant_id"}
	if !slices.Equal(keys, want) {
		t.Errorf("args JSON keys = %v, want %v", keys, want)
	}
	if m["tenant_id"] != args.TenantID {
		t.Errorf("tenant_id = %v, want %q", m["tenant_id"], args.TenantID)
	}
	opts := args.InsertOpts()
	if opts.MaxAttempts != 8 || !opts.UniqueOpts.ByArgs {
		t.Errorf("InsertOpts = %+v, want MaxAttempts 8 and ByArgs", opts)
	}
}

func TestWriteBackKey_ShapeAndLength(t *testing.T) {
	inv := uuid.NewString()
	k1, k2 := WriteBackKey(inv, 1), WriteBackKey(inv, 2)
	if k1 != "erp.writeback:"+inv+":1" || k2 != "erp.writeback:"+inv+":2" {
		t.Errorf("keys = %q, %q", k1, k2)
	}
	if k1 == k2 {
		t.Error("two versions share a key")
	}
	if len(k1) > 255 {
		t.Errorf("key is %d characters, want at most 255", len(k1))
	}
}
