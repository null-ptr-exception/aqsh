package auth

import (
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"
	"unicode"

	"gopkg.in/yaml.v3"
)

type Config struct {
	IncludeClusterInIdentity bool                     `yaml:"include_cluster_in_identity"`
	Audiences                []string                 `yaml:"audiences"`
	RequestTimeout           time.Duration            `yaml:"request_timeout"`
	Clusters                 map[string]ClusterConfig `yaml:"clusters"`
}

type ClusterConfig struct {
	AllowHTTP bool   `yaml:"allow_http"`
	APIServer string `yaml:"api_server"`
	CAFile    string `yaml:"ca_file"`
	TokenFile string `yaml:"token_file"`
}

func Load(path string) (Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return Config{}, err
	}
	defer f.Close()
	var cfg Config
	d := yaml.NewDecoder(f)
	d.KnownFields(true)
	if err := d.Decode(&cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func (c *Config) validate() error {
	if len(c.Audiences) == 0 {
		return fmt.Errorf("at least one audience is required")
	}
	for _, audience := range c.Audiences {
		if strings.TrimSpace(audience) != audience || audience == "" {
			return fmt.Errorf("audiences must be nonempty without surrounding whitespace")
		}
	}
	if c.RequestTimeout == 0 {
		c.RequestTimeout = 5 * time.Second
	}
	if c.RequestTimeout < time.Millisecond || c.RequestTimeout > time.Minute {
		return fmt.Errorf("request_timeout must be between 1ms and 1m")
	}
	if len(c.Clusters) != 1 {
		return fmt.Errorf("exactly one TokenReview endpoint is required")
	}
	for id, cluster := range c.Clusters {
		if !validClusterName(id) || cluster.TokenFile == "" {
			return fmt.Errorf("each endpoint requires a stable fallback cluster ID and token_file")
		}
		if !validEndpoint(cluster.APIServer, cluster.AllowHTTP) {
			return fmt.Errorf("cluster %q requires an HTTPS api_server (or explicit allow_http) without credentials, query or fragment", id)
		}
	}
	return nil
}

func validEndpoint(raw string, allowHTTP bool) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "https" || allowHTTP && u.Scheme == "http") && u.Host != "" && u.User == nil && u.RawQuery == "" && !u.ForceQuery && u.Fragment == "" && !strings.Contains(raw, "#")
}

// Cluster names are a single identity segment when qualification is enabled.
func validClusterName(name string) bool {
	return name != "" && strings.IndexFunc(name, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r) || r == '/' || r == '\\'
	}) == -1
}
