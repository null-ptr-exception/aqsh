package proxy

import "net/http"

func NewServer(cfg *Config, reviewer TokenReviewer, version string) (http.Handler, error) {
	rp, err := NewReverseProxyHandler(reviewer, cfg.Upstream)
	if err != nil {
		return nil, err
	}

	mux := http.NewServeMux()
	mux.Handle("/healthz", NewHealthHandler(version))
	mux.Handle("/", rp)

	return mux, nil
}
