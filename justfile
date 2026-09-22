# Signet — local development and QA tasks.
#
# Install just:  brew install just | cargo install just | pacman -S just | apt install just
# List recipes:  just

set shell := ["bash", "-uc"]

module := "github.com/vulnetix/signet"
binary := "signet"
pkg := "./cmd/signet"
bin := "bin"

version := `git describe --tags --always --dirty 2>/dev/null || echo dev`
commit := `git rev-parse --short HEAD 2>/dev/null || echo unknown`
builddate := `date -u +%Y-%m-%dT%H:%M:%SZ`

ldflags := "-X " + module + "/internal/version.Version=" + version + " -X " + module + "/internal/version.Commit=" + commit + " -X " + module + "/internal/version.BuildDate=" + builddate

# List available recipes.
default:
    @just --list --unsorted

# ----------------------------------------------------------------------------
# Run from source
# ----------------------------------------------------------------------------

# Run the TUI from source, no build artefact.
tui:
    go run -ldflags '{{ ldflags }}' {{ pkg }}

# Raw flags: `just run -version`. Multi-word values lose quoting; use `just prompt`.
run *ARGS:
    go run -ldflags '{{ ldflags }}' {{ pkg }} {{ ARGS }}

# One noninteractive prompt, quoting preserved: `just prompt "what model is this"`.
prompt $TEXT *ARGS:
    go run -ldflags '{{ ldflags }}' {{ pkg }} -prompt "$TEXT" {{ ARGS }}

# One prompt against an explicit provider and model; see docs/development.md.
ask $PROVIDER $MODEL $TEXT *ARGS:
    go run -ldflags '{{ ldflags }}' {{ pkg }} -provider "$PROVIDER" -model "$MODEL" -prompt "$TEXT" {{ ARGS }}

# Report the Role Manager's mode decision for a prompt without acting on it.
detect-mode $TEXT:
    go run -ldflags '{{ ldflags }}' {{ pkg }} -detect-mode -verbose -prompt "$TEXT"

# ----------------------------------------------------------------------------
# Build
# ----------------------------------------------------------------------------

# Build ./signet for this host.
build:
    go build -ldflags '{{ ldflags }}' -o {{ binary }} {{ pkg }}

# Prepare the embedded classifier models (download + convert + verify).
# MODELPREP_PYTHON names a python with torch+safetensors installed (defaults
# to python3). With no args both phases are prepared.
modelprep *ARGS:
    #!/usr/bin/env bash
    set -euo pipefail
    PY="${MODELPREP_PYTHON:-python3}"
    if [ "$#" -eq 0 ]; then set -- -phase1 -phase2; fi
    go run ./tools/modelprep -python "$PY" "$@"

# Build ./signet with both embedded models (phase 1 saturation + phase 2 jailbreak).
build-jailbreak: modelprep
    go build -tags signet_bert_jailbreak -ldflags '{{ ldflags }} -X {{ module }}/internal/version.Variant=bert-guardrails-jailbreak' -o {{ binary }} {{ pkg }}

# Build only the Linux amd64 jailbreak-classifier release binary into bin/.
build-jailbreak-linux-amd64: modelprep
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
      go build -tags signet_bert_jailbreak \
      -ldflags '-s -w {{ ldflags }} -X {{ module }}/internal/version.Variant=bert-guardrails-jailbreak' \
      -o {{ bin }}/signet-bert-guardrails-jailbreak-linux-amd64 {{ pkg }}

# Build ./signet with only the phase-1 prompt-saturation model embedded.
build-bert: (modelprep '-phase1')
    go build -tags signet_bert -ldflags '{{ ldflags }} -X {{ module }}/internal/version.Variant=bert-guardrails' -o {{ binary }} {{ pkg }}

# Install signet into $(go env GOPATH)/bin.
install:
    go install -ldflags '{{ ldflags }}' {{ pkg }}

# Cross-compile every release target and variant into bin/, mirroring
# .github/workflows/release.yml. Needs the prepared models (run modelprep).
build-all: modelprep
    #!/usr/bin/env bash
    set -euo pipefail
    mkdir -p {{ bin }}
    build() {
      local variant="$1" goos="$2" goarch="$3" suffix="${4:-}"
      local name="{{ binary }}" tags="" extra=""
      case "$variant" in
        no-classifier)
          name="{{ binary }}-no-classifier"
          extra="-X {{ module }}/internal/version.Variant=no-classifier"
          ;;
        bert-guardrails)
          name="{{ binary }}-bert-guardrails"
          tags="-tags signet_bert"
          extra="-X {{ module }}/internal/version.Variant=bert-guardrails"
          ;;
        bert-guardrails-jailbreak)
          name="{{ binary }}-bert-guardrails-jailbreak"
          tags="-tags signet_bert_jailbreak"
          extra="-X {{ module }}/internal/version.Variant=bert-guardrails-jailbreak"
          ;;
      esac
      echo "  {{ bin }}/${name}-${goos}-${goarch}${suffix}"
      CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
        go build $tags -ldflags '-s -w {{ ldflags }} '"$extra" \
        -o "{{ bin }}/${name}-${goos}-${goarch}${suffix}" {{ pkg }}
    }
    for variant in "" no-classifier bert-guardrails bert-guardrails-jailbreak; do
      build "$variant" linux   amd64
      build "$variant" linux   arm64
      build "$variant" darwin  amd64
      build "$variant" darwin  arm64
      build "$variant" windows amd64 .exe
      build "$variant" windows arm64 .exe
    done
    ( cd {{ bin }} && sha256sum {{ binary }}-* > checksums.txt )

# Print the version string this tree would stamp into a build.
version:
    @echo '{{ version }} ({{ commit }})'

# ----------------------------------------------------------------------------
# Test
# ----------------------------------------------------------------------------

# Run unit tests; extra args pass through, e.g. `just test -run TestResolve -v`.
test *ARGS:
    go test ./... {{ ARGS }}

# Run the full suite under the race detector, as CI does.
test-race *ARGS:
    go test -race ./... {{ ARGS }}

# Test one package: `just test-pkg ./internal/run -run TestStream -v`.
test-pkg PKG *ARGS:
    go test -race {{ PKG }} {{ ARGS }}

# Run the end-to-end suite: builds the binary, drives it against a mock provider.
e2e *ARGS:
    go test -race ./e2e {{ ARGS }}

# Write coverage.txt and print the per-function summary.
cover:
    go test -coverprofile=coverage.txt -covermode=atomic ./...
    go tool cover -func=coverage.txt | tail -20

# Open the coverage report in a browser.
cover-html: cover
    go tool cover -html=coverage.txt

# ----------------------------------------------------------------------------
# Lint and hygiene
# ----------------------------------------------------------------------------

# Format all Go source in place.
fmt:
    gofmt -w .

# Fail if any file is unformatted; CI's check, modifies nothing.
fmt-check:
    @test -z "$(gofmt -l .)" || { echo "unformatted files:"; gofmt -l .; exit 1; }

# Run go vet.
vet:
    go vet ./...

# Tidy and verify the module graph.
tidy:
    go mod tidy
    go mod verify

# Verify the cross-compile targets CI builds; leaves no artefacts behind.
cross:
    GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build ./...
    GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build ./...

# Everything CI runs, in CI's order. Run before pushing.
check: fmt-check vet test-race cross

# Remove build artefacts and coverage output.
clean:
    rm -rf {{ binary }} {{ bin }} coverage.txt
    go clean -testcache

# ----------------------------------------------------------------------------
# Site
# ----------------------------------------------------------------------------

# Run the marketing site dev server (http://localhost:4321).
site-dev:
    cd site && yarn dev

# Build the marketing site into site/dist.
site-build:
    cd site && yarn build

# Build the site, assert the custom domain survived, and check internal links.
site-check:
    cd site && yarn build && node scripts/check-links.mjs dist && test -f dist/CNAME && grep -qx 'signet.vulnetix.com' dist/CNAME

# Regenerate the TUI shot captures and their SVGs. Deterministic: a clean-tree
# run must produce an empty diff (that is what makes the captures CI-reproducible).
shots:
    go run ./tools/shot -out site/src/assets/shots
    cd site && node scripts/ansi-to-svg.mjs
