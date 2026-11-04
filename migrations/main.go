package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

type app struct{ Repository, Database, Secret, User string }
type config struct {
	Namespace, Audience, WorkflowRef, WorkflowSHA string
	Apps                                          map[string]app
}

var digits = regexp.MustCompile(`^[1-9][0-9]{0,19}$`)
var commit = regexp.MustCompile(`^[a-f0-9]{40}$`)
var sqlPath = regexp.MustCompile(`^script/engangs/[A-Za-z0-9_./-]+\.sql$`)
var k8sName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
var dbName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

func (c config) authorize(id claims) (app, error) {
	a, ok := c.Apps[id.RepositoryID]
	if !ok || a.Repository != id.Repository || id.Environment != "production-migrations" || id.EventName != "workflow_dispatch" || id.JobWorkflowRef != c.WorkflowRef || id.JobWorkflowSHA != c.WorkflowSHA || !commit.MatchString(id.SHA) || !digits.MatchString(id.RunID) || !digits.MatchString(id.RunAttempt) || id.Actor == "" || !strings.HasPrefix(id.WorkflowRef, id.Repository+"/.github/workflows/") {
		return app{}, errors.New("identity is not authorized for migrations")
	}
	return a, nil
}

type request struct {
	File     string `json:"file"`
	SQL      string `json:"sql"`
	Database string `json:"database"`
}

func validateRequest(r request, a app) error {
	if !sqlPath.MatchString(r.File) || path.Clean(r.File) != r.File || strings.Contains(r.File, "..") {
		return errors.New("file must be a clean SQL path under script/engangs/")
	}
	if r.Database != "" && r.Database != a.Database {
		return errors.New("database is not registered for this repository")
	}
	// psql metacommands can spawn processes, change connections and read files.
	// A conservative backslash ban also rejects backslashes inside literals.
	if strings.TrimSpace(r.SQL) == "" || len(r.SQL) > 256*1024 || !utf8.ValidString(r.SQL) || strings.ContainsAny(r.SQL, "\\\x00") {
		return errors.New("SQL must be UTF-8, 1–262144 bytes, without backslashes or NUL")
	}
	return nil
}

type server struct {
	cfg    config
	kube   *kubeClient
	verify func(context.Context, string) (claims, error)
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.Path == "/healthz" && r.Method == "GET" {
		w.WriteHeader(200)
		return
	}
	if r.URL.Path != "/migration" || (r.Method != "POST" && r.Method != "GET") {
		http.NotFound(w, r)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		http.Error(w, "GitHub identity required", 401)
		return
	}
	id, err := s.verify(r.Context(), strings.TrimPrefix(auth, "Bearer "))
	if err != nil {
		http.Error(w, "invalid GitHub identity", 401)
		return
	}
	a, err := s.cfg.authorize(id)
	if err != nil {
		http.Error(w, "migration not authorized", 403)
		return
	}
	name := "migration-" + id.RepositoryID + "-" + id.RunID
	if r.Method == "GET" {
		result, err := s.kube.status(r.Context(), s.cfg.Namespace, name)
		if errors.Is(err, errNotFound) {
			http.Error(w, "migration not found", 404)
			return
		}
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, 200, result)
		return
	}
	var input request
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		http.Error(w, "invalid request", 400)
		return
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		http.Error(w, "invalid request", 400)
		return
	}
	if err := validateRequest(input, a); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	checksum := sha256.Sum256([]byte(input.SQL))
	hash := hex.EncodeToString(checksum[:])
	audit := map[string]string{"repository": id.Repository, "repository-id": id.RepositoryID, "run-id": id.RunID, "run-attempt": id.RunAttempt, "actor": id.Actor, "sha": id.SHA, "workflow-ref": id.WorkflowRef, "workflow-sha": id.JobWorkflowSHA, "file": input.File, "sql-sha256": hash, "database": a.Database}
	// Stable name across run_attempt makes dispatch retries and reruns inspect
	// the original result. A new workflow_dispatch is needed to execute again.
	existing, err := s.kube.getJob(r.Context(), s.cfg.Namespace, name)
	if err == nil {
		old := existing.Metadata.Annotations
		if old["sha"] != id.SHA || old["sql-sha256"] != hash || old["file"] != input.File || old["database"] != a.Database {
			http.Error(w, "run already has a different migration", 409)
			return
		}
		writeJSON(w, 202, map[string]string{"job": name, "sha256": hash})
		return
	}
	if !errors.Is(err, errNotFound) {
		s.fail(w, err)
		return
	}
	if err := s.kube.createJob(r.Context(), s.cfg.Namespace, s.job(name, a, input.SQL, audit)); err != nil {
		// A simultaneous POST may have won. The client can safely submit again.
		if errors.Is(err, errConflict) {
			http.Error(w, "dispatch in progress; retry the same request", 409)
			return
		}
		s.fail(w, err)
		return
	}
	slog.Info("migration requested", "audit", audit, "job", name)
	writeJSON(w, 202, map[string]string{"job": name, "sha256": hash})
}
func (s *server) fail(w http.ResponseWriter, err error) {
	slog.Error("cluster request failed", "error", err)
	http.Error(w, "migration service unavailable", 503)
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

const runnerScript = `set -eu
printf '%s' "$MIGRATION_SQL_0" "$MIGRATION_SQL_1" "$MIGRATION_SQL_2" "$MIGRATION_SQL_3" "$MIGRATION_SQL_4" "$MIGRATION_SQL_5" | base64 -d > /work/migration.sql
cat > /work/lock.sql <<'SQL'
SET statement_timeout = '10min';
SET lock_timeout = '15s';
DO $migration_lock$ BEGIN
 IF NOT pg_try_advisory_lock(731041, 1) THEN
  RAISE EXCEPTION 'Another migration is running for this database';
 END IF;
END $migration_lock$;
SQL
unset MIGRATION_SQL_0 MIGRATION_SQL_1 MIGRATION_SQL_2 MIGRATION_SQL_3 MIGRATION_SQL_4 MIGRATION_SQL_5
exec psql -X --no-password --set=ON_ERROR_STOP=1 --set=VERBOSITY=terse --single-transaction --file=/work/lock.sql --file=/work/migration.sql
`

const postgresImage = "postgres:17.10-alpine@sha256:742f40ea20b9ff2ff31db5458d127452988a2164df9e17441e191f3b72252193"

// Linux limits individual exec environment strings to 128 KiB. Six 64-KiB
// chunks accommodate the maximum base64-encoded file without a ConfigMap or
// an oversized process environment entry.
func sqlEnvironment(sql string) [][2]string {
	encoded := base64.StdEncoding.EncodeToString([]byte(sql))
	result := make([][2]string, 6)
	for i := range result {
		end := min(len(encoded), 65536)
		result[i] = [2]string{fmt.Sprintf("MIGRATION_SQL_%d", i), encoded[:end]}
		encoded = encoded[end:]
	}
	return result
}

func (s *server) job(name string, a app, sql string, audit map[string]string) map[string]any {
	env := []any{}
	for _, key := range []string{"PGHOST", "PGPORT", "PGPASSWORD"} {
		env = append(env, map[string]any{"name": key, "valueFrom": map[string]any{"secretKeyRef": map[string]string{"name": a.Secret, "key": key}}})
	}
	for _, pair := range append([][2]string{{"PGUSER", a.User}, {"PGDATABASE", a.Database}, {"PGSSLMODE", "verify-full"}, {"PGSSLROOTCERT", "/etc/ssl/certs/ca-certificates.crt"}, {"PGCONNECT_TIMEOUT", "15"}, {"PGAPPNAME", name}}, sqlEnvironment(sql)...) {
		env = append(env, map[string]string{"name": pair[0], "value": pair[1]})
	}
	return map[string]any{"apiVersion": "batch/v1", "kind": "Job", "metadata": map[string]any{"name": name, "annotations": audit, "labels": map[string]string{"app.kubernetes.io/name": "platform-migration"}}, "spec": map[string]any{
		"backoffLimit": 0, "activeDeadlineSeconds": 900, "podReplacementPolicy": "Failed",
		// Retain the Job as a replay tombstone. Pruning it makes old run replays
		// executable again; no TTL controller is used for these records.
		"template": map[string]any{"metadata": map[string]any{"labels": map[string]string{"app.kubernetes.io/name": "platform-migration"}}, "spec": map[string]any{
			"restartPolicy": "Never", "automountServiceAccountToken": false, "serviceAccountName": "migration-worker",
			"securityContext": map[string]any{"runAsNonRoot": true, "runAsUser": 70, "runAsGroup": 70, "fsGroup": 70, "seccompProfile": map[string]string{"type": "RuntimeDefault"}},
			"containers": []any{map[string]any{"name": "migration", "image": postgresImage, "command": []string{"sh", "-c", runnerScript}, "env": env,
				"securityContext": map[string]any{"allowPrivilegeEscalation": false, "readOnlyRootFilesystem": true, "capabilities": map[string]any{"drop": []string{"ALL"}}},
				"resources":       map[string]any{"requests": map[string]string{"cpu": "10m", "memory": "32Mi"}, "limits": map[string]string{"cpu": "1", "memory": "128Mi"}},
				"volumeMounts":    []any{map[string]string{"name": "work", "mountPath": "/work"}}}},
			"volumes": []any{map[string]any{"name": "work", "emptyDir": map[string]string{"medium": "Memory", "sizeLimit": "2Mi"}}},
		}},
	}}
}

func main() {
	raw, err := os.ReadFile("/config/config.json")
	if err != nil {
		panic(err)
	}
	var cfg config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		panic(err)
	}
	if !k8sName.MatchString(cfg.Namespace) || !strings.HasPrefix(cfg.Audience, "https://") || !commit.MatchString(cfg.WorkflowSHA) || cfg.WorkflowRef == "" || len(cfg.Apps) == 0 {
		panic("incomplete migration configuration")
	}
	for id, a := range cfg.Apps {
		if !digits.MatchString(id) || !k8sName.MatchString(a.Secret) || !dbName.MatchString(a.Database) || !dbName.MatchString(a.User) || !strings.Contains(a.Repository, "/") {
			panic("invalid app registration")
		}
	}
	client, err := inClusterClient()
	if err != nil {
		panic(err)
	}
	verifier := &oidcVerifier{audience: cfg.Audience, jwksURL: githubIssuer + "/.well-known/jwks", client: &http.Client{Timeout: 10 * time.Second}}
	handler := &server{cfg: cfg, kube: client, verify: verifier.verify}
	srv := &http.Server{Addr: ":8080", Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 64 * 1024}
	slog.Info("migration dispatcher listening", "namespace", cfg.Namespace)
	if err := srv.ListenAndServe(); err != nil {
		panic(fmt.Errorf("serve: %w", err))
	}
}
