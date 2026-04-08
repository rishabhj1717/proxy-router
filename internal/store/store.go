package store

import (
	"database/sql"
	"fmt"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// Route is the canonical data record persisted to SQLite.
type Route struct {
	ID        int64
	SandboxID string
	Pattern   string
	TargetURL string
	Priority  int
	CreatedAt time.Time
}

// Store wraps the SQLite connection and exposes typed CRUD operations.
type Store struct {
	db *sql.DB
}

// New opens (or creates) the SQLite database at dbPath and runs migrations.
func New(dbPath string) (*Store, error) {
	dsn := fmt.Sprintf("%s?_journal_mode=WAL&_busy_timeout=5000&_foreign_keys=ON", dbPath)
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return s, nil
}

func (s *Store) migrate() error {
	const schema = `
	CREATE TABLE IF NOT EXISTS routes (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		sandbox_id  TEXT    NOT NULL,
		pattern     TEXT    NOT NULL UNIQUE,
		target_url  TEXT    NOT NULL,
		priority    INTEGER NOT NULL DEFAULT 100,
		created_at  DATETIME NOT NULL DEFAULT (datetime('now'))
	);
	CREATE INDEX IF NOT EXISTS idx_routes_priority ON routes(priority ASC);
	CREATE INDEX IF NOT EXISTS idx_routes_sandbox  ON routes(sandbox_id);
	`
	_, err := s.db.Exec(schema)
	return err
}

// ListAll returns all routes ordered by priority ascending.
func (s *Store) ListAll() ([]Route, error) {
	const q = `
		SELECT id, sandbox_id, pattern, target_url, priority, created_at
		FROM routes
		ORDER BY priority ASC, id ASC
	`
	rows, err := s.db.Query(q)
	if err != nil {
		return nil, fmt.Errorf("list routes: %w", err)
	}
	defer rows.Close()

	var routes []Route
	for rows.Next() {
		var r Route
		if err := rows.Scan(&r.ID, &r.SandboxID, &r.Pattern, &r.TargetURL, &r.Priority, &r.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan route: %w", err)
		}
		routes = append(routes, r)
	}
	return routes, rows.Err()
}

// Create inserts a new route and returns its assigned ID.
func (s *Store) Create(r *Route) (int64, error) {
	const q = `
		INSERT INTO routes (sandbox_id, pattern, target_url, priority)
		VALUES (?, ?, ?, ?)
	`
	res, err := s.db.Exec(q, r.SandboxID, r.Pattern, r.TargetURL, r.Priority)
	if err != nil {
		return 0, fmt.Errorf("create route: %w", err)
	}
	return res.LastInsertId()
}

// Delete removes a route by its ID.
func (s *Store) Delete(id int64) error {
	_, err := s.db.Exec(`DELETE FROM routes WHERE id = ?`, id)
	return err
}

// DeleteBySandbox removes all routes belonging to a sandbox.
func (s *Store) DeleteBySandbox(sandboxID string) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM routes WHERE sandbox_id = ?`, sandboxID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Close releases the database connection.
func (s *Store) Close() error {
	return s.db.Close()
}
