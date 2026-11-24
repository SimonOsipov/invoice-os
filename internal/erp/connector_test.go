package erp

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"testing"
	"time"
)

type stub struct {
	Fake
	name string
}

func (s *stub) System() string { return s.name }

var _ Connector = (*stub)(nil)

func TestRateLimitError_SurvivesWrapping(t *testing.T) {
	err := fmt.Errorf("pull: %w", &RateLimitError{RetryAfter: 7 * time.Second})
	var rl *RateLimitError
	if !errors.As(err, &rl) || rl.RetryAfter != 7*time.Second {
		t.Fatalf("errors.As = %v, %v", rl, err)
	}
	if errors.As(errors.New("plain"), &rl) {
		t.Fatal("a plain error matched")
	}
}

func TestNewRegistry_RefusesEmptyAndDuplicateSystems(t *testing.T) {
	if _, err := NewRegistry(&stub{name: ""}); err == nil {
		t.Error("empty system accepted")
	}
	if _, err := NewRegistry(&stub{name: "a"}, &stub{name: "a"}); err == nil {
		t.Error("duplicate system accepted")
	}
	r, err := NewRegistry(&stub{name: "b"}, &stub{name: "a"})
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(r.Systems()); got != "[a b]" {
		t.Errorf("Systems = %s", got)
	}
}

func TestRegistry_SystemsOfAnEmptyRegistryIsEmptyNotNil(t *testing.T) {
	r, err := NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	s := r.Systems()
	if s == nil || len(s) != 0 {
		t.Fatalf("Systems = %#v", s)
	}
	if b, _ := json.Marshal(s); string(b) != "[]" {
		t.Errorf("JSON = %s", b)
	}
}

// Nothing calls most of these methods through the interface until later stories, so a
// removed or retyped one would otherwise go unseen.
func TestConnector_DeclaresItsFourteenMethods(t *testing.T) {
	typ := reflect.TypeOf((*Connector)(nil)).Elem()
	var got []string
	for i := range typ.NumMethod() {
		got = append(got, typ.Method(i).Name)
	}
	want := []string{"Authorize", "Capabilities", "DefaultMapping", "EnsureSubscriptions", "Exchange", "Fetch", "FetchParty", "ListChanged", "Refresh", "Revoke", "System", "Validate", "VerifyWebhook", "WriteBack"}
	if !slices.Equal(got, want) {
		t.Fatalf("Connector methods = %v, want %v", got, want)
	}
	lc, _ := typ.MethodByName("ListChanged")
	if lc.Type.NumIn() != 4 || lc.Type.In(3) != reflect.TypeOf([]byte(nil)) {
		t.Errorf("ListChanged cursor parameter = %v, want an opaque []byte", lc.Type)
	}
	vw, _ := typ.MethodByName("VerifyWebhook")
	if vw.Type.NumOut() != 2 || vw.Type.Out(0) != reflect.TypeOf([]Hint(nil)) {
		t.Errorf("VerifyWebhook results = %v, want ([]Hint, error)", vw.Type)
	}
}

func TestConnector_DocRefAndCapabilitiesFields(t *testing.T) {
	fields := func(v any) map[string]string {
		out := map[string]string{}
		typ := reflect.TypeOf(v)
		for f := range typ.Fields() {
			out[f.Name] = f.Type.String()
		}
		return out
	}
	for _, c := range []struct {
		name string
		got  map[string]string
		want map[string]string
	}{
		{"DocRef", fields(DocRef{}), map[string]string{"SourceDocID": "string", "SourceRevision": "string", "Scope": "string", "Deleted": "bool"}},
		{"Capabilities", fields(Capabilities{}), map[string]string{"Webhooks": "bool", "Note": "bool", "Attachment": "bool", "CustomFieldSlots": "int", "RequiresRevisionOnWrite": "bool"}},
	} {
		if len(c.got) == 0 || !reflect.DeepEqual(c.got, c.want) {
			t.Errorf("%s fields = %v, want %v", c.name, c.got, c.want)
		}
	}
}
