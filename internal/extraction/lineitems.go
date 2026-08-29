// lineitems.go: EXTR-05-02. Projects a reader's tables into DocLines.
package extraction

import (
	"regexp"
	"strings"
)

// DocLine is one data row of a reader table, projected onto the invoice's line-item shape.
type DocLine struct {
	Index     int // 1-based, continuous across pages and tables in reader order
	Quantity  *string
	UnitPrice *string
	LineTotal *string
	Region    *Region // the line-total cell's region, or nil
}

// liRole is which of the three line-item fields a header column names.
type liRole int

const (
	liRoleNone liRole = iota
	liRoleQuantity
	liRoleUnitPrice
	liRoleLineTotal
)

// liLexicon maps a normalised header cell to its role. Exact match only -- a substring match
// would let "Description" contain "amount" style false positives.
var liLexicon = map[string]liRole{
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
			qtyCol, priceCol, totalCol := liClassifyHeader(tbl)
			if qtyCol == -1 && priceCol == -1 && totalCol == -1 {
				continue // fail closed only when the header names none of the three roles
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

				if qtyCol != -1 {
					if cell, ok := cells[qtyCol]; ok {
						if v, ok := liNormalizeQuantity(cell.Text); ok {
							line.Quantity = &v
						}
					}
				}
				if priceCol != -1 {
					if cell, ok := cells[priceCol]; ok {
						if readings := normalizeAmount(cell.Text); len(readings) > 0 {
							v := readings[0]
							line.UnitPrice = &v
						}
					}
				}
				if totalCol != -1 {
					if cell, ok := cells[totalCol]; ok {
						line.Region = cell.Region
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
func liClassifyHeader(tbl Table) (qtyCol, priceCol, totalCol int) {
	qtyCol, priceCol, totalCol = -1, -1, -1

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
