package systemhealth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	metadataBase     = "http://metadata.google.internal/computeMetadata/v1/"
	monitoringBase   = "https://monitoring.googleapis.com"
	runBase          = "https://run.googleapis.com"
	metadataTimeout  = 5 * time.Second
	metadataBodyCap  = 8 << 10
	tokenReuseMargin = 60 * time.Second
)

// Google holds the endpoints and credential source for the Cloud Monitoring and Run Admin calls.
type Google struct {
	Monitoring, Run string // base URLs; a test points them at an httptest.Server
	Client          *http.Client
	Meta            *Metadata
	// Suffix is "-pr-<N>" on a PR preview: reads keep that PR's services and drop it from names.
	Suffix string
}

// NewGoogle reads the production endpoints with client.
func NewGoogle(client *http.Client) *Google {
	return &Google{
		Monitoring: monitoringBase,
		Run:        runBase,
		Client:     client,
		Meta:       NewMetadata(metadataBase, client, time.Now),
	}
}

// Metadata reads the Cloud Run metadata server.
type Metadata struct {
	base   string
	client *http.Client
	now    func() time.Time

	mu      sync.Mutex // held across each fetch
	token   string
	expiry  time.Time
	project string
	region  string
}

// NewMetadata calls base with client; each call also has a 5 s context deadline, and the earlier of that and client.Timeout wins.
func NewMetadata(base string, client *http.Client, now func() time.Time) *Metadata {
	return &Metadata{base: strings.TrimSuffix(base, "/") + "/", client: client, now: now}
}

// Token returns the cached access token until 60 s before it expires.
func (m *Metadata) Token(ctx context.Context) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.token != "" && m.now().Before(m.expiry.Add(-tokenReuseMargin)) {
		return m.token, nil
	}
	body, err := m.get(ctx, "instance/service-accounts/default/token")
	if err != nil {
		return "", fmt.Errorf("systemhealth: access token: %w", err)
	}
	var ans struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &ans); err != nil {
		return "", fmt.Errorf("systemhealth: access token: bad body")
	}
	if ans.AccessToken == "" || ans.ExpiresIn <= 0 {
		return "", fmt.Errorf("systemhealth: access token: empty token or expiry")
	}
	m.token = ans.AccessToken
	m.expiry = m.now().Add(time.Duration(ans.ExpiresIn) * time.Second)
	return m.token, nil
}

// Location returns the project id and region; a success is kept for the process lifetime.
func (m *Metadata) Location(ctx context.Context) (project, region string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.project != "" {
		return m.project, m.region, nil
	}
	p, err := m.get(ctx, "project/project-id")
	if err != nil {
		return "", "", fmt.Errorf("systemhealth: project id: %w", err)
	}
	r, err := m.get(ctx, "instance/region")
	if err != nil {
		return "", "", fmt.Errorf("systemhealth: region: %w", err)
	}
	_, seg, ok := strings.Cut(strings.TrimSpace(string(r)), "regions/")
	project = strings.TrimSpace(string(p))
	if !ok || seg == "" || project == "" {
		return "", "", fmt.Errorf("systemhealth: region: unexpected shape")
	}
	m.project, m.region = project, seg
	return m.project, m.region, nil
}

func (m *Metadata) get(ctx context.Context, path string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, metadataTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.base+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Metadata-Flavor", "Google")
	resp, err := m.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, metadataBodyCap))
}
