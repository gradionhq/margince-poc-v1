# Add an RBAC object

For introducing a new kind of record into the permission model — a new name in
the closed set of objects a role document may grant. For adding an operation to
an existing module, use [add-an-endpoint.md](add-an-endpoint.md); for the whole
capability, [add-a-module.md](add-a-module.md). What the finished vocabulary
looks like: [reference/rbac-matrix.md](../reference/rbac-matrix.md); why it works
this way: [explanation/rbac-roles-and-teams.md](../explanation/rbac-roles-and-teams.md).

The change spans **five places**, and skipping any one of them fails in a
different way. Two of them are held by merge-blocking gates, so you will find out
before you push. The third — the backfill migration — has **no compile-time or
review-time signal at all** on a fresh database, and getting it wrong 403s every
existing installation forever.

## 1. Add the object to the policy vocabulary

In `backend/internal/modules/identity/internal/policy/policy.go`:

1. Append the name to `coreObjects`. This is the closed set `Parse` validates
   against, so nothing outside it can ever be granted.
2. Add one parameter to `objects(...)` — same name, same position — and one line
   to the map it returns.
3. Give **every one of the five system roles** its grant, in the same position,
   in the `defaults` map: `admin`, `manager`, `rep`, `read_only`, `ops`.

**The argument list is positional.** `objects(...)` zips its arguments onto the
map by declaration order, and a role's entry is a single line of bare `crud`,
`readOnly` and `grant{}` values. Go's compiler catches a *missing* argument;
nothing catches a *transposed* one, and a diff that shifts two grants one column
apart is invisible in review. Write the new grant at the **end** of every list
rather than in a "logical" middle position, and re-read the generated matrix in
step 4 before you believe your own change.

Pick the posture from an existing precedent rather than inventing one — the
comment block above `defaults` records the reasoning for each family (record
posture, pipeline-config posture, admin/ops-owned config), and matching one of
them is how a reviewer checks your choice.

## 2. Add the object to the contract enum

Add the same string to `RbacObject` in `backend/api/crm.yaml`.

**The server derives nothing from it.** `oapi-codegen` emits no Go constants for
a top-level standalone string enum, so `policy.coreObjects` is maintained by hand
and this enum is maintained by hand. What the enum *does* buy is the web client:
`openapi-typescript` renders it as a string union, so a capability check against
a misspelled object is a TypeScript error rather than a check that compiles and
silently denies forever.

What keeps the two halves equal is a merge-blocking parity test,
**`TestContractObjectEnumMatchesPolicyVocabulary`** in
`backend/rbacvocabulary_test.go`. Both sides are derived — the object list is
AST-parsed out of `policy.go` by `coreObjectsFromSource`
(`backend/rbacvocabularysource_test.go`), the contract side is read from the YAML
— so the test never becomes a third place to keep current. Editing the enum alone
changes what clients can *express*, never what the server *enforces*.

## 3. Write the backfill migration — this is the step that bites

**Role seeding runs once, at workspace creation, and never re-syncs.**
`identity.seedSystemRoles` writes `policy.MustDefaultJSON(role.key)` into
`role.permissions` when the workspace is bootstrapped
(`backend/internal/modules/identity/service.go`), and no code path ever reconciles
a stored document against the compiled-in defaults afterwards. Authentication
reads the *stored* document: `loadGrants` selects `role.permissions` and merges
it. So an object added to `coreObjects` without a migration is granted to nobody
who bootstrapped earlier. It works on your fresh database and 403s everywhere
else, permanently — which is exactly how `saved_view`, `webhook_subscription`,
`relationship`, `partner`, `list` and `tag` reached production ungranted.

Write the pair in `backend/migrations/core/` following
[apply-migrations.md](apply-migrations.md) for numbering and lane conventions,
and copy the shape of the most recent RBAC backfill,
**`0183_list_tag_rbac.up.sql`**:

- One `UPDATE role SET permissions = jsonb_set(permissions, '{objects,<name>}', '<grant>'::jsonb)`
  per distinct grant, grouped by the roles that share it.
- Guard every statement with `WHERE is_system AND ... AND NOT permissions->'objects' ? '<name>'`.
  The only-if-absent guard is what makes the migration free where it is not
  needed and non-destructive where an operator has already edited a role.
- The grants must reproduce, exactly, what step 1 seeds — the replay in step 6
  compares the upgraded end state against the seeded matrix, verb by verb.

**The `down` is a documented no-op.** Reversing the schema does not remove the
object from `policy.coreObjects`, so deleting the grant on rollback cannot
restore an earlier correct state — it can only recreate the permanent 403 the
migration exists to fix, and it would do so on workspaces that legitimately held
the grant already (where the up's guard wrote nothing, and left no trace
distinguishing those rows from the ones it did write). A forward-only data repair
has no meaningful inverse. `0183_list_tag_rbac.down.sql` says exactly this, and
is the file to copy.

### The typo that locks people out of login

`policy.Parse` **rejects** an unknown object key:

```go
for object := range doc.Objects {
    if !IsCoreObject(object) {
        return Document{}, fmt.Errorf("policy: unknown object %q in permissions document", object)
    }
}
```

`Parse` is called from `loadGrants`, which runs on the **login** path, on session
resolution, and on the agent-authority re-derivation in `identity/authority.go`.
A role carrying an invalid document is treated as a data defect to surface, not
as a reason to silently downgrade to no access — so the whole authentication
fails.

That makes a typo in the migration's JSON path far worse than a missing grant. If
you write `'{objects,webook_subscription}'`, the migration succeeds, the object is
never granted, **and every user holding that role can no longer log in** — the
document now names an object outside the closed set. Spell the path from the same
string literal you added to `coreObjects`, and prove it by running the replay in
step 6 rather than by reading it twice.

## 4. Regenerate the published matrix

[reference/rbac-matrix.md](../reference/rbac-matrix.md) is generated from the
seeded documents. From `backend/`:

```
go test ./internal/modules/identity/ -run RBAC -update
```

The same test — **`TestPublishedRBACMatrixMatchesTheSeededRoleDocuments`** in
`backend/internal/modules/identity/rbacmatrix_test.go` — runs on every build
*without* `-update` and fails when the page and the seeded values disagree, so the
two cannot drift apart unnoticed. Read the regenerated row: it is your best
chance to catch a transposed argument from step 1, because it renders each role's
grant as `CRUD` letters rather than as a position in a call.

## 5. Gate the store, then the UI

**Server side.** Every exported method on the owning `*Store` or `*Service` that
touches the new object calls `auth.Require(ctx, "<object>", principal.ActionX)`,
plus `auth.EnsureVisible` / `auth.ScopeClauseFor` for row scope — see
[reference/platform-toolkit.md](../reference/platform-toolkit.md#platformauth--the-admission-point).
This is not optional politeness: `TestEveryStoreEntryPointIsAuthGated`
(`backend/rbacgate_test.go`) derives the entry-point set from the tree and fails
an ungated one, because an ungated store method is a door into tenant data that
is reachable by any transport wired to it and invisible to review.

**Client side.** Bind the affordance to the **grant**, never to a role name. The
hooks are in `frontend/src/app/capability.ts` and take the generated `RbacObject`
union, so a misspelled object is a compile error:

| Hook | Use it for |
|---|---|
| `useCan(object, action)` | One specific request. Object RBAC only — no seat ceiling. |
| `useCanWrite(object, action)` | A control that issues a **mutating** request (the common case): grant ∧ seat. |
| `useCanUpsert(object)` | A control whose endpoint inserts *or* replaces, so the needed verb is not knowable client-side. |
| `useHoldsWriteGrant(object)` | An authoring *surface* — a nav entry, a section heading — where any write verb justifies showing it. |
| `useCanMutate()` | The licensing seat ceiling alone. |

The answers come from the server (`GET /me` carries the merged grants it
computed) and only the vocabulary comes from the contract, which is what keeps
the client honest on a workspace whose stored grants have drifted. This is UX
honesty, never enforcement — the server's `auth.Require` is the authority on
every call, and a client that gets it wrong shows the wrong button, not the wrong
data.

## 6. Verify

Run both lanes, and know which one proves what:

**`make check`** — the merge gate.

- `TestContractObjectEnumMatchesPolicyVocabulary` — the contract enum and
  `coreObjects` are the same set. Catches step 2 missing, or misspelled.
- `TestPublishedRBACMatrixMatchesTheSeededRoleDocuments` — the published page
  matches the seeded documents. Catches step 4 not run.
- `TestEveryStoreEntryPointIsAuthGated` — no ungated store entry point. Catches
  step 5 missing on the server.
- `TestLegacyInstallFixtureIsTheInitialCommitVocabulary`,
  `TestLegacyInstallFixtureReproducesTheInitialCommitGrants`,
  `TestMidlifeInstallFixtureSitsBetweenTheInitialCohortAndHead`
  (`backend/rbaclegacyinstall_test.go`, `backend/rbaclegacydocument_test.go`) —
  the replay's *starting state* is still faithfully derived from git history, so
  nobody can make step 6's replay pass by editing the fixture to already contain
  the grant the migration failed to deliver.
- `make check-fe` — the TypeScript build proves the new object is expressible in
  the capability hooks.

**`make test-integration`** — the real-Postgres lane, and **the only thing that
proves the backfill landed on an old install**.
`TestUpgradingALegacyInstallationYieldsTheSeededMatrix`
(`backend/migrations/rbac_upgrade_replay_integration_test.go`) seeds the role
documents an old installation actually held, migrates it to head, and asserts the
end state equals the matrix the server seeds today. It executes the obligation
rather than approximating it — which is why no list of objects, and no scan of
the migration SQL for `'{objects,<name>}'`, survives in that gate. Two
installations are replayed, an oldest-possible one and a mid-life one, because a
conditionally-written backfill only has somewhere to fail on a document that
already holds half the vocabulary.

Commit the policy change, the contract, the migration pair, the regenerated
matrix and the UI binding together — they are one change, and any one of them
alone is a broken state.
