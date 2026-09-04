# Get the latest commit branch, hash, and date
TAG=$(shell git describe --tags --abbrev=0 --exact-match 2>/dev/null)
BRANCH=$(if $(TAG),$(TAG),$(shell git rev-parse --abbrev-ref HEAD 2>/dev/null))
HASH=$(shell git rev-parse --short=7 HEAD 2>/dev/null)
# git formats the date itself: `date -r` means "reference FILE" in GNU and busybox and "seconds since
# epoch" only in BSD, so piping the epoch through date left TIMESTAMP empty everywhere but macOS.
TIMESTAMP=$(shell TZ=UTC0 git log -1 --date=format-local:%Y%m%dT%H%M%S --format=%cd HEAD 2>/dev/null)
GIT_REV=$(shell printf "%s-%s-%s" "$(BRANCH)" "$(HASH)" "$(TIMESTAMP)")
REV=$(if $(filter --,$(GIT_REV)),latest,$(GIT_REV))

# where `make install` links the binary; override for a prefix that needs no privileges
BINDIR ?= /usr/local/bin

all: test check-plugins build

# cp writes in place, to the same inode. Rebuilding while a revmux is running from .bin/revmux
# therefore rewrites the pages of a live Mach-O, and macOS then refuses to exec that file at all:
# `load code signature error 2`, killed by AppleSystemPolicy. Reviewing this repo with revmux hits
# exactly that, since an agent that runs `make build` breaks the binary supervising it.
# mv is a rename: the running process keeps its old inode and the new one is a clean file.
build:
	go build -ldflags "-X main.revision=$(REV) -s -w" -o .bin/revmux.$(BRANCH) ./app
	cp .bin/revmux.$(BRANCH) .bin/revmux.tmp && mv -f .bin/revmux.tmp .bin/revmux

# symlink rather than copy, so every subsequent `make build` is picked up without reinstalling.
# rm before ln instead of `ln -sf`: with an existing symlink to a directory, BSD ln follows it and
# creates the link inside it rather than replacing it.
install: build
	rm -f "$(BINDIR)/revmux"
	ln -s "$(CURDIR)/.bin/revmux" "$(BINDIR)/revmux"
	@echo "$(BINDIR)/revmux -> $(CURDIR)/.bin/revmux"

uninstall:
	rm -f "$(BINDIR)/revmux"

test:
	go clean -testcache
	go test -race -coverprofile=coverage.out ./...
	grep -v "_mock.go" coverage.out | grep -v mocks > coverage_no_mocks.out
	go tool cover -func=coverage_no_mocks.out
	rm coverage.out coverage_no_mocks.out

lint: lint-go lint-scripts

lint-go:
	golangci-lint run --max-issues-per-linter=0 --max-same-issues=0

# golangci-lint is Go-only, so the shipped shell scripts have their own CI job and would otherwise be
# checked by nothing local. The command is copied from .github/workflows/ci.yml verbatim: that job
# pipes shellcheck through xargs, so ANY output fails it, info-level findings included.
lint-scripts:
	find . -name '*.sh' -not -path './.git/*' -not -path './vendor/*' -print0 | xargs -0 shellcheck

# the two skill trees ship to different harnesses and each must be self-contained once installed, so
# plugins/codex/ is a hand-maintained copy rather than a link. What must not diverge is the content:
# an edit to one tree's references/ or scripts/ that never reached the other is a skill that behaves
# one way under claude and another under codex. The validator also pins the marketplace source, policy,
# manifest version and the payload's entry files, which the two diffs above do not cover.
check-plugins:
	diff -r .claude-plugin/skills/revmux/references plugins/codex/skills/revmux/references
	diff -r .claude-plugin/skills/revmux/scripts plugins/codex/skills/revmux/scripts
	python3 .github/scripts/validate-codex-marketplace.py
	@echo "plugin trees agree"

fmt:
	gofmt -s -w $(shell find . -type f -name "*.go" -not -path "./vendor/*" -not -path "*/mocks/*")
	goimports -w $(shell find . -type f -name "*.go" -not -path "./vendor/*" -not -path "*/mocks/*")

# the executor tests re-exec the race-instrumented test binary once per supervised run, so this
# package alone costs ~40s on a fast machine; the timeout is headroom for slower hardware, not a budget
race:
	go test -race -timeout=300s ./...

version:
	@echo "branch: $(BRANCH), hash: $(HASH), timestamp: $(TIMESTAMP)"
	@echo "revision: $(REV)"

.PHONY: all build install uninstall test lint lint-go lint-scripts check-plugins fmt race version
