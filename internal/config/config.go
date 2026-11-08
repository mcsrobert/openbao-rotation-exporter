// Package config loads and validates the exporter's configuration file.
package config

import (
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

const (
	defaultCollectionInterval = 5 * time.Minute
	defaultCollectionTimeout  = time.Minute
	defaultConcurrency        = 8
	defaultAuthMount          = "kubernetes"
	defaultJWTFile            = "/var/run/secrets/kubernetes.io/serviceaccount/token"
)

// Config is the exporter's configuration.
type Config struct {
	OpenBao    OpenBao             `yaml:"openbao"`
	Collection Collection          `yaml:"collection"`
	Keys       map[string]Interval `yaml:"keys"`
}

// OpenBao describes how to reach a KV v2 mount.
type OpenBao struct {
	Address string `yaml:"address"`
	Mount   string `yaml:"mount"`
	CAFile  string `yaml:"caFile"`
	Auth    Auth   `yaml:"auth"`
}

// Auth selects exactly one authentication method.
type Auth struct {
	TokenFile  string      `yaml:"tokenFile"`
	Kubernetes *Kubernetes `yaml:"kubernetes"`
}

// Kubernetes configures the Kubernetes auth method.
type Kubernetes struct {
	Mount   string `yaml:"mount"`
	Role    string `yaml:"role"`
	JWTFile string `yaml:"jwtFile"`
}

// Collection controls the background collection loop.
type Collection struct {
	Interval    time.Duration `yaml:"interval"`
	Timeout     time.Duration `yaml:"timeout"`
	Concurrency int           `yaml:"concurrency"`
}

// Interval is a rotation interval such as 12h, 90d or 2w.
type Interval time.Duration

var intervalPattern = regexp.MustCompile(`^([0-9]+)(h|d|w)$`)

// ParseInterval parses a positive whole number followed by h, d or w.
func ParseInterval(value string) (Interval, error) {
	match := intervalPattern.FindStringSubmatch(value)
	if match == nil {
		return 0, fmt.Errorf("invalid interval %q: want a whole number followed by h, d or w", value)
	}
	number, err := strconv.ParseInt(match[1], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid interval %q: %w", value, err)
	}
	unit := map[string]time.Duration{
		"h": time.Hour,
		"d": 24 * time.Hour,
		"w": 7 * 24 * time.Hour,
	}[match[2]]
	if number == 0 {
		return 0, fmt.Errorf("invalid interval %q: must be greater than zero", value)
	}
	if number > math.MaxInt64/int64(unit) {
		return 0, fmt.Errorf("invalid interval %q: too large", value)
	}
	return Interval(time.Duration(number) * unit), nil
}

// UnmarshalYAML implements yaml.Unmarshaler.
func (i *Interval) UnmarshalYAML(node *yaml.Node) error {
	var value string
	if err := node.Decode(&value); err != nil {
		return err
	}
	parsed, err := ParseInterval(value)
	if err != nil {
		return err
	}
	*i = parsed
	return nil
}

// Load reads, validates and applies defaults to the file at path.
func Load(path string) (*Config, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open config: %w", err)
	}
	defer func() { _ = file.Close() }()

	decoder := yaml.NewDecoder(file)
	decoder.KnownFields(true)

	var cfg Config
	if err := decoder.Decode(&cfg); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, errors.New("parse config: file is empty")
		}
		return nil, fmt.Errorf("parse config: %w", err)
	}
	cfg.applyDefaults()
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}
	return &cfg, nil
}

func (c *Config) applyDefaults() {
	if c.Collection.Interval == 0 {
		c.Collection.Interval = defaultCollectionInterval
	}
	if c.Collection.Timeout == 0 {
		c.Collection.Timeout = defaultCollectionTimeout
	}
	if c.Collection.Concurrency == 0 {
		c.Collection.Concurrency = defaultConcurrency
	}
	if k := c.OpenBao.Auth.Kubernetes; k != nil {
		if k.Mount == "" {
			k.Mount = defaultAuthMount
		}
		if k.JWTFile == "" {
			k.JWTFile = defaultJWTFile
		}
	}
}

func (c *Config) validate() error {
	var errs []error
	add := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf(format, args...))
	}

	if err := validateAddress(c.OpenBao.Address); err != nil {
		add("openbao.address: %w", err)
	}
	if err := validatePath(c.OpenBao.Mount); err != nil {
		add("openbao.mount: %w", err)
	}

	auth := c.OpenBao.Auth
	switch {
	case auth.TokenFile != "" && auth.Kubernetes != nil:
		add("openbao.auth: set only one of tokenFile and kubernetes")
	case auth.TokenFile == "" && auth.Kubernetes == nil:
		add("openbao.auth: set one of tokenFile and kubernetes")
	case auth.Kubernetes != nil:
		if err := validatePath(auth.Kubernetes.Mount); err != nil {
			add("openbao.auth.kubernetes.mount: %w", err)
		}
		if auth.Kubernetes.Role == "" {
			add("openbao.auth.kubernetes.role: required")
		}
	}

	if c.Collection.Interval < 0 {
		add("collection.interval: must be positive")
	}
	if c.Collection.Timeout < 0 {
		add("collection.timeout: must be positive")
	}
	if c.Collection.Timeout > c.Collection.Interval {
		add("collection.timeout: must not exceed collection.interval")
	}
	if c.Collection.Concurrency < 0 {
		add("collection.concurrency: must be positive")
	}

	if len(c.Keys) == 0 {
		add("keys: at least one key is required")
	}
	for key := range c.Keys {
		if err := validatePath(key); err != nil {
			add("keys[%q]: %w", key, err)
		}
	}
	return errors.Join(errs...)
}

func validateAddress(address string) error {
	if address == "" {
		return errors.New("required")
	}
	parsed, err := url.Parse(address)
	if err != nil {
		return errors.New("not a valid URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return errors.New("scheme must be http or https")
	}
	if parsed.Host == "" {
		return errors.New("host is required")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("must not contain credentials, a query or a fragment")
	}
	return nil
}

func validatePath(path string) error {
	if path == "" {
		return errors.New("required")
	}
	for segment := range strings.SplitSeq(path, "/") {
		switch segment {
		case "":
			return errors.New("must not have leading, trailing or repeated slashes")
		case ".", "..":
			return errors.New("must not contain . or .. segments")
		}
	}
	return nil
}
