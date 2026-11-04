package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTerminalJobStatus(t *testing.T) {
	for _, condition := range []string{"", "Complete", "Failed"} {
		t.Run(condition, func(t *testing.T) {
			logRequests := 0
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.Contains(r.URL.Path, "/jobs/"):
					conditions := []any{}
					if condition != "" {
						conditions = append(conditions, map[string]string{"type": condition, "status": "True"})
					}
					json.NewEncoder(w).Encode(map[string]any{"status": map[string]any{"conditions": conditions}})
				case strings.HasSuffix(r.URL.Path, "/log"):
					logRequests++
					w.Write([]byte("migration output"))
				default:
					if r.URL.Query().Get("labelSelector") != "job-name=migration-123-456" {
						t.Error("logs not scoped to run")
					}
					w.Write([]byte(`{"items":[{"metadata":{"name":"worker"}}]}`))
				}
			}))
			defer api.Close()
			k := kubeClient{base: api.URL, client: api.Client()}
			result, err := k.status(context.Background(), "gb-migrations", "migration-123-456")
			if err != nil {
				t.Fatal(err)
			}
			want := map[string]string{"": "running", "Complete": "succeeded", "Failed": "failed"}[condition]
			if result["state"] != want {
				t.Fatalf("state=%v", result["state"])
			}
			if condition == "" && logRequests != 0 {
				t.Fatal("partial output read as final result")
			}
			if condition != "" && result["logs"] != "migration output" {
				t.Fatal("terminal logs missing")
			}
		})
	}
}

func TestForbiddenStatusDoesNotContactCluster(t *testing.T) {
	c, id := fixture()
	id.RepositoryID = "999"
	s := server{cfg: c, verify: func(context.Context, string) (claims, error) { return id, nil }}
	for _, method := range []string{"GET", "POST"} {
		r := httptest.NewRequest(method, "/migration", strings.NewReader("{}"))
		r.Header.Set("Authorization", "Bearer token")
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatal(w.Code)
		}
	}
}
