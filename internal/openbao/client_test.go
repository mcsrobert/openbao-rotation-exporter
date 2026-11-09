package openbao

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mcsrobert/openbao-rotation-exporter/internal/config"
)

var testNow = time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

func newClient(t *testing.T, address string, auth config.Auth) *Client {
	t.Helper()
	client, err := New(config.OpenBao{Address: address, Mount: "secret", Auth: auth}, func() time.Time { return testNow })
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func writeFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// metadataServer fails the test if anything other than metadata is requested.
func metadataServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/v1/secret/metadata/") {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if r.Method != http.MethodGet {
			t.Errorf("unexpected method %s", r.Method)
		}
		if got := r.Header.Get("X-Vault-Token"); got != "token" {
			t.Errorf("token header = %q", got)
		}
		w.WriteHeader(status)
		_, _ = fmt.Fprint(w, body)
	}))
	t.Cleanup(server.Close)
	return server
}

func TestCurrentVersionCreated(t *testing.T) {
	versions := func(current int, version string) string {
		return fmt.Sprintf(`{"data":{"current_version":%d,"versions":{"1":{"created_time":"2020-01-01T00:00:00Z","deletion_time":"","destroyed":false},"2":%s}}}`, current, version)
	}
	const created = `"created_time":"2026-01-02T03:04:05.123456789Z"`
	want := time.Date(2026, 1, 2, 3, 4, 5, 123456789, time.UTC)

	tests := []struct {
		name   string
		status int
		body   string
		reason string
	}{
		{name: "live", status: 200, body: versions(2, `{`+created+`,"deletion_time":"","destroyed":false}`)},
		{name: "future deletion time is still live", status: 200, body: versions(2, `{`+created+`,"deletion_time":"2026-06-01T12:00:01Z","destroyed":false}`)},
		{name: "deletion time reached", status: 200, body: versions(2, `{`+created+`,"deletion_time":"2026-06-01T12:00:00Z","destroyed":false}`), reason: ReasonVersionUnavailable},
		{name: "destroyed", status: 200, body: versions(2, `{`+created+`,"deletion_time":"","destroyed":true}`), reason: ReasonVersionUnavailable},
		{name: "current version missing", status: 200, body: versions(3, `{`+created+`}`), reason: ReasonVersionUnavailable},
		{name: "invalid created time", status: 200, body: versions(2, `{"created_time":"yesterday"}`), reason: ReasonInvalidResponse},
		{name: "missing created time", status: 200, body: versions(2, `{}`), reason: ReasonInvalidResponse},
		{name: "invalid deletion time", status: 200, body: versions(2, `{`+created+`,"deletion_time":"soon"}`), reason: ReasonInvalidResponse},
		{name: "not json", status: 200, body: `<html>`, reason: ReasonInvalidResponse},
		{name: "forbidden", status: 403, reason: ReasonForbidden},
		{name: "not found", status: 404, reason: ReasonNotFound},
		{name: "bad request", status: 400, reason: ReasonHTTPError},
		{name: "rate limited", status: 429, reason: ReasonUnavailable},
		{name: "server error", status: 500, reason: ReasonUnavailable},
		{name: "sealed", status: 503, reason: ReasonUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := newClient(t, metadataServer(t, tt.status, tt.body).URL, config.Auth{})
			got, err := client.CurrentVersionCreated(t.Context(), "token", "app/db")
			if tt.reason != "" {
				if ReasonOf(err) != tt.reason {
					t.Fatalf("reason = %q (error %v), want %q", ReasonOf(err), err, tt.reason)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !got.Equal(want) {
				t.Fatalf("created = %s, want %s", got, want)
			}
		})
	}
}

func TestErrorsDoNotContainResponseBody(t *testing.T) {
	const marker = "s3cret-marker"
	for _, status := range []int{200, 400, 403, 500} {
		client := newClient(t, metadataServer(t, status, marker).URL, config.Auth{})
		_, err := client.CurrentVersionCreated(t.Context(), "token", "app/db")
		if err == nil {
			t.Fatalf("status %d: expected an error", status)
		}
		if strings.Contains(err.Error(), marker) {
			t.Fatalf("status %d: error contains the response body: %v", status, err)
		}
	}
}

func TestTimestampErrorsDoNotContainResponseValues(t *testing.T) {
	const marker = "s3cret-marker"
	for _, field := range []string{"created_time", "deletion_time"} {
		t.Run(field, func(t *testing.T) {
			version := map[string]any{"created_time": "2026-01-02T03:04:05Z", "deletion_time": "", "destroyed": false}
			version[field] = marker
			body, err := json.Marshal(map[string]any{"data": map[string]any{"current_version": 1, "versions": map[string]any{"1": version}}})
			if err != nil {
				t.Fatal(err)
			}
			client := newClient(t, metadataServer(t, 200, string(body)).URL, config.Auth{})
			_, err = client.CurrentVersionCreated(t.Context(), "token", "app/db")
			if ReasonOf(err) != ReasonInvalidResponse {
				t.Fatalf("reason = %q, want invalid_response", ReasonOf(err))
			}
			if strings.Contains(err.Error(), marker) {
				t.Fatal("error contains response value")
			}
		})
	}
}

func TestEscapesKeySegments(t *testing.T) {
	var got atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.Store(r.URL.EscapedPath())
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(server.Close)

	client := newClient(t, server.URL, config.Auth{})
	_, _ = client.CurrentVersionCreated(t.Context(), "token", "app one/db?x#y")
	const want = "/v1/secret/metadata/app%20one/db%3Fx%23y"
	if got.Load() != want {
		t.Fatalf("path = %v, want %s", got.Load(), want)
	}
}

func TestDoesNotFollowRedirects(t *testing.T) {
	var leaked atomic.Bool
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		leaked.Store(true)
	}))
	t.Cleanup(other.Close)
	server := httptest.NewServer(http.RedirectHandler(other.URL, http.StatusTemporaryRedirect))
	t.Cleanup(server.Close)

	client := newClient(t, server.URL, config.Auth{})
	_, err := client.CurrentVersionCreated(t.Context(), "token", "app/db")
	if ReasonOf(err) != ReasonHTTPError {
		t.Fatalf("reason = %q (error %v)", ReasonOf(err), err)
	}
	if leaked.Load() {
		t.Fatal("the token was sent to the redirect target")
	}
}

func TestLimitsResponseSize(t *testing.T) {
	body := `{"data":"` + strings.Repeat("x", maxResponseSize) + `"}`
	client := newClient(t, metadataServer(t, 200, body).URL, config.Auth{})
	_, err := client.CurrentVersionCreated(t.Context(), "token", "app/db")
	if ReasonOf(err) != ReasonInvalidResponse {
		t.Fatalf("reason = %q (error %v)", ReasonOf(err), err)
	}
}

func TestTransportFailures(t *testing.T) {
	t.Run("unavailable", func(t *testing.T) {
		server := httptest.NewServer(http.NotFoundHandler())
		address := server.URL
		server.Close()
		_, err := newClient(t, address, config.Auth{}).CurrentVersionCreated(t.Context(), "token", "app/db")
		if ReasonOf(err) != ReasonUnavailable {
			t.Fatalf("reason = %q (error %v)", ReasonOf(err), err)
		}
	})
	t.Run("deadline", func(t *testing.T) {
		release := make(chan struct{})
		server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
		t.Cleanup(server.Close)
		t.Cleanup(func() { close(release) })

		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
		defer cancel()
		_, err := newClient(t, server.URL, config.Auth{}).CurrentVersionCreated(ctx, "token", "app/db")
		if ReasonOf(err) != ReasonTimeout {
			t.Fatalf("reason = %q (error %v)", ReasonOf(err), err)
		}
	})
}

func TestKubernetesLogin(t *testing.T) {
	var body map[string]string
	var path string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		if r.Method != http.MethodPost {
			t.Errorf("method = %s", r.Method)
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		_, _ = fmt.Fprint(w, `{"auth":{"client_token":"issued"}}`)
	}))
	t.Cleanup(server.Close)

	jwtFile := writeFile(t, "first-jwt\n")
	client := newClient(t, server.URL, config.Auth{Kubernetes: &config.Kubernetes{Mount: "k8s/home", Role: "rotation", JWTFile: jwtFile}})

	token, err := client.Login(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if token != "issued" {
		t.Fatalf("token = %q", token)
	}
	if path != "/v1/auth/k8s/home/login" {
		t.Fatalf("path = %q", path)
	}
	if body["role"] != "rotation" || body["jwt"] != "first-jwt" {
		t.Fatalf("body = %v", body)
	}

	if err := os.WriteFile(jwtFile, []byte("rotated-jwt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Login(t.Context()); err != nil {
		t.Fatal(err)
	}
	if body["jwt"] != "rotated-jwt" {
		t.Fatalf("the rotated JWT was not reread: %v", body)
	}
}

func TestKubernetesLoginFailures(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    string
		jwtFile func(t *testing.T) string
		reason  string
	}{
		{name: "forbidden", status: 403, reason: ReasonForbidden},
		{name: "sealed", status: 503, reason: ReasonUnavailable},
		{name: "no client token", status: 200, body: `{"auth":{}}`, reason: ReasonInvalidResponse},
		{name: "null auth", status: 200, body: `{"auth":null}`, reason: ReasonInvalidResponse},
		{name: "jwt missing", status: 200, jwtFile: func(t *testing.T) string { return filepath.Join(t.TempDir(), "missing") }, reason: ReasonCredentials},
		{name: "jwt empty", status: 200, jwtFile: func(t *testing.T) string { return writeFile(t, " \n") }, reason: ReasonCredentials},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = fmt.Fprint(w, tt.body)
			}))
			t.Cleanup(server.Close)
			jwtFile := writeFile(t, "jwt")
			if tt.jwtFile != nil {
				jwtFile = tt.jwtFile(t)
			}
			client := newClient(t, server.URL, config.Auth{Kubernetes: &config.Kubernetes{Mount: "kubernetes", Role: "rotation", JWTFile: jwtFile}})
			if _, err := client.Login(t.Context()); ReasonOf(err) != tt.reason {
				t.Fatalf("reason = %q (error %v), want %q", ReasonOf(err), err, tt.reason)
			}
		})
	}
}

func TestTokenFileLogin(t *testing.T) {
	client := newClient(t, "http://127.0.0.1:1", config.Auth{TokenFile: writeFile(t, "static-token\n")})
	token, err := client.Login(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if token != "static-token" {
		t.Fatalf("token = %q", token)
	}
}

func TestCAFile(t *testing.T) {
	server := httptest.NewTLSServer(http.NotFoundHandler())
	t.Cleanup(server.Close)

	_, err := newClient(t, server.URL, config.Auth{}).CurrentVersionCreated(t.Context(), "token", "app/db")
	if ReasonOf(err) != ReasonUnavailable {
		t.Fatalf("untrusted server: reason = %q (error %v)", ReasonOf(err), err)
	}

	certificate := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	client, err := New(config.OpenBao{Address: server.URL, Mount: "secret", CAFile: writeFile(t, string(certificate))}, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.CurrentVersionCreated(t.Context(), "token", "app/db")
	if ReasonOf(err) != ReasonNotFound {
		t.Fatalf("trusted server: reason = %q (error %v)", ReasonOf(err), err)
	}

	if _, err := New(config.OpenBao{Address: server.URL, CAFile: writeFile(t, "not a certificate")}, time.Now); err == nil {
		t.Fatal("expected an error for an invalid CA file")
	}
	if _, err := New(config.OpenBao{Address: server.URL, CAFile: filepath.Join(t.TempDir(), "missing")}, time.Now); err == nil {
		t.Fatal("expected an error for a missing CA file")
	}
}

func TestReasonOfUnknownError(t *testing.T) {
	if got := ReasonOf(errors.New("other")); got != ReasonUnknown {
		t.Fatalf("reason = %q", got)
	}
	wrapped := fmt.Errorf("wrapped: %w", &Error{ReasonForbidden, errors.New("denied")})
	if got := ReasonOf(wrapped); got != ReasonForbidden {
		t.Fatalf("reason = %q", got)
	}
}
