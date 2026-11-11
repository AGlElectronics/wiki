package migrations_test

// Migration tests against a disposable database (TEST_DATABASE_URL). They
// DROP and recreate the public schema, like the HTTP tests.

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/AGlElectronics/wiki/backend/migrations"
)

func freshDB(t *testing.T) (*sql.DB, context.Context) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping DB-backed tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	t.Cleanup(cancel)
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.ExecContext(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public;`); err != nil {
		t.Fatalf("reset schema: %v", err)
	}
	goose.SetBaseFS(migrations.FS)
	goose.SetTableName("goose_db_version")
	goose.SetLogger(goose.NopLogger())
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatalf("dialect: %v", err)
	}
	return db, ctx
}

func mustExec(t *testing.T, ctx context.Context, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(ctx, q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

// TestGroupGrantBackfill: existing group grants are rewritten to their
// normalised key, keep the typed name for display, and two spellings of
// one group collapse into the higher level.
func TestGroupGrantBackfill(t *testing.T) {
	db, ctx := freshDB(t)
	if err := goose.UpToContext(ctx, db, ".", 20261113000001); err != nil {
		t.Fatalf("migrate to the version before: %v", err)
	}
	const org = "00000000-0000-0000-0000-000000000010"
	const space = "10000000-0000-0000-0000-000000000001"
	const page = "20000000-0000-0000-0000-000000000001"
	const user = "30000000-0000-0000-0000-000000000001"
	mustExec(t, ctx, db, `INSERT INTO users (id, issuer, subject, display_name) VALUES ($1, 'dev', 'eddie', 'Eddie')`, user)
	mustExec(t, ctx, db, `INSERT INTO spaces (id, organization_id, kind, name) VALUES ($1, $2, 'team', 'Ops')`, space, org)
	mustExec(t, ctx, db, `INSERT INTO pages (id, space_id, title) VALUES ($1, $2, 'Runbook')`, page, space)
	mustExec(t, ctx, db, `
		INSERT INTO space_grants (space_id, principal_kind, principal, level) VALUES
		  ($1, 'user',  $2,               'full'),
		  ($1, 'group', 'Dept-Security',  'view'),
		  ($1, 'group', '/dept-security', 'edit'),
		  ($1, 'group', '/org/Dept-X',    'comment')`, space, user)
	mustExec(t, ctx, db, `
		INSERT INTO page_grants (page_id, principal_kind, principal, level) VALUES
		  ($1, 'group', ' Dept-Bar ', 'view'),
		  ($1, 'group', 'dept-bar',   'view')`, page)

	if err := goose.UpContext(ctx, db, "."); err != nil {
		t.Fatalf("migrate up: %v", err)
	}

	got := map[string][2]string{}
	rows, err := db.QueryContext(ctx, `SELECT principal, level, principal_display FROM space_grants WHERE principal_kind = 'group'`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	for rows.Next() {
		var p, l, d string
		if err := rows.Scan(&p, &l, &d); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got[p] = [2]string{l, d}
	}
	_ = rows.Close()
	want := map[string][2]string{
		"dept-security": {"edit", "/dept-security"},
		"org/dept-x":    {"comment", "/org/Dept-X"},
	}
	if len(got) != len(want) {
		t.Fatalf("space group grants = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("space grant %q = %v, want %v", k, got[k], v)
		}
	}
	var userGrants int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM space_grants WHERE principal_kind = 'user' AND principal = $1 AND principal_display = ''`, user).Scan(&userGrants); err != nil || userGrants != 1 {
		t.Fatalf("person grant must stay untouched (count %d, err %v)", userGrants, err)
	}

	var p, d string
	var n int
	if err := db.QueryRowContext(ctx, `SELECT count(*), min(principal), min(principal_display) FROM page_grants`).Scan(&n, &p, &d); err != nil {
		t.Fatalf("page grants: %v", err)
	}
	if n != 1 || p != "dept-bar" || d != "Dept-Bar" {
		t.Fatalf("page grants: %d rows, principal %q, display %q; want 1, dept-bar, Dept-Bar", n, p, d)
	}

	if err := goose.DownToContext(ctx, db, ".", 20261113000001); err != nil {
		t.Fatalf("migrate down: %v", err)
	}
}
