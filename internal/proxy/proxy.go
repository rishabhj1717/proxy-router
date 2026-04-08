package proxy

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"

	"github.com/yourorg/alb/internal/router"
	"go.uber.org/zap"
)

// Handler is the main HTTP handler for the ALB data plane.
type Handler struct {
	engine    *router.Engine
	log       *zap.Logger
	transport http.RoundTripper
}

// NewHandler constructs a Handler with a tuned HTTP transport.
func NewHandler(engine *router.Engine, log *zap.Logger, dialTimeout, responseTimeout time.Duration) *Handler {
	transport := &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   dialTimeout,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:          200,
		MaxIdleConnsPerHost:   20,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ResponseHeaderTimeout: responseTimeout,
	}

	return &Handler{
		engine:    engine,
		log:       log,
		transport: transport,
	}
}

// ServeHTTP is the hot path for every proxied request.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	match, ok := h.engine.Match(path)
	if !ok {
		h.log.Info("no route matched",
			zap.String("path", path),
			zap.String("method", r.Method),
		)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprintf(w, `{"error":"no route matched","path":"%s"}`, path)
		return
	}

	target, err := url.Parse(match.TargetURL)
	if err != nil {
		h.log.Error("invalid target URL stored in route",
			zap.String("target", match.TargetURL),
			zap.Int64("route_id", match.RouteID),
		)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"internal routing error"}`))
		return
	}

	proxy := &httputil.ReverseProxy{
		Director:  h.buildDirector(r, target, match),
		Transport: h.transport,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			h.log.Error("proxy backend error",
				zap.String("target", target.String()),
				zap.String("path", path),
				zap.Error(err),
			)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			fmt.Fprintf(w, `{"error":"bad gateway","target":"%s","detail":"%s"}`,
				target.Host, err.Error())
		},
		ModifyResponse: func(resp *http.Response) error {
			resp.Header.Set("X-ALB-Sandbox", match.SandboxID)
			resp.Header.Set("X-ALB-Route-ID", fmt.Sprintf("%d", match.RouteID))
			return nil
		},
	}

	proxy.ServeHTTP(w, r)
}

// buildDirector returns a Director function that rewrites the outbound request.
func (h *Handler) buildDirector(
	original *http.Request,
	target *url.URL,
	match *router.MatchResult,
) func(*http.Request) {
	return func(req *http.Request) {
		req.URL.Scheme = target.Scheme
		req.URL.Host = target.Host

		if target.Path != "" {
			req.URL.Path = target.Path + req.URL.Path
		}

		req.Host = target.Host

		if clientIP, _, err := net.SplitHostPort(original.RemoteAddr); err == nil {
			req.Header.Set("X-Real-IP", clientIP)
		}

		req.Header.Set("X-Sandbox-ID", match.SandboxID)
		req.Header.Set("X-ALB-Route-ID", fmt.Sprintf("%d", match.RouteID))

		h.log.Debug("proxying request",
			zap.String("method", req.Method),
			zap.String("path", req.URL.Path),
			zap.String("target", req.URL.String()),
			zap.String("sandbox", match.SandboxID),
		)
	}
}
