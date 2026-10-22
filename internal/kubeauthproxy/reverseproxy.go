package proxy

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"
)

// Request headers forwarded to upstream
const (
	HeaderForwardedUser         = "X-Forwarded-User"
	HeaderForwardedGroups       = "X-Forwarded-Groups"
	HeaderForwardedExtraCluster = "X-Forwarded-Extra-Cluster-Name"
)

type ReverseProxyHandler struct {
	reviewer TokenReviewer
	proxy    *httputil.ReverseProxy
}

func NewReverseProxyHandler(reviewer TokenReviewer, upstreamURL string) (*ReverseProxyHandler, error) {
	target, err := url.Parse(upstreamURL)
	if err != nil {
		return nil, fmt.Errorf("parsing upstream URL: %w", err)
	}

	return &ReverseProxyHandler{
		reviewer: reviewer,
		proxy:    httputil.NewSingleHostReverseProxy(target),
	}, nil
}

type responseWriter struct {
	http.ResponseWriter
	status int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.status = code
	rw.ResponseWriter.WriteHeader(code)
}

func (rw *responseWriter) Flush() {
	if f, ok := rw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (h *ReverseProxyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	rw := &responseWriter{ResponseWriter: w, status: http.StatusOK}
	var user string

	defer func() {
		slog.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rw.status,
			"user", user,
			"duration", time.Since(start),
			"remote", r.RemoteAddr,
		)
	}()

	token := extractBearerToken(r)
	if token == "" {
		rw.WriteHeader(http.StatusUnauthorized)
		return
	}

	result, err := h.reviewer.Review(r.Context(), token)
	if err != nil {
		slog.Error("token review failed", "err", err)
		rw.WriteHeader(http.StatusInternalServerError)
		return
	}

	if !result.Status.Authenticated {
		rw.WriteHeader(http.StatusUnauthorized)
		return
	}

	user = result.Status.User.Username
	r.Header.Set(HeaderForwardedUser, result.Status.User.Username)
	if len(result.Status.User.Groups) > 0 {
		r.Header.Set(HeaderForwardedGroups, strings.Join(result.Status.User.Groups, ","))
	}
	if clusterNames, ok := result.Status.User.Extra[ExtraKeyClusterName]; ok && len(clusterNames) > 0 {
		r.Header.Set(HeaderForwardedExtraCluster, clusterNames[0])
	}

	r.Header.Del("Authorization")

	h.proxy.ServeHTTP(rw, r)
}

func extractBearerToken(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if auth == "" {
		return ""
	}
	const prefix = "Bearer "
	if !strings.HasPrefix(auth, prefix) {
		return ""
	}
	return strings.TrimPrefix(auth, prefix)
}
