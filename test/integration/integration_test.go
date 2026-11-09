//go:build integration

// Package integration tests the exporter against a real OpenBao dev server.
// It needs the bao binary on PATH.
package integration

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mcsrobert/openbao-rotation-exporter/internal/collector"
	"github.com/mcsrobert/openbao-rotation-exporter/internal/config"
	"github.com/mcsrobert/openbao-rotation-exporter/internal/openbao"
)

const rootToken = "integration-root"

type bao struct {
	t       *testing.T
	address string
}

func startBao(t *testing.T) *bao {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	command := exec.CommandContext(ctx, "bao", "server", "-dev",
		"-dev-root-token-id="+rootToken,
		"-dev-listen-address="+address,
		"-dev-no-store-token",
	)
	logFile, err := os.Create(filepath.Join(t.TempDir(), "bao.log"))
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	command.Stdout = logFile
	command.Stderr = logFile
	if err := command.Start(); err != nil {
		cancel()
		t.Fatalf("start bao: %v", err)
	}
	t.Cleanup(func() {
		cancel()
		_ = command.Wait()
		_ = logFile.Close()
	})

	server := &bao{t: t, address: "http://" + address}
	deadline := time.Now().Add(15 * time.Second)
	for {
		if status, _ := server.request(http.MethodGet, "sys/health", rootToken, nil); status == http.StatusOK {
			return server
		}
		if time.Now().After(deadline) {
			logs, _ := os.ReadFile(logFile.Name())
			t.Fatalf("bao did not become healthy:\n%s", logs)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func (b *bao) request(method, path, token string, body any) (int, []byte) {
	b.t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			b.t.Fatal(err)
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequest(method, b.address+"/v1/"+path, reader)
	if err != nil {
		b.t.Fatal(err)
	}
	request.Header.Set("X-Vault-Token", token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return 0, nil
	}
	defer response.Body.Close()
	content, err := io.ReadAll(response.Body)
	if err != nil {
		b.t.Fatal(err)
	}
	return response.StatusCode, content
}

func (b *bao) mustRoot(method, path string, body any) []byte {
	b.t.Helper()
	status, content := b.request(method, path, rootToken, body)
	if status < 200 || status > 299 {
		b.t.Fatalf("%s %s = %d: %s", method, path, status, content)
	}
	return content
}

func (b *bao) writeSecret(key string) {
	b.t.Helper()
	b.mustRoot(http.MethodPost, "secret/data/"+key, map[string]any{"data": map[string]string{"value": "do-not-read"}})
}

// writePolicy writes the policy from examples/policy.hcl as rotation, with its
// mount set to secret.
func (b *bao) writePolicy() {
	b.t.Helper()
	policy, err := os.ReadFile(filepath.Join("..", "..", "examples", "policy.hcl"))
	if err != nil {
		b.t.Fatal(err)
	}
	b.mustRoot(http.MethodPost, "sys/policies/acl/rotation", map[string]string{"policy": string(bytes.ReplaceAll(policy, []byte("MOUNT"), []byte("secret")))})
}

// scopedToken returns a token limited to the rotation policy.
func (b *bao) scopedToken() string {
	b.t.Helper()
	b.writePolicy()
	content := b.mustRoot(http.MethodPost, "auth/token/create", map[string]any{
		"policies":          []string{"rotation"},
		"no_default_policy": true,
		"ttl":               "1h",
	})
	var response struct {
		Auth struct {
			ClientToken string `json:"client_token"`
		} `json:"auth"`
	}
	if err := json.Unmarshal(content, &response); err != nil {
		b.t.Fatal(err)
	}
	return response.Auth.ClientToken
}

func (b *bao) client(token string) *openbao.Client {
	b.t.Helper()
	tokenFile := filepath.Join(b.t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte(token), 0o600); err != nil {
		b.t.Fatal(err)
	}
	client, err := openbao.New(config.OpenBao{
		Address: b.address,
		Mount:   "secret",
		Auth:    config.Auth{TokenFile: tokenFile},
	}, time.Now)
	if err != nil {
		b.t.Fatal(err)
	}
	return client
}

// tokenCount returns the number of tokens with an accessor, which excludes
// batch tokens.
// tokenValid reports whether token can still be used.
func (b *bao) tokenValid(token string) bool {
	b.t.Helper()
	status, content := b.request(http.MethodPost, "auth/token/lookup", rootToken, map[string]string{"token": token})
	switch status {
	case http.StatusOK:
		return true
	case http.StatusForbidden, http.StatusBadRequest:
		return false
	default:
		b.t.Fatalf("token lookup = %d: %s", status, content)
		return false
	}
}

func (b *bao) tokenCount() int {
	b.t.Helper()
	var response struct {
		Data struct {
			Keys []string `json:"keys"`
		} `json:"data"`
	}
	if err := json.Unmarshal(b.mustRoot("LIST", "auth/token/accessors", nil), &response); err != nil {
		b.t.Fatal(err)
	}
	return len(response.Data.Keys)
}

const (
	serviceAccountNamespace = "rotation"
	serviceAccountName      = "exporter"
	serviceAccountUID       = "4b1e8c1a-0000-4000-8000-000000000001"
	serviceAccountAudience  = "https://kubernetes.default.svc"
)

// serviceAccountJWT returns a projected service account token. OpenBao only
// parses it and relies on the token review for validation.
func serviceAccountJWT(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	encode := func(value any) string {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return base64.RawURLEncoding.EncodeToString(encoded)
	}
	now := time.Now()
	signed := encode(map[string]string{"alg": "RS256", "typ": "JWT"}) + "." + encode(map[string]any{
		"iss": serviceAccountAudience,
		"sub": "system:serviceaccount:" + serviceAccountNamespace + ":" + serviceAccountName,
		"aud": []string{serviceAccountAudience},
		"iat": now.Unix(),
		"exp": now.Add(time.Hour).Unix(),
		"kubernetes.io": map[string]any{
			"namespace":      serviceAccountNamespace,
			"serviceaccount": map[string]string{"name": serviceAccountName, "uid": serviceAccountUID},
		},
	})
	digest := sha256.Sum256([]byte(signed))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return signed + "." + base64.RawURLEncoding.EncodeToString(signature)
}

// tokenReviewServer stands in for the Kubernetes API and accepts every token
// as the exporter's service account.
func tokenReviewServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/apis/authentication.k8s.io/v1/tokenreviews" {
			t.Errorf("unexpected Kubernetes API request %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"apiVersion": "authentication.k8s.io/v1",
			"kind":       "TokenReview",
			"status": map[string]any{
				"authenticated": true,
				"audiences":     []string{serviceAccountAudience},
				"user": map[string]string{
					"username": "system:serviceaccount:" + serviceAccountNamespace + ":" + serviceAccountName,
					"uid":      serviceAccountUID,
				},
			},
		})
	}))
	t.Cleanup(server.Close)
	return server
}

func TestKubernetesAuthTokensAreRevoked(t *testing.T) {
	server := startBao(t)
	server.writePolicy()
	server.writeSecret("app/key")
	server.mustRoot(http.MethodPost, "sys/auth/kubernetes", map[string]string{"type": "kubernetes"})
	kubernetes := tokenReviewServer(t)
	server.mustRoot(http.MethodPost, "auth/kubernetes/config", map[string]any{
		"kubernetes_host":      kubernetes.URL,
		"kubernetes_ca_cert":   string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: kubernetes.Certificate().Raw})),
		"disable_local_ca_jwt": true,
	})
	for role, tokenType := range map[string]string{"service": "service", "batch": "batch"} {
		server.mustRoot(http.MethodPost, "auth/kubernetes/role/"+role, map[string]any{
			"bound_service_account_names":      []string{serviceAccountName},
			"bound_service_account_namespaces": []string{serviceAccountNamespace},
			"token_policies":                   []string{"rotation"},
			"token_no_default_policy":          true,
			"token_type":                       tokenType,
			"token_ttl":                        "768h",
		})
	}
	jwtFile := filepath.Join(t.TempDir(), "jwt")
	if err := os.WriteFile(jwtFile, []byte(serviceAccountJWT(t)), 0o600); err != nil {
		t.Fatal(err)
	}
	client := func(t *testing.T, role string) *openbao.Client {
		t.Helper()
		client, err := openbao.New(config.OpenBao{
			Address: server.address,
			Mount:   "secret",
			Auth:    config.Auth{Kubernetes: &config.Kubernetes{Mount: "kubernetes", Role: role, JWTFile: jwtFile}},
		}, time.Now)
		if err != nil {
			t.Fatal(err)
		}
		return client
	}
	collect := func(t *testing.T, client *openbao.Client, times int) string {
		t.Helper()
		var logs strings.Builder
		rotation := collector.New(client, collector.Options{
			Keys:        map[string]time.Duration{"app/key": 24 * time.Hour},
			Interval:    time.Minute,
			Timeout:     10 * time.Second,
			Concurrency: 1,
			Now:         time.Now,
			Logger:      slog.New(slog.NewTextHandler(&logs, nil)),
		})
		for range times {
			rotation.Collect(t.Context())
			if !rotation.Snapshot().Success {
				t.Fatalf("collection failed:\n%s", logs.String())
			}
		}
		return logs.String()
	}

	t.Run("service token is revoked with the example policy", func(t *testing.T) {
		client := client(t, "service")
		credential, err := client.Login(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if !credential.Revocable {
			t.Fatal("a service token should be revocable")
		}
		if !server.tokenValid(credential.Token) {
			t.Fatal("the token is not valid before revocation")
		}
		if err := client.Revoke(t.Context(), credential); err != nil {
			t.Fatal(err)
		}
		if server.tokenValid(credential.Token) {
			t.Fatal("the token is still valid after revocation")
		}
	})

	t.Run("collections do not accumulate tokens", func(t *testing.T) {
		before := server.tokenCount()
		logs := collect(t, client(t, "service"), 3)
		if after := server.tokenCount(); after != before {
			t.Fatalf("tokens went from %d to %d", before, after)
		}
		if strings.Contains(logs, "revocation") {
			t.Fatalf("unexpected revocation warning:\n%s", logs)
		}
	})

	t.Run("batch tokens are not revoked", func(t *testing.T) {
		client := client(t, "batch")
		credential, err := client.Login(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if credential.Revocable {
			t.Fatal("a batch token should not be revocable")
		}
		if logs := collect(t, client, 2); strings.Contains(logs, "revocation") {
			t.Fatalf("unexpected revocation warning:\n%s", logs)
		}
	})
}

func TestAgainstRealOpenBao(t *testing.T) {
	server := startBao(t)
	token := server.scopedToken()
	client := server.client(token)
	ctx := t.Context()

	t.Run("policy allows metadata and denies secret values", func(t *testing.T) {
		server.writeSecret("policy/key")
		if _, err := client.CurrentVersionCreated(ctx, token, "policy/key"); err != nil {
			t.Fatalf("metadata read: %v", err)
		}
		status, body := server.request(http.MethodGet, "secret/data/policy/key", token, nil)
		if status != http.StatusForbidden {
			t.Fatalf("secret value read = %d: %s", status, body)
		}
		if bytes.Contains(body, []byte("do-not-read")) {
			t.Fatal("the response contains the secret value")
		}
	})

	t.Run("policy denies writes and listing", func(t *testing.T) {
		status, _ := server.request(http.MethodPost, "secret/data/policy/key", token, map[string]any{"data": map[string]string{"value": "x"}})
		if status != http.StatusForbidden {
			t.Fatalf("write = %d", status)
		}
		status, _ = server.request("LIST", "secret/metadata/policy", token, nil)
		if status != http.StatusForbidden {
			t.Fatalf("list = %d", status)
		}
	})

	t.Run("created time follows the current version", func(t *testing.T) {
		before := time.Now().Add(-time.Second)
		server.writeSecret("versions/key")
		first, err := client.CurrentVersionCreated(ctx, token, "versions/key")
		if err != nil {
			t.Fatal(err)
		}
		if first.Before(before) || first.After(time.Now().Add(time.Second)) {
			t.Fatalf("created = %s, want about now", first)
		}
		time.Sleep(10 * time.Millisecond)
		server.writeSecret("versions/key")
		second, err := client.CurrentVersionCreated(ctx, token, "versions/key")
		if err != nil {
			t.Fatal(err)
		}
		if !second.After(first) {
			t.Fatalf("second version %s is not after first %s", second, first)
		}
	})

	t.Run("missing key", func(t *testing.T) {
		_, err := client.CurrentVersionCreated(ctx, token, "missing/key")
		if got := openbao.ReasonOf(err); got != openbao.ReasonNotFound {
			t.Fatalf("reason = %q (error %v)", got, err)
		}
	})

	t.Run("key on another mount is forbidden", func(t *testing.T) {
		server.mustRoot(http.MethodPost, "sys/mounts/other", map[string]any{"type": "kv", "options": map[string]string{"version": "2"}})
		other, err := openbao.New(config.OpenBao{Address: server.address, Mount: "other"}, time.Now)
		if err != nil {
			t.Fatal(err)
		}
		_, err = other.CurrentVersionCreated(ctx, token, "any")
		if got := openbao.ReasonOf(err); got != openbao.ReasonForbidden {
			t.Fatalf("reason = %q (error %v)", got, err)
		}
	})

	t.Run("deleted current version", func(t *testing.T) {
		server.writeSecret("deleted/key")
		server.mustRoot(http.MethodDelete, "secret/data/deleted/key", nil)
		_, err := client.CurrentVersionCreated(ctx, token, "deleted/key")
		if got := openbao.ReasonOf(err); got != openbao.ReasonVersionUnavailable {
			t.Fatalf("reason = %q (error %v)", got, err)
		}
	})

	t.Run("destroyed current version", func(t *testing.T) {
		server.writeSecret("destroyed/key")
		server.mustRoot(http.MethodPost, "secret/destroy/destroyed/key", map[string]any{"versions": []int{1}})
		_, err := client.CurrentVersionCreated(ctx, token, "destroyed/key")
		if got := openbao.ReasonOf(err); got != openbao.ReasonVersionUnavailable {
			t.Fatalf("reason = %q (error %v)", got, err)
		}
	})

	t.Run("scheduled deletion is still live", func(t *testing.T) {
		server.mustRoot(http.MethodPost, "secret/metadata/scheduled/key", map[string]string{"delete_version_after": "1h"})
		server.writeSecret("scheduled/key")
		if _, err := client.CurrentVersionCreated(ctx, token, "scheduled/key"); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("metadata changes do not reset the age", func(t *testing.T) {
		server.writeSecret("custom/key")
		first, err := client.CurrentVersionCreated(ctx, token, "custom/key")
		if err != nil {
			t.Fatal(err)
		}
		server.mustRoot(http.MethodPost, "secret/metadata/custom/key", map[string]any{"custom_metadata": map[string]string{"owner": "me"}})
		second, err := client.CurrentVersionCreated(ctx, token, "custom/key")
		if err != nil {
			t.Fatal(err)
		}
		if !first.Equal(second) {
			t.Fatalf("created changed from %s to %s", first, second)
		}
	})

	t.Run("collector end to end", func(t *testing.T) {
		server.writeSecret("collector/key")
		rotation := collector.New(client, collector.Options{
			Keys:        map[string]time.Duration{"collector/key": 24 * time.Hour, "missing/key": 24 * time.Hour},
			Interval:    time.Minute,
			Timeout:     10 * time.Second,
			Concurrency: 2,
			Now:         time.Now,
			Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		})
		rotation.Collect(ctx)
		snapshot := rotation.Snapshot()
		if !snapshot.Success {
			t.Fatalf("collection failed: %+v", snapshot)
		}
		if !server.tokenValid(token) {
			t.Fatal("the token file's token was revoked")
		}
		for _, status := range snapshot.Keys {
			switch status.Key {
			case "collector/key":
				if !status.Up || time.Until(status.Due) < 23*time.Hour || time.Until(status.Due) > 25*time.Hour {
					t.Errorf("collector/key = %+v", status)
				}
			case "missing/key":
				if status.Up || status.Failures[openbao.ReasonNotFound] != 1 {
					t.Errorf("missing/key = %+v", status)
				}
			default:
				t.Errorf("unexpected key %s", status.Key)
			}
		}
	})

	t.Run("sealed server is unavailable", func(t *testing.T) {
		server.mustRoot(http.MethodPost, "sys/seal", nil)
		_, err := client.CurrentVersionCreated(ctx, token, "policy/key")
		if got := openbao.ReasonOf(err); got != openbao.ReasonUnavailable {
			t.Fatalf("reason = %q (error %v)", got, err)
		}
	})
}
