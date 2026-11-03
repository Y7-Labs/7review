package accounting

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/Y4NN777/7review/agent/review"
)

type RuntimeSettings struct {
	DatabaseURL, BoundaryID, ProjectID string
	Operators                          *review.OperatorDirectory
	RouteBounds                        []RouteBound
}

func (s RuntimeSettings) Enabled() bool { return s.DatabaseURL != "" }

type Runtime struct {
	Authority  *Authority
	BoundaryID string
	db         *sql.DB
}

func (r *Runtime) Close() error {
	if r == nil || r.db == nil {
		return nil
	}
	return r.db.Close()
}
func ReadRuntimeSettings(getenv func(string) string) (RuntimeSettings, error) {
	s := RuntimeSettings{DatabaseURL: strings.TrimSpace(getenv("ACCOUNTING_DATABASE_URL")), BoundaryID: strings.TrimSpace(getenv("ACCOUNTING_BOUNDARY_ID")), ProjectID: strings.TrimSpace(getenv("ACCOUNTING_PROJECT_ID"))}
	if raw := strings.TrimSpace(getenv("ACCOUNTING_ROUTE_BOUNDS_JSON")); raw != "" {
		bounds, err := DecodeRouteBounds([]byte(raw))
		if err != nil {
			return RuntimeSettings{}, errors.New("ACCOUNTING_ROUTE_BOUNDS_JSON is invalid")
		}
		s.RouteBounds = bounds
	}
	return s, s.validate()
}
func (s RuntimeSettings) validate() error {
	if s.DatabaseURL == "" && s.BoundaryID == "" && s.ProjectID == "" && len(s.RouteBounds) == 0 {
		return nil
	}
	if s.DatabaseURL == "" || len(s.DatabaseURL) > 8192 || !validID(s.BoundaryID) || !validID(s.ProjectID) {
		return errors.New("ACCOUNTING_DATABASE_URL, ACCOUNTING_BOUNDARY_ID and ACCOUNTING_PROJECT_ID must be configured together")
	}
	return nil
}
func OpenDatabase(ctx context.Context, dsn string) (*sql.DB, error) {
	if strings.TrimSpace(dsn) == "" {
		return nil, errors.New("accounting database URL is required")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, errors.New("invalid accounting database configuration")
	}
	db.SetMaxOpenConns(16)
	checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err = db.PingContext(checkCtx); err != nil {
		db.Close()
		return nil, errors.New("accounting database is unavailable")
	}
	return db, nil
}
func OpenRuntime(ctx context.Context, settings RuntimeSettings, limits *review.ResourceLimits) (*Runtime, error) {
	if err := settings.validate(); err != nil {
		return nil, err
	}
	if !settings.Enabled() {
		return nil, nil
	}
	if limits == nil {
		return nil, errors.New("shared accounting requires trusted operator ceilings")
	}
	db, err := OpenDatabase(ctx, settings.DatabaseURL)
	if err != nil {
		return nil, err
	}
	catalog, err := NewCatalog(settings.RouteBounds)
	if err != nil {
		db.Close()
		return nil, err
	}
	a, err := NewAuthority(db, Config{RequireServiceJobs: true, BoundaryID: settings.BoundaryID, ProjectID: settings.ProjectID, Limits: *limits, Catalog: catalog, Operators: settings.Operators})
	if err != nil {
		db.Close()
		return nil, err
	}
	checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := a.begin(checkCtx)
	if err != nil {
		db.Close()
		return nil, errors.New("accounting schema is unavailable or incompatible; run explicit migration")
	}
	tx.Rollback()
	return &Runtime{Authority: a, BoundaryID: settings.BoundaryID, db: db}, nil
}
