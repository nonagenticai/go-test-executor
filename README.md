# go-test-executor

[![CI](https://github.com/nonagenticai/go-test-executor/actions/workflows/ci.yml/badge.svg)](https://github.com/nonagenticai/go-test-executor/actions/workflows/ci.yml)
[![License: Apache-2.0](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)

Sandboxed Go code-execution service. It accepts Go source and test code over HTTP, runs
`go test` in an ephemeral directory, and returns structured results. It is the Go member of the
language-specific executor family that `coding-testing` dispatches to; unlike a general CI runner
it needs no checkout, no cache and no database — each request is compiled and tested in a
throwaway directory that is deleted afterwards.

| | |
|---|---|
| **Runtime** | Go 1.26 — base image `golang:1.26-alpine`; `src/go.mod` requires `go 1.26.3` with toolchain `go1.26.6` |
| **Depends on** | Nothing at runtime: no database, cache, queue or external service. The Go toolchain ships inside the image. |
| **Runs on** | A container listening on `8091`. CI has no deploy job; the service is retired from the estate cluster. |

## Quick start

```bash
git clone https://github.com/nonagenticai/go-test-executor.git
cd go-test-executor
docker build -t go-test-executor .
docker run --rm -p 8091:8091 go-test-executor
curl http://localhost:8091/health
```

## Usage

`/health` is the only registered route that needs no header. Every other route requires
`X-Tenant-Id`, the access marker the orchestrator injects; without it the service answers
`401 {"error":"X-Tenant-Id header required"}`. This is a lightweight marker, **not**
authentication and not data tenancy — the real isolation control is the cluster NetworkPolicy
that admits only the orchestrator.

```bash
curl -X POST http://localhost:8091/execute \
  -H 'Content-Type: application/json' \
  -H 'X-Tenant-Id: coding-testing' \
  -d '{
        "code": "package main\n\nfunc Add(a, b int) int { return a + b }",
        "test_code": "package main\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) { if Add(1, 2) != 3 { t.Fatal(\"wrong\") } }",
        "timeout": 30
      }'
```

A passing response (note that `exit_code` is omitted when it is `0` — the field carries
`omitempty`):

```json
{
  "success": true,
  "test_id": "a1b2c3d4-...",
  "execution_time": "1.234",
  "output": "=== RUN   TestAdd\n--- PASS: TestAdd (0.00s)\nPASS\n",
  "tests_run": 1
}
```

A request that runs no test is **not** a pass. The `// test` liveness probe is answered without
compiling anything, and says so:

```json
{
  "success": false,
  "test_id": "a1b2c3d4-...",
  "execution_time": "0.000",
  "output": "Probe payload: nothing was compiled and nothing was executed.",
  "error": "no tests were run",
  "details": "probe_payload",
  "exit_code": 1,
  "tests_run": 0,
  "no_tests_run": true
}
```

## API

All responses are `application/json`. Every route except `/health` requires `X-Tenant-Id`.

### `GET /health`

Service health and runtime stats. No header required.

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
    "version": "1.26.6",
    "memory": { "used": 1234567, "total": 9876543 },
    "uptime": 3600.5
  }
}
```

`runtime.version` is `runtime.Version()` of the build, i.e. the Go 1.26 toolchain; the sample
shows the `go1.26.6` toolchain pinned in `src/go.mod`.

### `POST /execute`

Run Go test code. Maximum request body: 10 MB (`http.MaxBytesReader`, `MaxCodeSize`).

| Field | Required | Description |
|-------|----------|-------------|
| `code` | One of `code`/`test_code` | Source code, written to `main.go` |
| `test_code` | One of `code`/`test_code` | Test code, written to `main_test.go`. When omitted, **no test file is written** — the service never fabricates a test — and a run that collects zero tests is reported as a failure, not a pass. |
| `language` | No | Ignored (always Go) |
| `timeout` | No | Values `< 1000` are seconds, values `>= 1000` are milliseconds. Default 30 s. |

Response fields: `success`, `test_id`, `execution_time` (string, seconds), `output`,
`error`, `details`, `exit_code`, `tests_run`, `no_tests_run`, `no_tests_run_unknown`.
`exit_code`, `error`, `details` and the two `no_tests_run*` flags are omitted when empty/false.

`success` is true only when at least one `--- PASS:` marker was counted and no failure occurred.
Exit codes:

| Code | Meaning |
|------|---------|
| `0` (omitted from JSON) | At least one test ran and passed |
| `1` | Compile error, test failure, zero tests collected, or a setup failure |
| `124` | Execution timed out; `no_tests_run_unknown` is also set because a killed run cannot prove zero collection |

**Fast path.** The exact payload `{"code": "// test"}` with **no** `test_code` is a liveness
probe: nothing is compiled or executed, and the response is the honest
`success:false / exit_code:1 / no_tests_run:true / details:"probe_payload"` shown above. A
request that carries any `test_code` always takes the real execution path.

### `POST /validate`

Compile Go code without running tests (runs `go build <filename>` in a temp directory).

| Field | Required | Description |
|-------|----------|-------------|
| `code` | Yes | Go source code to validate |
| `filename` | No | Filename to use (default `test.go`); reduced to its base name, so a path cannot escape the temp directory |

```json
{
  "validationId": "a1b2c3d4-...",
  "valid": true,
  "errors": [],
  "output": ""
}
```

A missing `code` returns HTTP 400 with `valid: false` and `errors: ["No code provided"]`.

### `GET /info`

Static metadata: `service`, `version`, `go_version` (`runtime.Version()`), `platform`, `arch`,
`supported_languages`, `features` and `capabilities`. The example below is the `linux/amd64`
build:

```json
{
  "service": "go-test-executor",
  "version": "1.0.0",
  "go_version": "go1.26.6",
  "platform": "linux",
  "arch": "amd64",
  "supported_languages": ["go"],
  "features": ["go_modules", "testing_package", "benchmarks", "coverage", "race_detection", "timeout_management"],
  "capabilities": { "modules": true, "benchmarks": true, "coverage": true, "race_detection": true, "fuzzing": true }
}
```

### `GET /frameworks`

Returns `supported` (a map with `gotest` and `testify`), `default` (`gotest`),
`recommended` (`testify`) and `capabilities` (`testExecution`, `parallelTests`, `coverage`,
`mocking`, `benchmarking`, `linting`, `assertions`).

## Architecture

A single Go binary (`src/go_test_service.go`) using `gorilla/mux`. Each execution:

1. Creates `<TEST_WORKSPACE>/<uuid>` (mode `0750`; the workspace root defaults to
   `/app/test_workspace`).
2. Runs `go mod init test` in that directory.
3. Writes `main.go` when `code` is supplied, and `main_test.go` **only** when `test_code` is
   supplied.
4. Runs `go test -v -timeout <N>s` under a context deadline.
5. Counts `--- PASS:` / `--- FAIL:` markers; zero markers is not a success.
6. Deletes the directory.

The image is built from `golang:1.26-alpine` and additionally installs `golangci-lint` v1.64.8
(which bundles staticcheck, gosec, gocyclo and the other linters as runners), `goimports`
v0.27.0, and `ginkgo`/`gomega`. These tools are available in the image but no current endpoint
invokes them.

## Configuration

| Variable | Default | Read by code | Meaning |
|----------|---------|--------------|---------|
| `PORT` | `8091` | Yes — `main()` in `src/go_test_service.go` | HTTP listen port |
| `TEST_WORKSPACE` | `/app/test_workspace` | Yes — `getTestWorkspace()` in `src/go_test_service.go` | Root for per-request temp directories; an empty value falls back to the compiled-in constant |
| `GO_TEST_PORT` | `8091` | No | Dockerfile-only; the binary does not read it |
| `REPORTS_DIR` | `/app/reports` | No | Dockerfile-only; the directory is created but never used |
| `CGO_ENABLED` | `0` | No | Dockerfile build-time setting only |
| `TEST_MODE` | — | No | docker-compose-only; the binary does not read it |

`X-Tenant-Id` is a request header, not an environment variable; see **Usage**.

## Dependencies

- **Upstream:** `coding-testing` (port 8689) routes Go test requests to this service.
- **Downstream:** none — the executor makes no database or external-service calls.
- **Network:** `docker-compose.yml` attaches it to the `workflow-system-network` bridge. The
  service is retired from the estate cluster, so there is no live Kubernetes deployment.

## Development

The binary lives in `src/`; run the same checks CI runs:

```bash
cd src
go mod verify
go vet ./...
go test ./... -count=1
```

Build the image from the repository root:

```bash
docker build -t go-test-executor .
```

Run it with compose (port `8091`):

```bash
docker compose up -d go-test-executor
curl http://localhost:8091/health
```

## Security

Report vulnerabilities privately through the repository's Security tab or the organisation
policy at [`SECURITY.md`](https://github.com/nonagenticai/.github/blob/main/SECURITY.md). Do not
open a public issue for a security problem.

The `X-Tenant-Id` check is an access marker that keeps accidental callers out; it is not
authentication, and it does not verify a tenant. Do not rely on it for isolation — restrict
network reachability with a NetworkPolicy.

## Contributing

Issues and pull requests are welcome. See
[`CONTRIBUTING.md`](https://github.com/nonagenticai/.github/blob/main/CONTRIBUTING.md) for the
organisation-wide rules; this repository's own commands are in **Development** above.

## CI/CD

GitHub Actions on `nonagenticai/go-test-executor`, via the public `ci-workflows` archetype on
GitHub-hosted runners. On every PR and on `main`:

1. Secret scan and private-infrastructure scan
2. `go mod verify`, `go vet ./...`, `go test ./... -count=1`
3. Image build and a `/health` smoke test against it
4. From `main`: push `sha-<commit>` + `latest` to `ghcr.io/nonagenticai/go-test-executor` with a
   SLSA provenance attestation

There is no deploy job: this service is retired from the estate's cluster, and the repository is
published so its tests, image build and job contract stay reproducible.

## Licensing

The code here is Apache-2.0 and needs no licence key to build, test or run. This service
performs **no licence enforcement**: it reads no licence environment variable and never calls a
licence server, so a clone with no licensing infrastructure behaves exactly like the deployed
instance.

The estate's optional licence gate is external and lives outside this repository: an Envoy
`ext_authz` filter consults [`license-authz`](https://github.com/nonagenticai/license-authz) only
for hosts listed in its `LICENSE_GATED_APP_KEY` table. The default is **no gate** — a host that
is not in that table is never licence-checked — and `go-test-executor` is not in it.

## License

Apache-2.0 — see [`LICENSE`](LICENSE). This repository has no `NOTICE` file.
