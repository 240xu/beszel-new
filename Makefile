# Default OS/ARCH values
OS ?= $(shell go env GOOS)
ARCH ?= $(shell go env GOARCH)
# Skip building the web UI if true
SKIP_WEB ?= false
# Controls NVML/glibc agent build tag behavior:
# - auto (default): enable on linux/amd64 glibc hosts
# - true: always enable
# - false: always disable
NVML ?= auto

# Detect glibc host for local linux/amd64 builds.
HOST_GLIBC := $(shell \
	if [ "$(OS)" = "linux" ] && [ "$(ARCH)" = "amd64" ]; then \
		for p in /lib64/ld-linux-x86-64.so.2 /lib/x86_64-linux-gnu/ld-linux-x86-64.so.2 /lib/ld-linux-x86-64.so.2; do \
			[ -e "$$p" ] && { echo true; exit 0; }; \
		done; \
		if command -v ldd >/dev/null 2>&1; then \
			if ldd --version 2>&1 | tr '[:upper:]' '[:lower:]' | awk '/gnu libc|glibc/{found=1} END{exit !found}'; then \
				echo true; \
			else \
				echo false; \
			fi; \
		else \
			echo false; \
		fi; \
	else \
		echo false; \
	fi)

# Enable glibc build tag for NVML on supported Linux builds.
AGENT_GO_TAGS :=
ifeq ($(NVML),true)
AGENT_GO_TAGS := -tags glibc
else ifeq ($(NVML),auto)
ifeq ($(HOST_GLIBC),true)
AGENT_GO_TAGS := -tags glibc
endif
endif

# Set executable extension based on target OS
EXE_EXT := $(if $(filter windows,$(OS)),.exe,)

.PHONY: tidy test test-build test-go test-site ci-gate-drift build-agent build-hub build-hub-dev build clean lint dev-server dev-agent dev-hub dev generate-locales fetch-smartctl-conditional site-bundle lint-go test-site-dom
.DEFAULT_GOAL := build

clean:
	go clean
	rm -rf ./build

lint:
	golangci-lint run

# The conjunction, and nothing else: it owns no skip of its own, so `make test` can only
# exit 0 with both suites green. The gate below is the piece that used to be a skip which
# exited 0, and a build that reports success without running tests is the failure mode this
# arrangement exists to make impossible.
# The suite below builds the tree with `-tags 'testing no_ui'`, which is not the tree a
# release ships. A symbol that only exists behind that tag reads fine here and breaks
# `make build`, and nothing caught it: seenGuard was declared in a testing-tagged file and
# referenced unconditionally, so every test passed while every release build failed. This
# builds the configuration that actually ships, and it is first so a broken release build
# is reported before a suite that cannot be trusted to have seen it.
test-build:
	go build ./...

test: test-build test-go test-site

# Split out so a Go-only contributor does not have to stand up a 505MB bun install to check
# a Go change. Cost of merging these two again: 5.78s plus 505MB per Go-only change, which
# is the entire reason the escape valve that let the frontend suite go unrun had to go.
test-go:
	go test -tags='testing no_ui' ./...

# The gate owns every "can this run" decision rather than this file, because it can be
# tested there: its self-tests run first so that its refusal path is never taken on faith.
#
# The script itself grew: besides the bun suite it now compiles the catalogs, checks
# them against the source by message set, type checks the tree, lints it against a
# recorded baseline and audits the dependencies. None of those is a valve; each one fails
# the build. The production bundle is deliberately not in here -- it takes minutes, and
# `make site-bundle` is its own target so it can run as its own job.
test-site:
	@internal/site/scripts/test-gate.test.sh
	@internal/site/scripts/test-gate.sh
	@internal/site/scripts/test-gate-dom.sh

# The DOM component suite on its own, for a frontend-only change. It is a third runner:
# bun cannot apply the Lingui macro transform, so the components that use the macro can
# only be rendered by vitest, which uses the same Vite pipeline `vite build` uses.
test-site-dom:
	@internal/site/scripts/test-gate-dom.sh

# The production bundle, built the way `go:embed` will ship it: the catalogs are
# compiled first because the bundle imports src/locales/<locale>/<locale>.ts, and the
# extraction step is left out on purpose. `lingui extract --overwrite` rewrites every
# .po file with the entries in the order the source scan reached them, which on this
# tree is a 260-line reorder of a green tree; a build that dirties the checkout is a
# build nobody trusts.
site-bundle:
	cd internal/site && npx --no-install lingui compile && npx --no-install vite build

# staticcheck and golangci-lint, both against a recorded baseline: a finding that is
# new fails the build, a finding that is already recorded does not. The tools are not
# installed here -- a gate that installs its own linter is not a gate -- so they must
# be on PATH. See scripts/lint-go.sh for the version pins and why they are pins.
lint-go:
	@scripts/lint-go.sh

# Guards the one thing a test workflow can quietly stop doing. .github/workflows/tests.yml runs
# exactly one make target; this asserts that target is still the real conjunction of both suites
# rather than a name that happens to exist. Re-typing `go test ./...` into a workflow is how
# the `-tags 'testing no_ui'` and the entire frontend gate would both be lost at once, so the
# workflow calls make instead, and this checks the call. CI runs it before `make test`.
ci-gate-drift:
	@.github/workflows/check-gate-drift.sh

tidy:
	go mod tidy

build-web-ui:
	@if command -v bun >/dev/null 2>&1; then \
		bun install --cwd ./internal/site && \
		bun run --cwd ./internal/site build; \
	else \
		npm install --prefix ./internal/site && \
		npm run --prefix ./internal/site build; \
	fi

# Conditional .NET build - only for Windows
build-dotnet-conditional:
	@if [ "$(OS)" = "windows" ]; then \
		echo "Building .NET executable for Windows..."; \
		if command -v dotnet >/dev/null 2>&1; then \
			rm -rf ./agent/lhm/bin; \
			dotnet build -c Release ./agent/lhm/beszel_lhm.csproj; \
		else \
			echo "Error: dotnet not found. Install .NET SDK to build Windows agent."; \
			exit 1; \
		fi; \
	fi

# Download smartctl.exe at build time for Windows (skips if already present)
fetch-smartctl-conditional:
	@if [ "$(OS)" = "windows" ]; then \
		go generate -run fetchsmartctl ./agent; \
	fi

# Update build-agent to include conditional .NET build
build-agent: tidy build-dotnet-conditional fetch-smartctl-conditional
	GOOS=$(OS) GOARCH=$(ARCH) go build $(AGENT_GO_TAGS) -o ./build/beszel-agent_$(OS)_$(ARCH)$(EXE_EXT) -ldflags "-w -s" ./internal/cmd/agent

build-hub: tidy $(if $(filter false,$(SKIP_WEB)),build-web-ui)
	GOOS=$(OS) GOARCH=$(ARCH) go build -o ./build/beszel_$(OS)_$(ARCH)$(EXE_EXT) -ldflags "-w -s" ./internal/cmd/hub

build-hub-dev: tidy
	mkdir -p ./internal/site/dist && touch ./internal/site/dist/index.html
	GOOS=$(OS) GOARCH=$(ARCH) go build -tags development -o ./build/beszel-dev_$(OS)_$(ARCH)$(EXE_EXT) -ldflags "-w -s" ./internal/cmd/hub

build: build-agent build-hub

generate-locales:
	@if [ ! -f ./internal/site/src/locales/en/en.ts ]; then \
		echo "Generating locales..."; \
		command -v bun >/dev/null 2>&1 && cd ./internal/site && bun install && bun run sync || cd ./internal/site && npm install && npm run sync; \
	fi

dev-server: generate-locales
	cd ./internal/site
	@if command -v bun >/dev/null 2>&1; then \
		cd ./internal/site && bun run dev --host 0.0.0.0; \
	else \
		cd ./internal/site && npm run dev --host 0.0.0.0; \
	fi

dev-hub: export ENV=dev
dev-hub:
	mkdir -p ./internal/site/dist && touch ./internal/site/dist/index.html
	@if command -v entr >/dev/null 2>&1; then \
		find ./internal -type f -name '*.go' | entr -r -s "cd ./internal/cmd/hub && go run -tags development . serve --http 0.0.0.0:8090"; \
	else \
		cd ./internal/cmd/hub && go run -tags development . serve --http 0.0.0.0:8090; \
	fi

dev-agent:
	@if command -v entr >/dev/null 2>&1; then \
		find ./internal/cmd/agent/*.go ./agent/*.go | entr -r go run $(AGENT_GO_TAGS) github.com/henrygd/beszel/internal/cmd/agent; \
	else \
		go run $(AGENT_GO_TAGS) github.com/henrygd/beszel/internal/cmd/agent; \
	fi
	
build-dotnet:
	@if command -v dotnet >/dev/null 2>&1; then \
		rm -rf ./agent/lhm/bin; \
		dotnet build -c Release ./agent/lhm/beszel_lhm.csproj; \
	else \
		echo "dotnet not found"; \
	fi


# KEY="..." make -j dev
dev: dev-server dev-hub dev-agent
