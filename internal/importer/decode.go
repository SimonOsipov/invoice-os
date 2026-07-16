package importer

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"io"
	"unicode/utf8"

	"github.com/xuri/excelize/v2"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/unicode"
)

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
// the way. It is pure, DB-free, and mapping-unaware: no header/column
// normalization happens here (that is the service's job, M4-03-04).
func Decode(r io.Reader, format string) (header []string, rows [][]string, facts DecodeFacts, err error) {
	switch format {
	case "csv":
		return decodeCSV(r)
	case "xlsx":
		return decodeXLSX(r)
	default:
		return nil, nil, DecodeFacts{}, fmt.Errorf("importer: unsupported format %q", format)
	}
}

// utf8BOM / utf16LEBOM / utf16BEBOM are the byte-order-mark prefixes Decode
// sniffs for on a CSV upload before falling back to a "no BOM" heuristic.
var (
	utf8BOM    = []byte{0xEF, 0xBB, 0xBF}
	utf16LEBOM = []byte{0xFF, 0xFE}
	utf16BEBOM = []byte{0xFE, 0xFF}
)

// decodeCSV implements the CSV half of Decode: BOM/charset sniffing,
// delimiter sniffing off the (decoded) header line, then a tolerant
// encoding/csv parse that never errors on a ragged row.
func decodeCSV(r io.Reader) ([]string, [][]string, DecodeFacts, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return nil, nil, DecodeFacts{}, err
	}

	var decoded []byte
	var encodingName string

	switch {
	case bytes.HasPrefix(raw, utf8BOM):
		decoded = bytes.TrimPrefix(raw, utf8BOM)
		encodingName = "utf-8"
	case bytes.HasPrefix(raw, utf16LEBOM):
		decoded, err = unicode.UTF16(unicode.LittleEndian, unicode.ExpectBOM).NewDecoder().Bytes(raw)
		if err != nil {
			return nil, nil, DecodeFacts{}, fmt.Errorf("importer: decode utf-16le: %w", err)
		}
		encodingName = "utf-16le"
	case bytes.HasPrefix(raw, utf16BEBOM):
		decoded, err = unicode.UTF16(unicode.BigEndian, unicode.ExpectBOM).NewDecoder().Bytes(raw)
		if err != nil {
			return nil, nil, DecodeFacts{}, fmt.Errorf("importer: decode utf-16be: %w", err)
		}
		encodingName = "utf-16be"
	case utf8.Valid(raw):
		decoded = raw
		encodingName = "utf-8"
	default:
		// Windows-1252 (charmap.Windows1252) is a total decoder over all 256
		// byte values, so this branch never errors — it is the last-resort
		// fallback for non-UTF-8 CSV uploads.
		decoded, err = charmap.Windows1252.NewDecoder().Bytes(raw)
		if err != nil {
			return nil, nil, DecodeFacts{}, fmt.Errorf("importer: decode windows-1252: %w", err)
		}
		encodingName = "windows-1252"
	}

	delimiter := sniffDelimiter(headerLine(decoded))

	cr := csv.NewReader(bytes.NewReader(decoded))
	cr.Comma = delimiter
	cr.FieldsPerRecord = -1 // tolerate ragged rows; the service quarantines them later

	records, err := cr.ReadAll()
	if err != nil {
		return nil, nil, DecodeFacts{}, err
	}

	facts := DecodeFacts{Format: "csv", Delimiter: string(delimiter), Encoding: encodingName}
	if len(records) == 0 {
		return nil, nil, facts, nil
	}
	return records[0], records[1:], facts, nil
}

// headerLine returns the first line of decoded (up to but excluding the
// first \r or \n), for delimiter sniffing.
func headerLine(decoded []byte) []byte {
	if idx := bytes.IndexAny(decoded, "\r\n"); idx >= 0 {
		return decoded[:idx]
	}
	return decoded
}

// sniffDelimiter counts occurrences of each candidate delimiter on the
// header line and returns the most frequent one, defaulting to ',' on a tie
// (comma is checked first, so a strict-greater comparison keeps it as the
// winner on equal counts) or when none of the candidates appear.
func sniffDelimiter(header []byte) rune {
	candidates := []rune{',', ';', '\t', '|'}
	best := ','
	bestCount := -1
	for _, c := range candidates {
		count := bytes.Count(header, []byte(string(c)))
		if count > bestCount {
			bestCount = count
			best = c
		}
	}
	return best
}

// decodeXLSX implements the XLSX half of Decode: stream the first sheet's
// rows via excelize's row iterator so display values (formatted dates,
// grouped numbers) come back exactly as a human would see them in Excel —
// no normalization, that is the service's job.
func decodeXLSX(r io.Reader) ([]string, [][]string, DecodeFacts, error) {
	f, err := excelize.OpenReader(r)
	if err != nil {
		return nil, nil, DecodeFacts{}, err
	}
	defer f.Close()

	sheet := f.GetSheetName(0)
	rowsIter, err := f.Rows(sheet)
	if err != nil {
		return nil, nil, DecodeFacts{}, err
	}
	defer rowsIter.Close()

	var header []string
	var rows [][]string
	first := true
	for rowsIter.Next() {
		cols, err := rowsIter.Columns()
		if err != nil {
			return nil, nil, DecodeFacts{}, err
		}
		if first {
			header = cols
			first = false
			continue
		}
		rows = append(rows, cols)
	}
	if err := rowsIter.Error(); err != nil {
		return nil, nil, DecodeFacts{}, err
	}

	return header, rows, DecodeFacts{Format: "xlsx", Delimiter: "", Encoding: ""}, nil
}
