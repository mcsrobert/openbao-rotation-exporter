// Package openbao is a minimal client for OpenBao login and KV v2 metadata.
//
// It only ever calls the login and metadata endpoints, so it has no way to
// read secret values.
package openbao

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/mcsrobert/openbao-rotation-exporter/internal/config"
)

const (
	requestTimeout  = 10 * time.Second
	maxResponseSize = 1 << 20
)

// Client talks to a single OpenBao server and KV v2 mount.
type Client struct {
	base  *url.URL
	mount string
	auth  config.Auth
	http  *http.Client
	now   func() time.Time
}

// New returns a client for cfg. now is used to evaluate deletion times.
func New(cfg config.OpenBao, now func() time.Time) (*Client, error) {
	base, err := url.Parse(cfg.Address)
	if err != nil {
		return nil, fmt.Errorf("parse address: %w", err)
	}

	transport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, errors.New("unexpected default transport")
	}
	transport = transport.Clone()
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	if cfg.CAFile != "" {
		pem, err := os.ReadFile(cfg.CAFile)
		if err != nil {
			return nil, fmt.Errorf("read CA file: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("no certificates found in %s", cfg.CAFile)
		}
		transport.TLSClientConfig.RootCAs = pool
	}

	return &Client{
		base:  base,
		mount: cfg.Mount,
		auth:  cfg.Auth,
		now:   now,
		http: &http.Client{
			Transport: transport,
			Timeout:   requestTimeout,
			// Following a redirect would resend the token to whichever host
			// the redirect names.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

// Login returns a token to use for metadata requests.
func (c *Client) Login(ctx context.Context) (string, error) {
	if c.auth.TokenFile != "" {
		return readCredential(c.auth.TokenFile)
	}

	k := c.auth.Kubernetes
	jwt, err := readCredential(k.JWTFile)
	if err != nil {
		return "", err
	}
	var response struct {
		Auth struct {
			ClientToken string `json:"client_token"`
		} `json:"auth"`
	}
	body := map[string]string{"role": k.Role, "jwt": jwt}
	path := c.base.JoinPath("v1", "auth", escapePath(k.Mount), "login")
	if err := c.do(ctx, http.MethodPost, path, "", body, &response); err != nil {
		return "", fmt.Errorf("login: %w", err)
	}
	if response.Auth.ClientToken == "" {
		return "", &Error{ReasonInvalidResponse, errors.New("login: response has no client token")}
	}
	return response.Auth.ClientToken, nil
}

// CurrentVersionCreated returns the creation time of the current version of
// key. It fails with ReasonVersionUnavailable when that version is destroyed
// or its deletion time has passed.
func (c *Client) CurrentVersionCreated(ctx context.Context, token, key string) (time.Time, error) {
	var response struct {
		Data struct {
			CurrentVersion int `json:"current_version"`
			Versions       map[string]struct {
				CreatedTime  string `json:"created_time"`
				DeletionTime string `json:"deletion_time"`
				Destroyed    bool   `json:"destroyed"`
			} `json:"versions"`
		} `json:"data"`
	}
	path := c.base.JoinPath("v1", escapePath(c.mount), "metadata", escapePath(key))
	if err := c.do(ctx, http.MethodGet, path, token, nil, &response); err != nil {
		return time.Time{}, err
	}

	version, ok := response.Data.Versions[strconv.Itoa(response.Data.CurrentVersion)]
	if !ok {
		return time.Time{}, &Error{ReasonVersionUnavailable, errors.New("current version has no metadata")}
	}
	if version.Destroyed {
		return time.Time{}, &Error{ReasonVersionUnavailable, errors.New("current version is destroyed")}
	}
	if version.DeletionTime != "" {
		deletion, err := time.Parse(time.RFC3339Nano, version.DeletionTime)
		if err != nil {
			return time.Time{}, &Error{ReasonInvalidResponse, errors.New("invalid deletion time")}
		}
		if !deletion.After(c.now()) {
			return time.Time{}, &Error{ReasonVersionUnavailable, errors.New("current version is deleted")}
		}
	}
	created, err := time.Parse(time.RFC3339Nano, version.CreatedTime)
	if err != nil {
		return time.Time{}, &Error{ReasonInvalidResponse, errors.New("invalid creation time")}
	}
	return created, nil
}

func (c *Client) do(ctx context.Context, method string, target *url.URL, token string, body, out any) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, target.String(), reader)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		request.Header.Set("X-Vault-Token", token)
	}

	response, err := c.http.Do(request)
	if err != nil {
		return &Error{transportReason(err), err}
	}
	defer func() { _ = response.Body.Close() }()

	// Response bodies are never included in errors, so they cannot reach logs.
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return &Error{statusReason(response.StatusCode), fmt.Errorf("unexpected status %d", response.StatusCode)}
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, maxResponseSize))
	if err := decoder.Decode(out); err != nil {
		return &Error{ReasonInvalidResponse, errors.New("response is not valid JSON")}
	}
	return nil
}

func readCredential(path string) (string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return "", &Error{ReasonCredentials, err}
	}
	credential := strings.TrimSpace(string(content))
	if credential == "" {
		return "", &Error{ReasonCredentials, fmt.Errorf("%s is empty", path)}
	}
	return credential, nil
}

func escapePath(path string) string {
	segments := strings.Split(path, "/")
	for i, segment := range segments {
		segments[i] = url.PathEscape(segment)
	}
	return strings.Join(segments, "/")
}

func transportReason(err error) string {
	var netErr net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) {
		return ReasonTimeout
	}
	return ReasonUnavailable
}

func statusReason(status int) string {
	switch {
	case status == http.StatusForbidden:
		return ReasonForbidden
	case status == http.StatusNotFound:
		return ReasonNotFound
	case status == http.StatusTooManyRequests || status >= 500:
		return ReasonUnavailable
	default:
		return ReasonHTTPError
	}
}
