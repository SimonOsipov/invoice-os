// pagereader_test.go: the shape pins for EXTR-02's second port. Extractor answers which
// invoice FIELDS were found; PageReader answers where every TOKEN sits. Widening Extractor
// would break its three-method pin and law E07's unique Field.Name, so AC-2 gets its own port.
package extraction_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

// onPage is a callback, not a []Page return: no shape here permits holding every page image
// in memory at once, which is what makes the memory constraint enforceable rather than prose.
func TestPageReaderPortHasExactlyNameVersionRead(t *testing.T) {
	var (
		ctxType = reflect.TypeOf((*context.Context)(nil)).Elem()
		errType = reflect.TypeOf((*error)(nil)).Elem()
		strType = reflect.TypeOf("")
		docType = reflect.TypeOf(extraction.Document{})
		cbType  = reflect.TypeOf((func(extraction.Page) error)(nil))
		resType = reflect.TypeOf(extraction.PageResult{})
	)

	// FuncOf builds the exact signature; reflect drops the receiver from an interface method.
	want := map[string]reflect.Type{
		"Name":    reflect.FuncOf(nil, []reflect.Type{strType}, false),
		"Read":    reflect.FuncOf([]reflect.Type{ctxType, docType, cbType}, []reflect.Type{resType, errType}, false),
		"Version": reflect.FuncOf(nil, []reflect.Type{strType}, false),
	}

	it := reflect.TypeOf((*extraction.PageReader)(nil)).Elem()
	if it.Kind() != reflect.Interface {
		t.Fatalf("PageReader is %s, want an interface", it.Kind())
	}

	got := map[string]reflect.Type{}
	for i := range it.NumMethod() {
		m := it.Method(i)
		got[m.Name] = m.Type
	}
	if len(got) != len(want) {
		t.Fatalf("PageReader declares %d methods %v, want exactly 3: Name, Read, Version",
			len(got), sortedKeys(got))
	}

	for name, wantSig := range want {
		gotSig, ok := got[name]
		if !ok {
			t.Errorf("PageReader has no method %s; it declares %v", name, sortedKeys(got))
			continue
		}
		if gotSig != wantSig {
			t.Errorf("PageReader.%s is %s, want %s", name, gotSig, wantSig)
		}
	}
}

// ImagePNG is []byte, borrowed for the duration of the onPage call. An io.Reader would hand
// the caller something to drain later, which is the lifetime this port refuses.
func TestPageCarriesBorrowedImageBytes(t *testing.T) {
	rt := reflect.TypeOf(extraction.Page{})

	// First, so a renamed field fails here rather than being skipped below.
	if got := rt.NumField(); got != 8 {
		t.Fatalf("Page has %d fields, want 8: Number, WidthPt, HeightPt, ImageWidth, "+
			"ImageHeight, ImagePNG, Tokens, Tables", got)
	}

	order := []string{"Number", "WidthPt", "HeightPt", "ImageWidth", "ImageHeight", "ImagePNG", "Tokens", "Tables"}
	want := map[string]reflect.Type{
		"Number":      reflect.TypeOf(0),
		"WidthPt":     reflect.TypeOf(float64(0)),
		"HeightPt":    reflect.TypeOf(float64(0)),
		"ImageWidth":  reflect.TypeOf(0),
		"ImageHeight": reflect.TypeOf(0),
		"ImagePNG":    reflect.TypeOf([]byte(nil)),
		"Tokens":      reflect.TypeOf([]extraction.Token(nil)),
		"Tables":      reflect.TypeOf([]extraction.Table(nil)),
	}
	assertStructFields(t, rt, want, order)
}

// Table's Rows/Cols are the table's own dimensions, not len(Cells): a merged cell occupies
// several (row,col) positions and cannot be recovered by counting.
func TestTableCarriesRowsColsAndCells(t *testing.T) {
	rt := reflect.TypeOf(extraction.Table{})

	if got := rt.NumField(); got != 3 {
		t.Fatalf("Table has %d fields, want 3: Rows, Cols, Cells", got)
	}
	assertStructFields(t, rt, map[string]reflect.Type{
		"Rows":  reflect.TypeOf(0),
		"Cols":  reflect.TypeOf(0),
		"Cells": reflect.TypeOf([]extraction.TableCell(nil)),
	}, []string{"Rows", "Cols", "Cells"})
}

// TableCell.Region is nil when the source carried no box: an empty cell, or any DOCX table,
// which has no provenance at all. Region is last, matching Field.Region's position.
func TestTableCellCarriesSixFieldsRegionLast(t *testing.T) {
	rt := reflect.TypeOf(extraction.TableCell{})

	if got := rt.NumField(); got != 6 {
		t.Fatalf("TableCell has %d fields, want 6: Row, Col, RowSpan, ColSpan, Text, Region", got)
	}
	order := []string{"Row", "Col", "RowSpan", "ColSpan", "Text", "Region"}
	want := map[string]reflect.Type{
		"Row":     reflect.TypeOf(0),
		"Col":     reflect.TypeOf(0),
		"RowSpan": reflect.TypeOf(0),
		"ColSpan": reflect.TypeOf(0),
		"Text":    reflect.TypeOf(""),
		"Region":  reflect.TypeOf((*extraction.Region)(nil)),
	}
	assertStructFields(t, rt, want, order)
}

// PagesWithText is the hybrid-document signal (D-9): counted in the loop that already
// accumulates TextChars, so the Extractor path -- which passes a no-op onPage and reads only
// PageResult -- can see per-page structure without a second traversal.
func TestPageResultCarriesTheThreeCounts(t *testing.T) {
	rt := reflect.TypeOf(extraction.PageResult{})

	if got := rt.NumField(); got != 3 {
		t.Fatalf("PageResult has %d fields, want 3: Pages, TextChars, PagesWithText", got)
	}

	intType := reflect.TypeOf(0)
	order := []string{"Pages", "TextChars", "PagesWithText"}
	want := map[string]reflect.Type{"Pages": intType, "TextChars": intType, "PagesWithText": intType}
	assertStructFields(t, rt, want, order)
}

// Region is EXTR-01's, reused: a token becomes a Field with no conversion and inherits
// extraction_field_results_bbox_normalised's guarantees rather than restating them.
func TestTokenReusesTheRegionType(t *testing.T) {
	rt := reflect.TypeOf(extraction.Token{})

	if got := rt.NumField(); got != 2 {
		t.Fatalf("Token has %d fields, want 2: Text, Region", got)
	}
	assertStructFields(t, rt, map[string]reflect.Type{
		"Text":   reflect.TypeOf(""),
		"Region": reflect.TypeOf(extraction.Region{}),
	}, []string{"Text", "Region"})

	got := reflect.TypeOf(extraction.Token{}.Region)
	if got != reflect.TypeOf(extraction.Region{}) {
		t.Fatalf("Token.Region is %s, want extraction.Region -- a second box type would need a "+
			"conversion the bbox CHECK never sees", got)
	}
	if got.Name() != "Region" || got.PkgPath() != extractionPkg {
		t.Errorf("Token.Region is %q from %q, want Region from %q", got.Name(), got.PkgPath(), extractionPkg)
	}
}

// EXTR-07 owns the wire and builds its own DTO, so a tag here would be a speculative contract
// nothing reads. TestValueTypesCarryNoStructTags covers EXTR-01's three types; these are the
// three PageReader adds.
func TestPageReaderValueTypesCarryNoStructTags(t *testing.T) {
	for _, rt := range []reflect.Type{
		reflect.TypeOf(extraction.Token{}),
		reflect.TypeOf(extraction.Page{}),
		reflect.TypeOf(extraction.PageResult{}),
		reflect.TypeOf(extraction.Table{}),
		reflect.TypeOf(extraction.TableCell{}),
	} {
		if rt.Kind() != reflect.Struct {
			t.Fatalf("%s is %s, want a struct", rt.Name(), rt.Kind())
		}
		n := rt.NumField()
		if n == 0 {
			t.Fatalf("%s has no fields; the tag scan below would pass vacuously", rt.Name())
		}
		for i := range n {
			f := rt.Field(i)
			if tag := string(f.Tag); tag != "" {
				t.Errorf("%s.%s carries the struct tag %q; EXTR-07 owns the wire", rt.Name(), f.Name, tag)
			}
		}
	}
}
