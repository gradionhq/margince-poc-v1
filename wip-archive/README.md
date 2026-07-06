# WIP archive — signals (E08) + lead routing (E13)

This branch (`wip/signals-lead-routing`) exists only to **git-preserve** the
in-flight signals/lead-routing work so it cannot be lost, overwritten, or
applied onto the wrong base in a multi-agent worktree. It is **not** mergeable
as-is — the code did not build when archived (see the review addendum).

## Contents

- `e08-signals-e13-routing.patch` — the full working-tree diff.
- `e08-signals-e13-routing.patch.sha256` — integrity checksum.
- `BASE.txt` — the commit this patch applies onto.

## To resume

```
git switch main
git checkout <BASE from BASE.txt>
git apply wip-archive/e08-signals-e13-routing.patch
```

## Known-red gates at archive time (must be fixed before merge)

1. `compose.Server` does not implement the generated contract (missing
   `ArchiveSignal` etc.) — wire `signals.Handlers` into compose.
2. `signal` / `signal_resolution` not enrolled in `tableOwners`
   (`backend/tableownership_test.go`).
3. `UpdateSignal` / `ArchiveSignal` audit without emitting an outbox event
   (`TestEveryAuditedMutationEmitsAnEvent`).

Then re-run `make gen`, `make check`, and `make test-integration`.
