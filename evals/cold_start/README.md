# cold_start golden dataset (B-EP06.23a)

Version-controlled eval corpus for the cold-start read-back's
deterministic gates (`extractionShapeValid` + the no-guess evidence
gate `evidencedFields` in `backend/internal/compose/coldstart.go`).

- `cases/*.jsonl` — one case per line: `{name, class, inputs
  {source_url, page_text, model_output}, expected {shape_valid,
  survivors}, rubric}`. `model_output` is a RECORDED model reply
  (ai-operational-spec §3.3: CI gates run deterministically on recorded
  fixtures, never on live model calls).
- `thresholds.json` — corpus-shape minimums; shrinkage fails the build.
- The corpus is GENERATED, deterministically, by
  `backend/tools/gen-evals` (`go run ./tools/gen-evals -out
  ../evals/cold_start` from `backend/`). Edit the generator, re-run,
  commit both — a hand-edited case file is drift.

The runner is `backend/internal/compose/coldstart_eval_test.go` — part
of the plain `go test` lane, i.e. inside `make check` (that IS the CI
hard gate until hosted CI exists). `make eval` runs it verbosely.

Corpora for tasks whose pipelines are not built yet (routing, dedupe
merge, workflows-from-English) land with those slices — an eval without
its subject would be a fixture of nothing.
