# go-test-executor

Sandboxed Go code execution service for the code-graph-helper platform. Accepts Go source code and test code over HTTP, runs `go test` in an isolated temporary directory, and returns structured results. Part of a family of language-specific test executors (Python, JavaScript, Java, Rust, C#, C++, Go) that the `coding-testing` service dispatches to.

## Architecture

Single Go binary (`go_test_service.go`) using gorilla/mux. Each test execution:

1. Creates an ephemeral directory under `/app/test_workspace/<uuid>`
2. Initializes a Go module (`go mod init`)
3. Writes the submitted source and test files
4. Runs `go test -v -timeout <N>s` with context-based deadline enforcement
5. Cleans up the directory after execution

The container image ships with Go 1.24 plus static analysis and testing tools (golangci-lint, gosec, staticcheck, gocyclo, ineffassign, goimports, ginkgo, gomega) for potential future use in validation/linting endpoints.

## Port

**8091** (configurable via `PORT` env var)

## API

All responses are `application/json`.

### `GET /health`

Service health check. Returns runtime stats, supported frameworks, and capability flags.

**Response:**
```json
{
  "status": "healthy",
  "service": "go-test-executor",
  "version": "1.0.0",
  "timestamp": "2026-03-10T12:00:00Z",
  "capabilities": {
    "frameworks": ["go test", "testify"],
    "buildSystems": ["go mod", "go get"],
    "features": { "parallelExecution": true, "coverage": true, "debugging": true }
  },
  "runtime": {
    "version": "1.24.0",
    "memory": { "used": 1234567, "total": 9876543 },
    "uptime": 3600.5
  }
}
```

### `POST /execute`

Run Go tests. Max request body: 10 MB.

**Request:**
```json
{
  "code": "package main\n\nfunc Add(a, b int) int { return a + b }",
  "test_code": "package main\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n  if Add(1,2) != 3 { t.Fatal(\"wrong\") }\n}",
  "language": "go",
  "timeout": 30
}
```

| Field | Required | Description |
|-------|----------|-------------|
| `code` | At least one of `code`/`test_code` | Source code (written to `main.go`) |
| `test_code` | At least one of `code`/`test_code` | Test code (written to `main_test.go`). If omitted and `code` is provided, a compilation-check test is auto-generated. |
| `language` | No | Ignored (always Go) |
| `timeout` | No | Seconds (values <1000) or milliseconds (values >=1000). Default: 30s. |

**Response:**
```json
{
  "success": true,
  "test_id": "a1b2c3d4-...",
  "execution_time": "1.234",
  "output": "=== RUN   TestAdd\n--- PASS: TestAdd (0.00s)\nPASS\n",
  "error": "",
  "exit_code": 0
}
```

Exit codes: `0` = pass, `1` = test failure or build error, `124` = timeout.

A fast-path skips actual execution for minimal/probe requests (e.g., `code: "// test"`).

### `POST /validate`

Check whether Go code compiles without running tests.

**Request:**
```json
{
  "code": "package main\n\nfunc main() {}",
  "filename": "main.go"
}
```

| Field | Required | Description |
|-------|----------|-------------|
| `code` | Yes | Go source code to validate |
| `filename` | No | Filename to use (default: `test.go`) |

**Response:**
```json
{
  "validationId": "a1b2c3d4-...",
  "valid": true,
  "errors": [],
  "output": ""
}
```

### `GET /info`

Static metadata about the service (Go version, platform, supported features).

**Response:**
```json
{
  "service": "go-test-executor",
  "version": "1.0.0",
  "go_version": "go1.24.0",
  "platform": "linux",
  "arch": "amd64",
  "supported_languages": ["go"],
  "features": ["go_modules", "testing_package", "benchmarks", "coverage", "race_detection", "timeout_management"],
  "capabilities": { "modules": true, "benchmarks": true, "coverage": true, "race_detection": true, "fuzzing": true }
}
```

### `GET /frameworks`

Lists supported test frameworks and their capabilities.

**Response:** Framework details for `gotest` (built-in) and `testify`, plus capability flags (parallel tests, coverage, mocking, benchmarking, linting).

## Environment Variables

| Variable | Default | Read by code | Description |
|----------|---------|--------------|-------------|
| `PORT` | `8091` | Yes | HTTP listen port (the only env var the binary reads) |
| `GO_TEST_PORT` | `8091` | No | Dockerfile-only label; code ignores it |
| `TEST_WORKSPACE` | `/app/test_workspace` | No | Dockerfile-only; workspace path is hardcoded as a const |
| `REPORTS_DIR` | `/app/reports` | No | Dockerfile-only; directory created but never used |
| `CGO_ENABLED` | `0` | No | Dockerfile-only; disables CGO at build time |
| `TEST_MODE` | — | No | docker-compose-only; code does not read it |

## Dependencies

- **Upstream:** `coding-testing` (port 8689) routes Go test requests to this service
- **Downstream:** None. Standalone executor with no database or external service calls.
- **Network:** Connects to `workflow-system-network` bridge in docker-compose; in k8s, lives in the `coding` namespace

## Local Development

```bash
# Run with docker-compose
docker compose up -d go-test-executor

# Test health
curl http://localhost:8091/health

# Run a test
curl -X POST http://localhost:8091/execute \
  -H 'Content-Type: application/json' \
  -d '{"code":"package main\n\nfunc Add(a,b int) int { return a+b }","test_code":"package main\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) { if Add(1,2)!=3 { t.Fatal() } }"}'
```

## CI/CD

GitHub Actions on `nonagenticai/go-test-executor`, via the public `ci-workflows` archetype on GitHub-hosted runners. On every PR and on `main`:

1. Secret scan and private-infrastructure scan
2. `go mod verify`, `go vet ./...`, `go test ./...`
3. Image build and a `/health` smoke against it
4. From `main`: push `sha-<commit>` + `latest` to `ghcr.io/nonagenticai/go-test-executor` with a SLSA provenance attestation

There is no deploy job: this service is retired from the estate's cluster, and the repository is published so its tests, image build and job contract stay reproducible.
