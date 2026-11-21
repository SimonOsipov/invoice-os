package erp

import (
	"encoding/json"
	"errors"
	"fmt"
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
