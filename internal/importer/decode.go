package importer

import "io"

// DecodeFacts reports what Decode detected about the uploaded file: its
// format, and — for CSV only — the delimiter and character encoding it
// sniffed. XLSX carries none of the CSV-only facts (excelize owns its own
// decoding), so Delimiter/Encoding are "" for an xlsx input.
type DecodeFacts struct {
	Format    string // "csv" | "xlsx"
	Delimiter string // csv only, else ""
	Encoding  string // csv only, else ""
}

// Decode turns uploaded bytes + a declared format ("csv" | "xlsx") into a
// header row, the remaining data rows, and the facts Decode sniffed along
// the way. It is pure, DB-free, and mapping-unaware.
//
// Decode is implemented in Stage 3 ([csv-sniff]/[xlsx-lib], M4-03-02 /
// task-103). This stub returns zero values so the IMP-DECODE-01..09
// acceptance-criteria tests fail on their content assertions (correct RED),
// not on a compile error.
func Decode(r io.Reader, format string) (header []string, rows [][]string, facts DecodeFacts, err error) {
	return nil, nil, DecodeFacts{}, nil
}
