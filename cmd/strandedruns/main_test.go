package main

import (
	"bytes"
	"context"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/approval"
)

func TestReport_EmptyIsNoStrandedRuns(t *testing.T) {
	var buf bytes.Buffer
	if err := report(&buf, nil); err != nil {
		t.Fatal(err)
	}
	if buf.String() != "no stranded runs\n" {
		t.Errorf("got %q", buf.String())
	}
}

func TestReport_RowsAreTabSeparatedWithHeaderAndTotal(t *testing.T) {
	at := time.Date(2026, 11, 6, 15, 4, 5, 0, time.FixedZone("x", 2*3600))
	rows := []approval.StrandedRun{
		{WorkspaceID: "w1", Workspace: "One", InvoiceID: "i1", InvoiceNumber: "INV-1", RoleKey: "k", RoleTitle: "T", Reason: approval.StrandedDeleted, ArmedAt: at, Blocks: approval.BlocksNow},
		{WorkspaceID: "w1", Workspace: "One", InvoiceID: "i2", InvoiceNumber: "INV-2", RoleKey: "", RoleTitle: "", Reason: approval.StrandedDeleted, ArmedAt: at, Blocks: approval.BlocksLater},
		{WorkspaceID: "w2", Workspace: "Two", InvoiceID: "i3", InvoiceNumber: "INV-3", RoleKey: "k", RoleTitle: "T", Reason: approval.StrandedNoHolder, ArmedAt: at, Blocks: approval.BlocksNow},
	}
	var buf bytes.Buffer
	if err := report(&buf, rows); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
	if len(lines) != 5 {
		t.Fatalf("lines = %q, want header, 3 rows, totals", lines)
	}
	if lines[0] != "workspace_id\tworkspace\tinvoice_id\tinvoice\trole_key\trole_title\treason\tarmed_at\tblocks" {
		t.Errorf("header = %q", lines[0])
	}
	for _, l := range lines[1:4] {
		if f := strings.Split(l, "\t"); len(f) != 9 || !strings.HasSuffix(f[7], "Z") {
			t.Errorf("row %q: want 9 fields with a UTC armed_at", l)
		}
	}
	f := strings.Split(lines[2], "\t")
	if f[4] != "-" || f[5] != "-" {
		t.Errorf("empty key and title = %q, %q, want -", f[4], f[5])
	}
	if got := strings.Split(lines[1], "\t")[7]; got != "2026-11-06T13:04:05Z" {
		t.Errorf("armed_at = %q", got)
	}
	if lines[4] != "3 stranded run(s) in 2 workspace(s)" {
		t.Errorf("totals = %q", lines[4])
	}
}

func TestReport_EveryTextFieldIsSanitised(t *testing.T) {
	rows := []approval.StrandedRun{{
		WorkspaceID: "w", Workspace: "A\tB", InvoiceID: "i", InvoiceNumber: "INV\n1",
		RoleKey: "k\r1", RoleTitle: "T\t\nZ", Reason: approval.StrandedNoHolder, ArmedAt: time.Now(), Blocks: approval.BlocksNow,
	}}
	var buf bytes.Buffer
	if err := report(&buf, rows); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("lines = %q, want header, 1 row, totals", lines)
	}
	f := strings.Split(lines[1], "\t")
	if len(f) != 9 {
		t.Fatalf("fields = %q, want 9", f)
	}
	for i, want := range map[int]string{1: "A B", 3: "INV 1", 4: "k 1", 5: "T  Z"} {
		if f[i] != want {
			t.Errorf("field %d = %q, want %q", i, f[i], want)
		}
	}
	if strings.Contains(buf.String(), "\r") {
		t.Error("output holds a carriage return")
	}
}

func TestRun_MissingDatabaseURLFailsBeforeAnyConnection(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	t.Setenv("DATABASE_READER_URL", "postgres://u:p@127.0.0.1:1/x")
	err := run(context.Background(), &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Fatalf("err = %v, want one naming DATABASE_URL", err)
	}
}

func TestRun_MissingReaderURLFailsBeforeAnyConnection(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://%zz")
	t.Setenv("DATABASE_READER_URL", "")
	err := run(context.Background(), &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "DATABASE_READER_URL") {
		t.Fatalf("err = %v, want one naming DATABASE_READER_URL", err)
	}
}

func TestRun_AgainstTheDevDatabase(t *testing.T) {
	if os.Getenv("DATABASE_URL") == "" || os.Getenv("DATABASE_READER_URL") == "" {
		t.Skip("set DATABASE_URL and DATABASE_READER_URL")
	}
	var buf bytes.Buffer
	if err := run(context.Background(), &buf); err != nil {
		t.Fatalf("run: %v", err)
	}
	out := strings.TrimSuffix(buf.String(), "\n")
	last := out[strings.LastIndex(out, "\n")+1:]
	if out != "no stranded runs" && !regexp.MustCompile(`^\d+ stranded run\(s\) in \d+ workspace\(s\)$`).MatchString(last) {
		t.Errorf("output = %q", buf.String())
	}
}
