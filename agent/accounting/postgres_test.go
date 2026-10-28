package accounting

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Y4NN777/7review/agent/llm"
	"github.com/Y4NN777/7review/agent/orchestrator"
	"github.com/Y4NN777/7review/agent/review"
)

func postgresFixture(t *testing.T) (*sql.DB, string) {
	t.Helper()
	dsn := os.Getenv("SEVENREVIEW_TEST_POSTGRES")
	if dsn == "" {
		t.Skip("set SEVENREVIEW_TEST_POSTGRES to a disposable PostgreSQL database")
	}
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	var b [8]byte
	if _, err = rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	schema := "budget_test_" + hex.EncodeToString(b[:])
	if _, err = admin.Exec(`CREATE SCHEMA ` + schema); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	scoped := u.String()
	db, err := sql.Open("pgx", scoped)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(24)
	t.Cleanup(func() { db.Close(); _, _ = admin.Exec(`DROP SCHEMA ` + schema + ` CASCADE`); admin.Close() })
	if err = Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	return db, scoped
}

func ledgerLimits() review.ResourceLimits {
	s := review.ResourceLimitScope{ModelCalls: 10, ToolCalls: 10, InputTokens: 1000, OutputTokens: 1000, MoneyMicro: 1000, ActiveMS: 1000, ObservationBytes: 1000}
	p := &review.FixedBudgetPeriod{Kind: "fixed", Anchor: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), DurationMS: 86400000}
	l := review.ResourceLimits{Currency: "USD", Attempt: s, Change: s, Project: s}
	l.Change.Period = p
	l.Project.Period = p
	return l
}
func ledgerIntent(t *testing.T, db *sql.DB, attempt, change string, limits review.ResourceLimits) orchestrator.DispatchIntent {
	t.Helper()
	var now time.Time
	if err := db.QueryRow(`SELECT clock_timestamp()`).Scan(&now); err != nil {
		t.Fatal(err)
	}
	i := quoteIntent()
	i.ActionID = attempt + "-action"
	i.Role = "reasoner"
	i.Step = "review"
	i.OperatorCeilings = limits
	i.PolicyLimits = &limits
	i.PolicyDigest = "sha256:" + strings.Repeat("a", 64)
	snapshot := review.SnapshotIdentity{RepositoryID: "org/repo", BaseRevision: "base", HeadRevision: "head", FileManifestDigest: "sha256:" + strings.Repeat("b", 64)}
	i.Attempt = review.AttemptIdentity{AttemptID: attempt, WorkIntentID: attempt + "-work", IntentKind: review.IntentManual, Change: review.ChangeKey{Provider: "github", Host: "github.com", RepositoryID: "org/repo", ChangeNumber: change}, Snapshot: snapshot, PolicyDigest: i.PolicyDigest, Generation: 1, Version: 1, State: review.AttemptInvestigating, CreatedAt: now, UpdatedAt: now}
	i.Attestation = &review.SnapshotAttestation{Snapshot: snapshot, ProducerID: "trusted-test", ComparisonTree: "head", VerifiedAt: now, Verified: true}
	i.Execution = review.ExecutionContext{Mode: review.ExecutionService, ProducerID: "worker", ComparisonTree: "head", Persistence: review.PersistenceDurableService, TrustedBoundaryID: "test-authority", DeadlineAt: now.Add(time.Hour)}
	return i
}
func ledgerAuthority(t *testing.T, db *sql.DB, l review.ResourceLimits) *Authority {
	t.Helper()
	r := testRoute()
	r.ValidFrom = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	r.ValidUntil = time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)
	c, err := NewCatalog([]RouteBound{r})
	if err != nil {
		t.Fatal(err)
	}
	a, err := NewAuthority(db, Config{BoundaryID: "test-authority", ProjectID: "team/project", Limits: l, Catalog: c})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestPostgresConcurrentLastUnitAcrossAuthorities(t *testing.T) {
	for _, scope := range []string{"attempt", "change", "project", "project other repository"} {
		t.Run(scope, func(t *testing.T) {
			db, _ := postgresFixture(t)
			limits := ledgerLimits()
			switch scope {
			case "attempt":
				limits.Attempt.ModelCalls = 1
			case "change":
				limits.Change.ModelCalls = 1
			case "project", "project other repository":
				limits.Project.ModelCalls = 1
			}
			a := ledgerAuthority(t, db, limits)
			b := ledgerAuthority(t, db, limits)
			i := ledgerIntent(t, db, "one", "7", limits)
			j := i
			j.ActionID = "second"
			if scope != "attempt" {
				j = ledgerIntent(t, db, "two", "7", limits)
			}
			if strings.HasPrefix(scope, "project") {
				j.Attempt.Change.ChangeNumber = "8"
			}
			if scope == "project other repository" {
				j.Attempt.Change.RepositoryID = "org/other"
				j.Attempt.Snapshot.RepositoryID = "org/other"
				j.Attestation.Snapshot = j.Attempt.Snapshot
			}
			for _, intent := range []orchestrator.DispatchIntent{i, j} {
				if err := a.RegisterAttempt(context.Background(), intent); err != nil {
					t.Fatal(err)
				}
			}
			var successes atomic.Int32
			var wg sync.WaitGroup
			start := make(chan struct{})
			for n, intent := range []orchestrator.DispatchIntent{i, j} {
				wg.Add(1)
				go func(n int, intent orchestrator.DispatchIntent) {
					defer wg.Done()
					<-start
					authority := a
					if n == 1 {
						authority = b
					}
					if _, err := authority.ReserveAndBegin(context.Background(), intent); err == nil {
						successes.Add(1)
					}
				}(n, intent)
			}
			close(start)
			wg.Wait()
			if successes.Load() != 1 {
				t.Fatalf("last unit admitted %d calls", successes.Load())
			}
			var actions, holds int
			if err := db.QueryRow(`SELECT count(*) FROM budget_actions`).Scan(&actions); err != nil {
				t.Fatal(err)
			}
			_ = db.QueryRow(`SELECT count(*) FROM budget_holds`).Scan(&holds)
			if actions != 1 || holds != 3 {
				t.Fatalf("partial hierarchical commit: actions=%d holds=%d", actions, holds)
			}
		})
	}
}

func TestPostgresRestartReplayCannotAuthorizeResend(t *testing.T) {
	db, dsn := postgresFixture(t)
	limits := ledgerLimits()
	a := ledgerAuthority(t, db, limits)
	i := ledgerIntent(t, db, "one", "7", limits)
	if err := a.RegisterAttempt(context.Background(), i); err != nil {
		t.Fatal(err)
	}
	p, err := a.ReserveAndBegin(context.Background(), i)
	if err != nil {
		t.Fatal(err)
	}
	if p.AlreadyStarted {
		t.Fatal("new action marked replay")
	}
	reopened, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	b := ledgerAuthority(t, reopened, limits)
	replay, err := b.ReserveAndBegin(context.Background(), i)
	if err != nil || !replay.AlreadyStarted || replay.ID != p.ID {
		t.Fatalf("restart lost sending marker: %#v %v", replay, err)
	}
	i.Payload = append(i.Payload, ' ')
	i.RequestDigest = digestBytes(i.Payload)
	if _, err = b.ReserveAndBegin(context.Background(), i); err == nil {
		t.Fatal("same action accepted different bytes")
	}
}

func TestPostgresUnknownOutcomeAndCancellationKeepSharedHold(t *testing.T) {
	db, _ := postgresFixture(t)
	limits := ledgerLimits()
	limits.Change.ModelCalls = 1
	a := ledgerAuthority(t, db, limits)
	i := ledgerIntent(t, db, "old", "7", limits)
	if err := a.RegisterAttempt(context.Background(), i); err != nil {
		t.Fatal(err)
	}
	p, err := a.ReserveAndBegin(context.Background(), i)
	if err != nil {
		t.Fatal(err)
	}
	o := orchestrator.DispatchOutcome{Response: []byte("answer"), ResponseReceived: true, ResponseDigest: digestBytes([]byte("answer")), ElapsedMS: 1}
	if err = a.RecordOutcome(context.Background(), p, o); err != nil {
		t.Fatal(err)
	}
	if err = a.RecordOutcome(context.Background(), p, o); err != nil {
		t.Fatal(err)
	}
	if err = a.CloseAttempt(context.Background(), "old"); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE budget_holds SET origin_period='previous-period' WHERE scope_id NOT LIKE 'attempt:%'`); err != nil {
		t.Fatal(err)
	}
	j := ledgerIntent(t, db, "replacement", "7", limits)
	j.Attempt.Snapshot.HeadRevision = "new-head"
	j.Attestation.Snapshot = j.Attempt.Snapshot
	j.Attestation.ComparisonTree = "new-head"
	j.Execution.ComparisonTree = "new-head"
	if err = a.RegisterAttempt(context.Background(), j); err != nil {
		t.Fatal(err)
	}
	if _, err = a.ReserveAndBegin(context.Background(), j); err == nil {
		t.Fatal("new revision erased unknown usage hold")
	}
	i.ActionID = "cancelled-new-call"
	if _, err = a.ReserveAndBegin(context.Background(), i); err == nil {
		t.Fatal("closed attempt spent again")
	}
	o.UsageKnown = true
	if err = a.RecordOutcome(context.Background(), p, o); err == nil {
		t.Fatal("unverified UsageKnown flag settled accounting")
	}
}

type ledgerProvider struct {
	t     *testing.T
	db    *sql.DB
	calls int
}

func (*ledgerProvider) Name() string { return "qualified" }
func (p *ledgerProvider) Complete(_ context.Context, _ llm.LLMRequest) (string, error) {
	p.calls++
	var sending, holds int
	if err := p.db.QueryRow(`SELECT count(*) FROM budget_actions WHERE state='sending'`).Scan(&sending); err != nil {
		p.t.Fatal(err)
	}
	if err := p.db.QueryRow(`SELECT count(*) FROM budget_holds`).Scan(&holds); err != nil {
		p.t.Fatal(err)
	}
	if sending != 1 || holds != 3 {
		p.t.Fatalf("provider preceded durable admission: sending=%d holds=%d", sending, holds)
	}
	return "answer", nil
}

func TestPostgresAuthorityGovernsActualProviderDispatch(t *testing.T) {
	for _, limiting := range []string{"funded", "money", "input", "output", "active", "calls"} {
		t.Run(limiting, func(t *testing.T) {
			db, _ := postgresFixture(t)
			l := ledgerLimits()
			switch limiting {
			case "money":
				l.Project.MoneyMicro = 3
			case "input":
				l.Change.InputTokens = 99
			case "output":
				l.Attempt.OutputTokens = 19
			case "active":
				l.Attempt.ActiveMS = 49
			case "calls":
				l.Project.ModelCalls = 0
			}
			a := ledgerAuthority(t, db, l)
			i := ledgerIntent(t, db, "one", "7", l)
			if err := a.RegisterAttempt(context.Background(), i); err != nil {
				t.Fatal(err)
			}
			p := &ledgerProvider{t: t, db: db}
			cfg := orchestrator.DefaultOrchestratorConfig(i.Model, i.Model, i.Provider)
			for name, role := range cfg.Roles {
				role.MaxTokens = 20
				cfg.Roles[name] = role
			}
			o := orchestrator.NewOrchestrator(cfg, map[string]orchestrator.LLMProvider{i.Provider: p}, orchestrator.WithOperatorCeilings(&l), orchestrator.WithBudgetAuthority(a))
			rc := review.NewContext(review.Request{})
			rc.Source.Attempt = i.Attempt
			rc.Source.Execution = i.Execution
			rc.Source.Attestation = i.Attestation
			rc.Source.Policy = review.PolicyProjection{Enforced: true, Digest: i.PolicyDigest, Limits: i.PolicyLimits, OperatorCeilings: &l, AllowedCapabilities: []string{"model.review"}}
			_, err := o.Complete(context.Background(), rc, orchestrator.RoleReasoner, "review", "system", "user")
			if limiting == "funded" {
				if err != nil || p.calls != 1 {
					t.Fatalf("funded dispatch failed: %v calls=%d", err, p.calls)
				}
				var response []byte
				if err = db.QueryRow(`SELECT outcome FROM budget_actions`).Scan(&response); err != nil || !strings.Contains(string(response), "Response") {
					t.Fatalf("outcome not durable: %s %v", response, err)
				}
			} else if err == nil || p.calls != 0 {
				t.Fatalf("%s cap crossed provider boundary: calls=%d err=%v", limiting, p.calls, err)
			}
		})
	}
}

func TestFixedPeriodsAreHalfOpenAndAttemptIsLifetime(t *testing.T) {
	p := &review.FixedBudgetPeriod{Kind: "fixed", Anchor: boundsTime, DurationMS: 1000}
	for _, tc := range []struct {
		at   time.Time
		want string
	}{{boundsTime, "0"}, {boundsTime.Add(time.Second - time.Nanosecond), "0"}, {boundsTime.Add(time.Second), "1"}, {boundsTime.Add(3 * time.Second), "3"}} {
		id, err := periodID(p, tc.at)
		if err != nil || id != tc.want {
			t.Fatalf("period at %s: %s %v", tc.at, id, err)
		}
	}
	if _, err := periodID(p, boundsTime.Add(-time.Nanosecond)); err == nil {
		t.Fatal("pre-anchor period admitted")
	}
}

func TestPostgresPriceVersionsCannotBeRewritten(t *testing.T) {
	db, _ := postgresFixture(t)
	l := ledgerLimits()
	a := ledgerAuthority(t, db, l)
	i := ledgerIntent(t, db, "one", "7", l)
	if err := a.RegisterAttempt(context.Background(), i); err != nil {
		t.Fatal(err)
	}
	if _, err := a.ReserveAndBegin(context.Background(), i); err != nil {
		t.Fatal(err)
	}
	r := a.cfg.Catalog.routes[0]
	r.FixedMicro = 0
	c, _ := NewCatalog([]RouteBound{r})
	cfg := a.cfg
	cfg.Catalog = c
	b, err := NewAuthority(db, cfg)
	if err != nil {
		t.Fatal(err)
	}
	i.ActionID = "changed-price"
	if _, err = b.ReserveAndBegin(context.Background(), i); err == nil {
		t.Fatal("same price version silently changed its upper bound")
	}
}

func TestPostgresCrashChild(t *testing.T) {
	if os.Getenv("SEVENREVIEW_CRASH_CHILD") != "1" {
		t.Skip("subprocess helper")
	}
	db, err := sql.Open("pgx", os.Getenv("SEVENREVIEW_CRASH_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	var i orchestrator.DispatchIntent
	if err = json.Unmarshal([]byte(os.Getenv("SEVENREVIEW_CRASH_INTENT")), &i); err != nil {
		t.Fatal(err)
	}
	a := ledgerAuthority(t, db, ledgerLimits())
	if _, err = a.ReserveAndBegin(context.Background(), i); err != nil {
		t.Fatal(err)
	}
	os.Exit(23)
}

func TestPostgresProcessDeathAfterCommitRetainsHoldAndRefusesResend(t *testing.T) {
	db, dsn := postgresFixture(t)
	l := ledgerLimits()
	a := ledgerAuthority(t, db, l)
	i := ledgerIntent(t, db, "one", "7", l)
	if err := a.RegisterAttempt(context.Background(), i); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(i)
	command := exec.Command(os.Args[0], "-test.run=^TestPostgresCrashChild$")
	command.Env = append(os.Environ(), "SEVENREVIEW_CRASH_CHILD=1", "SEVENREVIEW_CRASH_DSN="+dsn, "SEVENREVIEW_CRASH_INTENT="+string(raw))
	err := command.Run()
	exit, ok := err.(*exec.ExitError)
	if !ok || exit.ExitCode() != 23 {
		t.Fatalf("child did not reach committed-send crash: %v", err)
	}
	p, err := a.ReserveAndBegin(context.Background(), i)
	if err != nil || !p.AlreadyStarted {
		t.Fatalf("process death erased sending identity: %#v %v", p, err)
	}
	var holds int
	if err = db.QueryRow(`SELECT count(*) FROM budget_holds`).Scan(&holds); err != nil || holds != 3 {
		t.Fatalf("process death lost holds: %d %v", holds, err)
	}
}

func TestPostgresUnavailableAuthorityNeverFallsBackToLocalCapacity(t *testing.T) {
	db, _ := postgresFixture(t)
	l := ledgerLimits()
	a := ledgerAuthority(t, db, l)
	i := ledgerIntent(t, db, "one", "7", l)
	if err := a.RegisterAttempt(context.Background(), i); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := a.ReserveAndBegin(context.Background(), i); err == nil {
		t.Fatal("database outage permitted spend")
	}
}

func TestPostgresIncompatibleSchemaCannotSpendOrReinitialize(t *testing.T) {
	db, _ := postgresFixture(t)
	l := ledgerLimits()
	a := ledgerAuthority(t, db, l)
	i := ledgerIntent(t, db, "one", "7", l)
	if err := a.RegisterAttempt(context.Background(), i); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE budget_schema SET version=999`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(context.Background(), db); err == nil {
		t.Fatal("incompatible schema silently initialized")
	}
	if _, err := a.ReserveAndBegin(context.Background(), i); err == nil {
		t.Fatal("live authority spent against incompatible schema")
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM budget_attempts`).Scan(&count); err != nil || count != 1 {
		t.Fatal("incompatible schema erased existing accounting")
	}
}

func TestPostgresRegisteredAttemptCannotSwitchProjects(t *testing.T) {
	db, _ := postgresFixture(t)
	l := ledgerLimits()
	a := ledgerAuthority(t, db, l)
	i := ledgerIntent(t, db, "one", "7", l)
	if err := a.RegisterAttempt(context.Background(), i); err != nil {
		t.Fatal(err)
	}
	cfg := a.cfg
	cfg.ProjectID = "other-project"
	b, err := NewAuthority(db, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = b.ReserveAndBegin(context.Background(), i); err == nil {
		t.Fatal("registered attempt moved to another project allowance")
	}
}

func TestPostgresExpiredPriceAfterLockWaitCannotCommitNewHold(t *testing.T) {
	db, _ := postgresFixture(t)
	l := ledgerLimits()
	a := ledgerAuthority(t, db, l)
	var now time.Time
	if err := db.QueryRow(`SELECT clock_timestamp()`).Scan(&now); err != nil {
		t.Fatal(err)
	}
	r := a.cfg.Catalog.routes[0]
	r.Version = "expiring-version"
	r.ValidFrom = now.Add(-time.Hour)
	r.ValidUntil = now.Add(time.Second)
	c, _ := NewCatalog([]RouteBound{r})
	cfg := a.cfg
	cfg.Catalog = c
	var err error
	a, err = NewAuthority(db, cfg)
	if err != nil {
		t.Fatal(err)
	}
	i := ledgerIntent(t, db, "one", "7", l)
	admit(t, a, i)
	j := ledgerIntent(t, db, "two", "8", l)
	if err = a.RegisterAttempt(context.Background(), j); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var id string
	if err = tx.QueryRow(`SELECT id FROM budget_routes FOR UPDATE`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := a.ReserveAndBegin(ctx, j); done <- err }()
	blocked := false
	for deadline := time.Now().Add(500 * time.Millisecond); time.Now().Before(deadline); {
		var count int
		if err = db.QueryRow(`SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE '%qualification_digest FROM budget_routes%'`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count > 0 {
			blocked = true
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !blocked {
		t.Fatal("fixture did not reach price-contract lock wait")
	}
	if delay := time.Until(r.ValidUntil.Add(20 * time.Millisecond)); delay > 0 {
		time.Sleep(delay)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err = <-done; err == nil {
		t.Fatal("expired price contract authorized a new reservation after lock wait")
	}
	var actions int
	if err = db.QueryRow(`SELECT count(*) FROM budget_actions`).Scan(&actions); err != nil || actions != 1 {
		t.Fatalf("expired price left a new hold: actions=%d err=%v", actions, err)
	}
}

func TestPostgresRollbackLeavesNoActionOrPartialHold(t *testing.T) {
	db, _ := postgresFixture(t)
	limits := ledgerLimits()
	a := ledgerAuthority(t, db, limits)
	i := ledgerIntent(t, db, "one", "7", limits)
	if err := a.RegisterAttempt(context.Background(), i); err != nil {
		t.Fatal(err)
	}
	_, err := db.Exec(`ALTER TABLE budget_holds ADD CONSTRAINT fault CHECK (false)`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.ReserveAndBegin(context.Background(), i); err == nil {
		t.Fatal("failed transaction returned dispatch permit")
	}
	var count int
	if err = db.QueryRow(`SELECT count(*) FROM budget_actions`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("rolled-back action survived: count=%d err=%v", count, err)
	}
	_, _ = db.Exec(`ALTER TABLE budget_holds DROP CONSTRAINT fault`)
	if _, err = a.ReserveAndBegin(context.Background(), i); err != nil {
		t.Fatalf("rollback leaked capacity: %v", err)
	}
}

func TestPostgresRejectsMissingIdentityAndAuthorityDrift(t *testing.T) {
	db, _ := postgresFixture(t)
	limits := ledgerLimits()
	a := ledgerAuthority(t, db, limits)
	i := ledgerIntent(t, db, "one", "7", limits)
	if _, err := a.ReserveAndBegin(context.Background(), i); err == nil {
		t.Fatal("unregistered worker invented attempt authority")
	}
	bad := i
	bad.Attestation = nil
	if err := a.RegisterAttempt(context.Background(), bad); err == nil {
		t.Fatal("unattested attempt registered")
	}
	if err := a.RegisterAttempt(context.Background(), i); err != nil {
		t.Fatal(err)
	}
	i.OperatorCeilings.Project.MoneyMicro++
	if _, err := a.ReserveAndBegin(context.Background(), i); err == nil {
		t.Fatal("operator configuration drift accepted")
	}
	var raw []byte
	if err := db.QueryRow(`SELECT binding FROM budget_attempts WHERE id='one'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if !json.Valid(raw) {
		t.Fatal("immutable attempt binding missing")
	}
}
