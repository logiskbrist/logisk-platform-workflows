package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func fixture() (config, claims) {
	c := config{Namespace: "gb-migrations", Audience: "https://migrations.example", WorkflowRef: "logiskbrist/logisk-platform-workflows/.github/workflows/run-migration.yaml@v3", WorkflowSHA: strings.Repeat("a", 40), Apps: map[string]app{"123": {Repository: "godtbrod/mittgb", Database: "mittgb", Secret: "mittgb-migration-pg", User: "mittgb_migrator"}}}
	id := claims{RepositoryID: "123", Repository: "godtbrod/mittgb", RunID: "456", RunAttempt: "1", Actor: "operator", SHA: strings.Repeat("b", 40), Environment: "production-migrations", EventName: "workflow_dispatch", WorkflowRef: "godtbrod/mittgb/.github/workflows/run-sql.yaml@refs/heads/feature/test", JobWorkflowRef: c.WorkflowRef, JobWorkflowSHA: c.WorkflowSHA}
	return c, id
}

func TestAuthorization(t *testing.T) {
	c, id := fixture()
	for _, tc := range []struct {
		name   string
		change func(*claims)
	}{
		{"other repository", func(x *claims) { x.RepositoryID = "999" }},
		{"renamed or transferred repository", func(x *claims) { x.Repository = "other/mittgb" }},
		{"no production environment", func(x *claims) { x.Environment = "preview" }},
		{"push event", func(x *claims) { x.EventName = "push" }},
		{"untrusted workflow", func(x *claims) { x.JobWorkflowRef = "attacker/workflow@main" }},
		{"moved workflow tag", func(x *claims) { x.JobWorkflowSHA = strings.Repeat("c", 40) }},
		{"invalid run id", func(x *claims) { x.RunID = "../secrets" }},
		{"missing commit", func(x *claims) { x.SHA = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x := id
			tc.change(&x)
			if _, err := c.authorize(x); err == nil {
				t.Fatal("accepted unauthorized identity")
			}
		})
	}
	if _, err := c.authorize(id); err != nil {
		t.Fatal(err)
	}
}

func TestRequestValidation(t *testing.T) {
	a := app{Database: "mittgb"}
	for _, file := range []string{"../file.sql", "script/engangs/../x.sql", "script/engangs/x.txt", "script/engangs//x.sql", "/script/engangs/x.sql"} {
		if err := validateRequest(request{File: file, SQL: "SELECT 1;"}, a); err == nil {
			t.Errorf("accepted %q", file)
		}
	}
	for _, sql := range []string{"", `\! env`, "SELECT 1;\n\\connect other", "SELECT '\\i';", "SELECT '\x00';"[:8] + string(rune(0))} {
		if err := validateRequest(request{File: "script/engangs/x.sql", SQL: sql}, a); err == nil {
			t.Errorf("accepted unsafe SQL %q", sql)
		}
	}
	if err := validateRequest(request{File: "script/engangs/x.sql", SQL: "SELECT 1;", Database: "other"}, a); err == nil {
		t.Fatal("cross-database request accepted")
	}
	if err := validateRequest(request{File: "script/engangs/x.sql", SQL: "BEGIN; DO $$ BEGIN RAISE NOTICE 'ok'; END $$; COMMIT;"}, a); err != nil {
		t.Fatal(err)
	}
}

func TestDispatchAndReplay(t *testing.T) {
	c, id := fixture()
	var saved map[string]any
	creates := 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			creates++
			if err := json.NewDecoder(r.Body).Decode(&saved); err != nil {
				t.Error(err)
			}
			w.WriteHeader(201)
			json.NewEncoder(w).Encode(saved)
			return
		}
		if saved == nil {
			w.WriteHeader(404)
			return
		}
		json.NewEncoder(w).Encode(saved)
	}))
	defer api.Close()
	s := server{cfg: c, kube: &kubeClient{base: api.URL, client: api.Client()}, verify: func(context.Context, string) (claims, error) { return id, nil }}
	call := func(sql string) int {
		b, _ := json.Marshal(request{File: "script/engangs/x.sql", SQL: sql})
		r := httptest.NewRequest("POST", "/migration", strings.NewReader(string(b)))
		r.Header.Set("Authorization", "Bearer test")
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		return w.Code
	}
	if got := call("SELECT 1;"); got != 202 {
		t.Fatalf("dispatch: %d", got)
	}
	if got := call("SELECT 1;"); got != 202 {
		t.Fatalf("replay: %d", got)
	}
	if creates != 1 {
		t.Fatalf("replay created %d jobs", creates)
	}
	if got := call("SELECT 2;"); got != 409 {
		t.Fatalf("changed replay: %d", got)
	}
	spec := saved["spec"].(map[string]any)
	if spec["backoffLimit"].(float64) != 0 {
		t.Fatal("migration retries enabled")
	}
	pod := spec["template"].(map[string]any)["spec"].(map[string]any)
	if pod["automountServiceAccountToken"] != false {
		t.Fatal("job has cluster credentials")
	}
	container := pod["containers"].([]any)[0].(map[string]any)
	raw, _ := json.Marshal(container)
	if strings.Contains(string(raw), "shared-prod") || !strings.Contains(string(raw), "mittgb-migration-pg") {
		t.Fatal("wrong credential scope")
	}
	if strings.Contains(string(raw), "envFrom") {
		t.Fatal("unrestricted secret injection")
	}
}

func TestUnauthenticatedRequestsNeverReachCluster(t *testing.T) {
	c, _ := fixture()
	s := server{cfg: c, verify: func(context.Context, string) (claims, error) {
		t.Fatal("unexpected verifier call")
		return claims{}, nil
	}}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest("POST", "/migration", strings.NewReader("{}")))
	if w.Code != 401 {
		t.Fatal(w.Code)
	}
}

func TestMaximumSQLFitsProcessEnvironment(t *testing.T) {
	c, _ := fixture()
	s := server{cfg: c}
	j := s.job("test", c.Apps["123"], strings.Repeat("x", 256*1024), nil)
	pod := j["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
	container := pod["containers"].([]any)[0].(map[string]any)
	for _, raw := range container["env"].([]any) {
		if env, ok := raw.(map[string]string); ok && len(env["value"]) > 65536 {
			t.Fatalf("environment value %s exceeds Linux's per-string exec limit", env["name"])
		}
	}
}
