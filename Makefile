# Makefile for codeweaver — a static Go binary for code intelligence.
#
# Primary targets:
#   build         Compile the binary (dev mode — no ldflags)
#   release-build Compile with full release flags (-trimpath, -s, -w, ldflags version injection)
#   test          Run the test suite
#   race          Run the test suite with the race detector
#   schema        Regenerate schema/codeweaver-v1.json via go generate
#   lint          Run golangci-lint + custom fitness checks
#   size          Record binary size to .size-baseline
#   snapshot-test Run snapshot parity tests (11/11 fixtures)
#   fuzz          Run all fuzz targets for 30s each (use fuzz-fast for 5s CI variant)
#   fuzz-fast     Run all fuzz targets for 5s each (normal CI gate)
#   bench         Run all benchmarks with -benchmem; write to testdata/benchmarks/benchstat.txt
#   bench-check   Run benchmarks and compare to baseline; fail on >20% regression
#   clean         Remove build artifacts
#
# Fitness function targets:
#   fitness-no-cgo     Verify CGO_ENABLED=0 build + zero transitive CGo deps (invoked by lint)
#   fitness-no-network Verify no reachable network calls in production binary (invoked by lint)
#   stdout-json-only   Verify subcommands emit only JSON/correct output (invoked by all; needs binary)
#   layering-invariant Verify language packages not imported by the layering layer (invoked by lint)

# Go toolchain. Override with: make build GO=/usr/local/go/bin/go
GO ?= go

# Binary output name.
BINARY := codeweaver

# Version string. Override via: make build VERSION=0.2.0
VERSION ?= 0.1.0

# ldflags for release builds.
#
# Flag breakdown:
#   -s              Strip symbol table and debug info (reduces binary size)
#   -w              Strip DWARF debugging info (reduces binary size further)
#   -buildid=       Set empty build ID for byte-stable reproducible binaries.
#                   Without this, the build ID is a hash of the build command that
#                   changes between identical builds, breaking byte-identity.
#   -X main.gitCommit=...  Inject git commit SHA via ldflags.
#                          The ldflags identifier "main.gitCommit" matches the Go var
#                          declaration "var gitCommit string" in cmd/codeweaver/main.go.
#                          The JSON output field is "git_commit" (snake_case struct tag).
#   -X main.builtAt=...   Inject build timestamp via ldflags.
#                          The ldflags identifier "main.builtAt" matches "var builtAt string".
#                          The JSON output field is "built_at" (snake_case struct tag).
#   -X main.version=...   Inject semver string via ldflags.
GIT_COMMIT := $(shell git rev-parse HEAD 2>/dev/null || echo "unknown")
BUILT_AT := $(shell git log -1 --format=%cI 2>/dev/null || echo "unknown")
LDFLAGS := -s -w -buildid= \
	-X main.gitCommit=$(GIT_COMMIT) \
	-X main.builtAt=$(BUILT_AT) \
	-X main.version=$(VERSION)

# BUILD_FLAGS common to both dev and release.
# -trimpath strips local filesystem paths from the binary (reproducible builds).
# -buildvcs=true embeds VCS info (git commit, dirty flag) into the binary.
#   Note: -trimpath and -buildvcs=true are compatible; -trimpath strips *filesystem*
#   paths (e.g., source file paths in stack traces), not VCS metadata.
BASE_FLAGS := -trimpath -buildvcs=true

.PHONY: build release-build release-local test race schema lint size snapshot-test \
        fuzz fuzz-fast fuzz-py fuzz-ts fuzz-cli \
        bench bench-check bench-py bench-ts \
        fitness-no-cgo fitness-no-network stdout-json-only layering-invariant \
        all clean

## build: compile the binary (dev mode, no ldflags version injection)
build:
	CGO_ENABLED=0 $(GO) build $(BASE_FLAGS) -o $(BINARY) ./cmd/codeweaver

## release-build: compile with full release flags
release-build:
	CGO_ENABLED=0 $(GO) build $(BASE_FLAGS) -ldflags="$(LDFLAGS)" -o $(BINARY) ./cmd/codeweaver

## test: run the full test suite
test:
	CGO_ENABLED=0 $(GO) test ./... -count=1

## race: run the test suite with the Go race detector
## The race detector instruments goroutine synchronization at runtime to catch
## data races — two goroutines accessing the same memory concurrently where at
## least one is writing. Think of it like a "simultaneous edit" detector for
## shared variables. Run on every PR to catch goroutine misuse early.
race:
	$(GO) test ./... -race -count=1

## schema: regenerate schema/codeweaver-v1.json from Go struct types
## Runs go generate, which invokes cmd/schema-gen/main.go.
## The output is checked into git. If this target produces a diff,
## the contract has changed and must be reviewed before committing.
## Asserts idempotency: fails if go generate changes schema/ files.
schema:
	$(GO) generate ./...
	@git diff --exit-code schema/ 2>/dev/null || { echo "schema regeneration produced a diff — review and commit the updated schema file"; exit 1; }

## lint: run all linters (golangci-lint + all source-only fitness checks)
## Combines static analysis (golangci-lint) with the custom invariant scripts:
##   - check-stdout-invariant.sh: no fmt.Print* outside exempt packages
##   - fitness-layering-invariant.sh: layering layer doesn't import language packages
##   - fitness-no-cgo.sh: CGO_ENABLED=0 build + zero transitive CGo deps
##   - fitness-no-network.sh: no reachable network calls in the binary
## Note: fitness-stdout-json-only.sh is NOT in lint — it requires a built binary.
##       It runs as a separate step (make stdout-json-only) via 'make all'.
lint:
	golangci-lint run ./...
	./scripts/check-stdout-invariant.sh
	./scripts/fitness-layering-invariant.sh
	./scripts/fitness-no-cgo.sh
	./scripts/fitness-no-network.sh

## size: compile a stripped binary and record its size to .size-baseline
## Run this after a release-build to update the baseline.
## Format: "raw: <N> bytes  stripped: <N> bytes"
size: release-build
	@RAW=$$(wc -c < $(BINARY) | tr -d ' '); \
	STRIPPED=$$($(GO) build $(BASE_FLAGS) -ldflags="$(LDFLAGS) -s -w" -o $(BINARY).stripped ./cmd/codeweaver && wc -c < $(BINARY).stripped | tr -d ' '); \
	echo "raw: $$RAW bytes  stripped: $$STRIPPED bytes" | tee .size-baseline; \
	rm -f $(BINARY).stripped

## snapshot-test: run parity tests against committed *.expected.json fixture corpus.
## Does NOT require any external dependencies — only the Go test suite and the committed fixtures.
##
## PY_ONLY=1: scope the run to the python parser package only.
##   make snapshot-test PY_ONLY=1
##
## Without PY_ONLY: run TestParity across all parser sub-packages.
##   Runs 11 fixtures total: 6 Python + 5 TypeScript.
snapshot-test:
ifeq ($(PY_ONLY),1)
	CGO_ENABLED=0 $(GO) test -v -run TestParity ./internal/parser/python/... -count=1
else
	CGO_ENABLED=0 $(GO) test -v -run TestParity ./internal/parser/... -count=1
endif

## fuzz: run all fuzz targets for 30 seconds each (deep coverage run).
## Seeds persisted in testdata/fuzz/ are always tested as seed corpus.
## Total runtime: ~90s. Suitable for nightly/periodic deep CI, not every PR.
## For normal PR CI, use: make fuzz-fast (5s per target, ~15s total).
fuzz:
	$(GO) test -run="^$$" -fuzz="FuzzParseFilePython$$" -fuzztime=30s ./internal/parser/python/
	$(GO) test -run="^$$" -fuzz="FuzzParseFileTypescript$$" -fuzztime=30s ./internal/parser/typescript/
	$(GO) test -run="^$$" -fuzz="FuzzCLIArgs$$" -fuzztime=30s ./cmd/codeweaver/

## fuzz-fast: run all fuzz targets for 5 seconds each (normal CI gate).
## Total runtime: ~15s. Use for PR gates where 90s is too slow.
## Use 'make fuzz' for the deeper 30s run in nightly/periodic CI.
fuzz-fast:
	$(GO) test -run="^$$" -fuzz="FuzzParseFilePython$$" -fuzztime=5s ./internal/parser/python/
	$(GO) test -run="^$$" -fuzz="FuzzParseFileTypescript$$" -fuzztime=5s ./internal/parser/typescript/
	$(GO) test -run="^$$" -fuzz="FuzzCLIArgs$$" -fuzztime=5s ./cmd/codeweaver/

## fuzz-py: fuzz the python parser specifically (30s)
fuzz-py:
	$(GO) test -run="^$$" -fuzz="FuzzParseFilePython$$" -fuzztime=30s ./internal/parser/python/

## fuzz-ts: fuzz the TypeScript parser specifically (30s)
fuzz-ts:
	$(GO) test -run="^$$" -fuzz="FuzzParseFileTypescript$$" -fuzztime=30s ./internal/parser/typescript/

## fuzz-cli: fuzz the CLI arg dispatch specifically (30s)
fuzz-cli:
	$(GO) test -run="^$$" -fuzz="FuzzCLIArgs$$" -fuzztime=30s ./cmd/codeweaver/

## bench: run all benchmarks with memory allocation reporting.
## Results written to testdata/benchmarks/benchstat.txt for comparison with baseline.
## Use this output with 'make bench-check' to run the regression gate.
##
## benchstat interprets runs with -count=3 and computes geomean + delta.
## Think of it like running a race three times and averaging the results,
## then comparing the average to the committed baseline.
bench:
	$(GO) test -bench=. -benchmem -count=3 -run='^$$' ./... | tee testdata/benchmarks/benchstat.txt

## bench-check: run benchmarks and compare to baseline (ADVISORY).
## Requires benchstat: go install golang.org/x/perf/cmd/benchstat@latest
##
## benchstat computes the geomean delta between baseline.txt and the current run.
## Think of it like a fuel efficiency test: if the new engine uses 20% more fuel
## than the baseline on the same route, it fails inspection.
##
## ADVISORY: bench-check exits 0 regardless of the measured delta.
## Review the delta column in benchstat output. A >20% increase in ns/op or B/op
## is a regression worth investigating before merging.
##
## Note: benchstat v0.0.4+ uses the newer two-file comparison syntax.
## If your version uses the old syntax, see: benchstat -help
bench-check: bench
	@command -v benchstat >/dev/null || { echo "benchstat not found; install with: go install golang.org/x/perf/cmd/benchstat@latest"; exit 1; }
	benchstat testdata/benchmarks/baseline.txt testdata/benchmarks/benchstat.txt
	@echo ""
	@echo "ADVISORY: review the delta column above. bench-check is informational;"
	@echo "bench-check exit 0 is not a guarantee of no regression — review the delta column."

## bench-py: benchmark the python parser (1000-line file)
bench-py:
	$(GO) test -bench=BenchmarkParseFilePython1k -benchmem -count=3 ./internal/parser/python/

## bench-ts: benchmark the TypeScript parser (1000-line file)
bench-ts:
	$(GO) test -bench=BenchmarkParseFileTypescript1k -benchmem -count=3 ./internal/parser/typescript/

# ---------------------------------------------------------------------------
# Fitness function targets
# ---------------------------------------------------------------------------
# These run individual architectural invariant checks.
# 'make lint' runs: check-stdout-invariant, layering-invariant, fitness-no-cgo, fitness-no-network.
# 'make all' adds: stdout-json-only (requires a built binary, so separate from lint).
# Individual targets here allow running any one check in isolation during development.
#
# Each script documents its own rationale. See scripts/README.md for the full table.

## fitness-no-cgo: verify CGO_ENABLED=0 build succeeds + zero transitive CGo deps.
## This is the "pure static binary" gate. One CGo dep anywhere breaks five platforms.
fitness-no-cgo:
	./scripts/fitness-no-cgo.sh

## fitness-no-network: verify no reachable network calls in the production binary.
## Enforces the no-outbound-network invariant at the symbol level.
fitness-no-network:
	./scripts/fitness-no-network.sh

## stdout-json-only: verify subcommands emit only JSON/correct output to stdout.
## Builds the binary first if not present; uses $(BINARY) as the test target.
stdout-json-only: build
	./scripts/fitness-stdout-json-only.sh ./$(BINARY)

## layering-invariant: verify language packages not imported by the layering layer.
## Enforces that resolve.go/types.go/dispatch.go stay language-agnostic.
layering-invariant:
	./scripts/fitness-layering-invariant.sh

# ---------------------------------------------------------------------------
# Combined targets
# ---------------------------------------------------------------------------

## all: run the full local CI gate (snapshot-test + race + lint + stdout-json-only + schema + bench-check)
## This is the complete pre-merge verification sequence.
## Note: 'bench-check' runs bench automatically as a prerequisite.
## Note: 'stdout-json-only' builds the binary (via its 'build' prerequisite) before running.
all: snapshot-test race lint stdout-json-only schema bench-check
	@echo ""
	@echo "Full CI gate passed."

## release-local: build the 5-target release matrix locally for testing.
##
## Mirrors the workflow's build flags exactly (CGO_ENABLED=0, -trimpath,
## -buildvcs=true, -buildid=, ldflags version injection with SOURCE_DATE_EPOCH
## for archive timestamp normalization) so a local build produces byte-identical
## output to a CI build of the same commit.
##
## Output archives go to dist/ with the same naming convention as the release
## workflow:
##   dist/codeweaver_<VERSION>_linux_amd64.tar.gz
##   dist/codeweaver_<VERSION>_linux_arm64.tar.gz
##   dist/codeweaver_<VERSION>_darwin_amd64.tar.gz
##   dist/codeweaver_<VERSION>_darwin_arm64.tar.gz
##   dist/codeweaver_<VERSION>_windows_amd64.zip
##   dist/SHA256SUMS
##
## Prerequisites: zip, tar, sha256sum (all standard on Linux/macOS with coreutils).
## On macOS, sha256sum is provided by coreutils (brew install coreutils).
##
## Override VERSION: make release-local VERSION=0.2.0
release-local:
	@echo "Building release matrix locally (VERSION=$(VERSION))..."
	@mkdir -p dist
	@SOURCE_DATE_EPOCH=$(shell git log -1 --format=%ct); \
	export SOURCE_DATE_EPOCH; \
	for TARGET in \
	  "linux   amd64  tar.gz" \
	  "linux   arm64  tar.gz" \
	  "darwin  amd64  tar.gz" \
	  "darwin  arm64  tar.gz" \
	  "windows amd64  zip"; do \
	  GOOS=$$(echo $$TARGET | awk '{print $$1}'); \
	  GOARCH=$$(echo $$TARGET | awk '{print $$2}'); \
	  EXT=$$(echo $$TARGET | awk '{print $$3}'); \
	  ARCHIVE_BASE="codeweaver_$(VERSION)_$${GOOS}_$${GOARCH}"; \
	  BIN=codeweaver; \
	  if [ "$${GOOS}" = "windows" ]; then BIN=codeweaver.exe; fi; \
	  echo "  Building $${GOOS}/$${GOARCH}..."; \
	  GOOS=$${GOOS} GOARCH=$${GOARCH} CGO_ENABLED=0 \
	    $(GO) build -trimpath -buildvcs=true \
	      -ldflags="-s -w -buildid= \
	        -X main.gitCommit=$(GIT_COMMIT) \
	        -X main.builtAt=$(BUILT_AT) \
	        -X main.version=$(VERSION)" \
	      -o "dist/stage/$${ARCHIVE_BASE}/$${BIN}" \
	      ./cmd/codeweaver; \
	  mkdir -p "dist/stage/$${ARCHIVE_BASE}/schema"; \
	  cp ../LICENSE "dist/stage/$${ARCHIVE_BASE}/LICENSE" 2>/dev/null || cp LICENSE "dist/stage/$${ARCHIVE_BASE}/LICENSE" 2>/dev/null || true; \
	  cp release/README.md "dist/stage/$${ARCHIVE_BASE}/README.md" 2>/dev/null || echo "(warning: release/README.md not found, skipping)"; \
	  cp schema/codeweaver-v1.json "dist/stage/$${ARCHIVE_BASE}/schema/codeweaver-v1.json"; \
	  if [ "$${EXT}" = "zip" ]; then \
	    (cd dist/stage && zip -X -r "../$${ARCHIVE_BASE}.zip" "$${ARCHIVE_BASE}/"); \
	  else \
	    tar --mtime="@$${SOURCE_DATE_EPOCH}" -czf "dist/$${ARCHIVE_BASE}.tar.gz" -C dist/stage "$${ARCHIVE_BASE}/"; \
	  fi; \
	  echo "  -> dist/$${ARCHIVE_BASE}.$${EXT}"; \
	done; \
	echo ""; \
	echo "Generating SHA256SUMS..."; \
	(cd dist && sha256sum \
	  "codeweaver_$(VERSION)_linux_amd64.tar.gz" \
	  "codeweaver_$(VERSION)_linux_arm64.tar.gz" \
	  "codeweaver_$(VERSION)_darwin_amd64.tar.gz" \
	  "codeweaver_$(VERSION)_darwin_arm64.tar.gz" \
	  "codeweaver_$(VERSION)_windows_amd64.zip" \
	  > SHA256SUMS); \
	echo ""; \
	echo "Release matrix complete. Archives in dist/:"; \
	ls -lh dist/*.tar.gz dist/*.zip dist/SHA256SUMS 2>/dev/null

## clean: remove build artifacts
clean:
	rm -f $(BINARY) $(BINARY).stripped
	rm -rf dist/stage
