// Package config loads and validates YAML config from the file given to
// Load, populating defaults and aggregating all validation errors into a
// single error value that wraps ddnserr.ErrConfig.
//
// The goal is "show the user every problem at once": running ddns with a
// config that has three independent bugs should surface all three in one
// error message so the operator can fix them in a single pass.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/rootwarp/ddns/internal/ddnserr"
)

// Config is the top-level YAML schema.
//
// The unexported path field carries the source file location for Phase 4
// SIGHUP hot-reload. It is populated by Load and exposed via Path().
type Config struct {
	PollInterval time.Duration  `yaml:"poll_interval"`
	StatePath    string         `yaml:"state_path"`
	LogFormat    string         `yaml:"log_format"`
	Resolver     ResolverConfig `yaml:"resolver"`
	Records      []RecordConfig `yaml:"records"`
	HealthAddr   string         `yaml:"health_addr"` // Phase 7 P1; unused in v1

	path string
}

// ResolverConfig captures the subset of config consumed by internal/resolver.
type ResolverConfig struct {
	Sources []string      `yaml:"sources"`
	Quorum  int           `yaml:"quorum"`
	Timeout time.Duration `yaml:"timeout"`
}

// RecordConfig is one DNS record to reconcile. In v1 only Type "A" is accepted.
type RecordConfig struct {
	Project     string `yaml:"project"`
	ManagedZone string `yaml:"managed_zone"`
	Name        string `yaml:"name"`
	TTL         int64  `yaml:"ttl"`
	Type        string `yaml:"type"`
}

// Path returns the filesystem path this config was loaded from. It is the
// hook Phase 4 will use to re-Load on SIGHUP.
func (c *Config) Path() string {
	if c == nil {
		return ""
	}
	return c.path
}

// defaultResolverSources is the v1 default echo-service list. The research
// doc (plan/bootstrap/research/02-ip-echo-availability.md) records the
// decision to ship exactly these three with 2-of-3 quorum.
var defaultResolverSources = []string{
	"https://api.ipify.org",
	"https://ifconfig.me/ip",
	"https://icanhazip.com",
}

const (
	defaultPollInterval    = 5 * time.Minute
	defaultResolverQuorum  = 2
	defaultResolverTimeout = 10 * time.Second
	defaultStatePathSuffix = ".local/state/ddns"
	defaultLogFormat       = "auto"
	defaultRecordTTL       = int64(300)
	defaultRecordType      = "A"
	minTTL                 = int64(30)
	maxTTL                 = int64(86400)
)

// Load reads YAML from path, applies defaults, validates, and returns the
// populated *Config. A validation failure is returned as an error chain that
// wraps ddnserr.ErrConfig so callers can errors.Is(err, ErrConfig) to drive
// exit-code 3.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	cfg.path = path

	if err := applyDefaults(&cfg); err != nil {
		return nil, fmt.Errorf("%w: %w", ddnserr.ErrConfig, err)
	}

	normalizeRecords(cfg.Records)

	if errs := validate(&cfg); len(errs) > 0 {
		return nil, fmt.Errorf("%w: %w", ddnserr.ErrConfig, errors.Join(errs...))
	}

	return &cfg, nil
}

// applyDefaults fills zero-valued fields with the documented defaults. It
// may return an error from os.UserHomeDir for ~-expansion; callers wrap that
// as a config error.
func applyDefaults(cfg *Config) error {
	if cfg.PollInterval == 0 {
		cfg.PollInterval = defaultPollInterval
	}
	if cfg.LogFormat == "" {
		cfg.LogFormat = defaultLogFormat
	}
	if cfg.Resolver.Quorum == 0 {
		cfg.Resolver.Quorum = defaultResolverQuorum
	}
	if cfg.Resolver.Timeout == 0 {
		cfg.Resolver.Timeout = defaultResolverTimeout
	}
	if len(cfg.Resolver.Sources) == 0 {
		cfg.Resolver.Sources = append([]string(nil), defaultResolverSources...)
	}

	if cfg.StatePath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("resolve default state_path: %w", err)
		}
		cfg.StatePath = filepath.Join(home, defaultStatePathSuffix)
	} else if strings.HasPrefix(cfg.StatePath, "~") {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("expand ~ in state_path: %w", err)
		}
		// Support both "~" alone and "~/...".
		rest := strings.TrimPrefix(cfg.StatePath, "~")
		rest = strings.TrimPrefix(rest, string(filepath.Separator))
		rest = strings.TrimPrefix(rest, "/")
		cfg.StatePath = filepath.Join(home, rest)
	}

	for i := range cfg.Records {
		if cfg.Records[i].TTL == 0 {
			cfg.Records[i].TTL = defaultRecordTTL
		}
		if cfg.Records[i].Type == "" {
			cfg.Records[i].Type = defaultRecordType
		}
	}

	return nil
}

// normalizeRecords forces trailing-dot form on every record Name so the
// provider layer does not have to branch on operator preference.
func normalizeRecords(records []RecordConfig) {
	for i := range records {
		records[i].Name = normalizeName(records[i].Name)
	}
}

func normalizeName(name string) string {
	n := strings.TrimSpace(name)
	if n == "" {
		return n
	}
	if !strings.HasSuffix(n, ".") {
		n += "."
	}
	return n
}

// validate returns every problem found; callers merge the slice with errors.Join.
func validate(cfg *Config) []error {
	var errs []error

	if len(cfg.Records) == 0 {
		errs = append(errs, errors.New("records: must contain at least one entry"))
	}

	// Resolver checks.
	if len(cfg.Resolver.Sources) == 0 {
		errs = append(errs, errors.New("resolver.sources: must contain at least one URL"))
	} else {
		for i, src := range cfg.Resolver.Sources {
			if u, err := url.Parse(src); err != nil || u.Scheme == "" || u.Host == "" {
				errs = append(errs, fmt.Errorf("resolver.sources[%d]: %q is not a valid URL", i, src))
			}
		}
	}
	if cfg.Resolver.Quorum < 1 {
		errs = append(errs, fmt.Errorf("resolver.quorum: must be >= 1 (got %d)", cfg.Resolver.Quorum))
	}
	if cfg.Resolver.Quorum > len(cfg.Resolver.Sources) {
		errs = append(errs, fmt.Errorf("resolver.quorum: %d exceeds len(sources)=%d", cfg.Resolver.Quorum, len(cfg.Resolver.Sources)))
	}
	if cfg.Resolver.Quorum >= 2 && len(cfg.Resolver.Sources) < 2 {
		errs = append(errs, fmt.Errorf("resolver.quorum: quorum>=2 requires at least 2 sources (got %d)", len(cfg.Resolver.Sources)))
	}

	// Per-record checks.
	seen := make(map[string]int, len(cfg.Records))
	for i, rec := range cfg.Records {
		if strings.TrimSpace(rec.Project) == "" {
			errs = append(errs, fmt.Errorf("records[%d].project: must not be empty", i))
		}
		if strings.TrimSpace(rec.ManagedZone) == "" {
			errs = append(errs, fmt.Errorf("records[%d].managed_zone: must not be empty", i))
		}
		if strings.TrimSpace(rec.Name) == "" {
			errs = append(errs, fmt.Errorf("records[%d].name: must not be empty", i))
		}
		if rec.TTL < minTTL || rec.TTL > maxTTL {
			errs = append(errs, fmt.Errorf("records[%d].ttl: %d out of range [%d, %d]", i, rec.TTL, minTTL, maxTTL))
		}
		if rec.Type != "A" {
			errs = append(errs, fmt.Errorf("records[%d].type: record type %q not supported in v1; only A records are supported", i, rec.Type))
		}

		// Duplicate detection (post-normalization).
		if rec.Name != "" {
			if prev, ok := seen[rec.Name]; ok {
				errs = append(errs, fmt.Errorf("records[%d].name: duplicate of records[%d].name (%q)", i, prev, rec.Name))
			} else {
				seen[rec.Name] = i
			}
		}
	}

	return errs
}
