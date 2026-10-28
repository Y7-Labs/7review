package accounting

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

func ensureRouteContract(ctx context.Context, tx *sql.Tx, quote Quote) error {
	qualification, _ := json.Marshal(quote.Qualification)
	routeID, _ := json.Marshal([]string{quote.Qualification.Provider, quote.Qualification.Model, quote.RouteVersion})
	if _, err := tx.ExecContext(ctx, `INSERT INTO budget_routes(id,qualification_digest,qualification) VALUES($1,$2,$3) ON CONFLICT(id) DO NOTHING`, hash(routeID), quote.QualificationDigest, qualification); err != nil {
		return err
	}
	var digest string
	if err := tx.QueryRowContext(ctx, `SELECT qualification_digest FROM budget_routes WHERE id=$1 FOR UPDATE`, hash(routeID)).Scan(&digest); err != nil {
		return err
	}
	if digest != quote.QualificationDigest {
		return errors.New("route qualification version is immutable")
	}
	return nil
}
