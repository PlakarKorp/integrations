// Package xoa is a client for the Xen Orchestra REST API.
package xoa

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

type Config struct {
	// URL is the Xen Orchestra base URL, e.g. https://xo.example.com.
	URL                string
	Token              string
	InsecureSkipVerify bool
}

type Client struct {
	base  *url.URL
	token string
	http  *http.Client
}

func New(cfg Config) (*Client, error) {
	base, err := url.Parse(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("xoa: invalid URL: %w", err)
	}
	if (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" {
		return nil, fmt.Errorf("xoa: URL must be http(s)://host, got %q", cfg.URL)
	}
	if cfg.Token == "" {
		return nil, errors.New("xoa: empty token")
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	if cfg.InsecureSkipVerify {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // explicit opt-in
	}

	return &Client{
		base:  base.JoinPath("rest/v0"),
		token: cfg.Token,
		http: &http.Client{
			Transport: transport,
			// A rejected token may redirect to the sign-in page; surface it
			// as an error instead of trying to decode HTML.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

func (c *Client) Get[T any](ctx context.Context, path string, query url.Values) (T, error) {
	var out T

	u := c.base.JoinPath(path)
	u.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return out, fmt.Errorf("xoa: GET %s: %w", path, err)
	}
	req.AddCookie(&http.Cookie{Name: "authenticationToken", Value: c.token})
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return out, fmt.Errorf("xoa: GET %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return out, &Error{
			Method:     http.MethodGet,
			Path:       path,
			StatusCode: resp.StatusCode,
			Message:    strings.TrimSpace(string(body)),
		}
	}

	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return out, fmt.Errorf("xoa: GET %s: decode: %w", path, err)
	}
	return out, nil
}
