package auth

import (
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode"
)

type Config struct {
	IncludeClusterInIdentity bool
	TrustClusterExtra        bool
	ClusterName              string
	Audiences                []string
	RequestTimeout           time.Duration
	CacheTTL                 time.Duration
	TokenReview              Endpoint
}

type Endpoint struct {
	URL       string
	CAFile    string
	TokenFile string
	AllowHTTP bool
}

func (c *Config) Validate() error {
	if len(c.Audiences) == 0 {
		return fmt.Errorf("AQSH_AUTH_AUDIENCES requires at least one audience")
	}
	for _, audience := range c.Audiences {
		if strings.TrimSpace(audience) != audience || audience == "" {
			return fmt.Errorf("AQSH_AUTH_AUDIENCES must contain nonempty audiences without surrounding whitespace")
		}
	}
	if c.RequestTimeout == 0 {
		c.RequestTimeout = 5 * time.Second
	}
	if c.RequestTimeout < time.Millisecond || c.RequestTimeout > time.Minute {
		return fmt.Errorf("AQSH_AUTH_REQUEST_TIMEOUT must be between 1ms and 1m")
	}
	if c.CacheTTL < 0 || c.CacheTTL > time.Minute {
		return fmt.Errorf("AQSH_AUTH_CACHE_TTL must be between 0 and 1m")
	}
	if c.ClusterName != "" && !validClusterName(c.ClusterName) {
		return fmt.Errorf("AQSH_AUTH_CLUSTER_NAME must be a single CSV-safe identity segment")
	}
	if c.IncludeClusterInIdentity && !c.TrustClusterExtra && c.ClusterName == "" {
		return fmt.Errorf("AQSH_AUTH_CLUSTER_NAME is required for direct cluster-qualified identity")
	}
	if c.TrustClusterExtra && c.ClusterName != "" {
		return fmt.Errorf("AQSH_AUTH_CLUSTER_NAME cannot be combined with AQSH_AUTH_TRUST_CLUSTER_EXTRA")
	}
	if c.TokenReview.TokenFile == "" {
		return fmt.Errorf("AQSH_AUTH_TOKEN_FILE is required")
	}
	if !validEndpoint(c.TokenReview.URL, c.TokenReview.AllowHTTP) {
		return fmt.Errorf("AQSH_AUTH_URL requires HTTPS (or AQSH_AUTH_ALLOW_HTTP) without credentials, query or fragment")
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
		return unicode.IsSpace(r) || unicode.IsControl(r) || r == '/' || r == '\\' || r == ','
	}) == -1
}
