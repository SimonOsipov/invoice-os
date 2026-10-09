package codelist

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "nrs", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func serve(t *testing.T, status int, body []byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func listByName(t *testing.T, name string) List {
	t.Helper()
	for _, l := range Lists {
		if l.Name == name {
			return l
		}
	}
	t.Fatalf("no list %q", name)
	return List{}
}

func TestFetch_EveryListExtractsItsCodes(t *testing.T) {
	if len(Lists) != 11 {
		t.Fatalf("Lists has %d entries, want 11", len(Lists))
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := os.ReadFile(filepath.Join("testdata", "nrs", strings.TrimPrefix(r.URL.Path, "/")+".json"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(b)
	}))
	defer srv.Close()
	wantCodes := map[string][]string{
		"countries":              {"AF", "AX", "AL"},
		"currencies":             {"USD", "CAD", "EUR"},
		"hs-codes":               {"0101.21", "0101.29", "0101.30"},
		"invoice-quantity-codes": {"10", "11", "13"},
		"invoice-types":          {"380", "381", "384"},
		"lgas":                   {"NG-AB-ANO", "NG-AB-ASO", "NG-AB-ARO"},
		"payment-means":          {"10", "20", "30"},
		"services-codes":         {"0111", "0112", "0113"},
		"states":                 {"NG-AB", "NG-AD", "NG-AK"},
		"tax-categories":         {"STANDARD_GST", "REDUCED_GST", "ZERO_GST"},
		"vat-exemptions":         {"2915.3100", "2916.3900", "0204.3000", "8708.3900 "},
	}
	for _, l := range Lists {
		var fx struct {
			Data []map[string]any `json:"data"`
		}
		if err := json.Unmarshal(fixture(t, l.Name), &fx); err != nil {
			t.Fatal(err)
		}
		want := map[string]bool{}
		for _, e := range fx.Data {
			want[e[l.CodeKey].(string)] = true
		}
		got, n, err := fetch(context.Background(), nil, srv.URL, l)
		if err != nil {
			t.Fatalf("%s: %v", l.Name, err)
		}
		if n != len(fx.Data) || len(got) != len(want) {
			t.Errorf("%s: count %d codes %d, want %d entries %d codes", l.Name, n, len(got), len(fx.Data), len(want))
		}
		for c := range want {
			if _, ok := got[c]; !ok {
				t.Errorf("%s: missing code %q", l.Name, c)
			}
		}
		if len(wantCodes[l.Name]) != len(got) {
			t.Errorf("%s: %d codes, want %d", l.Name, len(got), len(wantCodes[l.Name]))
		}
		for _, c := range wantCodes[l.Name] {
			if _, ok := got[c]; !ok {
				t.Errorf("%s: want code %q", l.Name, c)
			}
		}
	}
}

func TestFetch_GroupsDuplicateCodesVerbatim(t *testing.T) {
	body := fixture(t, "vat-exemptions")
	srv := serve(t, 200, body)
	got, n, err := fetch(context.Background(), nil, srv.URL, listByName(t, "vat-exemptions"))
	if err != nil {
		t.Fatal(err)
	}
	var fx struct {
		Data []json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &fx); err != nil {
		t.Fatal(err)
	}
	if n != len(fx.Data) {
		t.Errorf("count %d, want %d", n, len(fx.Data))
	}
	dup := got["0204.3000"]
	if len(dup) != 2 {
		t.Fatalf("0204.3000 holds %d entries, want 2", len(dup))
	}
	if !strings.Contains(string(dup[0]), "Boneless") || !strings.Contains(string(dup[1]), "Carcasses") {
		t.Errorf("entries out of fixture order: %s | %s", dup[0], dup[1])
	}
	if _, ok := got["8708.3900 "]; !ok {
		t.Error(`key "8708.3900 " absent`)
	}
	if _, ok := got["8708.3900"]; ok {
		t.Error(`key "8708.3900" present`)
	}
}

func TestFetch_NonOKStatusFails(t *testing.T) {
	cases := []struct {
		status int
		body   string
	}{
		{500, `oops`},
		{404, `{"code":404,"data":null,"message":"not found"}`},
	}
	for _, c := range cases {
		srv := serve(t, c.status, []byte(c.body))
		_, _, err := fetch(context.Background(), nil, srv.URL, listByName(t, "states"))
		if err == nil || !strings.Contains(err.Error(), "states") || !strings.Contains(err.Error(), "status "+strconv.Itoa(c.status)) {
			t.Errorf("status %d: err = %v", c.status, err)
		}
	}
}

func TestFetch_EmptyListFails(t *testing.T) {
	for _, body := range []string{`{"code":200,"data":[]}`, `{"code":200,"data":null}`, `{"code":200}`} {
		srv := serve(t, 200, []byte(body))
		_, _, err := fetch(context.Background(), nil, srv.URL, listByName(t, "states"))
		if !errors.Is(err, ErrEmptyList) || !strings.Contains(err.Error(), "states") {
			t.Errorf("%s: err = %v", body, err)
		}
	}
}

func TestFetch_MalformedEntryFails(t *testing.T) {
	good := `{"code":"NG-AB"}`
	bodies := []string{
		`not json`,
		`{"code":200,"data":["x",` + good + `]}`,
		`{"code":200,"data":[null,` + good + `]}`,
		`{"code":200,"data":[{"name":"x"},` + good + `]}`,
		`{"code":200,"data":[{"code":566},` + good + `]}`,
		`{"code":200,"data":[{"code":""},` + good + `]}`,
		`{"code":200,"data":[{"code":null},` + good + `]}`,
	}
	for _, body := range bodies {
		srv := serve(t, 200, []byte(body))
		_, _, err := fetch(context.Background(), nil, srv.URL, listByName(t, "states"))
		if err == nil || !strings.Contains(err.Error(), "states") || errors.Is(err, ErrEmptyList) {
			t.Errorf("%s: err = %v", body, err)
		}
	}
}

func TestFetch_CancelledContextFails(t *testing.T) {
	started := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-started
		cancel()
	}()
	done := make(chan error, 1)
	go func() {
		_, _, err := fetch(ctx, nil, srv.URL, listByName(t, "states"))
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "states") {
			t.Errorf("err = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("fetch did not return after cancel")
	}
}

func TestFetch_RedirectIsNotFollowed(t *testing.T) {
	var other atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/other" {
			other.Add(1)
			_, _ = w.Write(fixture(t, "states"))
			return
		}
		w.Header().Set("Location", "/other")
		w.WriteHeader(http.StatusFound)
	}))
	defer srv.Close()
	_, _, err := fetch(context.Background(), nil, srv.URL, listByName(t, "states"))
	if err == nil || !strings.Contains(err.Error(), "states") || !strings.Contains(err.Error(), "302") {
		t.Errorf("err = %v", err)
	}
	if n := other.Load(); n != 0 {
		t.Errorf("/other hit %d times", n)
	}
}

func TestFetch_OversizedBodyFails(t *testing.T) {
	if maxBody != 16<<20 {
		t.Fatalf("maxBody = %d, want 16 MiB", maxBody)
	}
	base := fixture(t, "states")
	exact := append(append([]byte{}, base...), bytes.Repeat([]byte(" "), maxBody-len(base))...)
	srv := serve(t, 200, exact)
	if _, _, err := fetch(context.Background(), nil, srv.URL, listByName(t, "states")); err != nil {
		t.Fatalf("maxBody bytes: %v", err)
	}
	srv2 := serve(t, 200, append(exact, ' '))
	_, _, err := fetch(context.Background(), nil, srv2.URL, listByName(t, "states"))
	if err == nil || !strings.Contains(err.Error(), "states") {
		t.Errorf("maxBody+1: err = %v", err)
	}
}
