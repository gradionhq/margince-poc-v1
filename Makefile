# Thin delegator: the real Makefile lives in backend/ (the Go module root).
# `make check` is the merge gate; `make dev` boots everything.

.PHONY: check build test test-integration lint arch-lint vet gen drift db-up db-init migrate dev clean

check build test test-integration lint arch-lint vet gen drift db-up db-init migrate dev clean:
	$(MAKE) -C backend $@
