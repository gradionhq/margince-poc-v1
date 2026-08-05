# The job fleet — the declaration, the dispatcher, and one row per tenant

Every background pass in this backend is a **declared** River job kind, and every fleet-wide pass is
**two** kinds: a dispatcher that enumerates the fleet and enqueues, and a workspace kind that carries
exactly one tenant's work. The declaration lives in `backend/api/jobs.yaml`, and it is what the
running system obeys — a worker cannot choose its own timeout, its own queue or its own attempt cap.

This is the deep reference. To *add* a job, jump to [how-to/add-a-job.md](../how-to/add-a-job.md);
for the operator's reading of the same fleet, see
[reference/configuration.md → Reading the job surfaces](../reference/configuration.md#reading-the-job-surfaces);
for the write shape every workspace pass commits through, see
[write-backbone.md](write-backbone.md).

## The shape at a glance

```text
DECLARATION                                    RUNTIME
backend/api/jobs.yaml
   │  make gen  (backend/tools/gen-jobs)
   ├──► platform/jobs/specs_gen.go   the Spec table every reader walks
   └──► compose/jobkinds_gen.go      the closed union + the role assertions
                                                │
   river periodic tick ◄── periodicFor(cfg, Args{}) ── cadence: from the declaration
        │  leader-elected, so replicas never double-dispatch
        ▼
   DISPATCHER row            role: dispatcher   ·   jobs.FleetWide
        │  enumerate the fleet — compose/dispatch.go, the ONE scan
        │  dispatchPerWorkspace | dispatchWith | dispatchOne
        ▼  one child per fan-out UNIT, one InsertMany, tagged `sweep`
   WORKSPACE row × N         role: workspace    ·   jobs.WorkspaceScoped
        │  workspaceJobCtx binds args.WorkspaceID() → app.workspace_id (RLS)
        │  Work(…) ──► jobs.FaultContext(ctx, err)
        ▼
   river_job: one row per tenant — it succeeds, retries and FAILS on its own
```

**Why two kinds and not one loop.** Before this split every sweep carried its own copy of the
workspace scan and its own inline per-workspace loop. A tenant whose pass failed became a log line
*inside* a job row River then recorded as `completed` — success on the outside, silent failure
within, and no durable place for the failure to land. Giving each workspace its own row makes the
failure a row: it is retried on its own, counted on its own, and reported on
`GET /v1/admin/job-health` on its own.

---

## 1. `backend/api/jobs.yaml` — the declaration

**Authority is this repo.** Unlike `crm.yaml` and `ai-tasks.yaml`, this file has no upstream
counterpart in the spec and claims none: it declares River *mechanics*, which the spec does not pin.
Where a cadence would restate a spec'd obligation the entry carries `derives-from:` and the chapter
owns the number — no entry carries one today.

Every kind River persists in `river_job.kind` is declared here. Kind strings are **persisted state**:
renaming one strands every live row that carries it, so they are append-only in practice — correct a
Go type name that reads wrong, never the kind.

Five fields are declared for **every** kind:

| Field | Meaning |
|---|---|
| `role` | `dispatcher` or `workspace` — §4. Held to the Go marker interface by a generated assertion |
| `go_type` | the compose args struct that returns this kind (`^[A-Z][A-Za-z0-9]*Args$`). Carried as data, not as an import, so a gate can assert the kind↔type pairing still holds |
| `queue` | must name an entry in the file's own `queues:` block; every non-`default` queue owes a `reason` for having been split out of the default pool |
| `timeout` | the whole-job wall clock — §3. There is **no default** |
| `opts_owner` | who supplies River's insert options: `fan_out` (SUPPLIED — the helper reads the declared queue and cap), `args` (CHECKED — the args' own `InsertOpts()` owns them and the census compares), `caller` (DECLARED ONLY — the options live at scattered enqueue sites, and the declared queue is documentation rather than a governed value) |

Three more are **conditional on what the kind is**, and generation refuses the mismatch in both
directions — a field owed and absent fails, and a field declared where it means nothing fails too:

- **`cadence`** — required on a dispatcher, refused on a workspace kind (*"a workspace kind is
  enqueued by its dispatcher, never ticked"*). Exactly one of a duration, `{operator: Field}` naming
  the `JobRunnerConfig` dial the number comes from, or `on_demand`. `on_demand` is a *declaration*,
  not an absence: `embed_reindex` is enqueued by a human's confirm and by no clock, and an absent
  cadence would read as a schedule somebody forgot. An optional `schedule_when_positive: Field` is a
  third posture — the workers stay registered and only the tick goes away.
- **`fans_out_to` + `fan_out_unit`** — one declaration, never one without the other. Required on a
  dispatcher (*"a dispatcher that fans out to nothing … does no work at all"*), refused elsewhere,
  and the named child must itself be `role: workspace`. The unit is `workspace`, `connection` or
  `build`, and it is what makes a child row readable: a `gmail_watch_renew_connection` row is one
  *connection's* renewal, not one tenant's. The unit is declared on the **dispatcher**, beside the
  edge it names, because that is where the fan-out decision is made.
- **`max_attempts`** — required for `opts_owner: fan_out` and refused for every other owner, because
  that is the only case the file actually governs: compose's `workspaceSweepOpts` reads this number
  and nothing else does. Publishing a cap the runtime does not honour is exactly the
  declared-versus-actual drift the file exists to remove. Three is the house number and it is small
  on purpose — a fanned-out pass's real retry cadence is the dispatcher's next tick.

And three are declared **by exception**, read that way on purpose — an omission is the strict
posture, never a licence:

- **`registration: {when: [Field, …], absent: registers_nothing | registers_anyway}`** — declared
  only where the kind's wiring depends on something the deployment may not have. `when` is a
  **conjunction** of `JobRunnerConfig` field paths, and an omitted block registers unconditionally.
  The two absence postures are opposites and neither is a default: *registers nothing* means a row
  nothing here could work is never queued at all; *registers anyway* keeps the worker, so a
  picked-up row fails with an actionable message instead of rotting queued. The **same** dependency
  takes different postures on different kinds — `Embedder` registers nothing for the embed drift
  sweep and anyway for a reindex — which is why the posture is per kind and never per field. A
  posture declared with no condition to be absent from fails generation.
- **`fault: {nil_after_logging: …}`** — this worker logs a failure and returns `nil`, and the text is
  the durable retry policy that makes a green River row honest (the connector sidecar's
  `next_sync_at`, a build row's own `deferred` state). Omitted, the worker must return what went
  wrong. A `fault` block with an empty rationale fails generation: *"an unstated waiver is a
  swallowed error with a heading"*.
- **`args: {Field: id | {scalar: true, reason: …} | {reason: …}}`** — §5. An omitted field is not
  waived; the census compares the declaration against the compiled struct.

A kind may also carry a free-form `reason:` stating why its numbers are what they are. Nothing
enforces it, and most non-obvious entries have one.

---

## 2. Generation, and the two halves it writes

`make gen` runs `backend/tools/gen-jobs` over the contract and writes two files that must never be
hand-edited:

- **`internal/platform/jobs/specs_gen.go`** — the `Spec` table. Every reader in the tree walks it:
  the fan-out helpers, the metrics catalogue, the health endpoint, the census.
- **`internal/compose/jobkinds_gen.go`** — the closed union `declaredJobArgs`, the two registration
  functions constrained to it, and one compile-time assertion per kind pairing its args type with its
  declared role.

Both carry the same `sha256` of `api/jobs.yaml`, so a half-regenerated pair is visible without
diffing the two tables (the census checks it: `bothGeneratedHalvesCameFromOneContract`).

The union is a union rather than a marker interface for one reason: **a marker is something a new
type can declare for itself, and the set has to be the file's to state.** An undeclared kind cannot
be named at `addDeclaredWorker`'s call site at all — the failure is `does not satisfy
declaredJobArgs`, on the registration line the author is writing.

---

## 3. Why a worker cannot answer for itself — `jobs.Govern`

River asks a worker four questions: `Work`, `Timeout`, `NextRetry`, `Middleware`. Three of them are
the contract's to answer. A hand-written worker in this tree therefore satisfies only
`jobs.WorkOnly[T]`:

```go
type WorkOnly[T river.JobArgs] interface {
    Work(context.Context, *river.Job[T]) error
}

func Govern[T river.JobArgs](w WorkOnly[T], s Spec, supplied time.Duration) river.Worker[T]
```

`Govern` wraps the worker in a type River reaches **only** through `Work`, so any option method the
worker happens to carry is unreachable. Narrowing the interface is what makes that impossible rather
than merely discouraged — an embedded `WorkerDefaults` override is shadowed by the outer type, which
both a marker interface and a linter rule would have missed.

**The incident this removes.** Of 53 kinds, **43 ran on River's silent one-minute default**: a worker
that declares no `Timeout` embeds `river.WorkerDefaults`, whose `Timeout` returns zero, and River
reads zero as one minute. The GDPR retention pass shipped that way through five reviews — under a
one-minute cap it would have been cancelled mid-pass nightly and left a permanently failing row for
the one obligation whose whole point is auditability. Nothing was broken enough to notice: a
cancelled job and a job that never had enough time look identical from the outside.

So `timeout` has no default and **absence is not one of its forms**. It takes exactly one of four:

| Form | Meaning |
|---|---|
| `2m` | a literal wall clock |
| `{derived: c, value: 4h, reason: …}` | computed from a Go constant elsewhere in the tree. `value` is what `Govern` hands River, and the census proves the two still agree — so the declaration tracks the constant instead of freezing a copy. Used only where the constant is read by something other than the census's own lookup table, or the check compares the file against a private copy of itself |
| `{operator: Field}` | computed at registration from a `JobRunnerConfig` dial; not knowable in the file at all (`site_deep_read` is the only one today) |
| `{none: true, reason: …}` | a deliberate absence. `TimeoutPolicy.Duration` yields `-1`, which takes the row out of River's rescuer on purpose — the pass is bounded by a backlog rather than a wall clock |

`declaredTimeoutSeconds` never publishes zero on `/metrics`: zero is River's silent minute wearing
the digits of a deliberate absence, and telling those two apart is what the declaration is for. A
deliberate absence is `-1`; an operator-supplied wall clock is reported as *unstated* and its label
is omitted rather than guessed.

Two more gates hold the same line at boot, because neither the union nor `Govern` can see a
hand-edited generated file or a fixture registering into a throwaway `*river.Workers`:

- **`jobs.MustBeTotal`** names every kind this role intends to work that the contract does not
  declare, and `NewJobRunner` refuses to boot. An undeclared kind is *indistinguishable* from the
  default this contract exists to remove, and a process that started anyway would hide it.
- **`everyKindIsRegisteredWithItsDeclaredType`** catches the other half: totality says every kind is
  declared, not that each is worked by the args type its declaration names. An args struct written by
  copying the one beside it and whose `Kind()` still returns the neighbour's string passes totality
  cleanly, runs under the neighbour's timeout, queue and attempt cap, and leaves the kind it was meant
  to serve with no worker. `Spec.GoType` is carried in the compiled table precisely so this can be
  asked.

---

## 4. The two roles

```go
type WorkspaceScoped interface {
    river.JobArgs
    WorkspaceID() ids.UUID
}

type FleetWide interface {
    river.JobArgs
    FleetWide()          // a declaration, not behaviour
}
```

The contract declares **fifty-five** kinds today — twenty-four dispatchers and thirty-one
workspace-scoped — and there is no third role. A third would be a change to what a job *is*, not
data: both operational surfaces read `Role` to decide whether a null `args->>'workspace_id'` is
correct or a defect.

**The biconditional.** `role: workspace` ⟺ the args type implements `jobs.WorkspaceScoped`, and
`role: dispatcher` ⟺ it implements `jobs.FleetWide`. Generation emits one `var _ jobs.FleetWide =
XArgs{}` / `var _ jobs.WorkspaceScoped = XArgs{}` line per kind, so a *declared* kind's role is the
compiler's to check. Two halves the generated assertions provably cannot reach are gates instead: a
type the contract has never heard of (`TestEveryJobArgsTypeIsDeclaredInTheContract`) and a type that
answers to **both** at once (`TestNoJobArgsDeclaresBothRoles` — *"a job does one workspace's work or
dispatches, never both"*).

**Binding comes from the args' own declaration.** A workspace pass runs under
`compose.workspaceJobCtx`, and nothing else in the tree binds a workspace inside a `Work` body:

```go
func workspaceJobCtx(ctx context.Context, args jobs.WorkspaceScoped) (context.Context, error) {
    ws := args.WorkspaceID()
    if ws == (ids.UUID{}) {
        return nil, fmt.Errorf("%s: declares WorkspaceScoped but carries no workspace", args.Kind())
    }
    return principal.WithWorkspaceID(ctx, ws), nil
}
```

It could not live in River middleware: `river.WorkerMiddleware` sees a `rivertype.JobRow` — raw JSON,
never the typed args — so a middleware could only bind by re-reading the wire key, which would leave
the role declaration a label *beside* the binding rather than the thing that governs it. Binding from
`WorkspaceID()` is what keeps the declaration load-bearing: a worker cannot claim one workspace and
work in another.

The **zero id is refused rather than bound**. An unbound GUC does not fail here; it fails at the
first tenant query, somewhere far less legible, and only after the job has already begun. A zero is
also what an args type decodes to when a queued row predates a change to its wire key, so the refusal
is the difference between a loud failure and a pass that quietly touches nothing.

**No role carries a deferred exception today.** `embed_reindex` is a full pair — a dispatcher whose
fan-out seeds the run's pending set and enqueues its children in one transaction, and an
`embed_reindex_workspace` child that re-embeds one tenant's corpus. Its only unusual properties are
declared ones: `cadence: on_demand` (a human's confirm enqueues it, no clock does) and
`max_attempts: 5` rather than the house three, because with no tick behind it nothing would
re-enqueue a lost workspace until a human confirms again.

**A dispatcher may read; it may not write.** `TestEveryFleetWideJobOnlyDispatches` holds the
`FleetWide` marker to the code: a dispatcher's `Work` must reach one of exactly three fan-out helpers
— `dispatchPerWorkspace`, `dispatchWith` or `dispatchOne` — and must issue no tenant write. That
allowlist is closed, and a direct `river.Insert` is deliberately not in it: those three are where a
child's insert options are built, which is where the `sweep` tag is stamped and the declared queue
and attempt cap are read. A dispatcher inserting around them enqueues a child invisible to both sweep
gauges, carrying whatever numbers its author typed — which is not hypothetical, it is what shipped in
the one dispatcher a hand-maintained comment forgot to list.

**Atomicity is the correctness argument, not a nicety.** `dispatchWith` inserts the whole fan-out as
one `InsertMany`. A per-workspace loop of single inserts that fails partway leaves some children
queued and then fails the dispatcher; by the time it retries, those children may already have
`completed` — and `activeSweepStates` deliberately excludes `completed`, so `ByArgs` uniqueness does
**not** suppress them. The retry would silently re-run those workspaces: a second overlay reconcile
spending incumbent API quota, a second AI-backed capture pass spending model budget. What this does
not buy is exactly-once — River is at-least-once, and the bound on that is the workspace passes
themselves, each re-reading its own backlog.

---

## 5. Args name rows, never carry content

`river_job` has **no workspace column and no RLS**, and River persists `args` verbatim into it. An
args field holding a message body or an address would therefore be a second store of subject data
that Art. 17 erasure never reaches — sitting in a fleet-visible table for as long as River's
retention keeps the row.

The rule is: **a job names a row and the worker reads it.** That is also what makes erasure reach an
in-flight job at all — the engine neutralizes it by scrubbing the row the job names (`comms_outbound`
goes to `parked`, and the waking job finds nothing to send). It only works while the job holds an id
and not a copy.

Three declared shapes, and the difference between them is the whole content of the field:

| Declaration | Meaning |
|---|---|
| `Field: id` | a reference to a row — the ordinary case, and the only one that owes nothing |
| `Field: {scalar: true, reason: …}` | the ratified exception: a value that is not an id and could not be one (`Provider: "gmail"`, the embed `Identity` string, a crawl's `MaxPages`). Generation refuses a scalar with no reason |
| `Field: {reason: …}` | an **id** that still owes an argument, because its NAME reads like content (`Body`, `Subject`, `RecipientEmail`) |

That third shape exists because coverage alone is not enough, and the two arms answer different
questions. `TestEveryJobArgsFieldIsAnIdOrAnArguedForScalar` runs both: **coverage** is total over the
fields that actually exist on the compiled struct, inferring nothing from a name — so `Snippet`,
`Note` and `Domain` are under the same rule as `Body`. **Suspicion** matches field names against a
word list and refuses a flagged name with no rationale, because coverage alone would let `Body: id`
through in silence. A word list is a poor detector and a fine prompt: it cannot decide whether a
field is safe, only insist that somebody said so. A reason on a name the list does *not* flag is
stale prose and fails the same gate.

The declared reasons are read as **waivers**, held to the same bar as every other ratified exception
in the tree: a reason that states a cost, and an entry that still describes live code.

---

## 6. The failure vocabulary — `jobs.Fault`

River persists `err.Error()` into `river_job.errors` **verbatim**. That column has no workspace, no
RLS, and a retention River chooses, so whatever a worker returns is stored fleet-visible for as long
as the ladder runs. A provider refusing a message routinely names the address it refused — the raw
cause is the one thing that may not travel this way.

So every worker returns through `jobs.Fault` / `jobs.FaultContext`, which renders a **fixed operator
sentence** chosen by what the cause IS, and keeps the real cause reachable through `errors.Is`:

```go
type fault struct { sentence string; cause error }
func (f *fault) Error() string { return f.sentence }   // fixed
func (f *fault) Unwrap() error { return f.cause }      // still classifies
```

The vocabulary maps the shared sentinel registry (`internal/shared/apperrors`) to fifteen sentences,
each saying what went wrong **and** what it means for the job — an operator reading a failure list
needs to know whether to retry, wait, or fix something (`"the record this job names no longer
exists"`, `"the incumbent CRM's API budget is spent; the poller will catch up"`). An unclassified
cause logs at ERROR with the caller's context and becomes one fixed fallback sentence that says where
the diagnosis went.

Two things pass through **untouched**: `river.JobSnoozeError` and `river.JobCancelError`. A snooze
reschedules and a cancel deliberately stops; neither is a failure and neither carries a cause to
publish. They are checked *before* the vocabulary, so a cancel carrying a known sentinel stays a
cancel — stopping deliberately is not failing. The check cannot live at the call sites: control
returns reach a worker through helpers as often as directly, and every routine provider throttle
would otherwise log as an unclassified failure.

**`river_job.errors` is never shown to a human raw.** `jobs.Failure.StoredReason` carries the column
verbatim and the caller must vet it with `jobs.VettedSentence(s)` before putting it on a wire: a
worker that bypassed `Fault` stored its raw cause there, and River writes into the column too (its
rescuer's `"Stuck job rescued by JobRescuer"` is not a `Fault` sentence and is correctly refused). The
comparison is **exact**, never a prefix or a contains — a raw cause that merely embeds a vetted
sentence would otherwise carry the rest of its text through on the strength of the part that matched.
The vocabulary itself stays unexported: a caller asks whether one string is vetted, it does not get
the list to render or match against by hand.

---

## 7. Reading the fleet

Two readers over one table, answering two different questions — `/metrics` (is a queue growing?) and
`GET /v1/admin/job-health` (whose work died, and why?). Both live in `internal/platform/jobs`
(`stats.go`, `health.go`) rather than in compose, because `river_job` has no RLS: **every statement
over it is a hand-imposed scope**, and two readers spelling that scope in two packages is how the
operational and the admin surface drift into different answers about one table.

The gauge families, their labels, the sweep-coverage pairs, the declaration-derived catalogue, and
the eight caveats that apply when you read a kind's rows are documented once, for operators, in
[reference/configuration.md → Reading the job surfaces](../reference/configuration.md#reading-the-job-surfaces).
They are not repeated here.

The one structural point worth restating: the scope `health.go` imposes admits the caller's own
workspace rows plus the untenanted rows of the **caller's declared dispatcher kinds** — closed
against that list rather than admitting every null. "The workspace key is null" is a property held by
source-shape tests, not by a database constraint, and the app role holds direct CRUD on an
RLS-less table; a malformed or externally inserted row would otherwise land in a global arm and carry
its kind, counts and failure class to every workspace's admin.

---

## 8. What holds it — the fitness tests

Eleven root gates under `backend/`, all in package `backendarch`, all part of `make check`. Every
gate that walks the tree carries a **floor** — a minimum number of things it must have inspected —
because most of them are prohibitions, and a walker that silently matched nothing would otherwise
read green. (The census carries its own, `declaredJobKindFloor`, beside the assembly it reads.)

| File | What it catches |
|---|---|
| `jobrole_test.go` | a River job args type (it declares `Kind()`) that `api/jobs.yaml` has never heard of; and a type declaring `WorkspaceID()` **and** `FleetWide()` at once |
| `jobwirekey_test.go` | a workspace key spelled anything but `json:"workspace_id"`, of any type but `ids.UUID`, absent, embedded, or duplicated at one depth — and, the other direction, a **dispatcher** shipping a workspace key at all. Both failures look exactly like the reassuring answer to `args->>'workspace_id'` |
| `jobbinding_test.go` | a `Work` body that binds its own workspace inline instead of through `workspaceJobCtx` — which could declare one field and bind another, with the role gate still green |
| `jobfleetwide_test.go` | a `FleetWide` dispatcher that never fans out, that fans out around the three chokepoints, that issues a tenant write, or that no worker runs at all |
| `jobfleetwideshapes_test.go` | the gate above, falsified: every dispatch shape the tree actually uses proven **accepted**, and the two shapes it exists to reject proven rejected. A gate that blocks a legitimate author gets weakened by the person it stopped |
| `jobfleetscan_test.go` | a `FROM workspace` collection read outside the ratified sites, each of which must name which of four things it is (a dispatcher's enumeration, a pure read, a boot path, or tenant resolution for an untenanted inbound request) |
| `jobfault_test.go` | a `Work` return, or an assignment to a named error result, that is not `nil`, `jobs.Fault(…)` or a River control return — plus a worker that logs an error and returns `nil` without a ratified `fault:` waiver |
| `jobargscontent_test.go` | an args field the contract does not declare, a scalar with no rationale, and a content-sounding field name declared `id` with nothing said about it |
| `jobkindgate_test.go` | the registration gate falsified — the three legitimate authoring shapes compile, an undeclared kind does not, and a worker registered under the wrong kind is named. The second half puts the undeclared registration in front of the **real** generated union, not a miniature of it |
| `jobregistrationban_test.go` | the forbidigo rules that ban a direct River registration and a runtime schedule mutation — held to River's own API rather than to a remembered list of spellings. Every exported function in package `river` whose first parameter is `*Workers` is derived as an entry point, so a fourth spelling in a future upgrade enrols itself |
| `jobcensus_test.go` | everything the others cannot see, by building a real (client-less) runner assembly and laying it beside the contract: a kind declared and never wired, a `{derived: …}` timeout whose Go constant moved, an args field nobody declared, a fan-out child not writing its unit key, an args-owned kind inserting on the wrong queue, one args type answering to a second kind, and a declared queue whose bound compose does not build |

The census is the only gate that holds **both** ends of the contract at once. Every other one holds a
single end: the union stops an undeclared kind compiling, `MustBeTotal` refuses a boot that got one in
anyway, `Govern` makes the declared timeout the one River applies — none of them can see a kind that
was declared and never wired.

---

## Rules of thumb

- **A kind is declared before it is written.** Not in the file ⇒ does not compile ⇒ cannot boot.
- **A worker exposes `Work` and nothing else.** Timeout, retry policy and middleware belong to the
  declaration.
- **A dispatcher enumerates and enqueues; a workspace job does the work and owns its failure.** If a
  `Work` body loops the fleet, it is the shape this whole layer removed.
- **A fan-out goes through `dispatchPerWorkspace` / `dispatchWith` / `dispatchOne`** — never a direct
  River insert, or the child loses the sweep tag and its declared cap.
- **Args carry ids.** A scalar is a ratified exception with a written reason.
- **Return the failure through `jobs.FaultContext`**, and vet anything read back out of
  `river_job.errors` before showing it to a human.
- **A null `args->>'workspace_id'` means a dispatcher and nothing else** — every read of the job table
  is built on that, in both directions.

## Where the code lives

| | |
|---|---|
| The declaration | `backend/api/jobs.yaml` |
| The generator + its validation rules | `backend/tools/gen-jobs/` (`contract.go`, `validate.go`) |
| Compiled Spec table (generated) | `internal/platform/jobs/specs_gen.go` |
| Spec, roles, fan-out units, timeout/cadence policies | `internal/platform/jobs/spec.go`, `role.go` |
| River client lifecycle + `MustBeTotal` | `internal/platform/jobs/jobs.go` |
| Timeout binding (`WorkOnly`, `Govern`) | `internal/platform/jobs/govern.go` |
| Failure vocabulary (`Fault`, `VettedSentence`) | `internal/platform/jobs/fault.go` |
| Job-table readers (`/metrics`, `job-health`) | `internal/platform/jobs/stats.go`, `health.go` |
| Closed union + role assertions (generated) | `internal/compose/jobkinds_gen.go` |
| Runner assembly, `JobRunnerConfig`, queue set | `internal/compose/jobs.go`, `jobqueues.go` |
| Registration path + kind↔type pairing | `internal/compose/jobregistry.go` |
| The ONE fleet enumeration + the three fan-out helpers | `internal/compose/dispatch.go` |
| Workspace binding | `internal/compose/workspacejob.go` |
| Schedule resolution from the declared cadence | `internal/compose/jobschedule.go` |
| The census (contract ⟷ wiring, both directions) | `internal/compose/jobcensus.go`, `jobcensusconfig.go` |
| Per-concern workers and args types | `internal/compose/jobs_*.go` |
| The fitness gates | `backend/job*_test.go` |

## Where to go next

- Adding a kind: [how-to/add-a-job.md](../how-to/add-a-job.md).
- Operating the fleet — gauges, `job-health`, and the dials that set a cadence:
  [reference/configuration.md](../reference/configuration.md#reading-the-job-surfaces).
- The write shape a workspace pass commits through: [write-backbone.md](write-backbone.md).
- The clock-triggered automations one of these dispatchers drives: [automation.md](automation.md).
- How compose wires seams and cross-module edges generally:
  [composition-layer.md](composition-layer.md).
