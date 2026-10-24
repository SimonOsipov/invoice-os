package systemhealth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	runTimeout      = 10 * time.Second
	limitsBodyCap   = 1 << 20
	limitsPageSize  = 100
	limitsMaxPages  = 5
	deploysBodyCap  = 16 << 20
	deploysPageSize = 1000
	deploysMaxPages = 20
)

// ServiceLimits are one Cloud Run service's limits; max_instances is the revision template's.
type ServiceLimits struct {
	Name         string `json:"name"`
	CPU          string `json:"cpu"`
	Memory       string `json:"memory"`
	MaxInstances int    `json:"max_instances"`
}

// Deploy is one revision creation time.
type Deploy struct {
	Service   string    `json:"service"`
	Revision  string    `json:"revision"`
	CreatedAt time.Time `json:"created_at"`
}

// limits returns the template limits of each service, or of service alone, sorted by name.
func (g *Google) limits(ctx context.Context, token, project, region, service string) ([]ServiceLimits, error) {
	var out []ServiceLimits
	// ceiling: a preview lists every PR's services (cap 5×100) and filters here; filter server-side if previews near 25 open PRs
	err := g.runPages(ctx, token, project, region, "services", limitsPageSize, limitsBodyCap, limitsMaxPages, func(body []byte) (string, error) {
		var page struct {
			Services []struct {
				Name     string `json:"name"`
				Template struct {
					Scaling struct {
						MaxInstanceCount int `json:"maxInstanceCount"`
					} `json:"scaling"`
					Containers []struct {
						Resources struct {
							Limits map[string]string `json:"limits"`
						} `json:"resources"`
					} `json:"containers"`
				} `json:"template"`
			} `json:"services"`
			NextPageToken string `json:"nextPageToken"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return "", fmt.Errorf("systemhealth: run services: bad body")
		}
		for _, s := range page.Services {
			name := path.Base(s.Name)
			if g.Suffix != "" {
				var ok bool
				if name, ok = strings.CutSuffix(name, g.Suffix); !ok {
					continue
				}
			}
			l := ServiceLimits{Name: name, MaxInstances: s.Template.Scaling.MaxInstanceCount}
			if len(s.Template.Containers) > 0 {
				lim := s.Template.Containers[0].Resources.Limits
				l.CPU, l.Memory = lim["cpu"], lim["memory"]
			}
			if service == "" || l.Name == service {
				out = append(out, l)
			}
		}
		return page.NextPageToken, nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// deploys returns the revisions created in [start, now] (now is the fetch time), oldest first.
func (g *Google) deploys(ctx context.Context, token, project, region, service string, start, now time.Time) ([]Deploy, error) {
	if service == "" {
		service = "-"
	} else {
		service += g.Suffix
	}
	var out []Deploy
	// ceiling: reads every revision page; stop at the first page older than the range above ~3k revisions
	// ceiling: a preview's wildcard lists every PR's revisions (cap 20×1000); filter server-side above ~15k preview revisions
	err := g.runPages(ctx, token, project, region, "services/"+url.PathEscape(service)+"/revisions", deploysPageSize, deploysBodyCap, deploysMaxPages, func(body []byte) (string, error) {
		var page struct {
			Revisions []struct {
				Name       string    `json:"name"`
				Service    string    `json:"service"`
				CreateTime time.Time `json:"createTime"`
			} `json:"revisions"`
			NextPageToken string `json:"nextPageToken"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return "", fmt.Errorf("systemhealth: run revisions: bad body")
		}
		for _, r := range page.Revisions {
			if r.CreateTime.Before(start) || r.CreateTime.After(now) {
				continue
			}
			svc := path.Base(r.Service)
			if g.Suffix != "" {
				var ok bool
				if svc, ok = strings.CutSuffix(svc, g.Suffix); !ok {
					continue
				}
			}
			out = append(out, Deploy{Service: svc, Revision: path.Base(r.Name), CreatedAt: r.CreateTime})
		}
		return page.NextPageToken, nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].Revision < out[j].Revision
	})
	return out, nil
}

// runPages GETs each page of a Run Admin list and hands its body to parse, which returns the next page token.
// Past maxPages the list is an error.
func (g *Google) runPages(ctx context.Context, token, project, region, tail string, pageSize, bodyCap, maxPages int, parse func([]byte) (string, error)) error {
	base := strings.TrimSuffix(g.Run, "/") + "/v2/projects/" + url.PathEscape(project) + "/locations/" + url.PathEscape(region) + "/" + tail
	next := ""
	for page := 1; ; page++ {
		if page > maxPages {
			return fmt.Errorf("systemhealth: run %s: over %d pages", tail, maxPages)
		}
		q := url.Values{"pageSize": {strconv.Itoa(pageSize)}}
		if next != "" {
			q.Set("pageToken", next)
		}
		body, err := g.runGet(ctx, token, base+"?"+q.Encode(), bodyCap)
		if err != nil {
			return fmt.Errorf("systemhealth: run %s: %w", tail, err)
		}
		if next, err = parse(body); err != nil {
			return err
		}
		if next == "" {
			return nil
		}
	}
}

func (g *Google) runGet(ctx context.Context, token, u string, bodyCap int) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, runTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := g.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, int64(bodyCap)+1))
	if err != nil {
		return nil, err
	}
	if len(body) > bodyCap {
		return nil, fmt.Errorf("body over cap")
	}
	return body, nil
}
