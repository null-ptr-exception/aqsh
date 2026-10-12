package adapter

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Bind           string       `yaml:"bind"`
	PublicURL      string       `yaml:"public_url"`
	BackendURL     string       `yaml:"backend_url"`
	AllowedOrigins []string     `yaml:"allowed_origins"`
	OAuth          OAuthConfig  `yaml:"oauth"`
	Tools          []ToolConfig `yaml:"tools"`
}

type OAuthConfig struct {
	Issuer         string            `yaml:"issuer"`
	RequiredScopes []string          `yaml:"required_scopes"`
	GroupsClaim    string            `yaml:"groups_claim"`
	GroupMapping   map[string]string `yaml:"group_mapping"`
}

type ToolConfig struct {
	Name           string `yaml:"name"`
	Task           string `yaml:"task"`
	Description    string `yaml:"description"`
	ReadOnly       bool   `yaml:"read_only"`
	ResultMode     string `yaml:"result_mode"`
	MaxResultBytes int    `yaml:"max_result_bytes"`
}

func LoadConfig(path string) (Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return Config{}, err
	}
	defer f.Close()
	var cfg Config
	d := yaml.NewDecoder(io.LimitReader(f, 1<<20))
	d.KnownFields(true)
	if err := d.Decode(&cfg); err != nil {
		return cfg, err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return cfg, errors.New("expected one YAML document")
	}
	return cfg, cfg.validate()
}

func (c *Config) validate() error {
	if c.Bind == "" {
		c.Bind = "127.0.0.1:8090"
	}
	if c.OAuth.GroupsClaim == "" {
		c.OAuth.GroupsClaim = "groups"
	}
	public, err := endpoint(c.PublicURL)
	if err != nil || public.Path != "/mcp" {
		return errors.New("public_url must be an HTTPS /mcp URL (HTTP allowed on loopback)")
	}
	backend, err := endpoint(c.BackendURL)
	if err != nil || !loopback(backend.Hostname()) || backend.Path != "" {
		return errors.New("backend_url must be a loopback URL without a path; run alongside aqsh")
	}
	if _, err := endpoint(c.OAuth.Issuer); err != nil {
		return fmt.Errorf("oauth issuer: %w", err)
	}
	if len(c.OAuth.RequiredScopes) == 0 {
		return errors.New("at least one required OAuth scope is required")
	}
	for _, scope := range c.OAuth.RequiredScopes {
		if scope == "" || strings.ContainsAny(scope, " \t\r\n\"\\") {
			return errors.New("invalid OAuth scope")
		}
	}
	for _, group := range c.OAuth.GroupMapping {
		if group == "" || strings.ContainsAny(group, ",\r\n") {
			return errors.New("invalid mapped group")
		}
	}
	names, tasks := map[string]bool{}, map[string]bool{}
	namePattern := regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)
	for i := range c.Tools {
		t := &c.Tools[i]
		if !namePattern.MatchString(t.Name) || strings.HasPrefix(t.Name, "aqsh_") || names[t.Name] {
			return fmt.Errorf("invalid, reserved or duplicate tool name %q", t.Name)
		}
		if t.Task == "" || tasks[t.Task] {
			return fmt.Errorf("empty or duplicate task %q", t.Task)
		}
		if !t.ReadOnly {
			return fmt.Errorf("tool %q must declare read_only: true", t.Name)
		}
		if t.ResultMode == "" {
			t.ResultMode = "omitted"
		}
		if t.ResultMode != "json" && t.ResultMode != "omitted" {
			return errors.New("result_mode must be json or omitted")
		}
		if t.MaxResultBytes == 0 {
			t.MaxResultBytes = 64 * 1024
		}
		if t.MaxResultBytes < 1 || t.MaxResultBytes > 1<<20 {
			return errors.New("max_result_bytes must be 1..1048576")
		}
		names[t.Name], tasks[t.Task] = true, true
	}
	return nil
}

func loopback(host string) bool {
	return host == "localhost" || net.ParseIP(host).IsLoopback()
}

func endpoint(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u == nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
		(u.Scheme != "https" && !(u.Scheme == "http" && loopback(u.Hostname()))) {
		return nil, errors.New("expected HTTPS URL; only loopback may use HTTP")
	}
	return u, nil
}
