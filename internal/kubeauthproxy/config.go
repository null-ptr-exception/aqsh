package proxy

import (
	"crypto/tls"
	"crypto/x509"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
)

const (
	inClusterTokenFile = "/var/run/secrets/kubernetes.io/serviceaccount/token"
	inClusterCAFile    = "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"
	tokenReviewPath    = "/apis/authentication.k8s.io/v1/tokenreviews"
)

type Config struct {
	TokenReviewURL string
	Upstream       string
	Port           int
}

func ParseFlags() *Config {
	cfg := &Config{}
	flag.StringVar(&cfg.TokenReviewURL, "token-review-url", getEnv("TOKEN_REVIEW_URL", ""), "URL of TokenReview endpoint (default: in-cluster Kubernetes API)")
	flag.StringVar(&cfg.Upstream, "upstream", getEnv("UPSTREAM", ""), "upstream URL to proxy to")
	flag.IntVar(&cfg.Port, "port", getEnvInt("PORT", 4180), "listen port")
	flag.Parse()
	return cfg
}

func (c *Config) Validate() error {
	if c.Upstream == "" {
		return fmt.Errorf("--upstream is required")
	}
	return nil
}

// NewTokenReviewer creates a TokenReviewer based on the configuration.
// If TokenReviewURL is set, it targets that URL; otherwise it derives the
// URL from in-cluster environment variables. In both cases, the SA token
// is sent as Bearer for authentication and TLS is configured if a CA cert exists.
func (c *Config) NewTokenReviewer() (*HTTPTokenReviewer, error) {
	// 1. Determine endpoint URL
	var url string
	if c.TokenReviewURL != "" {
		url = c.TokenReviewURL + tokenReviewPath
	} else {
		host := os.Getenv("KUBERNETES_SERVICE_HOST")
		port := os.Getenv("KUBERNETES_SERVICE_PORT")
		if host == "" || port == "" {
			return nil, fmt.Errorf("not running in-cluster: KUBERNETES_SERVICE_HOST and KUBERNETES_SERVICE_PORT not set (use --token-review-url for out-of-cluster usage)")
		}
		url = "https://" + net.JoinHostPort(host, port) + tokenReviewPath
	}

	// 2. Configure TLS if CA cert is available
	client := http.DefaultClient
	if caCert, err := os.ReadFile(inClusterCAFile); err == nil {
		caCertPool := x509.NewCertPool()
		if caCertPool.AppendCertsFromPEM(caCert) {
			client = &http.Client{
				Transport: &http.Transport{
					TLSClientConfig: &tls.Config{
						RootCAs: caCertPool,
					},
				},
			}
		}
	}

	// 3. Read SA token for caller authentication
	reviewer := &HTTPTokenReviewer{url: url, client: client}
	if token, err := os.ReadFile(inClusterTokenFile); err == nil {
		reviewer.bearerToken = string(token)
	}
	return reviewer, nil
}

func getEnv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	if value := os.Getenv(key); value != "" {
		if i, err := strconv.Atoi(value); err == nil {
			return i
		}
	}
	return fallback
}
