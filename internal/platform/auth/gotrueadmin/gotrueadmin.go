// Package gotrueadmin reads and deletes accounts through GoTrue's admin API.
package gotrueadmin

import (
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

// call sends an authenticated request for one account and returns the response with its body unread.
func (c *Client) call(ctx context.Context, method, userID string) (*http.Response, error) {
	if id, err := uuid.Parse(userID); err != nil || id.String() != userID {
		return nil, errors.New("gotrueadmin: user id is not a canonical UUID")
	}
	now := time.Now()
	tok := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.MapClaims{
		"role": "service_role", "iat": now.Unix(), "exp": now.Add(tokenTTL).Unix(),
	})
	tok.Header["kid"] = c.kid
	signed, err := tok.SignedString(c.key)
	if err != nil {
		return nil, errors.New("gotrueadmin: signing failed")
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+"/admin/users/"+userID, nil)
	if err != nil {
		return nil, &StatusError{}
	}
	req.Header.Set("Authorization", "Bearer "+signed)
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, &StatusError{}
	}
	return resp, nil
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
