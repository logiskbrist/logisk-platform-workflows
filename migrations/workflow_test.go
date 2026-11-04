package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Execute the actual Python client with a fake HTTPS transport. It validates
// symlinks and size before requesting an OIDC token or dispatching anything.
func TestWorkflowClient(t *testing.T) {
	raw, err := os.ReadFile("../.github/workflows/run-migration.yaml")
	if err != nil {
		t.Fatal(err)
	}
	_, script, ok := strings.Cut(string(raw), "        run: |\n")
	if !ok {
		t.Fatal("client missing")
	}
	lines := strings.Split(script, "\n")
	for i := range lines {
		lines[i] = strings.TrimPrefix(lines[i], "          ")
	}
	script = strings.Join(lines, "\n")
	for _, tc := range []struct {
		name, path, sql string
		symlink, fail   bool
	}{
		{"success", "script/engangs/test.sql", "SELECT 1;", false, false},
		{"traversal", "script/engangs/../test.sql", "SELECT 1;", false, true},
		{"symlink", "script/engangs/test.sql", "SELECT 1;", true, true},
		{"psql command", "script/engangs/test.sql", `\! env`, false, true},
		{"oversized", "script/engangs/test.sql", strings.Repeat("x", 262145), false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			_ = os.MkdirAll(filepath.Join(dir, "script/engangs"), 0700)
			target := filepath.Join(dir, tc.path)
			_ = os.WriteFile(target, []byte(tc.sql), 0600)
			if tc.symlink {
				_ = os.Remove(target)
				_ = os.Symlink("../outside.sql", target)
				_ = os.WriteFile(filepath.Join(dir, "script/outside.sql"), []byte(tc.sql), 0600)
			}
			harness := `import io, json, urllib.request
class Transport:
    def open(self, request, timeout=None):
        if 'identity.example' in request.full_url:
            return io.BytesIO(b'{"value":"signed-token"}')
        if request.method == 'POST':
            data = json.loads(request.data)
            assert data['sql'] == 'SELECT 1;'
            return io.BytesIO(b'{"job":"test-job","sha256":"17db4fd369edb9244b9f91d9aeed145c3d04ad8ba6e95d06247f07a63527d11a"}')
        return io.BytesIO(b'{"state":"succeeded","logs":"::error::untrusted SQL output"}')
urllib.request.build_opener = lambda *args: Transport()
`
			cmd := exec.Command("python3", "-c", harness+"\n"+script)
			cmd.Env = append(os.Environ(), "GITHUB_WORKSPACE="+dir, "MIGRATION_FILE="+tc.path, "MIGRATION_DATABASE=mittgb", "MIGRATION_ENDPOINT=https://migrations.example", "GITHUB_SHA="+strings.Repeat("b", 40), "GITHUB_STEP_SUMMARY="+filepath.Join(dir, "summary"), "ACTIONS_ID_TOKEN_REQUEST_URL=https://identity.example?x=1", "ACTIONS_ID_TOKEN_REQUEST_TOKEN=request-token")
			out, err := cmd.CombinedOutput()
			if (err != nil) != tc.fail {
				t.Fatalf("error=%v output=%s", err, out)
			}
			if !tc.fail && !strings.Contains(string(out), "::stop-commands::") {
				t.Fatal("SQL output can inject workflow commands")
			}
		})
	}
}
