package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// setupTestWorkspace creates a temp directory and sets TEST_WORKSPACE to it.
// Returns a cleanup function that must be deferred.
func setupTestWorkspace(t *testing.T) func() {
	t.Helper()
	dir := t.TempDir() // automatically cleaned up by testing
	t.Setenv("TEST_WORKSPACE", dir)
	return func() {} // TempDir + Setenv are cleaned up by t automatically
}

// goAvailable returns true when the `go` binary is on PATH.
func goAvailable() bool {
	_, err := exec.LookPath("go")
	return err == nil
}

// postJSON is a convenience wrapper around httptest for POST requests.
func postJSON(router http.Handler, path string, body interface{}) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(http.MethodPost, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	// Default the orchestrator-only tenant header so existing tests pass
	// through tenantGuardMiddleware; the guard's own behaviour is asserted
	// separately with no-header requests.
	req.Header.Set("X-Tenant-Id", "test-tenant")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

// getRequest is a convenience wrapper for GET requests.
func getRequest(router http.Handler, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("X-Tenant-Id", "test-tenant")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

// decodeBody decodes a JSON response body into a map.
func decodeBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]interface{} {
	t.Helper()
	var m map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("failed to decode response body: %v\nbody: %s", err, rec.Body.String())
	}
	return m
}

// ---------------------------------------------------------------------------
// NewRouter
// ---------------------------------------------------------------------------

func TestNewRouter(t *testing.T) {
	router := NewRouter()
	if router == nil {
		t.Fatal("NewRouter returned nil")
	}
}

// ---------------------------------------------------------------------------
// Orchestrator-only tenant guard
// ---------------------------------------------------------------------------

func TestTenantGuard_ExecuteRequiresHeader(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/execute", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	NewRouter().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for /execute without X-Tenant-Id, got %d", rec.Code)
	}
	body := decodeBody(t, rec)
	if body["error"] != "X-Tenant-Id header required" {
		t.Fatalf("expected tenant-required error, got %v", body["error"])
	}
}

func TestTenantGuard_HealthExemptWithoutHeader(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	NewRouter().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for exempt /health without header, got %d", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// Health endpoint
// ---------------------------------------------------------------------------

func TestHealthEndpoint_StatusCode(t *testing.T) {
	rec := getRequest(NewRouter(), "/health")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestHealthEndpoint_ContentType(t *testing.T) {
	rec := getRequest(NewRouter(), "/health")
	ct := rec.Header().Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Fatalf("expected application/json content-type, got %q", ct)
	}
}

func TestHealthEndpoint_ResponseFields(t *testing.T) {
	rec := getRequest(NewRouter(), "/health")
	body := decodeBody(t, rec)

	requiredFields := []string{"status", "service", "version", "timestamp", "capabilities", "runtime"}
	for _, f := range requiredFields {
		if _, ok := body[f]; !ok {
			t.Errorf("missing required field %q in health response", f)
		}
	}
}

func TestHealthEndpoint_StatusHealthy(t *testing.T) {
	rec := getRequest(NewRouter(), "/health")
	body := decodeBody(t, rec)

	if body["status"] != "healthy" {
		t.Fatalf("expected status=healthy, got %v", body["status"])
	}
}

func TestHealthEndpoint_ServiceName(t *testing.T) {
	rec := getRequest(NewRouter(), "/health")
	body := decodeBody(t, rec)

	if body["service"] != "go-test-executor" {
		t.Fatalf("expected service=go-test-executor, got %v", body["service"])
	}
}

func TestHealthEndpoint_CapabilitiesStructure(t *testing.T) {
	rec := getRequest(NewRouter(), "/health")
	body := decodeBody(t, rec)

	caps, ok := body["capabilities"].(map[string]interface{})
	if !ok {
		t.Fatal("capabilities is not an object")
	}
	for _, key := range []string{"frameworks", "buildSystems", "features"} {
		if _, ok := caps[key]; !ok {
			t.Errorf("capabilities missing field %q", key)
		}
	}
}

func TestHealthEndpoint_RuntimeStructure(t *testing.T) {
	rec := getRequest(NewRouter(), "/health")
	body := decodeBody(t, rec)

	rt, ok := body["runtime"].(map[string]interface{})
	if !ok {
		t.Fatal("runtime is not an object")
	}
	for _, key := range []string{"version", "memory", "uptime"} {
		if _, ok := rt[key]; !ok {
			t.Errorf("runtime missing field %q", key)
		}
	}
}

// ---------------------------------------------------------------------------
// Info endpoint
// ---------------------------------------------------------------------------

func TestInfoEndpoint_StatusCode(t *testing.T) {
	rec := getRequest(NewRouter(), "/info")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestInfoEndpoint_RequiredFields(t *testing.T) {
	rec := getRequest(NewRouter(), "/info")
	body := decodeBody(t, rec)

	for _, f := range []string{"service", "version", "go_version", "platform", "arch"} {
		if _, ok := body[f]; !ok {
			t.Errorf("missing field %q in info response", f)
		}
	}
}

func TestInfoEndpoint_GoVersionMatchesRuntime(t *testing.T) {
	rec := getRequest(NewRouter(), "/info")
	body := decodeBody(t, rec)

	if body["go_version"] != runtime.Version() {
		t.Fatalf("expected go_version=%s, got %v", runtime.Version(), body["go_version"])
	}
}

func TestInfoEndpoint_PlatformMatchesRuntime(t *testing.T) {
	rec := getRequest(NewRouter(), "/info")
	body := decodeBody(t, rec)

	if body["platform"] != runtime.GOOS {
		t.Fatalf("expected platform=%s, got %v", runtime.GOOS, body["platform"])
	}
	if body["arch"] != runtime.GOARCH {
		t.Fatalf("expected arch=%s, got %v", runtime.GOARCH, body["arch"])
	}
}

// ---------------------------------------------------------------------------
// Frameworks endpoint
// ---------------------------------------------------------------------------

func TestFrameworksEndpoint_StatusCode(t *testing.T) {
	rec := getRequest(NewRouter(), "/frameworks")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestFrameworksEndpoint_HasGotest(t *testing.T) {
	rec := getRequest(NewRouter(), "/frameworks")
	body := decodeBody(t, rec)

	supported, ok := body["supported"].(map[string]interface{})
	if !ok {
		t.Fatal("supported is not an object")
	}
	if _, ok := supported["gotest"]; !ok {
		t.Fatal("supported frameworks missing 'gotest'")
	}
}

func TestFrameworksEndpoint_HasTestify(t *testing.T) {
	rec := getRequest(NewRouter(), "/frameworks")
	body := decodeBody(t, rec)

	supported := body["supported"].(map[string]interface{})
	if _, ok := supported["testify"]; !ok {
		t.Fatal("supported frameworks missing 'testify'")
	}
}

func TestFrameworksEndpoint_DefaultAndRecommended(t *testing.T) {
	rec := getRequest(NewRouter(), "/frameworks")
	body := decodeBody(t, rec)

	if body["default"] != "gotest" {
		t.Fatalf("expected default=gotest, got %v", body["default"])
	}
	if body["recommended"] != "testify" {
		t.Fatalf("expected recommended=testify, got %v", body["recommended"])
	}
}

func TestFrameworksEndpoint_Capabilities(t *testing.T) {
	rec := getRequest(NewRouter(), "/frameworks")
	body := decodeBody(t, rec)

	caps, ok := body["capabilities"].(map[string]interface{})
	if !ok {
		t.Fatal("capabilities is not an object")
	}
	for _, key := range []string{"testExecution", "parallelTests", "coverage", "mocking", "benchmarking", "linting", "assertions"} {
		if _, ok := caps[key]; !ok {
			t.Errorf("capabilities missing field %q", key)
		}
	}
}

// ---------------------------------------------------------------------------
// Execute endpoint — validation / fast-path
// ---------------------------------------------------------------------------

func TestExecuteEndpoint_InvalidJSON(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/execute", strings.NewReader("{invalid"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Tenant-Id", "test-tenant")
	rec := httptest.NewRecorder()
	NewRouter().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
	body := decodeBody(t, rec)
	if _, ok := body["error"]; !ok {
		t.Fatal("expected error field in response")
	}
}

func TestExecuteEndpoint_EmptyBody(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/execute", strings.NewReader(""))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Tenant-Id", "test-tenant")
	rec := httptest.NewRecorder()
	NewRouter().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestExecuteEndpoint_MissingCodeAndTestCode(t *testing.T) {
	rec := postJSON(NewRouter(), "/execute", map[string]string{
		"language": "go",
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
	body := decodeBody(t, rec)
	errMsg, _ := body["error"].(string)
	if !strings.Contains(errMsg, "code or test_code") {
		t.Fatalf("expected error about missing code, got %q", errMsg)
	}
}

// ---------------------------------------------------------------------------
// The `// test` probe fast path: it must answer "nothing ran", never "pass",
// and it must never swallow a real suite.
//
// Measured on 0a412b3 before this change, both of these answered HTTP 200
// {"success":true,"tests_run":0} with `no_tests_run` absent:
//
//	A) {"code":"// test","test_code":"<a test calling t.Fatal>"} -> success:true
//	B) {"code":"// test"}                                        -> success:true
//
// (A) is the worse half: a genuine, FAILING suite was never compiled, never
// run, and reported green.
// ---------------------------------------------------------------------------

func TestExecuteEndpoint_ProbeIsNotAPass(t *testing.T) {
	rec := postJSON(NewRouter(), "/execute", map[string]string{
		"code": "// test",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := decodeBody(t, rec)

	if body["success"] != false {
		t.Fatalf("probe claimed success=%v; nothing was compiled and nothing ran", body["success"])
	}
	if body["no_tests_run"] != true {
		t.Fatalf("expected no_tests_run=true, got %v", body["no_tests_run"])
	}
	if body["details"] != "probe_payload" {
		t.Fatalf("expected details=probe_payload, got %v", body["details"])
	}
	if got, _ := body["tests_run"].(float64); got != 0 {
		t.Fatalf("expected tests_run=0, got %v", body["tests_run"])
	}
	if _, ok := body["test_id"]; !ok {
		t.Fatal("missing test_id in response")
	}
	if _, ok := body["execution_time"]; !ok {
		t.Fatal("missing execution_time in response")
	}
}

func TestExecuteEndpoint_ProbeWithWhitespaceIsNotAPass(t *testing.T) {
	rec := postJSON(NewRouter(), "/execute", map[string]string{
		"code": "  // test  ",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := decodeBody(t, rec)
	if body["success"] != false {
		t.Fatalf("whitespace-padded probe claimed success=%v", body["success"])
	}
	if body["no_tests_run"] != true {
		t.Fatalf("expected no_tests_run=true, got %v", body["no_tests_run"])
	}
}

// TestExecuteEndpoint_TestCodePresentNeverTakesTheProbePath is the regression
// test for the swallowed suite. `req.TestCode == ""` is the load-bearing half of
// the guard: a caller that sends a placeholder `code` alongside a genuine
// `test_code` has a real suite, and matching on `code` alone answered it with
// `success: true` having compiled and executed nothing.
//
// NB `// test` is not a compilable Go file, so on the real path the honest
// answer here is the placeholder's COMPILE failure — which is the point: the
// caller now learns their suite did not run, instead of being told it passed.
func TestExecuteEndpoint_TestCodePresentNeverTakesTheProbePath(t *testing.T) {
	if !goAvailable() {
		t.Skip("go binary not available")
	}
	setupTestWorkspace(t)

	suites := map[string]string{
		"a_suite_that_would_fail": "package main\n\nimport \"testing\"\n\n" +
			"func TestAlwaysFails(t *testing.T) {\n\tt.Fatal(\"this suite MUST fail\")\n}\n",
		"a_suite_that_would_pass": "package main\n\nimport \"testing\"\n\n" +
			"func TestAlwaysPasses(t *testing.T) {}\n",
	}

	for name, testCode := range suites {
		rec := postJSON(NewRouter(), "/execute", map[string]string{
			"code":      "// test",
			"test_code": testCode,
		})
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: expected 200, got %d", name, rec.Code)
		}
		body := decodeBody(t, rec)
		t.Logf("%-23s -> success=%v no_tests_run=%v details=%v", name, body["success"], body["no_tests_run"], body["details"])

		if body["success"] == true {
			t.Fatalf("%s: reported success=true without running", name)
		}
		if body["details"] == "probe_payload" {
			t.Fatalf("%s: a request carrying test_code took the probe path", name)
		}
		output, _ := body["output"].(string)
		if strings.Contains(output, "Probe payload") {
			t.Fatalf("%s: got the probe response body, go was never invoked: %q", name, output)
		}
		// Positive evidence that the real path ran: the go toolchain's own
		// output, which the probe path can never produce.
		if !strings.Contains(output, "test [setup failed]") && !strings.Contains(output, "--- FAIL:") {
			t.Fatalf("%s: no go toolchain output — the suite was swallowed: %q", name, output)
		}
	}
}

// TestExecuteEndpoint_ThreeOutcomesAreDistinguishable is the whole point of the
// change: zero-collected, all-passed and genuine-failure must be three states,
// and a COMPILE FAILURE must not be collapsed into zero-collected (go build
// failures are a real case here — `go test` exits non-zero having run nothing,
// which looks exactly like zero collection if you only count PASS/FAIL markers).
func TestExecuteEndpoint_ThreeOutcomesAreDistinguishable(t *testing.T) {
	if !goAvailable() {
		t.Skip("go binary not available")
	}
	setupTestWorkspace(t)

	type outcome struct {
		success    bool
		noTestsRun bool
	}
	seen := map[string]outcome{}

	cases := []struct {
		name string
		body map[string]string
	}{
		{"zero_collected", map[string]string{
			"code": "package main\n\nfunc main() {}\n",
		}},
		{"all_passed", map[string]string{
			"code":      "package main\n\nfunc Add(a, b int) int { return a + b }\n",
			"test_code": "package main\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif Add(1, 2) != 3 {\n\t\tt.Fatal(\"bad\")\n\t}\n}\n",
		}},
		{"genuine_failure", map[string]string{
			"code":      "package main\n\nfunc Add(a, b int) int { return a - b }\n",
			"test_code": "package main\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif Add(1, 2) != 3 {\n\t\tt.Fatal(\"bad\")\n\t}\n}\n",
		}},
		{"compile_failure", map[string]string{
			"code":      "package main\n\nfunc Add(a, b int) int { return a + }\n",
			"test_code": "package main\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\t_ = Add(1, 2)\n}\n",
		}},
	}

	for _, c := range cases {
		rec := postJSON(NewRouter(), "/execute", c.body)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: expected 200, got %d", c.name, rec.Code)
		}
		b := decodeBody(t, rec)
		seen[c.name] = outcome{
			success:    b["success"] == true,
			noTestsRun: b["no_tests_run"] == true,
		}
		t.Logf("%-16s -> success=%v no_tests_run=%v tests_run=%v error=%v",
			c.name, b["success"], b["no_tests_run"], b["tests_run"], b["error"])
	}

	if seen["zero_collected"] != (outcome{success: false, noTestsRun: true}) {
		t.Fatalf("zero_collected: got %+v", seen["zero_collected"])
	}
	if seen["all_passed"] != (outcome{success: true, noTestsRun: false}) {
		t.Fatalf("all_passed: got %+v", seen["all_passed"])
	}
	if seen["genuine_failure"] != (outcome{success: false, noTestsRun: false}) {
		t.Fatalf("genuine_failure: got %+v", seen["genuine_failure"])
	}
	// The trap the brief names: a compile failure collects zero tests, so a
	// naive marker count would mark it no_tests_run and hide a broken build
	// behind "nothing to run".
	if seen["compile_failure"] != (outcome{success: false, noTestsRun: false}) {
		t.Fatalf("compile_failure collapsed into zero-collected: got %+v", seen["compile_failure"])
	}
	if seen["zero_collected"] == seen["compile_failure"] {
		t.Fatal("zero_collected and compile_failure are indistinguishable")
	}
}

// TestRunGoTest_TimeoutReportsUnknownNotClean covers the one mode this runner
// genuinely cannot measure. On a kill the output is truncated, so zero PASS/FAIL
// markers is not evidence of zero collection — it is an unmeasured state, and it
// is named rather than defaulted clean.
func TestRunGoTest_TimeoutReportsUnknownNotClean(t *testing.T) {
	if !goAvailable() {
		t.Skip("go binary not available")
	}
	tempDir := t.TempDir()
	code := "package main\n\nfunc main() {}\n"
	testCode := "package main\n\nimport (\n\t\"testing\"\n\t\"time\"\n)\n\nfunc TestSleeps(t *testing.T) {\n\ttime.Sleep(30 * time.Second)\n}\n"

	result, err := runGoTest(tempDir, code, testCode, 1)
	if err != nil {
		t.Fatalf("runGoTest returned error: %v", err)
	}
	if result.Success {
		t.Fatal("a timed-out run must not be a pass")
	}
	if result.NoTestsRun {
		t.Fatal("a timed-out run must not claim zero tests were collected — it cannot know")
	}
	if !result.NoTestsRunUnknown {
		t.Fatalf("expected no_tests_run_unknown=true on timeout, got exit_code=%d error=%q", result.ExitCode, result.Error)
	}
}

// TestRunGoTest_MeasurableModesAreNotUnknown is the negative control for the
// field above: if every mode set it, it would carry no information.
func TestRunGoTest_MeasurableModesAreNotUnknown(t *testing.T) {
	if !goAvailable() {
		t.Skip("go binary not available")
	}
	passing := "package main\n\nimport \"testing\"\n\nfunc TestOK(t *testing.T) {}\n"
	for name, tc := range map[string][2]string{
		"zero_collected":  {"package main\n\nfunc main() {}\n", ""},
		"all_passed":      {"package main\n\nfunc main() {}\n", passing},
		"compile_failure": {"package main\n\nfunc Bad() int { return }\n", passing},
	} {
		result, err := runGoTest(t.TempDir(), tc[0], tc[1], 30)
		if err != nil {
			t.Fatalf("%s: runGoTest returned error: %v", name, err)
		}
		if result.NoTestsRunUnknown {
			t.Fatalf("%s: measurable mode marked unknown", name)
		}
	}
}

func TestExecuteEndpoint_CodeWithMainNoTestsFails(t *testing.T) {
	// Code with func main() but NO tests must NOT be auto-passed: the old
	// "short code + func main()" fast-path loophole is closed.
	if !goAvailable() {
		t.Skip("go binary not available")
	}
	setupTestWorkspace(t)
	rec := postJSON(NewRouter(), "/execute", map[string]string{
		"code": "package main\nfunc main() {}",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := decodeBody(t, rec)
	if body["success"] != false {
		t.Fatal("expected success=false for code with no tests")
	}
	if body["no_tests_run"] != true {
		t.Fatalf("expected no_tests_run=true, got %v", body["no_tests_run"])
	}
}

func TestExecuteEndpoint_ResponseStructure(t *testing.T) {
	rec := postJSON(NewRouter(), "/execute", map[string]string{
		"code": "// test",
	})
	body := decodeBody(t, rec)

	requiredFields := []string{"success", "test_id", "execution_time", "output"}
	for _, f := range requiredFields {
		if _, ok := body[f]; !ok {
			t.Errorf("missing required field %q in execute response", f)
		}
	}
}

func TestExecuteEndpoint_MethodNotAllowed(t *testing.T) {
	rec := getRequest(NewRouter(), "/execute")
	// gorilla/mux returns 405 for wrong method
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// Validate endpoint
// ---------------------------------------------------------------------------

func TestValidateEndpoint_InvalidJSON(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/validate", strings.NewReader("not json"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Tenant-Id", "test-tenant")
	rec := httptest.NewRecorder()
	NewRouter().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestValidateEndpoint_EmptyCode(t *testing.T) {
	setupTestWorkspace(t)
	rec := postJSON(NewRouter(), "/validate", map[string]string{
		"code": "",
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
	body := decodeBody(t, rec)
	if body["valid"] != false {
		t.Fatal("expected valid=false for empty code")
	}
	errs, ok := body["errors"].([]interface{})
	if !ok || len(errs) == 0 {
		t.Fatal("expected non-empty errors array for empty code")
	}
}

func TestValidateEndpoint_ValidCode(t *testing.T) {
	if !goAvailable() {
		t.Skip("go binary not available, skipping real validation test")
	}
	setupTestWorkspace(t)

	rec := postJSON(NewRouter(), "/validate", map[string]interface{}{
		"code":     "package main\n\nfunc main() {}\n",
		"filename": "main.go",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := decodeBody(t, rec)
	if _, ok := body["validationId"]; !ok {
		t.Fatal("missing validationId")
	}
	// Note: `go build` on a standalone file without go.mod may fail,
	// so we only check that we got a structured response — not necessarily valid=true.
}

func TestValidateEndpoint_DefaultFilename(t *testing.T) {
	if !goAvailable() {
		t.Skip("go binary not available")
	}
	setupTestWorkspace(t)

	// Don't provide filename — should default to "test.go"
	rec := postJSON(NewRouter(), "/validate", map[string]interface{}{
		"code": "package main\n\nfunc main() {}\n",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// sendError helper
// ---------------------------------------------------------------------------

func TestSendError_ContentType(t *testing.T) {
	rec := httptest.NewRecorder()
	sendError(rec, "test error", http.StatusBadRequest)

	ct := rec.Header().Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Fatalf("expected application/json, got %q", ct)
	}
}

func TestSendError_StatusCode(t *testing.T) {
	codes := []int{
		http.StatusBadRequest,
		http.StatusInternalServerError,
		http.StatusNotFound,
		http.StatusUnprocessableEntity,
	}
	for _, code := range codes {
		rec := httptest.NewRecorder()
		sendError(rec, "msg", code)
		if rec.Code != code {
			t.Errorf("expected status %d, got %d", code, rec.Code)
		}
	}
}

func TestSendError_Body(t *testing.T) {
	rec := httptest.NewRecorder()
	sendError(rec, "something broke", http.StatusInternalServerError)

	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to decode error body: %v", err)
	}
	if body["error"] != "something broke" {
		t.Fatalf("expected error='something broke', got %q", body["error"])
	}
}

// ---------------------------------------------------------------------------
// getTestWorkspace helper
// ---------------------------------------------------------------------------

func TestGetTestWorkspace_Default(t *testing.T) {
	t.Setenv("TEST_WORKSPACE", "")
	ws := getTestWorkspace()
	if ws != TestWorkspace {
		t.Fatalf("expected %q, got %q", TestWorkspace, ws)
	}
}

func TestGetTestWorkspace_Override(t *testing.T) {
	t.Setenv("TEST_WORKSPACE", "/tmp/custom_workspace")
	ws := getTestWorkspace()
	if ws != "/tmp/custom_workspace" {
		t.Fatalf("expected /tmp/custom_workspace, got %q", ws)
	}
}

// ---------------------------------------------------------------------------
// runGoTest (unit-level, requires go binary)
// ---------------------------------------------------------------------------

func TestRunGoTest_ValidCode(t *testing.T) {
	if !goAvailable() {
		t.Skip("go binary not available")
	}
	tempDir := t.TempDir()

	code := `package main

func Add(a, b int) int {
	return a + b
}
`
	testCode := `package main

import "testing"

func TestAdd(t *testing.T) {
	if Add(2, 3) != 5 {
		t.Fatal("expected 5")
	}
}
`
	result, err := runGoTest(tempDir, code, testCode, 30)
	if err != nil {
		t.Fatalf("runGoTest returned error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success=true, got false. output=%s error=%s", result.Output, result.Error)
	}
	if result.ExitCode != 0 {
		t.Fatalf("expected exit_code=0, got %d", result.ExitCode)
	}
	if !strings.Contains(result.Output, "PASS") {
		t.Fatalf("expected PASS in output, got: %s", result.Output)
	}
}

func TestRunGoTest_CompilationError(t *testing.T) {
	if !goAvailable() {
		t.Skip("go binary not available")
	}
	tempDir := t.TempDir()

	// Invalid Go code — won't compile
	code := `package main

func Broken() {
	x := // syntax error
}
`
	testCode := `package main

import "testing"

func TestBroken(t *testing.T) {
	Broken()
}
`
	result, err := runGoTest(tempDir, code, testCode, 30)
	if err != nil {
		t.Fatalf("runGoTest returned error: %v", err)
	}
	if result.Success {
		t.Fatal("expected success=false for code with syntax error")
	}
	if result.ExitCode == 0 {
		t.Fatal("expected non-zero exit code for compilation error")
	}
}

func TestRunGoTest_FailingTest(t *testing.T) {
	if !goAvailable() {
		t.Skip("go binary not available")
	}
	tempDir := t.TempDir()

	code := `package main

func Multiply(a, b int) int {
	return a * b
}
`
	testCode := `package main

import "testing"

func TestMultiply(t *testing.T) {
	// Deliberately wrong assertion
	if Multiply(3, 4) != 999 {
		t.Fatal("test intentionally fails")
	}
}
`
	result, err := runGoTest(tempDir, code, testCode, 30)
	if err != nil {
		t.Fatalf("runGoTest returned error: %v", err)
	}
	if result.Success {
		t.Fatal("expected success=false for failing test")
	}
	if !strings.Contains(result.Output, "FAIL") {
		t.Fatalf("expected FAIL in output, got: %s", result.Output)
	}
}

func TestRunGoTest_CodeOnlyNoTestCode(t *testing.T) {
	if !goAvailable() {
		t.Skip("go binary not available")
	}
	tempDir := t.TempDir()

	// Only code, no test_code — compiles cleanly but no test ran, so this is
	// NOT a pass. The old fabricated-TestMain stub that faked a green result
	// has been removed.
	code := `package main

func Hello() string {
	return "hello"
}

func main() {}
`
	result, err := runGoTest(tempDir, code, "", 30)
	if err != nil {
		t.Fatalf("runGoTest returned error: %v", err)
	}
	if result.Success {
		t.Fatal("expected success=false for code-only (no tests run)")
	}
	if !result.NoTestsRun {
		t.Fatal("expected NoTestsRun=true for code-only submission")
	}
	if result.TestsRun != 0 {
		t.Fatalf("expected TestsRun=0, got %d", result.TestsRun)
	}
}

func TestRunGoTest_DefaultTimeout(t *testing.T) {
	if !goAvailable() {
		t.Skip("go binary not available")
	}
	tempDir := t.TempDir()

	code := `package main

func Noop() {}
`
	testCode := `package main

import "testing"

func TestNoop(t *testing.T) { Noop() }
`
	// timeout=0 should use default 30s
	result, err := runGoTest(tempDir, code, testCode, 0)
	if err != nil {
		t.Fatalf("runGoTest returned error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success=true, output=%s error=%s", result.Output, result.Error)
	}
}

func TestRunGoTest_MillisecondTimeout(t *testing.T) {
	if !goAvailable() {
		t.Skip("go binary not available")
	}
	tempDir := t.TempDir()

	code := `package main

func Noop() {}
`
	testCode := `package main

import "testing"

func TestNoop(t *testing.T) { Noop() }
`
	// timeout=30000 (milliseconds) should be converted to 30 seconds
	result, err := runGoTest(tempDir, code, testCode, 30000)
	if err != nil {
		t.Fatalf("runGoTest returned error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success=true, output=%s error=%s", result.Output, result.Error)
	}
}

// ---------------------------------------------------------------------------
// Real execution via /execute endpoint (integration-level)
// ---------------------------------------------------------------------------

func TestExecuteEndpoint_RealExecution_ValidCode(t *testing.T) {
	if !goAvailable() {
		t.Skip("go binary not available")
	}
	setupTestWorkspace(t)

	code := `package main

func Sum(nums ...int) int {
	total := 0
	for _, n := range nums {
		total += n
	}
	return total
}
`
	testCode := `package main

import "testing"

func TestSum(t *testing.T) {
	if Sum(1, 2, 3) != 6 {
		t.Fatal("expected 6")
	}
}
`
	rec := postJSON(NewRouter(), "/execute", map[string]string{
		"code":      code,
		"test_code": testCode,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := decodeBody(t, rec)

	if body["success"] != true {
		t.Fatalf("expected success=true, got %v. output=%v error=%v", body["success"], body["output"], body["error"])
	}
	output, _ := body["output"].(string)
	if !strings.Contains(output, "PASS") {
		t.Fatalf("expected PASS in output, got: %s", output)
	}
}

func TestExecuteEndpoint_RealExecution_CompilationFailure(t *testing.T) {
	if !goAvailable() {
		t.Skip("go binary not available")
	}
	setupTestWorkspace(t)

	code := `package main

func Bad( {
}
`
	testCode := `package main

import "testing"

func TestBad(t *testing.T) {}
`
	rec := postJSON(NewRouter(), "/execute", map[string]string{
		"code":      code,
		"test_code": testCode,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 (error in body), got %d", rec.Code)
	}
	body := decodeBody(t, rec)
	if body["success"] != false {
		t.Fatal("expected success=false for compilation error via /execute")
	}
}

func TestExecuteEndpoint_RealExecution_TestCodeOnly(t *testing.T) {
	if !goAvailable() {
		t.Skip("go binary not available")
	}
	setupTestWorkspace(t)

	testCode := `package main

import "testing"

func TestSimple(t *testing.T) {
	if 1+1 != 2 {
		t.Fatal("math is broken")
	}
}
`
	rec := postJSON(NewRouter(), "/execute", map[string]string{
		"test_code": testCode,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := decodeBody(t, rec)
	// With only test_code and no code file, the test should still run
	// (the service writes test_code to main_test.go)
	if _, ok := body["test_id"]; !ok {
		t.Fatal("missing test_id in response")
	}
}

// ---------------------------------------------------------------------------
// Route method enforcement
// ---------------------------------------------------------------------------

func TestHealthEndpoint_PostNotAllowed(t *testing.T) {
	rec := postJSON(NewRouter(), "/health", nil)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405 for POST /health, got %d", rec.Code)
	}
}

func TestInfoEndpoint_PostNotAllowed(t *testing.T) {
	rec := postJSON(NewRouter(), "/info", nil)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405 for POST /info, got %d", rec.Code)
	}
}

func TestFrameworksEndpoint_PostNotAllowed(t *testing.T) {
	rec := postJSON(NewRouter(), "/frameworks", nil)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405 for POST /frameworks, got %d", rec.Code)
	}
}

func TestValidateEndpoint_GetNotAllowed(t *testing.T) {
	rec := getRequest(NewRouter(), "/validate")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405 for GET /validate, got %d", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// Nonexistent route
// ---------------------------------------------------------------------------

func TestNonexistentRoute(t *testing.T) {
	rec := getRequest(NewRouter(), "/nonexistent")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown route, got %d", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// Workspace directory cleanup (ensure execute cleans up after itself)
// ---------------------------------------------------------------------------

func TestExecuteEndpoint_CleansUpTempDir(t *testing.T) {
	if !goAvailable() {
		t.Skip("go binary not available")
	}
	cleanup := setupTestWorkspace(t)
	defer cleanup()

	wsDir := os.Getenv("TEST_WORKSPACE")

	rec := postJSON(NewRouter(), "/execute", map[string]string{
		"code":      "package main\nfunc Foo() {}\n",
		"test_code": "package main\nimport \"testing\"\nfunc TestFoo(t *testing.T) {}\n",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	// After the request completes, the temp sub-directory should be cleaned up
	entries, err := os.ReadDir(wsDir)
	if err != nil {
		t.Fatalf("failed to read workspace dir: %v", err)
	}
	if len(entries) != 0 {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Fatalf("expected workspace to be empty after execute, found: %v", names)
	}
}

// ---------------------------------------------------------------------------
// Validate endpoint — workspace cleanup
// ---------------------------------------------------------------------------

func TestValidateEndpoint_CleansUpTempDir(t *testing.T) {
	if !goAvailable() {
		t.Skip("go binary not available")
	}
	cleanup := setupTestWorkspace(t)
	defer cleanup()

	wsDir := os.Getenv("TEST_WORKSPACE")

	rec := postJSON(NewRouter(), "/validate", map[string]interface{}{
		"code":     "package main\nfunc main() {}\n",
		"filename": "main.go",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	entries, err := os.ReadDir(wsDir)
	if err != nil {
		t.Fatalf("failed to read workspace dir: %v", err)
	}
	if len(entries) != 0 {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Fatalf("expected workspace to be empty after validate, found: %v", names)
	}
}

// ---------------------------------------------------------------------------
// Edge case: very long code (not over limit, but substantial)
// ---------------------------------------------------------------------------

func TestExecuteEndpoint_LargeCode_FastPathNotTriggered(t *testing.T) {
	if !goAvailable() {
		t.Skip("go binary not available")
	}
	setupTestWorkspace(t)

	// Code that has func main() but is longer than 100 chars, so fast-path should NOT trigger
	code := `package main

import "fmt"

func main() {
	// This is a long comment that makes the code longer than 100 characters total so the fast-path check does not trigger
	fmt.Println("hello world")
}
`
	testCode := `package main

import "testing"

func TestMain(t *testing.T) {
	t.Log("test runs")
}
`
	rec := postJSON(NewRouter(), "/execute", map[string]string{
		"code":      code,
		"test_code": testCode,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := decodeBody(t, rec)

	// Should NOT be a fast-path response
	output, _ := body["output"].(string)
	if strings.Contains(output, "Fast-path") {
		t.Fatal("expected real execution, not fast-path, for long code with func main()")
	}
}

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

func TestConstants(t *testing.T) {
	if DefaultPort != "8091" {
		t.Fatalf("expected DefaultPort=8091, got %s", DefaultPort)
	}
	if MaxCodeSize != 10*1024*1024 {
		t.Fatalf("expected MaxCodeSize=10MB, got %d", MaxCodeSize)
	}
	if TestWorkspace != "/app/test_workspace" {
		t.Fatalf("expected TestWorkspace=/app/test_workspace, got %s", TestWorkspace)
	}
}

// ---------------------------------------------------------------------------
// runGoTest edge: writing files into a read-only directory should fail
// ---------------------------------------------------------------------------

func TestRunGoTest_BadTempDir(t *testing.T) {
	if !goAvailable() {
		t.Skip("go binary not available")
	}

	// Use a temp dir that we then make read-only
	base := t.TempDir()
	badDir := filepath.Join(base, "readonly")
	if err := os.MkdirAll(badDir, 0755); err != nil {
		t.Fatal(err)
	}

	// `go mod init` will try to write go.mod — if the dir is read-only it fails
	if err := os.Chmod(badDir, 0555); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(badDir, 0755) }() // restore so cleanup can remove it

	result, err := runGoTest(badDir, "package main", "", 10)
	if err != nil {
		t.Fatalf("runGoTest should not return Go error, got: %v", err)
	}
	if result.Success {
		t.Fatal("expected success=false when temp dir is read-only")
	}
	if result.ExitCode != 1 {
		t.Fatalf("expected exit_code=1, got %d", result.ExitCode)
	}
}

// ---------------------------------------------------------------------------
// Execute endpoint — MkdirAll failure (non-writable workspace)
// ---------------------------------------------------------------------------

func TestExecuteEndpoint_MkdirAllFailure(t *testing.T) {
	// Point TEST_WORKSPACE at a file (not a directory) so MkdirAll fails
	tmpFile := filepath.Join(t.TempDir(), "not_a_dir")
	if err := os.WriteFile(tmpFile, []byte("block"), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEST_WORKSPACE", tmpFile)

	// Use code that is NOT a fast-path candidate, so executeHandler actually
	// tries to create a temp directory.
	rec := postJSON(NewRouter(), "/execute", map[string]string{
		"code":      "package main\nfunc main() { println(\"hello\") }\n",
		"test_code": "package main\nimport \"testing\"\nfunc TestX(t *testing.T) {}\n",
	})
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d; body=%s", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	errMsg, _ := body["error"].(string)
	if !strings.Contains(errMsg, "test directory") {
		t.Fatalf("expected error about test directory, got %q", errMsg)
	}
}

// ---------------------------------------------------------------------------
// runGoTest — timeout / context.DeadlineExceeded
// ---------------------------------------------------------------------------

func TestRunGoTest_Timeout(t *testing.T) {
	if !goAvailable() {
		t.Skip("go binary not available")
	}
	tempDir := t.TempDir()

	// Code with a test that sleeps longer than our timeout
	code := `package main

func Slow() {}
`
	testCode := `package main

import (
	"testing"
	"time"
)

func TestSlow(t *testing.T) {
	time.Sleep(30 * time.Second)
}
`
	result, err := runGoTest(tempDir, code, testCode, 2)
	if err != nil {
		t.Fatalf("runGoTest returned error: %v", err)
	}
	if result.Success {
		t.Fatal("expected success=false for timed-out test")
	}
	if result.ExitCode != 124 {
		// The go test -timeout flag may kill it first (exit code 1)
		// Either way, it should not succeed
		t.Logf("exit code %d (124 = context timeout, other = go test -timeout killed it)", result.ExitCode)
	}
}

// ---------------------------------------------------------------------------
// runGoTest — code write failure (make dir read-only after go mod init)
// ---------------------------------------------------------------------------

func TestRunGoTest_CodeWriteFailure(t *testing.T) {
	if !goAvailable() {
		t.Skip("go binary not available")
	}
	if runtime.GOOS == "windows" {
		t.Skip("chmod not reliable on Windows")
	}

	tempDir := t.TempDir()

	// First, let go mod init succeed, then make the directory read-only
	// so that writing main.go fails.
	modCmd := exec.Command("go", "mod", "init", "test")
	modCmd.Dir = tempDir
	if err := modCmd.Run(); err != nil {
		t.Fatalf("go mod init failed: %v", err)
	}

	// Make directory read-only (files inside are still readable but new files can't be created)
	if err := os.Chmod(tempDir, 0555); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(tempDir, 0755) }()

	// runGoTest will try go mod init again (which will fail because go.mod already exists
	// and dir is read-only). Actually, go mod init will fail if it can't write.
	// But we already have go.mod, so let's try a different approach.
	// Since tempDir already has go.mod and is read-only, go mod init will fail
	// because it can't overwrite go.mod.
	result, err := runGoTest(tempDir, "package main\nfunc main(){}\n", "", 10)
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if result.Success {
		t.Fatal("expected failure when directory is read-only")
	}
}

// ---------------------------------------------------------------------------
// Validate endpoint — invalid Go code (go build fails, covers line 467-469)
// ---------------------------------------------------------------------------

func TestValidateEndpoint_InvalidGoCode(t *testing.T) {
	if !goAvailable() {
		t.Skip("go binary not available")
	}
	setupTestWorkspace(t)

	rec := postJSON(NewRouter(), "/validate", map[string]interface{}{
		"code":     "package main\n\nfunc Bad( {\n}\n",
		"filename": "main.go",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := decodeBody(t, rec)
	if body["valid"] != false {
		t.Fatal("expected valid=false for invalid Go code")
	}
	errs, ok := body["errors"].([]interface{})
	if !ok || len(errs) == 0 {
		t.Fatal("expected non-empty errors for invalid Go code")
	}
}

// ---------------------------------------------------------------------------
// Validate endpoint — MkdirAll failure
// ---------------------------------------------------------------------------

func TestValidateEndpoint_MkdirAllFailure(t *testing.T) {
	// Point TEST_WORKSPACE at a file so MkdirAll fails
	tmpFile := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(tmpFile, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEST_WORKSPACE", tmpFile)

	rec := postJSON(NewRouter(), "/validate", map[string]interface{}{
		"code": "package main\nfunc main() {}\n",
	})
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d; body=%s", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	errMsg, _ := body["error"].(string)
	if !strings.Contains(errMsg, "temp directory") {
		t.Fatalf("expected error about temp directory, got %q", errMsg)
	}
}

// ---------------------------------------------------------------------------
// Validate endpoint — filename path-traversal is sanitized (filepath.Base)
// ---------------------------------------------------------------------------

func TestValidateEndpoint_FilenamePathTraversalSanitized(t *testing.T) {
	if !goAvailable() {
		t.Skip("go binary not available")
	}

	wsDir := t.TempDir()
	t.Setenv("TEST_WORKSPACE", wsDir)

	// A filename carrying a directory component (a path-traversal attempt) must
	// be reduced to its base name via filepath.Base, so the write + build stay
	// inside the per-request temp dir. The request therefore SUCCEEDS — the code
	// compiles at <tempPath>/test.go — instead of escaping the workspace or
	// failing on a missing subdir (the pre-fix behavior this test used to assert).
	rec := postJSON(NewRouter(), "/validate", map[string]interface{}{
		"code":     "package main\nfunc main() {}\n",
		"filename": "../../nonexistent_subdir/test.go",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 (filename sanitized), got %d; body=%s", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	if valid, _ := body["valid"].(bool); !valid {
		t.Fatalf("expected valid=true after filename sanitization, got body=%s", rec.Body.String())
	}
}

// ---------------------------------------------------------------------------
// runGoTest — test file write failure
// ---------------------------------------------------------------------------

func TestRunGoTest_TestFileWriteFailure(t *testing.T) {
	if !goAvailable() {
		t.Skip("go binary not available")
	}
	if runtime.GOOS == "windows" {
		t.Skip("chmod not reliable on Windows")
	}

	tempDir := t.TempDir()

	// Let go mod init succeed and code file write succeed, then block
	// writing the test file by placing a read-only directory named main_test.go.
	modCmd := exec.Command("go", "mod", "init", "test")
	modCmd.Dir = tempDir
	if err := modCmd.Run(); err != nil {
		t.Fatal(err)
	}

	// Write code file first (so runGoTest's code-write succeeds)
	codeFile := filepath.Join(tempDir, "main.go")
	if err := os.WriteFile(codeFile, []byte("package main\nfunc main(){}\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Create a directory named main_test.go so WriteFile fails
	testFilePath := filepath.Join(tempDir, "main_test.go")
	if err := os.MkdirAll(testFilePath, 0755); err != nil {
		t.Fatal(err)
	}

	// runGoTest will try to go mod init again (which will see go.mod exists
	// and succeed since it's a no-op overwrite), write code (will overwrite),
	// then try to write main_test.go (which is a directory — WriteFile fails).
	//
	// Actually, go mod init will fail because go.mod already exists and it
	// won't overwrite it. Let me check... go mod init returns error if module
	// already initialized. So we need runGoTest to do its own go mod init.
	// Let me remove go.mod and let runGoTest handle it.
	_ = os.Remove(filepath.Join(tempDir, "go.mod"))
	_ = os.Remove(codeFile)

	result, err := runGoTest(tempDir, "package main\nfunc main(){}\n", "package main\nimport \"testing\"\nfunc TestX(t *testing.T){}\n", 10)
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if result.Success {
		t.Fatal("expected failure when main_test.go is a directory")
	}
	if result.Error != "Failed to write test file" {
		t.Fatalf("expected 'Failed to write test file', got %q", result.Error)
	}
}

// ---------------------------------------------------------------------------
// DefaultTimeout constant
// ---------------------------------------------------------------------------

func TestDefaultTimeout(t *testing.T) {
	if DefaultTimeout != 30*time.Second {
		t.Fatalf("expected DefaultTimeout=30s, got %v", DefaultTimeout)
	}
}

// ---------------------------------------------------------------------------
// runGoTest — code file (main.go) write failure
// ---------------------------------------------------------------------------

func TestRunGoTest_CodeFileWriteFailure(t *testing.T) {
	if !goAvailable() {
		t.Skip("go binary not available")
	}
	if runtime.GOOS == "windows" {
		t.Skip("chmod not reliable on Windows")
	}

	tempDir := t.TempDir()

	// Place a directory named "main.go" so that os.WriteFile("main.go", ...)
	// fails (you can't write a regular file over an existing directory).
	mainGoDir := filepath.Join(tempDir, "main.go")
	if err := os.MkdirAll(mainGoDir, 0755); err != nil {
		t.Fatal(err)
	}

	result, err := runGoTest(tempDir, "package main\nfunc main(){}\n", "package main\nimport \"testing\"\nfunc TestX(t *testing.T){}\n", 10)
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if result.Success {
		t.Fatal("expected failure when main.go is a directory")
	}
	if result.Error != "Failed to write code file" {
		t.Fatalf("expected 'Failed to write code file', got %q", result.Error)
	}
}
