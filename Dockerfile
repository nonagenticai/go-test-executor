# Go Test Executor
#
# ONE stage from the canonical upstream image, pinned by TAG: there is no private
# base image behind it, nothing pins a digest, and the tag keeps the revision
# resolvable on the public registry.
FROM docker.io/library/golang:1.26-alpine

WORKDIR /app

RUN apk add --no-cache curl wget git build-base bash ca-certificates

# Install Go tools — golangci-lint bundles staticcheck/gosec/gocyclo/etc as
# runners; only need the golangci-lint binary + goimports. Pre-built binary
# avoids transitive x/tools rebuild that breaks under Go 1.26.
RUN curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/HEAD/install.sh \
        | sh -s -- -b "$(go env GOPATH)/bin" v1.64.8 && \
    go install golang.org/x/tools/cmd/goimports@v0.27.0

RUN go install github.com/onsi/ginkgo/v2/ginkgo@latest && \
    go install github.com/onsi/gomega/...@latest

RUN mkdir -p /app/test_workspace /app/reports /app/cache

COPY src/go_test_service.go /app/

RUN go mod init go_test_service && \
    go mod tidy && \
    go build -o go_test_service go_test_service.go

ENV GO_TEST_PORT=8091 \
    TEST_WORKSPACE=/app/test_workspace \
    REPORTS_DIR=/app/reports \
    CGO_ENABLED=0

EXPOSE 8091

HEALTHCHECK --interval=30s --timeout=15s --start-period=60s --retries=3 \
    CMD curl -f http://localhost:8091/health || exit 1

CMD ["/app/go_test_service"]
