package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Opt-in integration test uses only a container created by this test.
func TestPostgresRunner(t *testing.T) {
	if os.Getenv("MIGRATION_POSTGRES_TEST") != "1" {
		t.Skip("set MIGRATION_POSTGRES_TEST=1 to use a scratch Docker database")
	}
	name := fmt.Sprintf("codex-migrations-%d", time.Now().UnixNano())
	run := func(args ...string) (string, error) {
		out, err := exec.Command("docker", args...).CombinedOutput()
		return string(out), err
	}
	out, err := run("run", "--rm", "-d", "--name", name, "-e", "POSTGRES_PASSWORD=scratch-only", postgresImage)
	if err != nil {
		t.Fatalf("start: %v %s", err, out)
	}
	t.Cleanup(func() {
		if out, err := run("rm", "-f", name); err != nil {
			t.Errorf("scratch cleanup: %v %s", err, out)
		}
	})
	ready := false
	for i := 0; i < 80; i++ {
		if _, err := run("exec", name, "pg_isready", "-U", "postgres"); err == nil {
			ready = true
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if !ready {
		t.Fatal("scratch database not ready")
	}
	psql := func(db, sql string) string {
		out, err := run("exec", name, "psql", "-U", "postgres", "-d", db, "-XAt", "-v", "ON_ERROR_STOP=1", "-c", sql)
		if err != nil {
			t.Fatalf("fixture query: %v %s", err, out)
		}
		return strings.TrimSpace(out)
	}
	psql("postgres", "CREATE ROLE migration_test LOGIN PASSWORD 'scratch-only' NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION;")
	psql("postgres", "CREATE DATABASE mittgb OWNER migration_test;")
	psql("postgres", "CREATE TABLE other_customer_secret (value text);")
	count := 0
	migration := func(sql, db string) (string, error) {
		count++
		dir := fmt.Sprintf("/tmp/migration-%d", count)
		script := "mkdir -p " + dir + "\n" + strings.ReplaceAll(runnerScript, "/work", dir)
		args := []string{"exec"}
		for _, pair := range sqlEnvironment(sql) {
			args = append(args, "-e", pair[0]+"="+pair[1])
		}
		args = append(args, "-e", "PGHOST=127.0.0.1", "-e", "PGUSER=migration_test", "-e", "PGPASSWORD=scratch-only", "-e", "PGDATABASE="+db, "-e", "PGSSLMODE=disable", name, "sh", "-c", script)
		return run(args...)
	}
	if out, err := migration("CREATE TABLE works (id int);", "mittgb"); err != nil {
		t.Fatalf("migration failed: %v %s", err, out)
	}
	if out, err := migration("--"+strings.Repeat("x", 80000)+"\nSELECT 1;", "mittgb"); err != nil {
		t.Fatalf("chunked SQL failed: %v %s", err, out)
	}
	if out, err := migration("CREATE TABLE rolled_back (id int); SELECT missing_column;", "mittgb"); err == nil {
		t.Fatalf("SQL error passed: %s", out)
	}
	if got := psql("mittgb", "SELECT count(*) FROM pg_tables WHERE tablename='rolled_back';"); got != "0" {
		t.Fatal("failed migration did not roll back")
	}
	if out, err := migration("BEGIN; CREATE TABLE explicit_transaction (id int); COMMIT;", "mittgb"); err != nil {
		t.Fatalf("legacy transaction failed: %v %s", err, out)
	}
	if out, err := migration("SELECT * FROM other_customer_secret;", "postgres"); err == nil || !strings.Contains(out, "permission denied") {
		t.Fatalf("cross-customer access: %v %s", err, out)
	}
	lock := exec.Command("docker", "exec", name, "psql", "-U", "postgres", "-d", "mittgb", "-XAt", "-c", "SELECT pg_advisory_lock(731041,1); SELECT pg_sleep(5);")
	if err := lock.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lock.Wait() })
	locked := false
	for i := 0; i < 20; i++ {
		if psql("mittgb", "SELECT count(*) FROM pg_locks WHERE locktype='advisory' AND granted;") != "0" {
			locked = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !locked {
		t.Fatal("lock fixture not ready")
	}
	if out, err := migration("CREATE TABLE must_not_run (id int);", "mittgb"); err == nil || !strings.Contains(out, "Another migration") {
		t.Fatalf("concurrent migration accepted: %v %s", err, out)
	}
	if got := psql("mittgb", "SELECT count(*) FROM pg_tables WHERE tablename='must_not_run';"); got != "0" {
		t.Fatal("locked migration executed SQL")
	}

	// Provisioning must retain the runtime's existing owner privileges without
	// giving the migration login membership in the runtime/admin role.
	psql("postgres", "CREATE ROLE customeradmin LOGIN CREATEROLE CREATEDB PASSWORD 'scratch-admin-only';")
	psql("postgres", "CREATE DATABASE bootstrap_app OWNER customeradmin;")
	psql("bootstrap_app", "SET ROLE customeradmin; CREATE TABLE existing (id serial PRIMARY KEY); CREATE VIEW existing_view AS SELECT * FROM existing;")
	if out, err := run("cp", "provision-role.sql", name+":/tmp/provision-role.sql"); err != nil {
		t.Fatalf("copy provisioning SQL: %v %s", err, out)
	}
	out, err = run("exec", "-e", "MIGRATION_PASSWORD=scratch-only-long-password", "-e", "PGPASSWORD=scratch-admin-only", name, "psql", "-h", "127.0.0.1", "-U", "customeradmin", "-d", "bootstrap_app", "-X", "-v", "expected_database=bootstrap_app", "-v", "migration_role=bootstrap_migrator", "-v", "runtime_role=customeradmin", "-f", "/tmp/provision-role.sql")
	if err != nil {
		t.Fatalf("provisioning failed: %v %s", err, out)
	}
	psql("bootstrap_app", "SET ROLE customeradmin; ALTER TABLE existing ADD COLUMN runtime_still_works int;")
	psql("bootstrap_app", "SET ROLE bootstrap_migrator; ALTER TABLE existing ADD COLUMN migration_works int;")
	if got := psql("bootstrap_app", "SELECT count(*) FROM pg_auth_members m JOIN pg_roles r ON r.oid=m.member WHERE r.rolname='bootstrap_migrator';"); got != "0" {
		t.Fatal("migration role inherits another role")
	}
	if got := psql("postgres", "SELECT pg_get_userbyid(relowner) FROM pg_class WHERE relname='other_customer_secret';"); got != "postgres" {
		t.Fatal("bootstrap changed another database's owner")
	}
}
