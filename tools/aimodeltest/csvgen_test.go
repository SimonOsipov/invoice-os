// Runs the mapping check's ground-truth generator (csvgen.py) and measures its output
// against the acceptance criteria. D-1: AC-4's first clause is respecified -- a header may be
// claimed by two fields only for the two real multi-description exports, pinned as an exact set.
package aimodeltest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

var cgCANON = []string{
	"invoice_number", "issue_date", "buyer_tin", "buyer_name", "currency",
	"subtotal", "vat", "total", "line_description", "line_quantity", "line_unit_price",
}

type cgLayout struct {
	ID               string               `json:"id"`
	Category         string               `json:"category"`
	Columns          []string             `json:"columns"`
	HeaderRow        int                  `json:"header_row"`
	Rows             [][]string           `json:"rows"`
	Key              map[string][]*string `json:"key"`
	DateFormat       string               `json:"date_format"`
	NumberStyle      string               `json:"number_style"`
	DecimalSeparator string               `json:"decimal_separator"`
	AmbiguousDates   bool                 `json:"ambiguous_dates"`
	SingleLine       bool                 `json:"single_line"`
}

func cgStr(s string) *string { return &s }

// cgPython follows fleetgate_test.go's precedent: fail loudly on a missing interpreter,
// never skip -- a skip would let this whole file go green having run nothing.
func cgPython(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Fatalf("python3 is not on PATH, so csvgen.py cannot be run here")
	}
}

// cgRun runs csvgen.py into a fresh t.TempDir(), overriding DATA and any extraEnv
// ("KEY=VALUE") on top of the ambient environment.
func cgRun(t *testing.T, extraEnv ...string) string {
	t.Helper()
	cgPython(t)
	dir := t.TempDir()

	env := map[string]string{}
	for _, kv := range os.Environ() {
		if i := strings.IndexByte(kv, '='); i >= 0 {
			env[kv[:i]] = kv[i+1:]
		}
	}
	env["DATA"] = dir
	for _, kv := range extraEnv {
		if i := strings.IndexByte(kv, '='); i >= 0 {
			env[kv[:i]] = kv[i+1:]
		}
	}
	var envList []string
	for k, v := range env {
		envList = append(envList, k+"="+v)
	}

	cmd := exec.CommandContext(t.Context(), "python3", "csvgen.py")
	cmd.Env = envList
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("csvgen.py: %v\n%s", err, out)
	}
	return dir
}

// cgReadLayouts is pure so TestCSVGen_ZeroLayoutsIsAFatal can drive it directly; every
// other test routes through the cgLoad wrapper below.
func cgReadLayouts(dir string) ([]cgLayout, error) {
	b, err := os.ReadFile(filepath.Join(dir, "layouts.json"))
	if err != nil {
		return nil, err
	}
	var out []cgLayout
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("decode layouts.json: %w", err)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("layouts.json decoded to zero layouts")
	}
	return out, nil
}

func cgLoad(t *testing.T, dir string) []cgLayout {
	t.Helper()
	out, err := cgReadLayouts(dir)
	if err != nil {
		t.Fatalf("cgReadLayouts(%s): %v", dir, err)
	}
	return out
}

// cgDigestTree walks dir into a relative-path -> sha256 map, for byte-comparing two runs.
func cgDigestTree(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		rel, rerr := filepath.Rel(dir, path)
		if rerr != nil {
			return rerr
		}
		sum := sha256.Sum256(b)
		out[filepath.ToSlash(rel)] = hex.EncodeToString(sum[:])
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	return out
}

func cgSetDiff(a, b []string) []string {
	bs := map[string]bool{}
	for _, x := range b {
		bs[x] = true
	}
	var out []string
	for _, x := range a {
		if !bs[x] {
			out = append(out, x)
		}
	}
	return out
}

// cgColumnValues returns header's data-row values (rows after HeaderRow), or nil if
// header is not one of Columns.
func cgColumnValues(l cgLayout, header string) []string {
	if l.HeaderRow < 0 || l.HeaderRow > len(l.Rows) {
		return nil
	}
	idx := slices.Index(l.Columns, header)
	if idx < 0 {
		return nil
	}
	var out []string
	for _, row := range l.Rows[l.HeaderRow:] {
		if idx < len(row) {
			out = append(out, row[idx])
		}
	}
	return out
}

// cgKeyViolations reports "id/field/header" for every keyed header absent from the
// layout's own columns.
func cgKeyViolations(l cgLayout) []string {
	set := map[string]bool{}
	for _, h := range l.Columns {
		set[h] = true
	}
	var out []string
	for _, f := range cgCANON {
		for _, hp := range l.Key[f] {
			if hp == nil {
				continue
			}
			if !set[*hp] {
				out = append(out, l.ID+"/"+f+"/"+*hp)
			}
		}
	}
	return out
}

// AC-1. Folds in the default-seed proof: default == SEED=20260914, and SEED=1 differs --
// otherwise a generator ignoring SEED would still pass.
func TestCSVGen_ProducesFortyEightLayouts(t *testing.T) {
	dir := cgRun(t)
	layouts := cgLoad(t, dir)
	if len(layouts) != 48 {
		t.Fatalf("got %d layouts, want 48", len(layouts))
	}

	entries, err := os.ReadDir(filepath.Join(dir, "csv"))
	if err != nil {
		t.Fatalf("read csv dir: %v", err)
	}
	if len(entries) != 48 {
		t.Errorf("csv/ has %d entries, want 48", len(entries))
	}
	for _, l := range layouts {
		if _, err := os.Stat(filepath.Join(dir, "csv", l.ID+".csv")); err != nil {
			t.Errorf("layout %s: no matching csv file: %v", l.ID, err)
		}
	}

	defaultDigest := cgDigestTree(t, dir)
	explicitDigest := cgDigestTree(t, cgRun(t, "SEED=20260914"))
	if !maps.Equal(defaultDigest, explicitDigest) {
		t.Errorf("default SEED and SEED=20260914 produced different output -- the default seed is not 20260914")
	}

	otherDigest := cgDigestTree(t, cgRun(t, "SEED=1"))
	if maps.Equal(defaultDigest, otherDigest) {
		t.Errorf("SEED=1 produced output identical to the default seed -- SEED is not being read")
	}
}

// AC-1, second row: cgReadLayouts must refuse an empty result, not report a clean 0-of-0.
func TestCSVGen_ZeroLayoutsIsAFatal(t *testing.T) {
	cases := []struct {
		name    string
		setup   func(dir string) error
		wantErr string
	}{
		{name: "missing layouts.json", setup: func(string) error { return nil }, wantErr: "no such file"},
		{
			name:    "empty array",
			setup:   func(dir string) error { return os.WriteFile(filepath.Join(dir, "layouts.json"), []byte("[]"), 0o644) },
			wantErr: "zero layouts",
		},
		{
			name: "not an array",
			setup: func(dir string) error {
				return os.WriteFile(filepath.Join(dir, "layouts.json"), []byte(`{"id":"x"}`), 0o644)
			},
			wantErr: "decode",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := tc.setup(dir); err != nil {
				t.Fatalf("setup: %v", err)
			}
			_, err := cgReadLayouts(dir)
			if err == nil {
				t.Fatalf("cgReadLayouts(%s) returned a nil error, want one naming %q", dir, tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("cgReadLayouts error = %q, want it to name %q", err.Error(), tc.wantErr)
			}
		})
	}

	// Positive leg: a real run must still decode cleanly through the same function.
	out, err := cgReadLayouts(cgRun(t))
	if err != nil {
		t.Fatalf("cgReadLayouts on a real run: %v", err)
	}
	if len(out) != 48 {
		t.Fatalf("cgReadLayouts on a real run returned %d layouts, want 48", len(out))
	}
}

// AC-2. Three separate equalities, plus a fourth-category control.
func TestCSVGen_TheCategorySplitIsThirteenTwentySevenEight(t *testing.T) {
	layouts := cgLoad(t, cgRun(t))

	counts := map[string]int{}
	for _, l := range layouts {
		counts[l.Category]++
	}
	if counts["software"] != 13 {
		t.Errorf("software = %d, want 13", counts["software"])
	}
	if counts["composed"] != 27 {
		t.Errorf("composed = %d, want 27", counts["composed"])
	}
	if counts["structure"] != 8 {
		t.Errorf("structure = %d, want 8", counts["structure"])
	}
	if len(counts) != 3 {
		t.Errorf("got %d distinct categories, want 3: %v", len(counts), counts)
	}
}

// AC-3. Every keyed header names one of its own layout's columns.
func TestCSVGen_EveryKeyNamesItsOwnHeader(t *testing.T) {
	layouts := cgLoad(t, cgRun(t))

	var checked int
	for _, l := range layouts {
		for _, f := range cgCANON {
			for _, hp := range l.Key[f] {
				if hp != nil {
					checked++
				}
			}
		}
		if v := cgKeyViolations(l); len(v) > 0 {
			t.Errorf("layout %s: header not in its own columns: %v", l.ID, v)
		}
	}
	if checked < 300 {
		t.Fatalf("checked only %d non-nil header entries, want >= 300 -- the predicate may not be reaching the data", checked)
	}

	bad := cgLayout{
		ID:      "control_bad",
		Columns: []string{"Invoice No", "Date"},
		Key:     map[string][]*string{"invoice_number": {cgStr("Not A Column")}},
	}
	if v := cgKeyViolations(bad); len(v) == 0 {
		t.Fatalf("planted violation not detected -- the predicate always reports clean")
	}
}

// AC-4, respecified per D-1: a header claimed by two fields is still banned; a field
// claiming two headers is allowed only for the exact pinned pair below.
func TestCSVGen_NoHeaderIsClaimedTwice(t *testing.T) {
	layouts := cgLoad(t, cgRun(t))

	var multi []string
	for _, l := range layouts {
		headerToFields := map[string][]string{}
		for _, f := range cgCANON {
			var nils, nonNils int
			for _, hp := range l.Key[f] {
				if hp == nil {
					nils++
					continue
				}
				nonNils++
				headerToFields[*hp] = append(headerToFields[*hp], f)
			}
			if nils > 0 && nonNils > 0 {
				t.Errorf("layout %s field %s: a null mixes with a header in the same key list: %v", l.ID, f, l.Key[f])
			}
			if nonNils > 1 {
				multi = append(multi, l.ID+"/"+f)
			}
		}
		for h, fields := range headerToFields {
			if len(fields) > 1 {
				t.Errorf("layout %s: header %q claimed by fields %v", l.ID, h, fields)
			}
		}
	}

	slices.Sort(multi)
	want := []string{"sw_quickbooks_import/line_description", "sw_zoho/line_description"}
	if !slices.Equal(multi, want) {
		t.Errorf("multi-header (layout/field) set = %v, want exactly %v", multi, want)
	}
}

// AC-5, D-6: four measured oracles, not type checks -- Go's decoder already enforces the
// AC's literal reading, which would leave the criterion near-vacuous.
func TestCSVGen_EveryLayoutDeclaresItsFormatFacts(t *testing.T) {
	layouts := cgLoad(t, cgRun(t))

	t.Run("header_row points at the real header row", func(t *testing.T) {
		var gt1 int
		for _, l := range layouts {
			if l.HeaderRow < 1 || l.HeaderRow > len(l.Rows) {
				t.Errorf("layout %s: header_row %d out of range (rows=%d)", l.ID, l.HeaderRow, len(l.Rows))
				continue
			}
			if !slices.Equal(l.Rows[l.HeaderRow-1], l.Columns) {
				t.Errorf("layout %s: row at header_row (%d) does not equal columns", l.ID, l.HeaderRow)
			}
			if l.HeaderRow > 1 {
				gt1++
			}
		}
		if gt1 < 8 {
			t.Fatalf("only %d layouts have header_row > 1, want >= 8 -- the title-row layouts were not reached", gt1)
		}
	})

	t.Run("date_format matches every issue_date cell", func(t *testing.T) {
		dateRe := map[string]*regexp.Regexp{
			"YYYY-MM-DD":  regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`),
			"DD/MM/YYYY":  regexp.MustCompile(`^\d{2}/\d{2}/\d{4}$`),
			"MM/DD/YYYY":  regexp.MustCompile(`^\d{2}/\d{2}/\d{4}$`),
			"DD-MMM-YYYY": regexp.MustCompile(`^\d{2}-[A-Za-z]{3}-\d{4}$`),
		}
		var checked int
		for _, l := range layouts {
			re, ok := dateRe[l.DateFormat]
			if !ok {
				t.Errorf("layout %s: unrecognised date_format %q", l.ID, l.DateFormat)
				continue
			}
			for _, hp := range l.Key["issue_date"] {
				if hp == nil {
					continue
				}
				for _, v := range cgColumnValues(l, *hp) {
					checked++
					head, _, _ := strings.Cut(v, " ") // sw_pos_receipts carries a trailing time
					if !re.MatchString(head) {
						t.Errorf("layout %s: issue_date value %q does not match declared date_format %q", l.ID, v, l.DateFormat)
					}
				}
			}
		}
		if checked < 400 {
			t.Fatalf("checked only %d issue_date cells, want >= 400", checked)
		}
	})

	t.Run("decimal_separator matches every money cell", func(t *testing.T) {
		moneyFields := []string{"subtotal", "vat", "total", "line_unit_price"}
		var checked int
		for _, l := range layouts {
			for _, f := range moneyFields {
				for _, hp := range l.Key[f] {
					if hp == nil {
						continue
					}
					for _, v := range cgColumnValues(l, *hp) {
						sep := ""
						for i := len(v) - 1; i >= 0; i-- {
							if v[i] == '.' || v[i] == ',' {
								sep = string(v[i])
								break
							}
						}
						if sep == "" {
							t.Errorf("layout %s field %s: value %q has no decimal separator", l.ID, f, v)
							continue
						}
						checked++
						if sep != l.DecimalSeparator {
							t.Errorf("layout %s field %s: value %q's last separator is %q, want declared %q", l.ID, f, v, sep, l.DecimalSeparator)
						}
					}
				}
			}
		}
		if checked < 1000 {
			t.Fatalf("checked only %d money cells, want >= 1000", checked)
		}
	})

	t.Run("single_line implies no repeated invoice number", func(t *testing.T) {
		var trueChecked, falseWithRepeat int
		for _, l := range layouts {
			var invHeader *string
			for _, hp := range l.Key["invoice_number"] {
				if hp != nil {
					invHeader = hp
					break
				}
			}
			if invHeader == nil {
				t.Errorf("layout %s: no invoice_number header keyed", l.ID)
				continue
			}
			seen := map[string]bool{}
			repeats := false
			for _, v := range cgColumnValues(l, *invHeader) {
				if seen[v] {
					repeats = true
				}
				seen[v] = true
			}
			if l.SingleLine {
				trueChecked++
				if repeats {
					t.Errorf("layout %s: single_line is true but invoice_number repeats", l.ID)
				}
			} else if repeats {
				falseWithRepeat++
			}
		}
		if trueChecked < 6 {
			t.Fatalf("checked only %d single_line=true layouts, want >= 6", trueChecked)
		}
		if falseWithRepeat < 1 {
			t.Fatalf("no single_line=false layout repeats an invoice number -- the control leg found nothing")
		}
	})
}

// AC-6, D-2 (no row existed before this): every buyer name is synthetic, and the source
// reads no file.
func TestCSVGen_EveryValueIsSyntheticAndNoFileIsRead(t *testing.T) {
	dir := cgRun(t)
	src, err := os.ReadFile("csvgen.py")
	if err != nil {
		t.Fatalf("read csvgen.py: %v", err)
	}
	text := string(src)

	t.Run("buyer_name values come from the declared BUYERS list", func(t *testing.T) {
		m := regexp.MustCompile(`(?s)BUYERS = \[(.*?)\]`).FindStringSubmatch(text)
		if m == nil {
			t.Fatalf("parsed zero BUYERS from csvgen.py -- the parser did not reach the literal")
		}
		buyers := map[string]bool{}
		for _, qm := range regexp.MustCompile(`"([^"]*)"`).FindAllStringSubmatch(m[1], -1) {
			buyers[qm[1]] = true
		}
		if len(buyers) == 0 {
			t.Fatalf("BUYERS literal parsed to zero names")
		}

		violations := func(l cgLayout) []string {
			var out []string
			for _, hp := range l.Key["buyer_name"] {
				if hp == nil {
					continue
				}
				for _, v := range cgColumnValues(l, *hp) {
					if !buyers[v] {
						out = append(out, v)
					}
				}
			}
			return out
		}

		layouts := cgLoad(t, dir)
		var layoutsChecked int
		distinct := map[string]bool{}
		for _, l := range layouts {
			if v := violations(l); len(v) > 0 {
				t.Errorf("layout %s: buyer_name values not in BUYERS: %v", l.ID, v)
			}
			for _, hp := range l.Key["buyer_name"] {
				if hp == nil {
					continue
				}
				layoutsChecked++
				for _, v := range cgColumnValues(l, *hp) {
					distinct[v] = true
				}
			}
		}
		if len(distinct) < 10 {
			t.Fatalf("only %d distinct buyer names seen, want >= 10", len(distinct))
		}
		if layoutsChecked < 30 {
			t.Fatalf("only %d layouts key buyer_name, want >= 30", layoutsChecked)
		}

		control := cgLayout{
			ID:        "control_bad_buyer",
			Columns:   []string{"Customer"},
			HeaderRow: 1,
			Rows:      [][]string{{"Customer"}, {"Acme Ltd"}},
			Key:       map[string][]*string{"buyer_name": {cgStr("Customer")}},
		}
		if v := violations(control); len(v) == 0 {
			t.Fatalf("planted buyer violation %q not detected -- the predicate always reports clean", "Acme Ltd")
		}
	})

	t.Run("the source reads no file", func(t *testing.T) {
		calls := cgOpenCallArgs(text)
		if len(calls) != 2 {
			t.Fatalf("csvgen.py has %d open( call(s), want exactly 2", len(calls))
		}
		for _, args := range calls {
			modes := cgOpenModes(args)
			if len(modes) != 1 || modes[0] != "w" {
				t.Errorf("open( call args %q: modes found %v, want exactly [\"w\"]", args, modes)
			}
		}

		// Control: a scanner that finds nothing is indistinguishable from a clean file.
		if got := cgOpenCallArgs(`f = open(p)` + "\n" + `g = open(p, "rb")` + "\n"); len(got) != 2 {
			t.Fatalf("control fixture: open(-scan found %d calls, want 2", len(got))
		}

		m := regexp.MustCompile(`(?m)^import (.+)$`).FindStringSubmatch(text)
		if m == nil {
			t.Fatalf("csvgen.py has no top-level import line")
		}
		allowed := map[string]bool{"csv": true, "datetime": true, "io": true, "json": true, "os": true, "random": true, "re": true}
		for _, mod := range strings.Split(m[1], ",") {
			name, _, _ := strings.Cut(strings.TrimSpace(mod), " as ")
			if !allowed[name] {
				t.Errorf("csvgen.py imports non-allowlisted module %q", name)
			}
		}
	})
}

func cgIsIdentChar(b byte) bool {
	return b == '_' || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

// cgOpenCallArgs finds each bare "open(" call and returns its argument text, balancing
// nested parens so a call like os.path.join(...) inside doesn't truncate the match.
func cgOpenCallArgs(src string) []string {
	var out []string
	for i := 0; i < len(src); i++ {
		if !strings.HasPrefix(src[i:], "open(") {
			continue
		}
		if i > 0 && cgIsIdentChar(src[i-1]) {
			continue
		}
		start := i + len("open(")
		depth := 1
		j := start
		for ; j < len(src) && depth > 0; j++ {
			switch src[j] {
			case '(':
				depth++
			case ')':
				depth--
			}
		}
		out = append(out, src[start:j-1])
		i = j - 1
	}
	return out
}

var cgQuotedStringRe = regexp.MustCompile(`"([^"]*)"`)

func cgOpenModes(args string) []string {
	var modes []string
	for _, m := range cgQuotedStringRe.FindAllStringSubmatch(args, -1) {
		switch m[1] {
		case "w", "r", "a", "rb", "wb", "r+", "w+", "a+":
			modes = append(modes, m[1])
		}
	}
	return modes
}

// AC-7. Two independent runs at the same seed must write the same 49 files with the same bytes.
func TestCSVGen_IsDeterministicAtOneSeed(t *testing.T) {
	digestA := cgDigestTree(t, cgRun(t))
	digestB := cgDigestTree(t, cgRun(t))

	keysA := slices.Sorted(maps.Keys(digestA))
	keysB := slices.Sorted(maps.Keys(digestB))
	if !slices.Equal(keysA, keysB) {
		t.Fatalf("run A and run B wrote different file sets\nA-only: %v\nB-only: %v", cgSetDiff(keysA, keysB), cgSetDiff(keysB, keysA))
	}
	if len(digestA) != 49 {
		t.Fatalf("a run wrote %d files, want 49 (48 CSVs + layouts.json)", len(digestA))
	}
	for _, k := range keysA {
		if digestA[k] != digestB[k] {
			t.Errorf("first divergent file %s: run A digest %s, run B digest %s", k, digestA[k], digestB[k])
			break
		}
	}
}
