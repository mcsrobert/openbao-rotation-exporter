package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const validConfig = `
openbao:
  address: https://openbao.example.com
  mount: secret
  auth:
    kubernetes:
      role: rotation
keys:
  app/database: 90d
  app/api: 2w
`

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestParseInterval(t *testing.T) {
	tests := []struct {
		value string
		want  time.Duration
		err   string
	}{
		{value: "12h", want: 12 * time.Hour},
		{value: "90d", want: 90 * 24 * time.Hour},
		{value: "2w", want: 14 * 24 * time.Hour},
		{value: "0d", err: "greater than zero"},
		{value: "-1d", err: "whole number"},
		{value: "1.5d", err: "whole number"},
		{value: "1m", err: "whole number"},
		{value: "d", err: "whole number"},
		{value: "", err: "whole number"},
		{value: " 1d", err: "whole number"},
		{value: "99999999999w", err: "too large"},
		{value: "99999999999999999999d", err: "out of range"},
	}
	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			got, err := ParseInterval(tt.value)
			if tt.err != "" {
				if err == nil || !strings.Contains(err.Error(), tt.err) {
					t.Fatalf("error = %v, want it to contain %q", err, tt.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if time.Duration(got) != tt.want {
				t.Fatalf("got %s, want %s", time.Duration(got), tt.want)
			}
		})
	}
}

func TestLoadAppliesDefaults(t *testing.T) {
	cfg, err := Load(writeConfig(t, validConfig))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Collection.Interval != 5*time.Minute {
		t.Errorf("interval = %s", cfg.Collection.Interval)
	}
	if cfg.Collection.Timeout != time.Minute {
		t.Errorf("timeout = %s", cfg.Collection.Timeout)
	}
	if cfg.Collection.Concurrency != 8 {
		t.Errorf("concurrency = %d", cfg.Collection.Concurrency)
	}
	k := cfg.OpenBao.Auth.Kubernetes
	if k.Mount != "kubernetes" {
		t.Errorf("auth mount = %q", k.Mount)
	}
	if k.JWTFile != "/var/run/secrets/kubernetes.io/serviceaccount/token" {
		t.Errorf("jwt file = %q", k.JWTFile)
	}
	if time.Duration(cfg.Keys["app/api"]) != 14*24*time.Hour {
		t.Errorf("app/api interval = %s", time.Duration(cfg.Keys["app/api"]))
	}
}

func TestLoadRejects(t *testing.T) {
	replace := func(old, replacement string) string {
		if !strings.Contains(validConfig, old) {
			t.Fatalf("base config does not contain %q", old)
		}
		return strings.Replace(validConfig, old, replacement, 1)
	}
	tests := []struct {
		name    string
		content string
		err     string
	}{
		{"empty file", "", "file is empty"},
		{"unknown field", validConfig + "extra: true\n", "extra"},
		{"bad interval", replace("90d", "90"), "invalid interval"},
		{"null interval", replace("90d", "null"), "interval must be positive"},
		{"empty interval", replace("90d", ""), "interval must be positive"},
		{"duplicate key", validConfig + "  app/api: 3w\n", "already defined"},
		{"no keys", strings.Split(validConfig, "keys:")[0], "at least one key"},
		{"missing address", replace("address: https://openbao.example.com", "address: ''"), "openbao.address: required"},
		{"bad scheme", replace("https://openbao.example.com", "ftp://openbao.example.com"), "scheme"},
		{"no host", replace("https://openbao.example.com", "https://"), "host"},
		{"credentials", replace("https://openbao.example.com", "https://user:pass@openbao.example.com"), "credentials"},
		{"missing mount", replace("mount: secret", "mount: ''"), "openbao.mount: required"},
		{"mount slash", replace("mount: secret", "mount: secret/"), "slashes"},
		{"no auth", replace("kubernetes:\n      role: rotation", "tokenFile: ''"), "set one of"},
		{"both auth", replace("kubernetes:", "tokenFile: /token\n    kubernetes:"), "only one"},
		{"no role", replace("role: rotation", "role: ''"), "role: required"},
		{"leading slash", replace("app/api", "/app/api"), "slashes"},
		{"trailing slash", replace("app/api", "app/api/"), "slashes"},
		{"double slash", replace("app/api", "app//api"), "slashes"},
		{"traversal", replace("app/api", "app/../sys"), ".."},
		{"timeout over interval", validConfig + "collection:\n  interval: 1m\n  timeout: 2m\n", "must not exceed"},
		{"negative interval", validConfig + "collection:\n  interval: -1m\n", "collection.interval"},
		{"negative concurrency", validConfig + "collection:\n  concurrency: -1\n", "collection.concurrency"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(writeConfig(t, tt.content))
			if err == nil || !strings.Contains(err.Error(), tt.err) {
				t.Fatalf("error = %v, want it to contain %q", err, tt.err)
			}
		})
	}
}

func TestLoadTokenFile(t *testing.T) {
	content := strings.Replace(validConfig, "kubernetes:\n      role: rotation", "tokenFile: /run/token", 1)
	cfg, err := Load(writeConfig(t, content))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.OpenBao.Auth.TokenFile != "/run/token" {
		t.Fatalf("token file = %q", cfg.OpenBao.Auth.TokenFile)
	}
}

func TestLoadMissingFile(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "missing.yaml")); err == nil {
		t.Fatal("expected an error")
	}
}
