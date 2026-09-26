# Belai — local development and QA tasks.
#
# Install just:  brew install just | cargo install just | pacman -S just | apt install just
# List recipes:  just

set shell := ["bash", "-uc"]

module := "github.com/vulnetix/belai"
binary := "belai"
pkg := "./cmd/belai"
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

# Build ./belai for this host.
build:
    go build -ldflags '{{ ldflags }}' -o {{ binary }} {{ pkg }}

# Prepare the embedded classifier models (download + convert + verify).
# When `uv` is available this runs under `uv run --with torch --with
# safetensors --with numpy` so no system Python setup is required.
# Otherwise set MODELPREP_PYTHON to a python with torch+safetensors installed.
modelprep *ARGS:
    if command -v uv >/dev/null 2>&1; then \
        if [ -z "{{ ARGS }}" ]; then set -- -phase1 -phase2; fi; \
        uv run --index-url https://download.pytorch.org/whl/cpu --extra-index-url https://pypi.org/simple --with torch --with safetensors --with numpy go run ./tools/modelprep -python python3 {{ ARGS }} "$@"; \
    else \
        PY="${MODELPREP_PYTHON:-python3}"; \
        if [ -z "{{ ARGS }}" ]; then set -- -phase1 -phase2; fi; \
        go run ./tools/modelprep -python "$PY" {{ ARGS }} "$@"; \
    fi

# Build ./belai with both embedded models (phase 1 saturation + phase 2 jailbreak).
# Extra args are forwarded to modelprep, e.g. `just build-jailbreak -force`.
build-jailbreak *ARGS: (modelprep '-phase1' '-phase2' ARGS)
    go build -tags belai_bert_jailbreak -ldflags '{{ ldflags }} -X {{ module }}/internal/version.Variant=bert-guardrails-jailbreak' -o {{ binary }} {{ pkg }}

# Build only the Linux amd64 jailbreak-classifier release binary into bin/.
# Extra args are forwarded to modelprep, e.g. `just build-jailbreak-linux-amd64 -force`.
build-jailbreak-linux-amd64 *ARGS: (modelprep '-phase1' '-phase2' ARGS)
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
      go build -tags belai_bert_jailbreak \
      -ldflags '-s -w {{ ldflags }} -X {{ module }}/internal/version.Variant=bert-guardrails-jailbreak' \
      -o {{ bin }}/belai-bert-guardrails-jailbreak-linux-amd64 {{ pkg }}

# Build ./belai with only the phase-1 prompt-saturation model embedded.
# Extra args are forwarded to modelprep, e.g. `just build-bert -force`.
build-bert *ARGS: (modelprep '-phase1' ARGS)
    go build -tags belai_bert -ldflags '{{ ldflags }} -X {{ module }}/internal/version.Variant=bert-guardrails' -o {{ binary }} {{ pkg }}

# Install belai into $(go env GOPATH)/bin.
install:
    go install -ldflags '{{ ldflags }}' {{ pkg }}

# Cross-compile every release target and variant into bin/, mirroring
# .github/workflows/release.yml. Needs the prepared models (run modelprep).
# Extra args are forwarded to modelprep, e.g. `just build-all -force`.
build-all *ARGS: (modelprep '-phase1' '-phase2' ARGS)
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
          tags="-tags belai_bert"
          extra="-X {{ module }}/internal/version.Variant=bert-guardrails"
          ;;
        bert-guardrails-jailbreak)
          name="{{ binary }}-bert-guardrails-jailbreak"
          tags="-tags belai_bert_jailbreak"
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

# Replay AIxploit's payloads through each classifier variant (built from source)
# and write a Markdown report to .vulnetix/redteam/. Makes real provider calls:
# `just redteam -provider cloudflare-ai-gateway -model @cf/deepseek-ai/deepseek-r1-distill-qwen-32b`.
redteam *ARGS:
    go run ./tools/redteam {{ ARGS }}

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
    cd site && yarn build && node scripts/check-links.mjs dist && test -f dist/CNAME && grep -qx 'belai.vulnetix.com' dist/CNAME

# Notify search engines that the sitemap changed. Google retired its sitemap
# ping endpoint in 2023, so Bing (whose index also feeds DuckDuckGo/Yahoo) is
# the only ping left. Set INDEXNOW_KEY to also push via IndexNow (Bing,
# Yandex, Seznam, Naver); publish site/public/$INDEXNOW_KEY.txt containing
# that same key first, or the submission is rejected.
site-submit-sitemap:
    #!/usr/bin/env bash
    set -euo pipefail
    sitemap="https://belai.vulnetix.com/sitemap.xml"
    echo "Pinging Bing: ${sitemap}"
    curl -fsS "https://www.bing.com/ping?sitemap=${sitemap}"
    if [ -n "${INDEXNOW_KEY:-}" ]; then
      echo "Submitting via IndexNow..."
      curl -fsS -X POST "https://api.indexnow.org/indexnow" \
        -H "Content-Type: application/json" \
        -d "{\"host\":\"belai.vulnetix.com\",\"key\":\"${INDEXNOW_KEY}\",\"urlList\":[\"https://belai.vulnetix.com/\"]}"
    else
      echo "INDEXNOW_KEY not set; skipping IndexNow submission."
    fi

# Regenerate the TUI shot captures and their SVGs. Deterministic: a clean-tree
# run must produce an empty diff (that is what makes the captures CI-reproducible).
shots:
    go run ./tools/shot -out site/src/assets/shots
    cd site && node scripts/ansi-to-svg.mjs
