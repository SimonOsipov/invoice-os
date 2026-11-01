package extraction_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

// The nightly accuracy corpus: 30 PDFs read from testdata/nightly_key.json, so bytes and key
// cannot drift. Synthetic: no source document, so no scrubber. Every name, TIN and amount is
// invented; parties are the two in extraction-corpus.md. Not layouts: no corpus_ registry.

const nlKeyFile = "nightly_key.json"

type nlRow struct {
	File   string              `json:"file"`
	Kind   string              `json:"kind"`
	Fields map[string][]string `json:"fields"`
}

var nlRows = nlLoadKey()

func nlLoadKey() []nlRow {
	raw, err := os.ReadFile(filepath.Join(fxDir, nlKeyFile))
	if err != nil {
		panic(fmt.Sprintf("nightly key: %v", err))
	}
	var rows []nlRow
	if err := json.Unmarshal(raw, &rows); err != nil {
		panic(fmt.Sprintf("nightly key: %v", err))
	}
	return rows
}

func init() {
	for _, r := range nlRows {
		if r.Kind == "layout" {
			continue
		}
		fxCorpus = append(fxCorpus, struct {
			name  string
			build func() []byte
		}{r.File, func() []byte { return nlBuild(r) }})
	}
}

// nlIndex is the NN of nightly_<kind>_<NN>.pdf, from 1.
func nlIndex(file string) int {
	parts := strings.Split(strings.TrimSuffix(file, ".pdf"), "_")
	var n int
	if _, err := fmt.Sscanf(parts[len(parts)-1], "%d", &n); err != nil {
		panic("nightly file name: " + file)
	}
	return n
}

// nlTINSupplier holds the supplier TIN of each tin row; the buyer's is in the key. Rows 4 and
// 5 sit one digit from the buyer's, and rows 1 to 3 print a run of one digit.
var nlTINSupplier = map[int]string{
	1: "99999999-9998", 2: "99999999-1112", 3: "99999999-8889",
	4: "99999999-2345", 5: "99999999-4317", 6: "99999999-7777",
}

func nlSupplierTIN(r nlRow) string {
	n := nlIndex(r.File)
	switch r.Kind {
	case "tin":
		return nlTINSupplier[n]
	case "scan":
		return fmt.Sprintf("99999999-40%02d", n)
	}
	return fmt.Sprintf("99999999-30%02d", n)
}

// nlDoc is what one page prints; an empty value prints nothing.
type nlDoc struct {
	number, date, supTIN, sup, buyTIN, buy, cur, sub, vat, total string
}

// nlDateLayouts are the four printings; every key date has a day above 12, so each reads one way.
var nlDateLayouts = [4]string{"2006-01-02", "02/01/2006", "02 Jan 2006", "Jan 02, 2006"}

func nlMoney(plain string) string {
	if plain == "" {
		return ""
	}
	whole, frac, _ := strings.Cut(plain, ".")
	var b strings.Builder
	for i, d := range whole {
		if i > 0 && (len(whole)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(d)
	}
	return b.String() + "." + frac
}

func nlDocOf(r nlRow) nlDoc {
	first := func(f string) string {
		if v := r.Fields[f]; len(v) > 0 {
			return v[0]
		}
		return ""
	}
	n := nlIndex(r.File)
	d := nlDoc{
		number: first("invoice_number"), date: first("issue_date"), buyTIN: first("buyer_tin"),
		buy: first("buyer_name"), cur: first("currency"), sub: nlMoney(first("subtotal")),
		vat: nlMoney(first("vat")), total: nlMoney(first("total")), supTIN: nlSupplierTIN(r),
	}
	d.sup = "Adeyemi Trading Limited"
	if strings.EqualFold(d.buy, d.sup) {
		d.sup = "Honeywell Group"
	}
	if r.Kind != "scan" && d.date != "" {
		t, err := time.Parse(nlDateLayouts[0], d.date)
		if err != nil {
			panic("nightly date: " + d.date)
		}
		d.date = t.Format(nlDateLayouts[((n-1)/4)%4])
	}
	if r.Kind == "scan" {
		d.sup = strings.ToUpper(d.sup)
	}
	return d
}

func nlBuild(r nlRow) []byte {
	if r.Kind == "scan" {
		return nlBuildScan(r)
	}
	d := nlDocOf(r)
	var lines []fxLine
	switch (nlIndex(r.File) - 1) % 4 {
	case 0:
		lines = nlInline(d)
	case 1:
		lines = nlSplit(d)
	case 2:
		lines = nlStacked(d)
	default:
		lines = nlTwoColumn(d)
	}
	return fxTextPage(lines...)
}

// nlPen lays lines down a column from y, skipping an empty value.
type nlPen struct {
	lines []fxLine
	y     int
}

func (p *nlPen) put(step int, x int, text string) {
	p.lines = append(p.lines, fxLine{12, x, p.y, text})
	p.y -= step
}

func nlInline(d nlDoc) []fxLine {
	p := &nlPen{lines: []fxLine{{24, 72, 720, "INVOICE"}}, y: 690}
	for _, kv := range [][2]string{
		{"Invoice No", d.number}, {"Invoice Date", d.date}, {"Supplier TIN", d.supTIN}, {"Supplier", d.sup},
		{"Buyer TIN", d.buyTIN}, {"Buyer", d.buy}, {"Currency", d.cur},
	} {
		if kv[1] != "" {
			p.put(18, 72, kv[0]+": "+kv[1])
		}
	}
	p.y = 240
	for _, kv := range [][2]string{{"Sub-total", d.sub}, {"VAT", d.vat}, {"Total", d.total}} {
		if kv[1] != "" {
			p.put(18, 72, kv[0]+": "+kv[1])
		}
	}
	return p.lines
}

func nlSplit(d nlDoc) []fxLine {
	p := &nlPen{lines: []fxLine{{24, 72, 720, "INVOICE"}}, y: 690}
	pair := func(label, value string) {
		if value == "" {
			return
		}
		p.lines = append(p.lines, fxLine{12, 72, p.y, label}, fxLine{12, 220, p.y, value})
		p.y -= 18
	}
	pair("Invoice No", d.number)
	pair("Invoice Date", d.date)
	pair("Supplier TIN", d.supTIN)
	pair("Supplier", d.sup)
	pair("Currency", d.cur)
	p.y = 360
	pair("Buyer TIN", d.buyTIN)
	pair("Buyer", d.buy)
	p.y = 240
	pair("Sub-total", d.sub)
	pair("VAT", d.vat)
	pair("Total", d.total)
	return p.lines
}

func nlStacked(d nlDoc) []fxLine {
	p := &nlPen{lines: []fxLine{{24, 72, 720, "INVOICE"}}, y: 690}
	group := func(label string, values ...string) {
		if values[0] == "" {
			return
		}
		p.put(16, 72, label)
		for _, v := range values {
			if v != "" {
				p.put(16, 72, v)
			}
		}
		p.y -= 16
	}
	group("Invoice No", d.number)
	group("Invoice Date", d.date)
	group("Supplier", d.sup, d.supTIN)
	group("Buyer", d.buy, d.buyTIN)
	p.y = 240
	group("Total", strings.TrimSpace(d.cur+" "+d.total))
	if d.sub != "" {
		group("Sub-total", d.sub)
		group("VAT", d.vat)
	}
	return p.lines
}

func nlTwoColumn(d nlDoc) []fxLine {
	p := &nlPen{lines: []fxLine{{24, 72, 720, "INVOICE"}}, y: 690}
	if d.number != "" {
		p.put(18, 72, "Invoice No: "+d.number)
	}
	if d.date != "" {
		p.put(18, 72, "Invoice Date: "+d.date)
	}
	if d.cur != "" {
		p.put(18, 72, "Currency: "+d.cur)
	}
	block := func(x int, role, name, tin string) {
		p.y = 630
		p.put(16, x, role)
		p.put(16, x, name)
		if tin != "" {
			p.put(16, x, "TIN: "+tin)
		}
	}
	block(72, "Supplier", d.sup, d.supTIN)
	block(400, "Buyer", d.buy, d.buyTIN)
	p.y = 240
	for _, kv := range [][2]string{{"Sub-total", d.sub}, {"VAT", d.vat}, {"Total", d.total}} {
		if kv[1] != "" {
			p.lines = append(p.lines, fxLine{12, 380, p.y, kv[0]}, fxLine{12, 500, p.y, kv[1]})
			p.y -= 18
		}
	}
	return p.lines
}

// nlDeg is a scan's degradation, plain arithmetic on (x, y) so a build never reads a clock or a
// random source.
type nlDeg struct{ speckle, fade, lineLoss bool }

// nlDegOf maps a scan's NN to a distinct combination: 1 is clean, 8 has all three.
func nlDegOf(n int) nlDeg {
	b := n - 1
	return nlDeg{speckle: b&1 != 0, fade: b&2 != 0, lineLoss: b&4 != 0}
}

func nlHash(x, y, seed int) uint32 {
	h := uint32(x)*73856093 ^ uint32(y)*19349663 ^ uint32(seed)*83492791
	h ^= h >> 13
	h *= 0x5bd1e995
	return h ^ h>>15
}

// apply speckles the paper, fades one ink pixel in nine and drops every 53rd scan line. Each
// pass reads the canvas as it stood before it, so the passes do not feed each other.
func (d nlDeg) apply(c *fxCanvas) {
	if d.fade {
		for y := range c.h {
			for x := range c.w {
				if c.ink[y*c.w+x] != 0 && nlHash(x, y, 1)%9 == 0 {
					c.ink[y*c.w+x] = 0
				}
			}
		}
	}
	if d.lineLoss {
		for y := 26; y < c.h; y += 53 {
			c.fill(0, y, c.w, y+1)
			for x := range c.w {
				c.ink[y*c.w+x] = 0
			}
		}
	}
	if d.speckle {
		for y := range c.h {
			for x := range c.w {
				if nlHash(x, y, 2)%1499 == 0 {
					c.ink[y*c.w+x] = 1
				}
			}
		}
	}
}

// nlScanCanvas draws the page on the wild scan's geometry rules: one glyph scale, no two strings
// on one y, glyphs from the raster font only.
func nlScanCanvas(r nlRow, deg nlDeg) *fxCanvas {
	d := nlDocOf(r)
	c := fxNewCanvas(fxRasterW, fxRasterH)
	text := func(label, value string, x, y int) {
		if value != "" {
			c.draw(label+value, x, y, fxDenseBody)
		}
	}
	c.draw("INVOICE", 45, 55, 8)
	c.fill(45, 130, 1235, 136)

	c.draw(d.sup, 45, 165, fxDenseBody)
	c.draw("14 MARINA STREET", 45, 203, fxDenseBody)
	c.draw("LAGOS ISLAND, LAGOS STATE", 45, 241, fxDenseBody)
	text("TIN: ", d.supTIN, 45, 279)

	text("INVOICE NO: ", d.number, 635, 317)
	text("ISSUE DATE: ", d.date, 635, 355)
	text("CURRENCY: ", d.cur, 635, 393)

	c.draw("BILL TO:", 45, 430, fxDenseBody)
	c.draw(strings.ToUpper(d.buy), 45, 468, fxDenseBody)
	c.draw("7 AWOLOWO ROAD, IKOYI", 45, 506, fxDenseBody)
	text("TIN: ", d.buyTIN, 45, 544)

	row := func(label, value string, y int) {
		if value != "" {
			c.drawRight(label, 900, y, fxDenseBody)
			c.drawRight(value, 1225, y, fxDenseBody)
		}
	}
	row("SUBTOTAL", d.sub, 700)
	row("VAT", d.vat, 742)
	c.fill(700, 782, 1235, 784)
	row("TOTAL DUE", d.total, 798)

	deg.apply(c)
	return c
}

func nlBuildScan(r nlRow) []byte {
	c := nlScanCanvas(r, nlDegOf(nlIndex(r.File)))
	return fxAssemble([]fxObject{
		fxObject("<< /Type /Catalog /Pages 2 0 R >>"),
		fxObject("<< /Type /Pages /Kids [3 0 R] /Count 1 >>"),
		fxPage(fxImageRes(5), 4),
		fxStream([]byte(fxImageDraw)),
		fxImageObjectRLE(fxRasterW, fxRasterH, c.pack()),
	})
}

// --- the tests --------------------------------------------------------------

func nlNightlyRows(kind string) []nlRow {
	var out []nlRow
	for _, r := range nlRows {
		if r.Kind == kind {
			out = append(out, r)
		}
	}
	return out
}

func TestNightlyCorpus_HasThePlannedKinds(t *testing.T) {
	want := map[string]int{"layout": 14, "text": 16, "scan": 8, "tin": 6}
	got := map[string]int{}
	for _, r := range nlRows {
		got[r.Kind]++
		if _, err := os.Stat(filepath.Join(fxDir, r.File)); err != nil {
			t.Errorf("key row %s has no file: %v", r.File, err)
		}
	}
	for kind, n := range want {
		if got[kind] != n {
			t.Errorf("key holds %d %q row(s), want %d", got[kind], kind, n)
		}
	}
	if len(got) != len(want) {
		t.Errorf("key kinds are %v, want exactly %v", got, want)
	}

	files, err := filepath.Glob(filepath.Join(fxDir, "nightly_*.pdf"))
	if err != nil || len(files) != 30 {
		t.Errorf("%d nightly_*.pdf file(s) on disk (%v), want 30", len(files), err)
	}
	for kind, n := range map[string]int{"text": 16, "scan": 8, "tin": 6} {
		m, _ := filepath.Glob(filepath.Join(fxDir, "nightly_"+kind+"_*.pdf"))
		if len(m) != n {
			t.Errorf("%d nightly_%s_*.pdf file(s) on disk, want %d", len(m), kind, n)
		}
	}
	extra, _ := filepath.Glob(filepath.Join(fxDir, "nightly_*.docling.json"))
	if len(extra) != 0 {
		t.Errorf("%v committed: the nightly set has no docling golden", extra)
	}
}

func TestNightlyCorpus_ScansCarryNoText(t *testing.T) {
	scans := nlNightlyRows("scan")
	if len(scans) != 8 {
		t.Fatalf("%d scan row(s), want 8", len(scans))
	}
	for _, r := range scans {
		t.Run(r.File, func(t *testing.T) {
			doc := extraction.Document{Bytes: fxRead(t, r.File), ContentType: "application/pdf"}
			pages, tokens, ink := 0, 0, 0
			_, err := extraction.NewPDFiumReader().Read(t.Context(), doc, func(p extraction.Page) error {
				pages++
				tokens += len(p.Tokens)
				img, err := png.Decode(bytes.NewReader(p.ImagePNG))
				if err != nil {
					return err
				}
				b := img.Bounds()
				for y := b.Min.Y; y < b.Max.Y; y++ {
					for x := b.Min.X; x < b.Max.X; x++ {
						if r, g, bl, _ := img.At(x, y).RGBA(); r+g+bl < 3*0x4000 {
							ink++
						}
					}
				}
				return nil
			})
			if err != nil {
				t.Fatalf("read %s: %v", r.File, err)
			}
			if pages != 1 || tokens != 0 {
				t.Errorf("%s reads %d page(s) and %d token(s), want 1 and 0", r.File, pages, tokens)
			}
			if ink == 0 {
				t.Errorf("%s renders no ink", r.File)
			}
		})
	}
}

func TestNightlyCorpus_DegradationChangesThePixels(t *testing.T) {
	scans := nlNightlyRows("scan")
	r := scans[len(scans)-1]
	clean := nlScanCanvas(r, nlDeg{}).pack()
	for name, deg := range map[string]nlDeg{
		"speckle": {speckle: true}, "fade": {fade: true}, "lineLoss": {lineLoss: true}, "all": nlDegOf(8),
	} {
		if bytes.Equal(clean, nlScanCanvas(r, deg).pack()) {
			t.Errorf("%s leaves the raster of %s unchanged", name, r.File)
		}
	}
	if !bytes.Equal(nlScanCanvas(r, nlDegOf(8)).pack(), nlScanCanvas(r, nlDegOf(8)).pack()) {
		t.Errorf("two builds of %s differ", r.File)
	}
}

var nlTINPattern = regexp.MustCompile(`\d{8}-\d{4}`)

// nlTINProblem names why tin is outside the free block, or "" when it is inside.
func nlTINProblem(tin string) string {
	var n int
	if !nlTINPattern.MatchString(tin) || tin[:9] != "99999999-" {
		return "is not in the 99999999- block"
	}
	fmt.Sscanf(tin[9:], "%d", &n)
	if n < 101 {
		return "is below 99999999-0101"
	}
	return ""
}

func nlTINAbove(t *testing.T, tin string) {
	t.Helper()
	if p := nlTINProblem(tin); p != "" {
		t.Errorf("TIN %q %s", tin, p)
	}
}

func TestNightlyCorpus_UsesOnlyTheFreeTINBlock(t *testing.T) {
	for _, bad := range []string{"99999999-0001", "99999999-0009", "99999999-0100", "12345678-0200"} {
		if nlTINProblem(bad) == "" {
			t.Errorf("control: TIN %s passes the free-block check", bad)
		}
	}
	if p := nlTINProblem("99999999-0101"); p != "" {
		t.Errorf("control: 99999999-0101 %s", p)
	}
	seen := 0
	for _, r := range nlRows {
		if r.Kind == "layout" {
			continue
		}
		for _, tin := range append([]string{nlSupplierTIN(r)}, r.Fields["buyer_tin"]...) {
			seen++
			nlTINAbove(t, tin)
		}
		for _, tin := range nlTINPattern.FindAllString(string(fxRead(t, r.File)), -1) {
			nlTINAbove(t, tin)
		}
	}
	if seen != 60 {
		t.Errorf("checked %d TIN(s), want 60 (two per document)", seen)
	}
}

func TestNightlyCorpus_HasARunOfOneDigitTIN(t *testing.T) {
	buyers := map[string]bool{}
	apart := false
	for _, r := range nlNightlyRows("tin") {
		buy := r.Fields["buyer_tin"][0]
		buyers[buy] = true
		diff := 0
		sup := nlSupplierTIN(r)
		for i := range buy {
			if buy[i] != sup[i] {
				diff++
			}
		}
		apart = apart || diff == 1
	}
	for _, want := range []string{"99999999-9999", "99999999-1111", "99999999-8888"} {
		if !buyers[want] {
			t.Errorf("no tin row has buyer TIN %s", want)
		}
	}
	if !apart {
		t.Errorf("no tin row has a buyer TIN one digit from its supplier's")
	}
}

var nlMetadataNeedles = []string{"/Info", "/ID", "<x:xmpmeta"}

func nlMetadataHit(b []byte) string {
	for _, needle := range nlMetadataNeedles {
		if bytes.Contains(b, []byte(needle)) {
			return needle
		}
	}
	return ""
}

func TestNightlyCorpus_CarriesNoMetadata(t *testing.T) {
	for _, planted := range []string{"/Info", "/ID", "<x:xmpmeta"} {
		if nlMetadataHit([]byte("1 0 obj << "+planted+" 2 0 R >>")) != planted {
			t.Errorf("control: a planted %s is not found", planted)
		}
	}
	checked := 0
	for _, r := range nlRows {
		if r.Kind == "layout" {
			continue
		}
		checked++
		if hit := nlMetadataHit(fxRead(t, r.File)); hit != "" {
			t.Errorf("%s carries %s", r.File, hit)
		}
	}
	if checked != 30 {
		t.Errorf("scanned %d file(s), want 30", checked)
	}
}

func TestNightlyCorpus_UsesOnlyTheNamedParties(t *testing.T) {
	buyers := 0
	for _, r := range nlRows {
		for _, name := range r.Fields["buyer_name"] {
			buyers++
			if !strings.EqualFold(name, "Adeyemi Trading Limited") && !strings.EqualFold(name, "Honeywell Group") {
				t.Errorf("%s names buyer %q", r.File, name)
			}
		}
		if r.Kind != "layout" {
			d := nlDocOf(r)
			if !strings.EqualFold(d.sup, "Adeyemi Trading Limited") && !strings.EqualFold(d.sup, "Honeywell Group") {
				t.Errorf("%s prints supplier %q", r.File, d.sup)
			}
			if strings.EqualFold(d.sup, d.buy) {
				t.Errorf("%s prints one party on both sides", r.File)
			}
		}
	}
	if buyers == 0 {
		t.Fatal("no buyer_name value in the key")
	}
}
