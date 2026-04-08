package router

import (
	"fmt"
	"regexp"
	"sync"

	"github.com/yourorg/alb/internal/store"
	"go.uber.org/zap"
)

// compiledRoute pairs a compiled regex with its backend target.
type compiledRoute struct {
	id        int64
	sandboxID string
	pattern   *regexp.Regexp
	targetURL string
	priority  int
}

// MatchResult is returned by the engine when a route is found.
type MatchResult struct {
	RouteID   int64
	TargetURL string
	SandboxID string
}

// Engine is the in-memory, thread-safe regex routing table.
type Engine struct {
	mu     sync.RWMutex
	routes []compiledRoute
	log    *zap.Logger
}

// NewEngine creates an Engine and hydrates it from the persistence store.
func NewEngine(s *store.Store, log *zap.Logger) (*Engine, error) {
	e := &Engine{log: log}
	records, err := s.ListAll()
	if err != nil {
		return nil, fmt.Errorf("load routes from store: %w", err)
	}

	compiled := make([]compiledRoute, 0, len(records))
	for _, r := range records {
		cr, err := compile(r)
		if err != nil {
			log.Warn("skipping route with invalid regex",
				zap.Int64("id", r.ID),
				zap.String("pattern", r.Pattern),
				zap.Error(err),
			)
			continue
		}
		compiled = append(compiled, cr)
	}

	e.routes = compiled
	log.Info("routing engine initialised", zap.Int("routes_loaded", len(compiled)))
	return e, nil
}

// Match walks the priority-ordered route list and returns the first match.
func (e *Engine) Match(path string) (*MatchResult, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	for _, r := range e.routes {
		if r.pattern.MatchString(path) {
			e.log.Debug("route matched",
				zap.String("path", path),
				zap.String("target", r.targetURL),
				zap.Int64("route_id", r.id),
			)
			return &MatchResult{
				RouteID:   r.id,
				TargetURL: r.targetURL,
				SandboxID: r.sandboxID,
			}, true
		}
	}
	return nil, false
}

// Add compiles the regex for a new route and inserts it into the sorted slice.
func (e *Engine) Add(r store.Route) error {
	cr, err := compile(r)
	if err != nil {
		return fmt.Errorf("compile route pattern %q: %w", r.Pattern, err)
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	inserted := false
	newRoutes := make([]compiledRoute, 0, len(e.routes)+1)
	for _, existing := range e.routes {
		if !inserted && cr.priority <= existing.priority {
			newRoutes = append(newRoutes, cr)
			inserted = true
		}
		newRoutes = append(newRoutes, existing)
	}
	if !inserted {
		newRoutes = append(newRoutes, cr)
	}
	e.routes = newRoutes

	e.log.Info("route added to engine",
		zap.Int64("id", r.ID),
		zap.String("pattern", r.Pattern),
		zap.String("target", r.TargetURL),
	)
	return nil
}

// Remove deletes a route from the in-memory table by ID.
func (e *Engine) Remove(id int64) {
	e.mu.Lock()
	defer e.mu.Unlock()

	filtered := e.routes[:0]
	for _, r := range e.routes {
		if r.id != id {
			filtered = append(filtered, r)
		}
	}
	e.routes = filtered
	e.log.Info("route removed from engine", zap.Int64("id", id))
}

// Reload atomically replaces the entire routing table from the store.
func (e *Engine) Reload(s *store.Store) error {
	records, err := s.ListAll()
	if err != nil {
		return fmt.Errorf("reload from store: %w", err)
	}

	compiled := make([]compiledRoute, 0, len(records))
	for _, r := range records {
		cr, err := compile(r)
		if err != nil {
			e.log.Warn("skipping invalid pattern on reload",
				zap.String("pattern", r.Pattern), zap.Error(err))
			continue
		}
		compiled = append(compiled, cr)
	}

	e.mu.Lock()
	e.routes = compiled
	e.mu.Unlock()

	e.log.Info("routing table reloaded", zap.Int("count", len(compiled)))
	return nil
}

// ListAll returns a snapshot of current routes for the admin API.
func (e *Engine) ListAll() []map[string]interface{} {
	e.mu.RLock()
	defer e.mu.RUnlock()

	out := make([]map[string]interface{}, 0, len(e.routes))
	for _, r := range e.routes {
		out = append(out, map[string]interface{}{
			"id":         r.id,
			"sandbox_id": r.sandboxID,
			"pattern":    r.pattern.String(),
			"target_url": r.targetURL,
			"priority":   r.priority,
		})
	}
	return out
}

// compile validates and compiles the raw pattern from a store.Route.
func compile(r store.Route) (compiledRoute, error) {
	re, err := regexp.Compile(r.Pattern)
	if err != nil {
		return compiledRoute{}, err
	}
	return compiledRoute{
		id:        r.ID,
		sandboxID: r.SandboxID,
		pattern:   re,
		targetURL: r.TargetURL,
		priority:  r.Priority,
	}, nil
}
