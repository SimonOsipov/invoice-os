// reconcile_doubt_internal_test.go: doubtfulFields itself. Package extraction -- the list is
// unexported and the behavioural walk over it cannot see an entry that names nothing.
package extraction

import (
	"slices"
	"testing"
)

// The behavioural partition (TestReconcile_TheDoubtCoversExactlyFourOfTheTenHeaderFields)
// quantifies over HeaderFields, so an entry naming no header field is invisible there: it
// widens nothing and reds nothing, and the next reader takes it for a covered field. This is
// the set-equality pin the list did not have.
func TestReconcile_TheDoubtfulFieldsAreFourRealHeaderFields(t *testing.T) {
	// Compared as a set: uncorroborated reads it through slices.Contains, so the order carries
	// no meaning and pinning it would red on an edit that changes nothing.
	want := []string{"buyer_name", "buyer_tin", "total", "vat"}
	got := slices.Clone(doubtfulFields)
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("doubtfulFields = %q, want the four %q", doubtfulFields, want)
	}
	for i, f := range doubtfulFields {
		if !slices.Contains(HeaderFields, f) {
			t.Errorf("doubtfulFields[%d] = %q, which HeaderFields does not name; uncorroborated reads Candidate.Field, so this entry can never match", i, f)
		}
		if slices.Contains(doubtfulFields[i+1:], f) {
			t.Errorf("doubtfulFields names %q twice", f)
		}
	}
}
