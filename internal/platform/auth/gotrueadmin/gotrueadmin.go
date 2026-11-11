// Package gotrueadmin reads, lists, tags and deletes accounts through GoTrue's admin API.
package gotrueadmin

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v4"
	"github.com/google/uuid"
)

// ceiling: the worker holds GoTrue's signing key and can mint any token; revisit with a scoped admin key if GoTrue supports one.

const (
	tokenTTL    = 60 * time.Second
	callTimeout = 10 * time.Second
	maxAnswer   = 16 << 10
	// maxListAnswer bounds one page of 100 accounts.
	maxListAnswer = 4 << 20
)

var errKeys = errors.New("gotrueadmin: GOTRUE_JWT_KEYS must be a JSON array with exactly one ES256 P-256 signing key")

// StatusError is a refused or failed call; Status 0 is a transport failure.
type StatusError struct{ Status int }

func (e *StatusError) Error() string { return "gotrueadmin: status " + strconv.Itoa(e.Status) }

// Client signs a short-lived service_role token per call.
type Client struct {
	base string
	key  *ecdsa.PrivateKey
	kid  string
	hc   *http.Client
}

type jwk struct {
	Kty    string   `json:"kty"`
	Crv    string   `json:"crv"`
	Alg    string   `json:"alg"`
	Kid    string   `json:"kid"`
	KeyOps []string `json:"key_ops"`
	X      string   `json:"x"`
	Y      string   `json:"y"`
	D      string   `json:"d"`
}

// New takes the one key of keysJSON whose key_ops holds sign. Errors carry no key material.
func New(baseURL, keysJSON string, hc *http.Client) (*Client, error) {
	var keys []jwk
	if json.Unmarshal([]byte(keysJSON), &keys) != nil {
		return nil, errKeys
	}
	var sign []jwk
	for _, k := range keys {
		if slices.Contains(k.KeyOps, "sign") {
			sign = append(sign, k)
		}
	}
	if len(sign) != 1 {
		return nil, errKeys
	}
	k := sign[0]
	if k.Kty != "EC" || k.Crv != "P-256" || k.Alg != "ES256" || k.Kid == "" {
		return nil, errKeys
	}
	dec := base64.RawURLEncoding.DecodeString
	d, errD := dec(k.D)
	x, errX := dec(k.X)
	y, errY := dec(k.Y)
	if errD != nil || errX != nil || errY != nil || len(d) != 32 || len(x) != 32 || len(y) != 32 {
		return nil, errKeys
	}
	pub := append(append([]byte{4}, x...), y...)
	priv, err := ecdsa.ParseRawPrivateKey(elliptic.P256(), d)
	if err != nil {
		return nil, errKeys
	}
	if got, err := priv.PublicKey.Bytes(); err != nil || !slices.Equal(got, pub) {
		return nil, errKeys
	}
	c := *hc
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{base: strings.TrimRight(baseURL, "/"), key: priv, kid: k.Kid, hc: &c}, nil
}

// FromEnv builds the client from AUTH_URL and GOTRUE_JWT_KEYS. Off Cloud Run, both unset answers (nil, nil).
// Errors name variables, never values.
func FromEnv(getenv func(string) string, onCloudRun bool, rt http.RoundTripper) (*Client, error) {
	url, keys := getenv("AUTH_URL"), getenv("GOTRUE_JWT_KEYS")
	if url == "" && keys == "" && !onCloudRun {
		return nil, nil
	}
	var missing []string
	if url == "" {
		missing = append(missing, "AUTH_URL")
	}
	if keys == "" {
		missing = append(missing, "GOTRUE_JWT_KEYS")
	}
	if len(missing) > 0 {
		return nil, errors.New("gotrueadmin: " + strings.Join(missing, " and ") + " must be set")
	}
	return New(url, keys, &http.Client{Transport: rt, Timeout: callTimeout})
}

// do sends an authenticated request to an admin path and returns the response with its body unread.
func (c *Client) do(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	now := time.Now()
	tok := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.MapClaims{
		"role": "service_role", "iat": now.Unix(), "exp": now.Add(tokenTTL).Unix(),
	})
	tok.Header["kid"] = c.kid
	signed, err := tok.SignedString(c.key)
	if err != nil {
		return nil, errors.New("gotrueadmin: signing failed")
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return nil, &StatusError{}
	}
	req.Header.Set("Authorization", "Bearer "+signed)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, &StatusError{}
	}
	return resp, nil
}

func checkUserID(userID string) error {
	if id, err := uuid.Parse(userID); err != nil || id.String() != userID {
		return errors.New("gotrueadmin: user id is not a canonical UUID")
	}
	return nil
}

// call sends a bodiless request for one account.
func (c *Client) call(ctx context.Context, method, userID string) (*http.Response, error) {
	if err := checkUserID(userID); err != nil {
		return nil, err
	}
	return c.do(ctx, method, "/admin/users/"+userID, nil)
}

// AdminUser is one account of the admin list; Unsubscribe is user_metadata.unsubscribe.
type AdminUser struct{ ID, Email, Unsubscribe string }

// ListUsers reads one page of accounts. A non-2xx answer is a *StatusError with the status only.
func (c *Client) ListUsers(ctx context.Context, page, perPage int) ([]AdminUser, error) {
	resp, err := c.do(ctx, http.MethodGet, "/admin/users?page="+strconv.Itoa(page)+"&per_page="+strconv.Itoa(perPage), nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxAnswer))
		return nil, &StatusError{Status: resp.StatusCode}
	}
	var out struct {
		Users []struct {
			ID    string `json:"id"`
			Email string `json:"email"`
			// Raw: an account can write any JSON into its own user_metadata; a non-string value reads as absent.
			UserMetadata struct {
				Unsubscribe json.RawMessage `json:"unsubscribe"`
			} `json:"user_metadata"`
		} `json:"users"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, maxListAnswer)).Decode(&out) != nil {
		return nil, &StatusError{Status: resp.StatusCode}
	}
	users := make([]AdminUser, len(out.Users))
	for i, u := range out.Users {
		var unsub string
		_ = json.Unmarshal(u.UserMetadata.Unsubscribe, &unsub)
		users[i] = AdminUser{ID: u.ID, Email: u.Email, Unsubscribe: unsub}
	}
	return users, nil
}

// SetUserMetadata merges meta into the account's user_metadata; GoTrue keeps the other keys.
func (c *Client) SetUserMetadata(ctx context.Context, userID string, meta map[string]any) error {
	if err := checkUserID(userID); err != nil {
		return err
	}
	body, err := json.Marshal(map[string]any{"user_metadata": meta})
	if err != nil {
		return err
	}
	resp, err := c.do(ctx, http.MethodPut, "/admin/users/"+userID, bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxAnswer))
	if resp.StatusCode/100 != 2 {
		return &StatusError{Status: resp.StatusCode}
	}
	return nil
}

// DeleteUser deletes the account; 200 and 404 are success.
func (c *Client) DeleteUser(ctx context.Context, userID string) error {
	resp, err := c.call(ctx, http.MethodDelete, userID)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxAnswer))
	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusNotFound {
		return nil
	}
	return &StatusError{Status: resp.StatusCode}
}

// UserEmail reads the account's sign-in email; a missing account answers "" and nil.
func (c *Client) UserEmail(ctx context.Context, userID string) (string, error) {
	resp, err := c.call(ctx, http.MethodGet, userID)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		var u struct {
			Email string `json:"email"`
		}
		if json.NewDecoder(io.LimitReader(resp.Body, maxAnswer)).Decode(&u) != nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxAnswer))
			return "", &StatusError{Status: resp.StatusCode}
		}
		return u.Email, nil
	case http.StatusNotFound:
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxAnswer))
		return "", nil
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxAnswer))
	return "", &StatusError{Status: resp.StatusCode}
}
