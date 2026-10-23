package systemhealth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type promReq struct {
	method, path, auth string
	form               map[string]string
}

type promFake struct {
	mu   sync.Mutex
	reqs []promReq
}

func newPromFake(t *testing.T, code int, body string) (*promFake, *Google) {
	t.Helper()
	f := &promFake{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		form := map[string]string{}
		for k := range r.PostForm {
			form[k] = r.PostForm.Get(k)
		}
		f.mu.Lock()
		f.reqs = append(f.reqs, promReq{r.Method, r.URL.Path, r.Header.Get("Authorization"), form})
		f.mu.Unlock()
		w.WriteHeader(code)
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return f, &Google{Monitoring: srv.URL, Run: srv.URL, Client: srv.Client()}
}

const emptyMatrix = `{"status":"success","data":{"resultType":"matrix","result":[]}}`

func TestWindow_EndsFourMinutesBeforeNow(t *testing.T) {
	now := time.Date(2026, 10, 23, 18, 47, 31, 600_000_000, time.UTC)
	want := time.Date(2026, 10, 23, 18, 43, 31, 0, time.UTC)
	for k, r := range Ranges {
		start, end := Window(r, now)
		if !end.Equal(want) {
			t.Errorf("%s: end = %v, want %v", k, end, want)
		}
		if end.Sub(start) != r.Span {
			t.Errorf("%s: span = %v, want %v", k, end.Sub(start), r.Span)
		}
	}
}

func TestQueryRange_EachRangeSendsItsWindowAndStep(t *testing.T) {
	if _, ok := Ranges["2h"]; ok {
		t.Fatal("2h must not be a range")
	}
	now := time.Date(2026, 10, 23, 18, 47, 31, 0, time.UTC)
	for k, r := range Ranges {
		f, g := newPromFake(t, 200, emptyMatrix)
		start, end := Window(r, now)
		for _, p := range Panels {
			if _, err := g.queryRange(context.Background(), "tok", "p1", p.Query(r, ""), start, end, r.Step); err != nil {
				t.Fatalf("%s %s: %v", k, p.Key, err)
			}
		}
		if len(f.reqs) != len(Panels) {
			t.Fatalf("%s: %d requests, want %d", k, len(f.reqs), len(Panels))
		}
		win := fmt.Sprintf("[%ds]", int(max(r.Step, 300*time.Second).Seconds()))
		for _, q := range f.reqs {
			s, _ := strconv.ParseInt(q.form["start"], 10, 64)
			e, _ := strconv.ParseInt(q.form["end"], 10, 64)
			if time.Duration(e-s)*time.Second != r.Span {
				t.Errorf("%s: end-start = %ds, want %v", k, e-s, r.Span)
			}
			if q.form["step"] != strconv.Itoa(int(r.Step.Seconds())) {
				t.Errorf("%s: step = %q", k, q.form["step"])
			}
			if !strings.Contains(q.form["query"], win) || strings.Contains(q.form["query"], "{W}") {
				t.Errorf("%s: query lacks window %s: %s", k, win, q.form["query"])
			}
		}
	}
}

func TestQueryRange_PostsTheFormWithTheBearer(t *testing.T) {
	f, g := newPromFake(t, 200, emptyMatrix)
	start := time.Unix(1792781040, 0)
	end := start.Add(time.Hour)
	if _, err := g.queryRange(context.Background(), "tok", "p1", "up", start, end, 60*time.Second); err != nil {
		t.Fatal(err)
	}
	q := f.reqs[0]
	if q.method != http.MethodPost || q.path != "/v1/projects/p1/location/global/prometheus/api/v1/query_range" {
		t.Errorf("got %s %s", q.method, q.path)
	}
	if q.auth != "Bearer tok" {
		t.Errorf("auth = %q", q.auth)
	}
	want := map[string]string{"query": "up", "start": "1792781040", "end": "1792784640", "step": "60"}
	for k, v := range want {
		if q.form[k] != v {
			t.Errorf("%s = %q, want %q", k, q.form[k], v)
		}
	}
}

func TestPanels_TheServiceFilterNarrowsOnlyCloudRunPanels(t *testing.T) {
	r := Ranges["1h"]
	for _, p := range Panels {
		withSvc, without := p.Query(r, "app"), p.Query(r, "")
		runPanel := strings.Contains(withSvc, "cloud_run_revision")
		sqlPanel := strings.Contains(withSvc, "cloudsql_database")
		if runPanel == sqlPanel {
			t.Errorf("%s: must hit exactly one resource", p.Key)
		}
		if runPanel != p.CloudRun {
			t.Errorf("%s: CloudRun flag disagrees with the query", p.Key)
		}
		if runPanel && !strings.Contains(withSvc, `service_name="app"`) {
			t.Errorf("%s: no service filter: %s", p.Key, withSvc)
		}
		if sqlPanel && strings.Contains(withSvc, `service_name=`) {
			t.Errorf("%s: SQL panel carries a service filter", p.Key)
		}
		if strings.Contains(without, `service_name=`) {
			t.Errorf("%s: unfiltered query carries service_name=", p.Key)
		}
	}
}

func TestQueryRange_ParsesAMatrixAndDropsNonFinitePoints(t *testing.T) {
	body := `{"status":"success","data":{"resultType":"matrix","result":[
	 {"metric":{"service_name":"b"},"values":[[100,"1.5"],[160,"NaN"],[220,"+Inf"],[280,"0"]]},
	 {"metric":{"service_name":"a"},"values":[[100,"-Inf"],[160,"2"]]},
	 {"metric":{},"values":[[100.5,"7"]]}]}}`
	_, g := newPromFake(t, 200, body)
	got, err := g.queryRange(context.Background(), "tok", "p1", "q", time.Unix(0, 0), time.Unix(300, 0), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(got)
	want := `[{"name":"a","points":[[160,2]]},{"name":"b","points":[[100,1.5],[280,0]]},{"name":"database","points":[[100.5,7]]}]`
	if string(b) != want {
		t.Errorf("got %s\nwant %s", b, want)
	}
}

func TestQueryRange_RefusesABadAnswer(t *testing.T) {
	cases := map[string]struct {
		code int
		body string
	}{
		"500":          {500, emptyMatrix},
		"status error": {200, `{"status":"error"}`},
		"html":         {200, `<html>`},
		"vector":       {200, `{"status":"success","data":{"resultType":"vector","result":[]}}`},
		"over cap":     {200, strings.Repeat(" ", promBodyCap+1)},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, g := newPromFake(t, c.code, c.body)
			if _, err := g.queryRange(context.Background(), "tok", "p1", "q", time.Unix(0, 0), time.Unix(60, 0), time.Minute); err == nil {
				t.Fatal("want an error")
			}
		})
	}
}

func TestQueryRange_AnEmptyMatrixIsAnEmptySeriesList(t *testing.T) {
	_, g := newPromFake(t, 200, emptyMatrix)
	got, err := g.queryRange(context.Background(), "tok", "p1", "q", time.Unix(0, 0), time.Unix(60, 0), time.Minute)
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("got %#v, %v", got, err)
	}
	if b, _ := json.Marshal(got); string(b) != "[]" {
		t.Errorf("marshalled %s", b)
	}
}

func TestErrorPct_KeepsAZeroPoint(t *testing.T) {
	body := `{"status":"success","data":{"resultType":"matrix","result":[
	 {"metric":{"service_name":"app"},"values":[[100,"0"],[160,"0"]]},
	 {"metric":{"service_name":"gateway"},"values":[[100,"3.4"],[160,"NaN"]]}]}}`
	f, g := newPromFake(t, 200, body)
	r := Ranges["1h"]
	var q string
	for _, p := range Panels {
		if p.Key == "error_pct" {
			q = p.Query(r, "")
		}
	}
	got, err := g.queryRange(context.Background(), "tok", "p1", q, time.Unix(0, 0), time.Unix(3600, 0), r.Step)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Name != "app" || len(got[0].Points) != 2 || got[0].Points[0][1] != 0 || got[0].Points[1][1] != 0 {
		t.Errorf("app = %+v", got)
	}
	if len(got[1].Points) != 1 || got[1].Points[0][1] != 3.4 {
		t.Errorf("gateway = %+v", got[1])
	}
	if !strings.Contains(f.reqs[0].form["query"], "or 0 * sum by (service_name)") {
		t.Errorf("query lacks the zero term: %s", f.reqs[0].form["query"])
	}
}

func TestPanels_HoldTheTwelveKeysInOrder(t *testing.T) {
	seen := map[string]bool{}
	for _, p := range Panels {
		q := p.Query(Ranges["1h"], "")
		if seen[p.Key] || p.Unit == "" || strings.ContainsAny(q, "{}") && (strings.Contains(q, "{{") || strings.Contains(q, "{F}") || strings.Contains(q, "{S}") || strings.Contains(q, "{W}")) {
			t.Errorf("bad panel %+v", p)
		}
		seen[p.Key] = true
	}
	if len(Panels) != 12 || Panels[1].Key != "error_pct" || Panels[11].Key != "sql_connections" {
		t.Errorf("panels = %d", len(Panels))
	}
}
