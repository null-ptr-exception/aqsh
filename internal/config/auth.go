package config

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/rophy/aqsh/internal/auth"
)

// Match Kubernetes Service discovery variables without ignoring arbitrary PORT
// settings or misspelled authentication options that share the AQSH_AUTH_ prefix.
var authServiceLinkEnv = regexp.MustCompile(`^AQSH_AUTH(_[A-Z0-9_]+)?_(SERVICE_HOST|SERVICE_PORT(_[A-Z0-9_]+)?|PORT(_[0-9]+_(TCP|UDP|SCTP)(_(PROTO|PORT|ADDR))?)?)$`)

// Authentication settings fail on invalid input instead of silently changing policy.
func loadAuthEnv(mode string) (auth.Config, error) {
	c := auth.Config{RequestTimeout: 5 * time.Second, CacheTTL: 10 * time.Second}
	texts := map[string]*string{
		"AQSH_AUTH_URL": &c.TokenReview.URL, "AQSH_AUTH_CA_FILE": &c.TokenReview.CAFile,
		"AQSH_AUTH_TOKEN_FILE": &c.TokenReview.TokenFile, "AQSH_AUTH_CLUSTER_NAME": &c.ClusterName,
	}
	bools := map[string]*bool{
		"AQSH_AUTH_ALLOW_HTTP":                  &c.TokenReview.AllowHTTP,
		"AQSH_AUTH_TRUST_CLUSTER_EXTRA":         &c.TrustClusterExtra,
		"AQSH_AUTH_INCLUDE_CLUSTER_IN_IDENTITY": &c.IncludeClusterInIdentity,
	}
	durations := map[string]*time.Duration{
		"AQSH_AUTH_REQUEST_TIMEOUT": &c.RequestTimeout, "AQSH_AUTH_CACHE_TTL": &c.CacheTTL,
	}
	for _, entry := range os.Environ() {
		key, value, _ := strings.Cut(entry, "=")
		if !strings.HasPrefix(key, "AQSH_AUTH_") || key == "AQSH_AUTH_MODE" || authServiceLinkEnv.MatchString(key) {
			continue
		}
		if mode != "kubernetes" {
			return c, fmt.Errorf("%s requires AQSH_AUTH_MODE=kubernetes", key)
		}
		var err error
		switch {
		case texts[key] != nil:
			*texts[key] = value
		case bools[key] != nil:
			*bools[key], err = strconv.ParseBool(value)
		case durations[key] != nil:
			*durations[key], err = time.ParseDuration(value)
			if err == nil && key == "AQSH_AUTH_REQUEST_TIMEOUT" && *durations[key] == 0 {
				err = fmt.Errorf("timeout must be positive")
			}
		case key == "AQSH_AUTH_AUDIENCES":
			c.Audiences = strings.Split(value, ",")
		default:
			return c, fmt.Errorf("unknown authentication setting %s", key)
		}
		if err != nil {
			return c, fmt.Errorf("invalid %s", key)
		}
	}
	return c, nil
}
