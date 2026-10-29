package accounting

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Y4NN777/7review/agent/orchestrator"
	"github.com/Y4NN777/7review/agent/policy"
	"github.com/Y4NN777/7review/agent/review"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// Config is trusted host authority. Reconfiguration is deliberately refused in
// v1 rather than creating a new allowance by changing currency/period/version.
type Config struct {
	ReceiptVerifier ReceiptVerifier
	BoundaryID      string
	ProjectID       string
	Limits          review.ResourceLimits
	Catalog         *Catalog
}
type Authority struct {
	db  *sql.DB
	cfg Config
}

func NewAuthority(db *sql.DB, cfg Config) (*Authority, error) {
	if db == nil || !validID(cfg.BoundaryID) || !validID(cfg.ProjectID) || cfg.Catalog == nil {
		return nil, errors.New("database, boundary and route catalog are required")
	}
	if err := validateLimits(cfg.Limits); err != nil {
		return nil, err
	}
	cfg.Limits = cfg.Limits.Clone()
	return &Authority{db: db, cfg: cfg}, nil
}

//go:embed schema.sql
var schemaSQL string

//go:embed migration002.sql
var migration002 string

//go:embed migration003.sql
var migration003 string

//go:embed migration004.sql
var migration004 string

// Migrate is an explicit operator operation, not a fallback on runtime failure.
func Migrate(ctx context.Context, db *sql.DB) error {
	for _, setting := range []string{"fsync", "full_page_writes"} {
		var value string
		if err := db.QueryRowContext(ctx, "SHOW "+setting).Scan(&value); err != nil {
			return err
		}
		if value != "on" {
			return fmt.Errorf("durable accounting requires %s=on", setting)
		}
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `SET LOCAL synchronous_commit = on`); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(734021601)`); err != nil {
		return err
	}
	var initialized bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_class WHERE relnamespace=current_schema()::regnamespace AND relname='budget_schema')`).Scan(&initialized); err != nil {
		return err
	}
	if !initialized {
		var existing int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM pg_class WHERE relnamespace=current_schema()::regnamespace AND left(relname,7)='budget_'`).Scan(&existing); err != nil {
			return err
		}
		if existing != 0 {
			return errors.New("partial accounting schema requires recovery, not initialization")
		}
		if _, err = tx.ExecContext(ctx, schemaSQL); err != nil {
			return err
		}
	}
	var version int
	if err = tx.QueryRowContext(ctx, `SELECT version FROM budget_schema WHERE singleton=true`).Scan(&version); err != nil {
		return err
	}
	for _, table := range []string{"budget_attempts", "budget_scopes", "budget_actions", "budget_holds", "budget_routes"} {
		if err = requireRelation(ctx, tx, table); err != nil {
			return err
		}
	}
	if version == 1 {
		if _, err = tx.ExecContext(ctx, migration002); err != nil {
			return err
		}
		version = 2
	}
	if version == 2 {
		if _, err = tx.ExecContext(ctx, migration003); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE budget_schema SET version=3 WHERE singleton=true`); err != nil {
			return err
		}
		version = 3
	}
	if version == 3 {
		// Existing v3 tables must be intact before extending the ledger.
		for _, table := range []string{"budget_heads", "budget_intakes", "budget_intake_conflicts", "budget_receipts", "budget_quarantine"} {
			if err = requireRelation(ctx, tx, table); err != nil {
				return err
			}
		}
		if _, err = tx.ExecContext(ctx, migration004); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE budget_schema SET version=4 WHERE singleton=true`); err != nil {
			return err
		}
		version = 4
	}
	if version != 4 {
		return errors.New("incompatible accounting schema")
	}
	for _, table := range []string{"budget_receipts", "budget_quarantine"} {
		if err = requireRelation(ctx, tx, table); err != nil {
			return err
		}
	}
	for _, table := range []string{"budget_heads", "budget_intakes", "budget_intake_conflicts"} {
		if err = requireRelation(ctx, tx, table); err != nil {
			return err
		}
	}
	for _, table := range []string{"budget_inbox", "budget_inbox_conflicts"} {
		if err = requireRelation(ctx, tx, table); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func requireRelation(ctx context.Context, tx *sql.Tx, name string) error {
	var present bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_class WHERE relnamespace=current_schema()::regnamespace AND relname=$1)`, name).Scan(&present); err != nil {
		return err
	}
	if !present {
		return fmt.Errorf("accounting relation %s missing; restore required", name)
	}
	return nil
}

func validateLimits(limits review.ResourceLimits) error {
	data, err := json.Marshal(struct {
		SchemaVersion int                   `json:"schema_version"`
		Limits        review.ResourceLimits `json:"limits"`
	}{1, limits})
	if err != nil {
		return err
	}
	_, err = policy.DecodeOperatorCeilings(data)
	return err
}
func validID(s string) bool {
	return strings.TrimSpace(s) != "" && len(s) <= 256 && utf8.ValidString(s) && !strings.ContainsRune(s, 0)
}
func canonicalDigest(s string) bool {
	if len(s) != 71 || !strings.HasPrefix(s, "sha256:") {
		return false
	}
	_, err := hex.DecodeString(s[7:])
	return err == nil && s == strings.ToLower(s)
}
func hash(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

type attemptBinding struct {
	ProjectID        string
	Attempt          review.AttemptIdentity
	Execution        review.ExecutionContext
	Attestation      *review.SnapshotAttestation
	PolicyLimits     *review.ResourceLimits
	OperatorCeilings review.ResourceLimits
}

func (a *Authority) binding(i orchestrator.DispatchIntent) ([]byte, error) {
	if err := i.Attempt.Validate(); err != nil {
		return nil, err
	}
	if err := i.Execution.Validate(); err != nil {
		return nil, err
	}
	if !validID(i.Attempt.AttemptID) || !validID(i.Attempt.WorkIntentID) || !validID(i.Attempt.Change.Host) || !validID(i.Attempt.Change.RepositoryID) || !canonicalDigest(i.PolicyDigest) || i.Attempt.PolicyDigest != i.PolicyDigest || i.Execution.TrustedBoundaryID != a.cfg.BoundaryID {
		return nil, errors.New("invalid canonical accounting identity")
	}
	if i.Attestation == nil {
		return nil, errors.New("verified attestation required")
	}
	if err := i.Attestation.Validate(i.Attempt.Change); err != nil {
		return nil, err
	}
	if i.Attestation.Snapshot != i.Attempt.Snapshot || i.Execution.ComparisonTree != i.Attestation.ComparisonTree {
		return nil, errors.New("attempt/execution snapshot mismatch")
	}
	switch i.Attempt.State {
	case review.AttemptAccepted, review.AttemptReady, review.AttemptInvestigating:
	default:
		return nil, errors.New("attempt cannot dispatch in this state")
	}
	if i.PolicyLimits == nil || !reflect.DeepEqual(i.OperatorCeilings, a.cfg.Limits) {
		return nil, errors.New("missing policy limits or stale operator authority")
	}
	if err := validateLimits(*i.PolicyLimits); err != nil {
		return nil, err
	}
	if i.PolicyLimits.Currency != a.cfg.Limits.Currency || !reflect.DeepEqual(i.PolicyLimits.Change.Period, a.cfg.Limits.Change.Period) || !reflect.DeepEqual(i.PolicyLimits.Project.Period, a.cfg.Limits.Project.Period) {
		return nil, errors.New("incomparable scope currency or period")
	}
	for n, scope := range limitScopes(*i.PolicyLimits) {
		if exceeds(scope, limitScopes(a.cfg.Limits)[n]) {
			return nil, errors.New("policy exceeds operator authority")
		}
	}
	return json.Marshal(attemptBinding{ProjectID: a.cfg.ProjectID, Attempt: i.Attempt, Execution: i.Execution, Attestation: i.Attestation, PolicyLimits: i.PolicyLimits, OperatorCeilings: i.OperatorCeilings})
}

// RegisterAttempt must be invoked by trusted intake, never inferred from a
// dispatch request. Registration cannot reopen or mutate a closed attempt.
func (a *Authority) RegisterAttempt(ctx context.Context, i orchestrator.DispatchIntent) error {
	binding, err := a.binding(i)
	if err != nil {
		return err
	}
	tx, err := a.begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT INTO budget_attempts(id,binding_digest,binding) VALUES($1,$2,$3) ON CONFLICT(id) DO NOTHING`, i.Attempt.AttemptID, hash(binding), binding); err != nil {
		return err
	}
	var stored string
	var active bool
	if err = tx.QueryRowContext(ctx, `SELECT binding_digest,active FROM budget_attempts WHERE id=$1 FOR UPDATE`, i.Attempt.AttemptID).Scan(&stored, &active); err != nil {
		return err
	}
	if stored != hash(binding) || !active {
		return errors.New("attempt registration conflicts or is closed")
	}
	return tx.Commit()
}

func (a *Authority) CloseAttempt(ctx context.Context, id string) error {
	tx, err := a.begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE budget_attempts SET active=false WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return errors.New("unknown attempt")
	}
	return tx.Commit()
}

type scopeRef struct {
	id, kind, authority, origin string
	limit                       review.ResourceLimitScope
}

func (a *Authority) scopeRefs(i orchestrator.DispatchIntent, binding []byte) []scopeRef {
	project, _ := json.Marshal([]string{a.cfg.BoundaryID, a.cfg.ProjectID})
	change, _ := json.Marshal(struct {
		Boundary string
		Change   review.ChangeKey
	}{a.cfg.BoundaryID, i.Attempt.Change})
	operator, _ := json.Marshal(a.cfg.Limits)
	limits := limitScopes(*i.PolicyLimits)
	return []scopeRef{{"attempt:" + hash([]byte(i.Attempt.AttemptID)), "attempt", hash(binding), "lifetime", limits[0]}, {"change:" + hash(change), "change", hash(operator), "", limits[1]}, {"project:" + hash(project), "project", hash(operator), "", limits[2]}}
}

func (a *Authority) ReserveAndBegin(ctx context.Context, i orchestrator.DispatchIntent) (orchestrator.DispatchPermit, error) {
	if !validID(i.ActionID) || !validID(i.Provider) || !validID(i.Model) || !validID(i.Role) || !validID(i.Step) {
		return orchestrator.DispatchPermit{}, errors.New("action and route identity required")
	}
	binding, err := a.binding(i)
	if err != nil {
		return orchestrator.DispatchPermit{}, err
	}
	request, err := json.Marshal(i)
	if err != nil {
		return orchestrator.DispatchPermit{}, err
	}
	tx, err := a.begin(ctx)
	if err != nil {
		return orchestrator.DispatchPermit{}, err
	}
	defer tx.Rollback()
	var tracking sql.NullString
	if err = tx.QueryRowContext(ctx, `SELECT tracking_id FROM budget_attempts WHERE id=$1`, i.Attempt.AttemptID).Scan(&tracking); err != nil {
		return orchestrator.DispatchPermit{}, err
	}
	if tracking.Valid {
		var current sql.NullString
		if err = tx.QueryRowContext(ctx, `SELECT current_attempt_id FROM budget_heads WHERE id=$1 FOR UPDATE`, tracking.String).Scan(&current); err != nil {
			return orchestrator.DispatchPermit{}, err
		}
		if current.String != i.Attempt.AttemptID {
			return orchestrator.DispatchPermit{}, errors.New("attempt is no longer current")
		}
	}
	var stored string
	var active bool
	if err = tx.QueryRowContext(ctx, `SELECT binding_digest,active FROM budget_attempts WHERE id=$1 FOR UPDATE`, i.Attempt.AttemptID).Scan(&stored, &active); err != nil {
		return orchestrator.DispatchPermit{}, fmt.Errorf("attempt is not registered: %w", err)
	}
	if stored != hash(binding) {
		return orchestrator.DispatchPermit{}, errors.New("dispatch does not match registered attempt")
	}
	var actionDigest string
	err = tx.QueryRowContext(ctx, `SELECT intent_digest FROM budget_actions WHERE id=$1`, i.ActionID).Scan(&actionDigest)
	if err == nil {
		if actionDigest != hash(request) {
			return orchestrator.DispatchPermit{}, errors.New("action identity conflicts with immutable request")
		}
		return orchestrator.DispatchPermit{ID: i.ActionID, RequestDigest: i.RequestDigest, AlreadyStarted: true}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return orchestrator.DispatchPermit{}, err
	}
	if !active {
		return orchestrator.DispatchPermit{}, errors.New("attempt is closed")
	}
	refs := a.scopeRefs(i, binding)
	// Scope identity excludes renewal period, keeping every old hold visible.
	// Every writer takes the same attempt -> change -> project lock order.
	for _, ref := range refs {
		if _, err = tx.ExecContext(ctx, `INSERT INTO budget_scopes(id,kind,authority_digest) VALUES($1,$2,$3) ON CONFLICT(id) DO NOTHING`, ref.id, ref.kind, ref.authority); err != nil {
			return orchestrator.DispatchPermit{}, err
		}
		var authority string
		if err = tx.QueryRowContext(ctx, `SELECT authority_digest FROM budget_scopes WHERE id=$1 FOR UPDATE`, ref.id).Scan(&authority); err != nil {
			return orchestrator.DispatchPermit{}, err
		}
		if authority != ref.authority {
			return orchestrator.DispatchPermit{}, errors.New("scope reconfiguration requires explicit migration")
		}
	}
	var now time.Time
	if err = tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return orchestrator.DispatchPermit{}, err
	}
	if !now.Before(i.Execution.DeadlineAt) {
		return orchestrator.DispatchPermit{}, errors.New("execution deadline expired")
	}
	quote, err := a.cfg.Catalog.Quote(i, now)
	if err != nil {
		return orchestrator.DispatchPermit{}, err
	}
	if quote.Currency != a.cfg.Limits.Currency {
		return orchestrator.DispatchPermit{}, errors.New("route price currency mismatch")
	}
	if err = ensureRouteContract(ctx, tx, quote); err != nil {
		return orchestrator.DispatchPermit{}, err
	}
	// Contract locking can wait across price expiry or a period boundary.
	if err = tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return orchestrator.DispatchPermit{}, err
	}
	if !now.Before(i.Execution.DeadlineAt) {
		return orchestrator.DispatchPermit{}, errors.New("execution deadline expired during admission")
	}
	quote, err = a.cfg.Catalog.Quote(i, now)
	if err != nil {
		return orchestrator.DispatchPermit{}, err
	}
	upper := review.ResourceLimitScope{ModelCalls: 1, InputTokens: quote.InputTokens, OutputTokens: quote.OutputTokens, MoneyMicro: quote.MoneyMicro, ActiveMS: quote.ActiveMS}
	for n := range refs {
		if refs[n].kind != "attempt" {
			refs[n].origin, err = periodID(refs[n].limit.Period, now)
			if err != nil {
				return orchestrator.DispatchPermit{}, err
			}
		}
		if err = checkCapacity(ctx, tx, refs[n], upper); err != nil {
			return orchestrator.DispatchPermit{}, err
		}
	}
	upperJSON, _ := json.Marshal(upper)
	quoteJSON, _ := json.Marshal(quote)
	if _, err = tx.ExecContext(ctx, `INSERT INTO budget_actions(id,attempt_id,request_digest,intent_digest,request,quote,upper_amount,state) VALUES($1,$2,$3,$4,$5,$6,$7,'sending')`, i.ActionID, i.Attempt.AttemptID, i.RequestDigest, hash(request), request, quoteJSON, upperJSON); err != nil {
		return orchestrator.DispatchPermit{}, err
	}
	for _, ref := range refs {
		if _, err = tx.ExecContext(ctx, `INSERT INTO budget_holds(action_id,scope_id,origin_period) VALUES($1,$2,$3)`, i.ActionID, ref.id, ref.origin); err != nil {
			return orchestrator.DispatchPermit{}, err
		}
	}
	if err = tx.Commit(); err != nil {
		return orchestrator.DispatchPermit{}, err
	}
	notAfter := i.Execution.DeadlineAt
	if quote.Qualification.ValidUntil.Before(notAfter) {
		notAfter = quote.Qualification.ValidUntil
	}
	return orchestrator.DispatchPermit{ID: i.ActionID, RequestDigest: i.RequestDigest, ActiveMS: quote.ActiveMS, NotAfter: notAfter}, nil
}

func checkCapacity(ctx context.Context, tx *sql.Tx, ref scopeRef, requested review.ResourceLimitScope) error {
	var incident bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM budget_actions a JOIN budget_holds h ON h.action_id=a.id WHERE h.scope_id=$1 AND a.integrity_incident)`, ref.id).Scan(&incident); err != nil {
		return err
	}
	if incident {
		return ErrIntegrity
	}
	used := requested
	if exceeds(used, ref.limit) {
		return fmt.Errorf("%s budget exhausted", ref.kind)
	}
	rows, err := tx.QueryContext(ctx, `SELECT CASE WHEN a.settled THEN a.actual_amount ELSE a.upper_amount END FROM budget_actions a JOIN budget_holds h ON h.action_id=a.id WHERE h.scope_id=$1 AND (NOT a.settled OR h.origin_period=$2)`, ref.id, ref.origin)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		var amount review.ResourceLimitScope
		if err = rows.Scan(&raw); err != nil {
			return err
		}
		if err = json.Unmarshal(raw, &amount); err != nil {
			return err
		}
		current, additional, cap := amounts(used), amounts(amount), amounts(ref.limit)
		for n := range current {
			if additional[n] < 0 || additional[n] > cap[n]-current[n] {
				return fmt.Errorf("%s budget exhausted", ref.kind)
			}
			current[n] += additional[n]
		}
		used = fromAmounts(current)
	}
	return rows.Err()
}

func (a *Authority) RecordOutcome(ctx context.Context, p orchestrator.DispatchPermit, o orchestrator.DispatchOutcome) error {
	if !validID(p.ID) || !canonicalDigest(p.RequestDigest) || o.UsageKnown || o.ElapsedMS < 0 || len(o.Response) > 8<<20 || hash(o.Response) != o.ResponseDigest {
		return errors.New("invalid outcome or unsupported unverified usage")
	}
	raw, err := json.Marshal(o)
	if err != nil {
		return err
	}
	tx, err := a.begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var digest string
	var previous []byte
	if err = lockActionScopes(ctx, tx, p.ID); err != nil {
		return err
	}
	var upperJSON []byte
	var incident bool
	if err = tx.QueryRowContext(ctx, `SELECT request_digest,outcome,upper_amount,integrity_incident FROM budget_actions WHERE id=$1 FOR UPDATE`, p.ID).Scan(&digest, &previous, &upperJSON, &incident); err != nil {
		return err
	}
	if digest != p.RequestDigest {
		return errors.New("outcome request identity mismatch")
	}
	if previous != nil {
		if !bytes.Equal(previous, raw) {
			return errors.New("outcome conflicts with durable receipt")
		}
		if incident {
			return ErrIntegrity
		}
		return nil
	}
	state := "uncertain"
	if o.ResponseReceived {
		state = "result_recorded"
	}
	var upper review.ResourceLimitScope
	if err = json.Unmarshal(upperJSON, &upper); err != nil {
		return err
	}
	incident = incident || o.ElapsedMS > upper.ActiveMS
	if _, err = tx.ExecContext(ctx, `UPDATE budget_actions SET outcome=$2,state=$3,integrity_incident=$4 WHERE id=$1`, p.ID, raw, state, incident); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	if incident {
		return ErrIntegrity
	}
	return nil
}

func limitScopes(l review.ResourceLimits) []review.ResourceLimitScope {
	return []review.ResourceLimitScope{l.Attempt, l.Change, l.Project}
}
func amounts(s review.ResourceLimitScope) [7]int64 {
	return [7]int64{s.ModelCalls, s.ToolCalls, s.InputTokens, s.OutputTokens, s.MoneyMicro, s.ActiveMS, s.ObservationBytes}
}
func fromAmounts(v [7]int64) review.ResourceLimitScope {
	return review.ResourceLimitScope{ModelCalls: v[0], ToolCalls: v[1], InputTokens: v[2], OutputTokens: v[3], MoneyMicro: v[4], ActiveMS: v[5], ObservationBytes: v[6]}
}
func exceeds(used, limit review.ResourceLimitScope) bool {
	u, l := amounts(used), amounts(limit)
	for n := range u {
		if u[n] < 0 || u[n] > l[n] {
			return true
		}
	}
	return false
}

func periodID(p *review.FixedBudgetPeriod, now time.Time) (string, error) {
	if p == nil || p.Kind != "fixed" || p.DurationMS <= 0 || now.Before(p.Anchor) {
		return "", errors.New("invalid or not-yet-active fixed period")
	}
	// Millisecond arithmetic avoids time.Duration's roughly 292-year range.
	delta := now.UnixMilli() - p.Anchor.UnixMilli()
	if delta < 0 {
		return "", errors.New("period arithmetic overflow")
	}
	return fmt.Sprintf("%d", delta/p.DurationMS), nil
}

func (a *Authority) begin(ctx context.Context) (*sql.Tx, error) {
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `SET LOCAL synchronous_commit=on`); err != nil {
		tx.Rollback()
		return nil, err
	}
	var version int
	if err = tx.QueryRowContext(ctx, `SELECT version FROM budget_schema WHERE singleton=true FOR SHARE`).Scan(&version); err != nil {
		tx.Rollback()
		return nil, err
	}
	if version != 4 {
		tx.Rollback()
		return nil, errors.New("incompatible accounting schema")
	}
	return tx, nil
}
