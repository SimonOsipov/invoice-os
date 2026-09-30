package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"reflect"
	"testing"
)

// jsonFields returns the keys of one JSON object in document order, and its values.
func jsonFields(t *testing.T, line []byte) ([]string, map[string]any) {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(line))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		t.Fatalf("log line is not a JSON object: %q", line)
	}
	var keys []string
	vals := map[string]any{}
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			t.Fatalf("decode key in %q: %v", line, err)
		}
		k := kt.(string)
		var v any
		if err := dec.Decode(&v); err != nil {
			t.Fatalf("decode value of %q in %q: %v", k, line, err)
		}
		keys = append(keys, k)
		vals[k] = v
	}
	return keys, vals
}

func TestNewLogger_NoDSNWritesTodaysLine(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	prev := os.Stdout
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = prev })

	ctx := WithTenantID(WithRequestID(context.Background(), "req-1"), "tnt-2")
	newLogger(Config{Service: "svc", Environment: "development", LogLevel: "info"}).InfoContext(ctx, "m", "k", "v")
	os.Stdout = prev
	_ = w.Close()
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read pipe: %v", err)
	}

	var ref bytes.Buffer
	slog.New(&contextHandler{Handler: slog.NewJSONHandler(&ref, &slog.HandlerOptions{Level: slog.LevelInfo})}).With(
		slog.String("service", "svc"),
		slog.String("environment", "development"),
	).InfoContext(ctx, "m", "k", "v")

	gotKeys, gotVals := jsonFields(t, bytes.TrimSpace(got))
	wantKeys, wantVals := jsonFields(t, bytes.TrimSpace(ref.Bytes()))
	if len(wantKeys) == 0 || wantVals["msg"] != "m" {
		t.Fatalf("reference line is empty or wrong: %q", ref.Bytes())
	}
	if !reflect.DeepEqual(gotKeys, wantKeys) {
		t.Errorf("keys = %v, want %v, in that order", gotKeys, wantKeys)
	}
	delete(gotVals, "time")
	delete(wantVals, "time")
	if !reflect.DeepEqual(gotVals, wantVals) {
		t.Errorf("values = %v, want %v", gotVals, wantVals)
	}
}
