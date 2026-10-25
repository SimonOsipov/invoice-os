// Package gotrueadmin deletes accounts through GoTrue's admin API.
package gotrueadmin

import (
	"context"
	"errors"
	"net/http"
	"strconv"
)

var errNotImplemented = errors.New("gotrueadmin: not implemented")

// StatusError is a refused or failed call; Status 0 is a transport failure.
type StatusError struct{ Status int }

func (e *StatusError) Error() string { return "gotrueadmin: status " + strconv.Itoa(e.Status) }

// Client signs a short-lived service_role token per call.
type Client struct{}

// New is a compile stub.
func New(baseURL, keysJSON string, hc *http.Client) (*Client, error) { return nil, errNotImplemented }

// FromEnv is a compile stub.
func FromEnv(getenv func(string) string, onCloudRun bool, rt http.RoundTripper) (*Client, error) {
	return nil, errNotImplemented
}

// DeleteUser is a compile stub.
func (c *Client) DeleteUser(ctx context.Context, userID string) error { return errNotImplemented }
