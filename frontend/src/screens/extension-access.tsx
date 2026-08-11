import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { AlertTriangle, KeyRound, Route, Timer } from "lucide-react";
import type { ReactNode } from "react";
import { useCanMutate } from "../app/capability";
import {
  Badge,
  Checkbox,
  EmptyState,
  SectionHeader,
} from "../design-system/atoms";
import { useT } from "../i18n";
import {
  problemMessageOf,
  QueryGate,
  type QueryLike,
  throwProblem,
  useMe,
} from "./common";
import "./extension-access.css";

// Extension access (settings → Extensions): what the composed extension tier
// brought into this installation, and who may use it.
//
// The problem it exists to solve: a unit registers its own RBAC objects at
// composition time (ext_notes_note, ext_notes_signing_key, …), but nothing in
// the product could grant them — the only lever was hand-written SQL against
// role.permissions. So a shipped, enabled extension renders "you do not hold
// access" for every seat, and the screen that would explain why did not exist.
// This is that screen: the unit inventory, and one role × CRUD matrix per
// object the unit registered.
//
// It is deliberately NOT a general role editor. It shows the objects the
// extension tier contributed, because those are the ones no seeded role can
// possibly hold a grant on — every core object already comes granted by the
// bootstrap matrix.

// The wire shapes. These are hand-written rather than read off
// src/api/schema.d.ts because the contract is landing in parallel and the
// generated types do not carry /roles or /extensions yet. When they do, delete
// these and the raw fetches below in favour of `api.GET("/roles")` — every
// other screen goes through the typed client and this one should too.
export type CrudAction = "read" | "create" | "update" | "delete";

export type ObjectGrant = Readonly<Record<CrudAction, boolean>>;

export type ExtensionRole = Readonly<{
  key: string;
  name: string;
  is_system: boolean;
  // An index signature: an object a role was never granted is ABSENT, not
  // written as an all-false grant, exactly as /me reports its own grants. A
  // missing key therefore has to read as a denial everywhere below.
  objects: Readonly<Record<string, ObjectGrant | undefined>>;
}>;

export type ExtensionUnit = Readonly<{
  name: string;
  version: string;
  rbac_objects: readonly string[];
  routes: readonly string[];
  jobs: readonly string[];
}>;

// The four verbs, in the order an operator reads them: read first, because it
// is the one that decides whether the extension's screens render at all, and
// the one whose absence produces the confusing empty state.
const CRUD: readonly CrudAction[] = ["read", "create", "update", "delete"];

const ROLES_KEY = ["extension-access", "roles"] as const;
const EXTENSIONS_KEY = ["extension-access", "extensions"] as const;

// Every call here goes through a bare fetch on the same terms the typed client
// uses — the same same-origin /v1 base, the session cookie, an RFC-7807 body on
// refusal — so the swap to the generated client is a one-file change and the
// failure copy never moves. The base resolves per call rather than at module
// load, so a test or a preview host that swaps the location is followed rather
// than baked in.
function apiUrl(path: string): string {
  const origin =
    typeof globalThis.window === "undefined"
      ? "http://localhost"
      : globalThis.location.origin;
  return `${origin}/v1${path}`;
}

async function getJson<Data>(path: string): Promise<Data> {
  const response = await fetch(apiUrl(path), { credentials: "include" });
  const payload = await response.json().catch(() => undefined);
  if (!response.ok) {
    throwProblem(payload);
  }
  return payload as Data;
}

function useRoles(enabled: boolean) {
  return useQuery({
    queryKey: ROLES_KEY,
    enabled,
    queryFn: async (): Promise<readonly ExtensionRole[]> => {
      const body = await getJson<{ roles?: readonly ExtensionRole[] }>(
        "/roles",
      );
      return body.roles ?? [];
    },
  });
}

function useExtensions(enabled: boolean) {
  return useQuery({
    queryKey: EXTENSIONS_KEY,
    enabled,
    queryFn: async (): Promise<readonly ExtensionUnit[]> => {
      const body = await getJson<{ extensions?: readonly ExtensionUnit[] }>(
        "/extensions",
      );
      return body.extensions ?? [];
    },
  });
}

// The PATCH takes the whole grant, not a delta, so the request states the
// grant the operator is looking at rather than an edit against a version of it
// the server may no longer hold.
function useSetGrant() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (input: {
      roleKey: string;
      object: string;
      grant: ObjectGrant;
    }): Promise<ExtensionRole> => {
      const response = await fetch(
        apiUrl(
          `/roles/${encodeURIComponent(input.roleKey)}/objects/${encodeURIComponent(input.object)}`,
        ),
        {
          method: "PATCH",
          credentials: "include",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify(input.grant),
        },
      );
      const payload = await response.json().catch(() => undefined);
      if (!response.ok) {
        throwProblem(payload);
      }
      return payload as ExtensionRole;
    },
    // The server answers with the whole updated role, so the cache takes it
    // verbatim: a refetch would repaint the matrix a beat later and a local
    // merge would invent a grant the server never confirmed.
    onSuccess: (role) => {
      queryClient.setQueryData<readonly ExtensionRole[]>(ROLES_KEY, (roles) =>
        (roles ?? []).map((existing) =>
          existing.key === role.key ? role : existing,
        ),
      );
    },
    // A refused write leaves the checkbox showing the state the server holds
    // (nothing was applied locally), but another admin's concurrent change is
    // the likelier reason for a refusal here — so re-read rather than trust
    // the snapshot the failure was computed against.
    onError: () => {
      void queryClient.invalidateQueries({ queryKey: ROLES_KEY });
    },
  });
}

// One pending/error surface for the two reads the screen needs together: a
// matrix with roles but no unit inventory is not a partial screen, it is a
// different screen. QueryStates takes any QueryLike, so the merge needs no
// react-query lie.
function useExtensionAccess(enabled: boolean): QueryLike<{
  extensions: readonly ExtensionUnit[];
  roles: readonly ExtensionRole[];
}> {
  const extensions = useExtensions(enabled);
  const roles = useRoles(enabled);
  return {
    isPending: extensions.isPending || roles.isPending,
    isError: extensions.isError || roles.isError,
    error: extensions.error ?? roles.error,
    data:
      extensions.data && roles.data
        ? { extensions: extensions.data, roles: roles.data }
        : undefined,
    refetch: () => {
      void extensions.refetch();
      void roles.refetch();
    },
  };
}

// The grant a role holds on an object, with an absent key resolving to the
// zero grant — the same fail-closed reading the capability hook applies to
// /me, and the reason a freshly composed extension shows an empty matrix
// instead of a row of ticks nobody granted.
const NO_GRANT: ObjectGrant = {
  read: false,
  create: false,
  update: false,
  delete: false,
};

function grantOf(role: ExtensionRole, object: string): ObjectGrant {
  return role.objects[object] ?? NO_GRANT;
}

export function ExtensionAccessCard() {
  const t = useT();
  const me = useMe();
  // Editing role permissions is admin-only server-side, so the whole card is
  // admin-only here — the same shape UsersAdminCard uses, and for the same
  // reason: an ops seat in the Organization group would otherwise be handed
  // controls that only ever 403. The seat ceiling ANDs on top, because a read
  // seat may read this page and may not write anything on it.
  const isAdmin = (me.data?.roles ?? []).includes("admin");
  const canMutate = useCanMutate();
  const query = useExtensionAccess(isAdmin);

  return (
    <section className="card">
      <SectionHeader title={t("extAccess.title")} sub={t("extAccess.sub")} />
      {/* Gate on the role probe itself so the admin-only notice appears only
          once /me has answered — never as a flash while it loads. */}
      <QueryGate query={me}>
        {() =>
          isAdmin ? (
            <QueryGate query={query}>
              {({ extensions, roles }) =>
                extensions.length === 0 ? (
                  <EmptyState>
                    <p className="t-small">{t("extAccess.empty")}</p>
                  </EmptyState>
                ) : (
                  <div className="ext-units">
                    {/* The seat ceiling, said once rather than beside every
                        matrix: a read seat sees the whole inventory and every
                        grant, and changes none of them. */}
                    {canMutate ? null : (
                      <p className="t-small ext-note">
                        {t("extAccess.readOnly")}
                      </p>
                    )}
                    {extensions.map((unit) => (
                      <UnitBlock
                        key={unit.name}
                        unit={unit}
                        roles={roles}
                        canManage={canMutate}
                      />
                    ))}
                  </div>
                )
              }
            </QueryGate>
          ) : (
            <EmptyState>
              <p className="t-small">{t("extAccess.adminOnly")}</p>
            </EmptyState>
          )
        }
      </QueryGate>
    </section>
  );
}

function UnitBlock({
  unit,
  roles,
  canManage,
}: Readonly<{
  unit: ExtensionUnit;
  roles: readonly ExtensionRole[];
  canManage: boolean;
}>) {
  const t = useT();
  return (
    <article className="ext-unit">
      <header className="ext-unit-head">
        <h3 className="ext-unit-name">{unit.name}</h3>
        <Badge>{t("extAccess.version", { version: unit.version })}</Badge>
      </header>
      <dl className="ext-brings">
        <BringsRow
          icon={<KeyRound aria-hidden size={15} />}
          label={t("extAccess.brings.objects")}
          items={unit.rbac_objects}
        />
        <BringsRow
          icon={<Route aria-hidden size={15} />}
          label={t("extAccess.brings.routes")}
          items={unit.routes}
        />
        <BringsRow
          icon={<Timer aria-hidden size={15} />}
          label={t("extAccess.brings.jobs")}
          items={unit.jobs}
        />
      </dl>
      {unit.rbac_objects.length === 0 ? (
        <p className="t-small ext-note">{t("extAccess.noObjects")}</p>
      ) : (
        unit.rbac_objects.map((object) => (
          <ObjectMatrix
            key={object}
            object={object}
            roles={roles}
            canManage={canManage}
          />
        ))
      )}
    </article>
  );
}

function BringsRow({
  icon,
  label,
  items,
}: Readonly<{
  icon: ReactNode;
  label: string;
  items: readonly string[];
}>) {
  const t = useT();
  return (
    <div className="ext-brings-row">
      <dt className="t-label ext-brings-term">
        {icon}
        {label}
      </dt>
      <dd className="ext-brings-def">
        {items.length === 0 ? (
          <span className="t-small ext-none">{t("extAccess.brings.none")}</span>
        ) : (
          <ul className="ext-chips">
            {items.map((item) => (
              <li key={item} className="ext-chip t-mono">
                {item}
              </li>
            ))}
          </ul>
        )}
      </dd>
    </div>
  );
}

/**
 * One object's role × CRUD matrix.
 *
 * A real `<table>`, not a grid of divs: the two axes ARE the meaning here, and
 * a screen-reader user landing on a tick in the middle of it has to be able to
 * ask which role and which verb it belongs to. `scope="col"` / `scope="row"`
 * is what answers that, and the `<caption>` names which object the whole grid
 * is about — a table announced as "read create update delete" with no subject
 * is unreadable however many ticks it contains.
 *
 * Every cell is a native checkbox carrying its own full sentence as its
 * accessible name ("Allow Rep to read ext_notes_note"), because the header
 * association alone is a hint, not a name: a control has to be identifiable
 * out of context, and it is the only thing a user hears when tabbing straight
 * to it. The visible tick therefore has no visible text of its own — the name
 * lives in an `.sr-only` span inside the label the Checkbox atom wraps around
 * the input, which is also what makes the whole cell clickable.
 *
 * Native checkboxes are what keeps it keyboard-operable: Tab reaches every
 * cell in reading order and Space toggles it, with no key handling of our own
 * to get wrong.
 */
function ObjectMatrix({
  object,
  roles,
  canManage,
}: Readonly<{
  object: string;
  roles: readonly ExtensionRole[];
  canManage: boolean;
}>) {
  const t = useT();
  const setGrant = useSetGrant();
  // The whole point of the screen: an object no role can read is an extension
  // whose every screen renders "you do not hold access", and that is invisible
  // from anywhere else in the product. Said plainly, next to the toggles that
  // fix it.
  const nobodyReads = roles.every((role) => !grantOf(role, object).read);

  return (
    <div className="ext-object">
      <div className="ext-matrix-wrap">
        <table className="ext-matrix">
          <caption className="t-label ext-matrix-caption">
            {t("extAccess.matrixCaption", { object })}
          </caption>
          <thead>
            <tr>
              <th scope="col">{t("extAccess.roleColumn")}</th>
              {CRUD.map((action) => (
                <th scope="col" key={action}>
                  {t(`extAccess.action.${action}`)}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {roles.map((role) => {
              const grant = grantOf(role, object);
              return (
                <tr key={role.key}>
                  <th scope="row">
                    <span className="ext-role-name">{role.name}</span>
                    {role.is_system ? (
                      <span className="t-small ext-role-note">
                        {t("extAccess.systemRole")}
                      </span>
                    ) : null}
                  </th>
                  {CRUD.map((action) => (
                    <td key={action}>
                      <Checkbox
                        className="ext-cell"
                        checked={grant[action]}
                        disabled={!canManage || setGrant.isPending}
                        onChange={(event) =>
                          setGrant.mutate({
                            roleKey: role.key,
                            object,
                            grant: { ...grant, [action]: event.target.checked },
                          })
                        }
                        label={
                          <span className="sr-only">
                            {t("extAccess.cell", {
                              role: role.name,
                              action: t(`extAccess.action.${action}`),
                              object,
                            })}
                          </span>
                        }
                      />
                    </td>
                  ))}
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
      {nobodyReads ? (
        // role="status" rather than plain prose: the sentence appears and
        // disappears as the last read grant is toggled, and a change nobody is
        // told about is the same silence this screen exists to break.
        <p role="status" className="ext-warn t-small">
          <AlertTriangle aria-hidden size={15} />
          {t("extAccess.nobodyReads", { object })}
        </p>
      ) : null}
      {setGrant.isError ? (
        <p role="alert" className="form-error">
          {problemMessageOf(setGrant.error, t)}
        </p>
      ) : null}
    </div>
  );
}
