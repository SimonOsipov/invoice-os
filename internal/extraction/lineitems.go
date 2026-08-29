// lineitems.go: EXTR-05-02. Projects a reader's tables into DocLines. Stub only -- the
// executor replaces the panic; the signature and shape are pinned by lineitems_test.go.
package extraction

// DocLine is one data row of a reader table, projected onto the invoice's line-item shape.
type DocLine struct {
	Index     int // 1-based, continuous across pages and tables in reader order
	Quantity  *string
	UnitPrice *string
	LineTotal *string
	Region    *Region // the line-total cell's region, or nil
}

// LineItems projects every table's data rows across pages into DocLines, in reader order.
func LineItems(pages []Page) []DocLine {
	panic("not implemented")
}
