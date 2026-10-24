package systemhealth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	promTimeout = 10 * time.Second
	promBodyCap = 4 << 20
	minRateWin  = 5 * time.Minute
	windowLag   = 4 * time.Minute // Cloud Monitoring ingest lag
)

// Range is one selectable span; Step is the query_range resolution.
type Range struct {
	Span, Step time.Duration
}

// Rate returns the rate window: the step, but never under 5 minutes.
func (r Range) Rate() time.Duration {
	return max(r.Step, minRateWin)
}

var Ranges = map[string]Range{
	"15m": {15 * time.Minute, time.Minute},
	"1h":  {time.Hour, time.Minute},
	"6h":  {6 * time.Hour, 5 * time.Minute},
	"24h": {24 * time.Hour, 15 * time.Minute},
	"7d":  {7 * 24 * time.Hour, 2 * time.Hour},
}

// Window ends exactly windowLag before now, not aligned to the step.
func Window(r Range, now time.Time) (start, end time.Time) {
	end = now.Add(-windowLag).Truncate(time.Second)
	return end.Add(-r.Span), end
}

// Series is one named line of a panel; Points are [unix seconds, value].
type Series struct {
	Name   string       `json:"name"`
	Points [][2]float64 `json:"points"`
}

// Panel is one chart: its wire key, unit and PromQL template.
// In the template, {F} is the Cloud Run selector, {S} the Cloud SQL one, {W} the rate window.
type Panel struct {
	Key, Unit, Template string
	CloudRun            bool // the service filter narrows this panel
}

const (
	reqRate  = `sum by (service_name) (rate(run_googleapis_com:request_count{{F}}[{W}]))`
	req5xx   = `sum by (service_name) (rate(run_googleapis_com:request_count{{F},response_code_class="5xx"}[{W}]))`
	latRate  = `sum by (service_name, le) (rate(run_googleapis_com:request_latencies_bucket{{F}}[{W}]))`
	cpuRate  = `sum by (service_name, le) (rate(run_googleapis_com:container_cpu_utilizations_bucket{{F}}[{W}]))`
	ramRate  = `sum by (service_name, le) (rate(run_googleapis_com:container_memory_utilizations_bucket{{F}}[{W}]))`
	sqlMax   = `max(max_over_time(cloudsql_googleapis_com:%s{{S}}[{W}]))`
	connsSQL = `max(sum by (database_id) (max_over_time(cloudsql_googleapis_com:database_postgresql_num_backends{{S}}[{W}])))`
)

// Panels are the 12 panels in wire order.
// error_pct: the "or 0 *" term reads 0 for a service with no 5xx series; clamp_min makes an idle step 0, not NaN.
// A step with no request series still has no point.
var Panels = []Panel{
	{"req_per_min", "req/min", `60 * ` + reqRate, true},
	{"error_pct", "%", `100 * (` + req5xx + ` or 0 * ` + reqRate + `) / clamp_min(` + reqRate + `, 1e-9)`, true},
	{"latency_p50_ms", "ms", `histogram_quantile(0.5, ` + latRate + `)`, true},
	{"latency_p95_ms", "ms", `histogram_quantile(0.95, ` + latRate + `)`, true},
	{"cpu_pct", "% of limit", `100 * histogram_quantile(0.95, ` + cpuRate + `)`, true},
	{"ram_pct", "% of limit", `100 * histogram_quantile(0.95, ` + ramRate + `)`, true},
	{"instances", "count", `sum by (service_name) (max_over_time(run_googleapis_com:container_instance_count{{F}}[{W}]))`, true},
	{"cold_starts_per_min", "/min", `60 * sum by (service_name) (rate(run_googleapis_com:container_startup_latencies_count{{F}}[{W}]))`, true},
	{"sql_cpu_pct", "%", `100 * ` + fmt.Sprintf(sqlMax, "database_cpu_utilization"), false},
	{"sql_ram_pct", "%", `100 * ` + fmt.Sprintf(sqlMax, "database_memory_utilization"), false},
	{"sql_disk_pct", "%", `100 * ` + fmt.Sprintf(sqlMax, "database_disk_utilization"), false},
	{"sql_connections", "count", connsSQL, false},
}

// Query renders the template for r, service and a preview suffix; service reaches it only after the handler matched MetricServices.
func (p Panel) Query(r Range, service, suffix string) string {
	sel := `monitored_resource="cloud_run_revision"`
	switch {
	case service != "":
		sel += `,service_name="` + service + suffix + `"`
	case suffix != "":
		sel += `,service_name=~".+` + suffix + `"`
	}
	return strings.NewReplacer(
		"{F}", sel,
		"{S}", `monitored_resource="cloudsql_database"`,
		"{W}", strconv.Itoa(int(r.Rate().Seconds()))+"s",
	).Replace(p.Template)
}

// queryRange runs one PromQL query_range and returns its series sorted by name.
func (g *Google) queryRange(ctx context.Context, token, project, query string, start, end time.Time, step time.Duration) ([]Series, error) {
	ctx, cancel := context.WithTimeout(ctx, promTimeout)
	defer cancel()
	form := url.Values{
		"query": {query},
		"start": {strconv.FormatInt(start.Unix(), 10)},
		"end":   {strconv.FormatInt(end.Unix(), 10)},
		"step":  {strconv.FormatInt(int64(step.Seconds()), 10)},
	}
	u := strings.TrimSuffix(g.Monitoring, "/") + "/v1/projects/" + url.PathEscape(project) + "/location/global/prometheus/api/v1/query_range"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := g.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("systemhealth: query_range: status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, promBodyCap+1))
	if err != nil {
		return nil, err
	}
	if len(body) > promBodyCap {
		return nil, fmt.Errorf("systemhealth: query_range: body over cap")
	}
	return parseMatrix(body)
}

func parseMatrix(body []byte) ([]Series, error) {
	var ans struct {
		Status string `json:"status"`
		Data   struct {
			ResultType string `json:"resultType"`
			Result     []struct {
				Metric map[string]string    `json:"metric"`
				Values [][2]json.RawMessage `json:"values"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &ans); err != nil {
		return nil, fmt.Errorf("systemhealth: query_range: bad body")
	}
	if ans.Status != "success" || ans.Data.ResultType != "matrix" {
		return nil, fmt.Errorf("systemhealth: query_range: status %q type %q", ans.Status, ans.Data.ResultType)
	}
	out := make([]Series, 0, len(ans.Data.Result))
	for _, r := range ans.Data.Result {
		name := r.Metric["service_name"]
		if name == "" {
			name = "database"
		}
		pts := make([][2]float64, 0, len(r.Values))
		for _, v := range r.Values {
			var t float64
			var s string
			if json.Unmarshal(v[0], &t) != nil || json.Unmarshal(v[1], &s) != nil {
				return nil, fmt.Errorf("systemhealth: query_range: bad point")
			}
			f, err := strconv.ParseFloat(s, 64)
			if err != nil {
				return nil, fmt.Errorf("systemhealth: query_range: bad point")
			}
			if math.IsNaN(f) || math.IsInf(f, 0) {
				continue
			}
			pts = append(pts, [2]float64{t, f})
		}
		out = append(out, Series{Name: name, Points: pts})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
