package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
)

const (
	DefaultPort    = "8091"
	DefaultTimeout = 30 * time.Second
	TestWorkspace  = "/app/test_workspace"
	MaxCodeSize    = 10 * 1024 * 1024 // 10MB
)

var startTime = time.Now()

// getTestWorkspace returns the workspace directory, preferring the
// TEST_WORKSPACE environment variable over the compiled-in default.
func getTestWorkspace() string {
	if ws := os.Getenv("TEST_WORKSPACE"); ws != "" {
		return ws
	}
	return TestWorkspace
}

// resolveWithinRoot resolves rel beneath root and reports whether the cleaned
// absolute result stays inside root. The comparison appends the path separator
// before the prefix test, so a sibling directory sharing the root's name prefix
// (for example "/app/test_workspace-evil") cannot pass. Callers must use the
// returned, cleaned path for the file operation.
func resolveWithinRoot(root, rel string) (string, bool) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", false
	}
	cleaned := filepath.Clean(filepath.Join(absRoot, rel))
	if cleaned != absRoot && !strings.HasPrefix(cleaned, absRoot+string(os.PathSeparator)) {
		return "", false
	}
	return cleaned, true
}

type TestRequest struct {
	Code     string `json:"code"`
	TestCode string `json:"test_code"`
	Language string `json:"language"`
	Timeout  int    `json:"timeout,omitempty"`
}

type TestResponse struct {
	Success       bool   `json:"success"`
	TestID        string `json:"test_id"`
	ExecutionTime string `json:"execution_time"`
	Output        string `json:"output,omitempty"`
	Error         string `json:"error,omitempty"`
	Details       string `json:"details,omitempty"`
	ExitCode      int    `json:"exit_code,omitempty"`
	TestsRun      int    `json:"tests_run"`
	NoTestsRun    bool   `json:"no_tests_run,omitempty"`
	// NoTestsRunUnknown marks the one mode in which this runner genuinely
	// CANNOT measure zero-collection, rather than defaulting that mode clean.
	// See the DeadlineExceeded branch of runGoTest.
	NoTestsRunUnknown bool `json:"no_tests_run_unknown,omitempty"`
}

type HealthResponse struct {
	Status       string       `json:"status"`
	Service      string       `json:"service"`
	Version      string       `json:"version"`
	Timestamp    string       `json:"timestamp"`
	Capabilities Capabilities `json:"capabilities"`
	Runtime      Runtime      `json:"runtime"`
}

type Capabilities struct {
	Frameworks   []string   `json:"frameworks"`
	BuildSystems []string   `json:"buildSystems"`
	Features     FeatureSet `json:"features"`
}

type FeatureSet struct {
	ParallelExecution bool `json:"parallelExecution"`
	Coverage          bool `json:"coverage"`
	Debugging         bool `json:"debugging"`
}

type Runtime struct {
	Version string      `json:"version"`
	Memory  MemoryStats `json:"memory"`
	Uptime  float64     `json:"uptime"`
}

type MemoryStats struct {
	Used  uint64 `json:"used"`
	Total uint64 `json:"total"`
}

type FrameworksResponse struct {
	Supported    map[string]FrameworkInfo `json:"supported"`
	Default      string                   `json:"default"`
	Recommended  string                   `json:"recommended"`
	Capabilities FrameworkCapabilities    `json:"capabilities"`
}

type FrameworkInfo struct {
	Name           string   `json:"name"`
	Version        string   `json:"version"`
	Dependencies   []string `json:"dependencies"`
	ConfigFile     *string  `json:"configFile"`
	TestExtensions []string `json:"testExtensions"`
}

type FrameworkCapabilities struct {
	TestExecution bool     `json:"testExecution"`
	ParallelTests bool     `json:"parallelTests"`
	Coverage      bool     `json:"coverage"`
	Mocking       bool     `json:"mocking"`
	Benchmarking  bool     `json:"benchmarking"`
	Linting       bool     `json:"linting"`
	Assertions    []string `json:"assertions"`
}

// tenantExemptPaths are open to unauthenticated probes; every other route
// requires the orchestrator-only X-Tenant-Id header.
var tenantExemptPaths = map[string]bool{
	"/health":  true,
	"/healthz": true,
	"/ready":   true,
}

// tenantGuardMiddleware enforces the orchestrator-only access marker: every
// non-exempt route must carry an X-Tenant-Id header (this executor is reachable
// only from the orchestrator). This is a lightweight marker, NOT data-tenancy —
// the executor is stateless and the header is unauthenticated; a NetworkPolicy
// restricting ingress to the orchestrator is the real isolation control.
func tenantGuardMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !tenantExemptPaths[r.URL.Path] && r.Header.Get("X-Tenant-Id") == "" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "X-Tenant-Id header required"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// NewRouter creates and returns the configured mux.Router with all routes registered.
func NewRouter() *mux.Router {
	router := mux.NewRouter()
	router.Use(tenantGuardMiddleware)
	router.HandleFunc("/health", healthHandler).Methods("GET")
	router.HandleFunc("/execute", executeHandler).Methods("POST")
	router.HandleFunc("/info", infoHandler).Methods("GET")
	router.HandleFunc("/frameworks", frameworksHandler).Methods("GET")
	router.HandleFunc("/validate", validateHandler).Methods("POST")
	return router
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = DefaultPort
	}

	router := NewRouter()

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
	}

	log.Printf("Go Test Executor starting on port %s", port) // #nosec G706 -- PORT is operator-set deployment env, not untrusted request input
	log.Fatal(srv.ListenAndServe())
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	// Get memory stats
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	response := HealthResponse{
		Status:    "healthy",
		Service:   "go-test-executor",
		Version:   "1.0.0",
		Timestamp: time.Now().Format(time.RFC3339),
		Capabilities: Capabilities{
			Frameworks:   []string{"go test", "testify"},
			BuildSystems: []string{"go mod", "go get"},
			Features: FeatureSet{
				ParallelExecution: true,
				Coverage:          true,
				Debugging:         true,
			},
		},
		Runtime: Runtime{
			Version: strings.TrimPrefix(runtime.Version(), "go"),
			Memory: MemoryStats{
				Used:  m.HeapAlloc,
				Total: m.HeapSys,
			},
			Uptime: time.Since(startTime).Seconds(),
		},
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(response)
}

func executeHandler(w http.ResponseWriter, r *http.Request) {
	// Limit request size
	r.Body = http.MaxBytesReader(w, r.Body, MaxCodeSize)

	var req TestRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		sendError(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// Validate input
	if req.Code == "" && req.TestCode == "" {
		sendError(w, "Either code or test_code must be provided", http.StatusBadRequest)
		return
	}

	testID := uuid.New().String()
	startTime := time.Now()

	// Fast-path ONLY for the exact `// test` liveness probe (synthetic
	// endpoint-existence ping). It exists to answer "is /execute wired up?"
	// without paying for a `go mod init` + `go test` cycle. It deliberately
	// does NOT cover real code: a submission with code but no tests must go
	// through runGoTest and be reported as "no tests were run", not
	// auto-passed.
	//
	// It MUST NOT claim tests passed. Nothing here is compiled and nothing is
	// executed, so the honest answer is zero tests run — and by this service's
	// own rule (see the `run == 0` branch of runGoTest) an execution in which
	// no test ran is NOT a success. It previously answered `success: true,
	// exit_code: 0` with `no_tests_run` absent: a PASS having run nothing.
	//
	// `req.TestCode == ""` IS LOAD-BEARING. A probe is a request with nothing
	// to run. A caller that sends a placeholder `code` alongside a genuine
	// `test_code` has a real suite: matching on `code` alone swallowed it —
	// runGoTest was never called, the response was 200 `success: true`, and a
	// suite that would have gone RED reported green. Any request carrying
	// test_code takes the real path, however trivial its `code` looks.
	isProbeRequest := strings.TrimSpace(req.Code) == "// test" && req.TestCode == ""

	if isProbeRequest {
		// Same field set as the full path, with honest values: the three
		// outcomes stay distinguishable (zero-collected here, all-passed and
		// genuine-failure in runGoTest), and `details` names WHY this one is
		// zero — a probe payload, not a user suite that collected nothing.
		response := TestResponse{
			Success:       false,
			TestID:        testID,
			ExecutionTime: fmt.Sprintf("%.3f", time.Since(startTime).Seconds()),
			Output:        "Probe payload: nothing was compiled and nothing was executed.",
			Error:         "no tests were run",
			Details:       "probe_payload",
			ExitCode:      1,
			TestsRun:      0,
			NoTestsRun:    true,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(response)
		return
	}

	// Create temp directory
	tempDir := filepath.Join(getTestWorkspace(), testID)
	if err := os.MkdirAll(tempDir, 0750); err != nil {
		sendError(w, "Failed to create test directory", http.StatusInternalServerError)
		return
	}
	defer func() { _ = os.RemoveAll(tempDir) }()

	// Execute test
	result, err := runGoTest(tempDir, req.Code, req.TestCode, req.Timeout)
	if err != nil {
		log.Printf("Test execution error: %v", err)
	}

	executionTime := time.Since(startTime).Seconds()

	response := TestResponse{
		Success:       result.Success,
		TestID:        testID,
		ExecutionTime: fmt.Sprintf("%.3f", executionTime),
		Output:        result.Output,
		Error:         result.Error,
		ExitCode:      result.ExitCode,
		TestsRun:      result.TestsRun,
		NoTestsRun:    result.NoTestsRun,

		NoTestsRunUnknown: result.NoTestsRunUnknown,
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(response)
}

func infoHandler(w http.ResponseWriter, r *http.Request) {
	info := map[string]interface{}{
		"service":             "go-test-executor",
		"version":             "1.0.0",
		"go_version":          runtime.Version(),
		"platform":            runtime.GOOS,
		"arch":                runtime.GOARCH,
		"supported_languages": []string{"go"},
		"features": []string{
			"go_modules",
			"testing_package",
			"benchmarks",
			"coverage",
			"race_detection",
			"timeout_management",
		},
		"capabilities": map[string]bool{
			"modules":        true,
			"benchmarks":     true,
			"coverage":       true,
			"race_detection": true,
			"fuzzing":        true,
		},
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(info)
}

type TestResult struct {
	Success           bool
	Output            string
	Error             string
	ExitCode          int
	TestsRun          int
	NoTestsRun        bool
	NoTestsRunUnknown bool
}

func runGoTest(tempDir, code, testCode string, timeout int) (*TestResult, error) {
	// Initialize go module
	modCmd := exec.Command("go", "mod", "init", "test")
	modCmd.Dir = tempDir
	if err := modCmd.Run(); err != nil {
		return &TestResult{
			Success:  false,
			Error:    "Failed to initialize Go module",
			ExitCode: 1,
		}, nil
	}

	// Write code file
	if code != "" {
		codePath := filepath.Join(tempDir, "main.go")
		if err := os.WriteFile(codePath, []byte(code), 0600); err != nil {
			return &TestResult{
				Success:  false,
				Error:    "Failed to write code file",
				ExitCode: 1,
			}, nil
		}
	}

	// Write the test file ONLY when real tests were supplied. We never
	// fabricate a passing test: a submission with no tests is not a pass.
	// `go test` still compiles the package below (reporting "no test files"
	// on exit 0), so genuine compile errors are still surfaced.
	if testCode != "" {
		testPath := filepath.Join(tempDir, "main_test.go")
		if err := os.WriteFile(testPath, []byte(testCode), 0600); err != nil {
			return &TestResult{
				Success:  false,
				Error:    "Failed to write test file",
				ExitCode: 1,
			}, nil
		}
	}

	// Set timeout (convert from milliseconds to seconds if needed)
	var timeoutSeconds int
	if timeout > 0 && timeout < 1000 {
		// If timeout is less than 1000, assume it's in seconds
		timeoutSeconds = timeout
	} else if timeout >= 1000 {
		// If timeout is 1000 or more, assume it's in milliseconds
		timeoutSeconds = timeout / 1000
	} else {
		// Default timeout
		timeoutSeconds = 30
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSeconds)*time.Second)
	defer cancel()

	// Run go test
	cmd := exec.CommandContext(ctx, "go", "test", "-v", "-timeout", fmt.Sprintf("%ds", timeoutSeconds)) // #nosec G204 -- sandboxed go-toolchain run on user-submitted code; ephemeral per-request temp dir, orchestrator-only ingress (NetworkPolicy)
	cmd.Dir = tempDir

	output, err := cmd.CombinedOutput()

	run, _, _ := countGoTests(string(output))
	result := &TestResult{
		Output:   string(output),
		TestsRun: run,
	}

	switch {
	case ctx.Err() == context.DeadlineExceeded:
		// The test binary was killed mid-run, so `output` is whatever had been
		// flushed before the kill. countGoTests over a truncated stream cannot
		// distinguish "collected nothing" from "hung before printing its first
		// marker", so zero markers here is NOT evidence of zero collection.
		// Report the unmeasurable state by name instead of letting
		// `no_tests_run: false` assert a measurement that was never taken.
		result.Success = false
		result.Error = "Test execution timeout"
		result.ExitCode = 124
		result.NoTestsRunUnknown = true
	case err != nil:
		// Non-zero exit: a compile error or a failing test. Never a pass.
		if exitErr, ok := err.(*exec.ExitError); ok {
			result.ExitCode = exitErr.ExitCode()
		} else {
			result.ExitCode = 1
		}
		result.Success = false
		if result.Error == "" {
			result.Error = "Test execution failed"
		}
	case run == 0:
		// Compiled cleanly but no test actually ran. Per tournament policy
		// this is NOT a pass — report "no tests were run".
		result.Success = false
		result.NoTestsRun = true
		result.ExitCode = 1
		result.Error = "no tests were run"
	default:
		result.Success = true
		result.ExitCode = 0
	}

	return result, nil
}

// countGoTests parses `go test -v` output, counting the per-test PASS/FAIL
// markers. A submission that produces zero markers ran no tests.
func countGoTests(output string) (run, passed, failed int) {
	for _, line := range strings.Split(output, "\n") {
		s := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(s, "--- PASS:"):
			passed++
		case strings.HasPrefix(s, "--- FAIL:"):
			failed++
		}
	}
	return passed + failed, passed, failed
}

func frameworksHandler(w http.ResponseWriter, r *http.Request) {
	response := FrameworksResponse{
		Supported: map[string]FrameworkInfo{
			"gotest": {
				Name:           "Go Test",
				Version:        "built-in",
				Dependencies:   []string{},
				ConfigFile:     nil,
				TestExtensions: []string{".go"},
			},
			"testify": {
				Name:           "Testify",
				Version:        "1.8.0+",
				Dependencies:   []string{"github.com/stretchr/testify"},
				ConfigFile:     nil,
				TestExtensions: []string{".go"},
			},
		},
		Default:     "gotest",
		Recommended: "testify",
		Capabilities: FrameworkCapabilities{
			TestExecution: true,
			ParallelTests: true,
			Coverage:      true,
			Mocking:       true,
			Benchmarking:  true,
			Linting:       true,
			Assertions:    []string{"testing.T", "assert", "require", "mock"},
		},
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(response)
}

func validateHandler(w http.ResponseWriter, r *http.Request) {
	validationID := uuid.New().String()

	var req struct {
		Code     string `json:"code"`
		Filename string `json:"filename"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		sendError(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if req.Code == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"validationId": validationID,
			"valid":        false,
			"errors":       []string{"No code provided"},
			"output":       "",
		})
		return
	}

	if req.Filename == "" {
		req.Filename = "test.go"
	}
	// Strip any directory component so writes/builds stay inside tempPath (path-traversal guard).
	req.Filename = filepath.Base(req.Filename)

	// Create temporary directory
	workspaceRoot := getTestWorkspace()
	tempPath := filepath.Join(workspaceRoot, validationID)
	if err := os.MkdirAll(tempPath, 0750); err != nil {
		sendError(w, "Failed to create temp directory", http.StatusInternalServerError)
		return
	}
	defer func() { _ = os.RemoveAll(tempPath) }()

	// Write code to file. filepath.Base above already removes directory
	// components, but CodeQL go/path-injection does not model that call as a
	// sanitizer. Resolve the destination beneath the workspace root, require
	// the cleaned result to sit under root + separator (the component boundary
	// refuses a "<root>-evil" sibling), and use that resolved path for the
	// write.
	goFile, ok := resolveWithinRoot(workspaceRoot, filepath.Join(validationID, req.Filename))
	if !ok {
		sendError(w, "Invalid filename", http.StatusBadRequest)
		return
	}
	if err := os.WriteFile(goFile, []byte(req.Code), 0600); err != nil {
		sendError(w, "Failed to write file", http.StatusInternalServerError)
		return
	}

	// Try to compile the file
	cmd := exec.Command("go", "build", req.Filename) // #nosec G204 -- sandboxed go-toolchain run on user-submitted code; filename is filepath.Base-sanitized, ephemeral temp dir, orchestrator-only ingress (NetworkPolicy)
	cmd.Dir = tempPath
	output, err := cmd.CombinedOutput()

	valid := err == nil
	errors := []string{}
	if err != nil {
		errors = append(errors, string(output))
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"validationId": validationID,
		"valid":        valid,
		"errors":       errors,
		"output":       string(output),
	})
}

func sendError(w http.ResponseWriter, message string, statusCode int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"error": message,
	})
}
