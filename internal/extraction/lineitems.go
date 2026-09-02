// lineitems.go: EXTR-05-02. Projects a reader's tables into DocLines.
package extraction

import (
	"regexp"
	"strconv"
	"strings"
)

// Role names are the <role> half of line_items[N].<role>: one source for the Regions key, the
// field name and the grid's column id.
const (
	LineRoleDescription = "description"
	LineRoleQuantity    = "quantity"
	LineRoleUnitPrice   = "unit_price"
	LineRoleLineTotal   = "line_total"
)

// LineRoles is one line's cells in emit order: reading order, left to right.
var LineRoles = []string{LineRoleDescription, LineRoleQuantity, LineRoleUnitPrice, LineRoleLineTotal}

// DocLine is one data row of a reader table, projected onto the invoice's line-item shape.
type DocLine struct {
	Index       int // 1-based, continuous across pages and tables in reader order
	Description *string
	Quantity    *string
	UnitPrice   *string
	LineTotal   *string
	Region      *Region            // the line-total cell's region, or nil
	Regions     map[string]*Region // per-role cell region, keyed by LineRole*; nil-safe to read
}

// Cell returns one role's value, or nil when that cell is absent.
func (l DocLine) Cell(role string) *string {
	switch role {
	case LineRoleDescription:
		return l.Description
	case LineRoleQuantity:
		return l.Quantity
	case LineRoleUnitPrice:
		return l.UnitPrice
	case LineRoleLineTotal:
		return l.LineTotal
	}
	return nil
}

// setRegion records one cell's box. Allocated lazily so a boxless table leaves Regions nil.
func (l *DocLine) setRegion(role string, region *Region) {
	if region == nil {
		return
	}
	if l.Regions == nil {
		l.Regions = make(map[string]*Region, len(LineRoles))
	}
	l.Regions[role] = region
}

// LineFieldName is the only source for the line_items[N].<role> name -- reconcileLines'
// arithmetic flag and LineItemResults' value rows must never drift apart.
func LineFieldName(index int, role string) string {
	return "line_items[" + strconv.Itoa(index) + "]." + role
}

// LineItemResults projects one FieldResult per populated cell: lines in slice order, roles in
// LineRoles order. An absent cell emits no row at all, never a row carrying an empty value.
func LineItemResults(lines []DocLine) []FieldResult {
	out := make([]FieldResult, 0, len(lines)*len(LineRoles))
	for _, line := range lines {
		for _, role := range LineRoles {
			cell := line.Cell(role)
			if cell == nil {
				continue
			}
			v := *cell // copied: a caller mutating its DocLine must not reach an emitted row
			out = append(out, FieldResult{
				Field:        Field{Name: LineFieldName(line.Index, role), Value: &v, Region: line.Regions[role], Reason: ReasonNone},
				Alternatives: []Field{},
			})
		}
	}
	return out
}

// liRole is which of the four line-item fields a header column names.
type liRole int

const (
	liRoleNone liRole = iota
	liRoleDescription
	liRoleQuantity
	liRoleUnitPrice
	liRoleLineTotal
)

// liLexicon maps a normalised header cell to its role. Exact match only -- a substring match
// would let "Description" contain "amount" style false positives.
var liLexicon = map[string]liRole{
	"description": liRoleDescription,
	"item":        liRoleDescription,
	"details":     liRoleDescription,
	"particulars": liRoleDescription,

	"qty":        liRoleQuantity,
	"quantity":   liRoleQuantity,
	"unit price": liRoleUnitPrice,
	"rate":       liRoleUnitPrice,
	"price":      liRoleUnitPrice,
	"line total": liRoleLineTotal,
	"total":      liRoleLineTotal,
	"amount":     liRoleLineTotal,
}

// reLineQty is quantity's own pattern, distinct from ShapeAmount's: line_items.quantity is
// numeric(14,3), a third fraction digit money's numeric(14,2) has no room for.
var reLineQty = regexp.MustCompile(`^\s*(-?)([0-9]{1,3}(?:,[0-9]{3})+|[0-9]+)(?:\.([0-9]{1,3}))?\s*$`)

// LineItems projects every table's data rows across pages into DocLines, in reader order.
func LineItems(pages []Page) []DocLine {
	lines := make([]DocLine, 0)
	index := 0
	for _, page := range pages {
		for _, tbl := range page.Tables {
			descCol, qtyCol, priceCol, totalCol := liClassifyHeader(tbl)
			if qtyCol == -1 && priceCol == -1 && totalCol == -1 {
				// The gate counts quantity, unit price and line total only: a header naming
				// description alone is prose, not a line-item table.
				continue
			}

			byRow := liIndexRows(tbl)
			rowKeys := make([]int, 0, len(byRow))
			for r := range byRow {
				rowKeys = append(rowKeys, r)
			}
			liSortInts(rowKeys)

			for _, r := range rowKeys {
				index++
				line := DocLine{Index: index}
				cells := byRow[r]

				if descCol != -1 {
					if cell, ok := cells[descCol]; ok {
						line.setRegion(LineRoleDescription, cell.Region)
						if v, ok := liNormalizeDescription(cell.Text); ok {
							line.Description = &v
						}
					}
				}
				if qtyCol != -1 {
					if cell, ok := cells[qtyCol]; ok {
						line.setRegion(LineRoleQuantity, cell.Region)
						if v, ok := liNormalizeQuantity(cell.Text); ok {
							line.Quantity = &v
						}
					}
				}
				if priceCol != -1 {
					if cell, ok := cells[priceCol]; ok {
						line.setRegion(LineRoleUnitPrice, cell.Region)
						if readings := normalizeAmount(cell.Text); len(readings) > 0 {
							v := readings[0]
							line.UnitPrice = &v
						}
					}
				}
				if totalCol != -1 {
					if cell, ok := cells[totalCol]; ok {
						line.Region = cell.Region
						line.setRegion(LineRoleLineTotal, cell.Region)
						if readings := normalizeAmount(cell.Text); len(readings) > 0 {
							v := readings[0]
							line.LineTotal = &v
						}
					}
				}

				lines = append(lines, line)
			}
		}
	}
	return lines
}

// liClassifyHeader reads row 0 (the header, by convention) and returns each role's column, or
// -1 when the header does not name it. A role named twice keeps its lowest-numbered column.
func liClassifyHeader(tbl Table) (descCol, qtyCol, priceCol, totalCol int) {
	descCol, qtyCol, priceCol, totalCol = -1, -1, -1, -1

	headerByCol := make(map[int]TableCell)
	cols := make([]int, 0)
	for _, c := range tbl.Cells {
		if c.Row != 0 {
			continue
		}
		headerByCol[c.Col] = c
		cols = append(cols, c.Col)
	}
	liSortInts(cols)

	for _, col := range cols {
		switch liLexicon[liNormalizeHeaderText(headerByCol[col].Text)] {
		case liRoleDescription:
			if descCol == -1 {
				descCol = col
			}
		case liRoleQuantity:
			if qtyCol == -1 {
				qtyCol = col
			}
		case liRoleUnitPrice:
			if priceCol == -1 {
				priceCol = col
			}
		case liRoleLineTotal:
			if totalCol == -1 {
				totalCol = col
			}
		}
	}
	return
}

// liIndexRows buckets every non-header cell by row then column. Cells arrive sparse and
// unsorted, so this is the only way to look one up by position.
func liIndexRows(tbl Table) map[int]map[int]TableCell {
	byRow := make(map[int]map[int]TableCell)
	for _, c := range tbl.Cells {
		if c.Row == 0 {
			continue
		}
		if byRow[c.Row] == nil {
			byRow[c.Row] = make(map[int]TableCell)
		}
		byRow[c.Row][c.Col] = c
	}
	return byRow
}

// liNormalizeHeaderText case-folds, trims and collapses internal whitespace so the lexicon can
// match exactly rather than by substring.
func liNormalizeHeaderText(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(s)), " ")
}

// liNormalizeDescription trims a description cell and reports whether anything is left. A blank
// cell is an absence: extraction_field_results.value forbids the empty string. Internal
// whitespace is verbatim -- liNormalizeHeaderText's fold is for headers, never cell content.
func liNormalizeDescription(raw string) (string, bool) {
	out := strings.TrimSpace(raw)
	return out, out != ""
}

// liNormalizeQuantity validates and normalises the same way normalizeAmount does (strip
// grouping commas, keep the fraction verbatim), just against quantity's own pattern.
func liNormalizeQuantity(raw string) (string, bool) {
	m := reLineQty.FindStringSubmatch(raw)
	if m == nil {
		return "", false
	}
	out := m[1] + strings.ReplaceAll(m[2], ",", "")
	if m[3] != "" {
		out += "." + m[3]
	}
	return out, true
}

// liSortInts sorts a in place. Insertion sort: row/column counts are small, and "sort" is
// outside lineitems.go's allowed import set (the purity scan pins it to regexp/strconv/
// strings/unicode).
func liSortInts(a []int) {
	for i := 1; i < len(a); i++ {
		v := a[i]
		j := i - 1
		for j >= 0 && a[j] > v {
			a[j+1] = a[j]
			j--
		}
		a[j+1] = v
	}
}
