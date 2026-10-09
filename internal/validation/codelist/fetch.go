package codelist

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// ErrEmptyList marks a list whose data is absent, null or empty.
var ErrEmptyList = errors.New("empty list")

const maxBody = 16 << 20

func noRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

// fetch GETs one list and returns its entries grouped by code, in published
// order, plus the published entry count. A nil hc never follows a redirect.
func fetch(ctx context.Context, hc *http.Client, baseURL string, l List) (map[string][]json.RawMessage, int, error) {
	if hc == nil {
		hc = &http.Client{Timeout: 60 * time.Second, CheckRedirect: noRedirect}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/"+l.Name, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("codelist: fetch %s: %w", l.Name, err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("codelist: fetch %s: %w", l.Name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, 0, fmt.Errorf("codelist: fetch %s: status %d", l.Name, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return nil, 0, fmt.Errorf("codelist: fetch %s: read body: %w", l.Name, err)
	}
	if len(body) > maxBody {
		return nil, 0, fmt.Errorf("codelist: fetch %s: body exceeds %d bytes", l.Name, maxBody)
	}
	var env struct {
		Data []json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, 0, fmt.Errorf("codelist: fetch %s: decode: %w", l.Name, err)
	}
	if len(env.Data) == 0 {
		return nil, 0, fmt.Errorf("codelist: fetch %s: %w", l.Name, ErrEmptyList)
	}
	grouped := make(map[string][]json.RawMessage)
	for i, raw := range env.Data {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
			return nil, 0, fmt.Errorf("codelist: fetch %s: entry %d is not an object", l.Name, i)
		}
		var code string
		if err := json.Unmarshal(obj[l.CodeKey], &code); err != nil || code == "" {
			return nil, 0, fmt.Errorf("codelist: fetch %s: entry %d has no string %q", l.Name, i, l.CodeKey)
		}
		grouped[code] = append(grouped[code], raw)
	}
	return grouped, len(env.Data), nil
}
