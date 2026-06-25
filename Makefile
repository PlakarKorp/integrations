# Root Makefile for the PlakarKorp integrations monorepo.
#
# Each connector lives in its own top-level directory as an independent Go
# module with its own Makefile. This root Makefile does not build anything
# itself -- it discovers those connectors and fans a target out to one of them
# (via INTEGRATION=<connector>) or to all of them (the *-all targets).
#
# Examples:
#   make list                          # list discovered connectors
#   make build INTEGRATION=s3          # run `make build` inside s3/
#   make test  INTEGRATION=imap        # run `make test`  inside imap/
#   make tidy  INTEGRATION=fs          # run `go mod tidy` inside fs/
#   make build-all                     # build every connector
#   make test-all                      # test every connector (skips those w/o a test target)
#   make build-changed                 # build only connectors changed vs origin/main
#   make help                          # show this help

GO  ?= go

# A connector is any top-level directory containing a go.mod.
CONNECTORS := $(sort $(patsubst %/go.mod,%,$(wildcard */go.mod)))

# Base ref used by the *-changed targets to detect touched connectors.
BASE_REF ?= origin/main

# Per-connector targets that simply delegate to the connector's own Makefile.
DELEGATED := build test check clean package install uninstall

.DEFAULT_GOAL := help

# ---------------------------------------------------------------------------
# Single-connector targets: `make <target> INTEGRATION=<connector>`
# ---------------------------------------------------------------------------

$(DELEGATED):
	@if [ -z "$(INTEGRATION)" ]; then \
		echo "error: set INTEGRATION=<connector> (e.g. make $@ INTEGRATION=s3), or use '$@-all'." >&2; \
		echo "       available connectors: $(CONNECTORS)" >&2; \
		exit 2; \
	fi
	@if [ ! -d "$(INTEGRATION)" ] || [ ! -f "$(INTEGRATION)/go.mod" ]; then \
		echo "error: '$(INTEGRATION)' is not a connector. available: $(CONNECTORS)" >&2; \
		exit 2; \
	fi
	@if ! $(MAKE) -C "$(INTEGRATION)" -n $@ >/dev/null 2>&1; then \
		echo "error: connector '$(INTEGRATION)' has no '$@' target." >&2; \
		exit 2; \
	fi
	@$(MAKE) -C "$(INTEGRATION)" $@

# `go mod tidy` is useful per-module but not defined in the sub-Makefiles,
# so the root drives it directly.
tidy:
	@if [ -z "$(INTEGRATION)" ]; then \
		echo "error: set INTEGRATION=<connector> (e.g. make tidy INTEGRATION=s3), or use 'tidy-all'." >&2; \
		exit 2; \
	fi
	@if [ ! -f "$(INTEGRATION)/go.mod" ]; then \
		echo "error: '$(INTEGRATION)' is not a connector. available: $(CONNECTORS)" >&2; \
		exit 2; \
	fi
	@cd "$(INTEGRATION)" && $(GO) mod tidy

# ---------------------------------------------------------------------------
# All-connector targets: run across every connector, keep going on failure,
# and print a pass/fail summary (non-zero exit if any failed).
# ---------------------------------------------------------------------------

build-all:   ; @$(call run_all,build,$(CONNECTORS))
test-all:    ; @$(call run_all,test,$(CONNECTORS))
check-all:   ; @$(call run_all,check,$(CONNECTORS))
clean-all:   ; @$(call run_all,clean,$(CONNECTORS))
tidy-all:    ; @$(call run_tidy,$(CONNECTORS))

# Same as *-all but scoped to connectors changed vs $(BASE_REF). This is the
# hook CI reuses so local and CI agree on "what changed".
build-changed: ; @$(call run_all,build,$(call changed_connectors))
test-changed:  ; @$(call run_all,test,$(call changed_connectors))

# ---------------------------------------------------------------------------
# Introspection
# ---------------------------------------------------------------------------

list:
	@for c in $(CONNECTORS); do echo "$$c"; done

changed:
	@for c in $(call changed_connectors); do echo "$$c"; done

help:
	@echo "Integrations monorepo -- root Makefile"
	@echo ""
	@echo "Single connector (set INTEGRATION=<connector>):"
	@echo "  make build INTEGRATION=s3     build one connector"
	@echo "  make test  INTEGRATION=imap   test one connector"
	@echo "  make tidy  INTEGRATION=fs     go mod tidy one connector"
	@echo "  targets: $(DELEGATED) tidy"
	@echo ""
	@echo "All connectors (keep going, summary at end):"
	@echo "  make build-all / test-all / check-all / clean-all / tidy-all"
	@echo ""
	@echo "Changed only (vs BASE_REF=$(BASE_REF)):"
	@echo "  make build-changed / test-changed / changed"
	@echo ""
	@echo "Introspection:"
	@echo "  make list              list all connectors"
	@echo "  make changed           list connectors changed vs $(BASE_REF)"
	@echo ""
	@echo "Discovered connectors:"
	@echo "  $(CONNECTORS)"

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

# changed_connectors: top-level connector dirs with changes vs $(BASE_REF).
# Falls back silently to nothing if the base ref is unavailable.
changed_connectors = $(sort $(filter $(CONNECTORS), \
	$(shell git diff --name-only $(BASE_REF)...HEAD 2>/dev/null | cut -d/ -f1) \
	$(shell git diff --name-only $(BASE_REF) 2>/dev/null | cut -d/ -f1)))

# run_all,<target>,<connectors>: run `make <target>` in each connector.
# A connector whose Makefile lacks <target> is skipped (counted as skipped,
# not failed). Prints a summary and exits non-zero if anything failed.
define run_all
set -e; \
target="$(1)"; connectors="$(2)"; \
pass=""; fail=""; skip=""; \
if [ -z "$$connectors" ]; then echo "nothing to do for '$$target'."; exit 0; fi; \
for c in $$connectors; do \
	if ! $(MAKE) -C "$$c" -n "$$target" >/dev/null 2>&1; then \
		echo ">>> $$c: no '$$target' target, skipping"; \
		skip="$$skip $$c"; \
		continue; \
	fi; \
	echo ">>> $$c: make $$target"; \
	if $(MAKE) -C "$$c" "$$target"; then \
		pass="$$pass $$c"; \
	else \
		echo ">>> $$c: FAILED"; \
		fail="$$fail $$c"; \
	fi; \
done; \
echo ""; \
echo "==== $$target summary ===="; \
echo "  passed: $${pass:- (none)}"; \
echo "  skipped:$${skip:- (none)}"; \
echo "  failed: $${fail:- (none)}"; \
[ -z "$$fail" ]
endef

# run_tidy,<connectors>: `go mod tidy` in each connector, with a summary.
define run_tidy
set -e; \
connectors="$(1)"; \
pass=""; fail=""; \
for c in $$connectors; do \
	echo ">>> $$c: go mod tidy"; \
	if ( cd "$$c" && $(GO) mod tidy ); then \
		pass="$$pass $$c"; \
	else \
		echo ">>> $$c: FAILED"; \
		fail="$$fail $$c"; \
	fi; \
done; \
echo ""; \
echo "==== tidy summary ===="; \
echo "  passed: $${pass:- (none)}"; \
echo "  failed: $${fail:- (none)}"; \
[ -z "$$fail" ]
endef

.PHONY: $(DELEGATED) tidy \
	build-all test-all check-all clean-all tidy-all \
	build-changed test-changed \
	list changed help
