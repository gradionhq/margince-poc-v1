// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  AlertTriangle,
  ArrowUpRight,
  Info,
  KeyRound,
  Route,
  Timer,
} from "lucide-react";
import { type ReactNode, useEffect } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { useCanMutate, useHoldsAdminRole } from "../app/capability";
import { EXTENSION_SCREEN, findExtension } from "../app/extensions";
import { routeHash } from "../app/router";
import {
  Badge,
  Disclosure,
  EmptyState,
  TableScroll,
} from "../design-system/atoms";
import { Callout } from "../design-system/callout";
import { Panel, PanelBody } from "../design-system/panel";
import { SettingList, SettingRow } from "../design-system/settingrow";
import { Switch } from "../design-system/switch";
import { useT } from "../i18n";
import {
  isVersionSkew,
  ProblemError,
  problemMessageOf,
  QueryGate,
  type QueryLike,
  QueryStates,
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

// The wire shapes, read straight off the generated contract — this screen was
// written against hand-written copies while /roles and /extensions were landing
// in parallel, and they are gone now that the contract carries both. Everything
// below goes through the typed client, like every other screen.
//
// `RbacAction` is the contract's own verb enum, so the column set here cannot
// drift from the one the server accepts.
export type CrudAction = components["schemas"]["RbacAction"];

// The four booleans a role holds on one object. Shared with /me's effective
// grants (same schema), which is exactly why an absent key has to read the same
// way in both places.
export type ObjectGrant = components["schemas"]["RbacObjectGrant"];

// `Role.version` is an int64 in the contract, NOT a string: it is a RowVersion,
// and it rides back out as `If-Match` — stringified at the call, since the
// header is a string like every other versioned write in the product.
export type ExtensionRole = components["schemas"]["Role"];

// One entry per OPERATION, not per path: the inventory is deduplicated by
// (path, method) server-side precisely so a DELETE cannot hide behind a GET on
// the same path. The method is therefore rendered, never collapsed away — an
// operator reading this screen has to be able to see that a unit's route can
// delete.
export type ExtensionRoute = components["schemas"]["ComposedExtensionRoute"];

export type ExtensionUnit = components["schemas"]["ComposedExtension"];

// The four verbs, in the order an operator reads them: read first, because it
// is the one that decides whether the extension's screens render at all, and
// the one whose absence produces the confusing empty state.
const CRUD: readonly CrudAction[] = ["read", "create", "update", "delete"];

const ROLES_KEY = ["extension-access", "roles"] as const;
const EXTENSIONS_KEY = ["extension-access", "extensions"] as const;

// Both reads go through the typed client: same same-origin /v1 base, same
// session cookie, same RFC-7807 body on refusal, and now the same generated
// types as every other screen.
//
// `roles` and `extensions` are REQUIRED in their envelopes, so the `?? []` is
// not defending against a server that omits them — it narrows `data`, which
// openapi-fetch types as possibly-undefined on the error branch that
// `throwProblem` has already left.
function useRoles(enabled: boolean) {
  return useQuery({
    queryKey: ROLES_KEY,
    enabled,
    queryFn: async (): Promise<readonly ExtensionRole[]> => {
      const { data, error } = await api.GET("/roles");
      if (error) {
        throwProblem(error);
      }
      return data?.roles ?? [];
    },
  });
}

function useExtensions(enabled: boolean) {
  return useQuery({
    queryKey: EXTENSIONS_KEY,
    enabled,
    queryFn: async (): Promise<readonly ExtensionUnit[]> => {
      const { data, error } = await api.GET("/extensions");
      if (error) {
        throwProblem(error);
      }
      return data?.extensions ?? [];
    },
  });
}

// The PATCH takes the whole grant, not a delta, so the request states the
// grant the operator is looking at rather than an edit against a version of it
// the server may no longer hold. `If-Match` carries the version of the role
// that grant was read from: the contract makes the header optional only because
// every versioned write here is, and a client that omits it is asking the
// server to overwrite a row it never looked at.
function useSetGrant() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (input: {
      roleKey: string;
      object: string;
      grant: ObjectGrant;
      version: number;
    }): Promise<ExtensionRole | undefined> => {
      const { data, error } = await api.PATCH("/roles/{key}/objects/{object}", {
        params: {
          path: { key: input.roleKey, object: input.object },
          // Stringified because the header is text and the version is an
          // int64 — the same spelling every other If-Match write here uses.
          header: { "If-Match": String(input.version) },
        },
        body: input.grant,
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
    // The server answers with the whole updated role, so the cache takes it
    // verbatim: a refetch would repaint the matrix a beat later and a local
    // merge would invent a grant the server never confirmed.
    onSuccess: (role) => {
      if (!role) {
        return;
      }
      queryClient.setQueryData<readonly ExtensionRole[]>(ROLES_KEY, (roles) =>
        (roles ?? []).map((existing) =>
          existing.key === role.key ? role : existing,
        ),
      );
    },
    // A refused write leaves the checkbox showing the state the server holds
    // (nothing was applied locally), but another admin's concurrent change is
    // the likelier reason for a refusal here — so re-read rather than trust
    // the snapshot the failure was computed against. On a version skew that
    // re-read is the whole remedy: it repaints the matrix with what the other
    // admin left, and the message beside it says the tick did not apply. The
    // write is never replayed against the fresh version — that would apply an
    // intent the operator formed against grants they had not seen.
    onError: () => {
      void queryClient.invalidateQueries({ queryKey: ROLES_KEY });
    },
  });
}

// What the two reads answer together. Named because the lead card and the unit
// cards read the same merged result: the lead card renders its pending/error
// surface, the unit cards render the units in it.
type ComposedAccess = Readonly<{
  extensions: readonly ExtensionUnit[];
  roles: readonly ExtensionRole[];
}>;

// One pending/error surface for the two reads the screen needs together: a
// matrix with roles but no unit inventory is not a partial screen, it is a
// different screen. QueryStates takes any QueryLike, so the merge needs no
// react-query lie.
function useExtensionAccess(enabled: boolean): QueryLike<ComposedAccess> {
  const extensions = useExtensions(enabled);
  const roles = useRoles(enabled);
  const queryClient = useQueryClient();
  // `enabled: false` stops the next READ; it does not forget the last one. A seat
  // whose admin role is taken away mid-session keeps rendering from the cache
  // otherwise — the whole inventory and every role's grant on every object, to a
  // reader the server would now refuse. Losing the role evicts what it bought.
  useEffect(() => {
    if (enabled) {
      return;
    }
    queryClient.removeQueries({ queryKey: EXTENSIONS_KEY });
    queryClient.removeQueries({ queryKey: ROLES_KEY });
  }, [enabled, queryClient]);
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

// The lookup is widened to `| undefined` on the way in, because the generated
// index signature cannot say what the contract's prose does: an object a role
// was never granted is ABSENT from the map, and `{[key: string]: Grant}` types
// every key as present. Reading it as the zero grant is the fail-closed
// behaviour /me gets too — without this widening the `??` would look like dead
// code and be deleted, turning a missing key into `undefined.read` at runtime.
function grantOf(role: ExtensionRole, object: string): ObjectGrant {
  const objects: Readonly<Record<string, ObjectGrant | undefined>> =
    role.objects;
  return objects[object] ?? NO_GRANT;
}

export function ExtensionAccessCard() {
  const t = useT();
  const me = useMe();
  // Editing role permissions is admin-only server-side, so the whole card is
  // admin-only here — the same shape UsersAdminCard uses, and for the same
  // reason: an ops seat in the Admin settings group would otherwise be handed
  // controls that only ever 403. The seat ceiling ANDs on top, because a read
  // seat may read this page and may not write anything on it.
  //
  // There is no `useCan` question to ask instead: a `role` RBAC object was
  // considered and declined, because object RBAC narrows who among PEERS may
  // touch a record and no such narrowing exists here — nobody but an admin
  // should hold it, so the grant would be a constant, and an admin who revoked
  // their own would have no way back. The role check is the ratified answer,
  // not a stand-in for one.
  const isAdmin = useHoldsAdminRole();
  const canMutate = useCanMutate();
  const query = useExtensionAccess(isAdmin);
  const composed = query.data;

  return (
    <div className="ext-stack">
      {/* tone="accent" — the one lead on this tab. Everything else here reports
          or manages PEOPLE; this is the only surface in the product that can
          grant an extension's RBAC objects at all, and until somebody does, a
          shipped and enabled unit renders "you do not hold access" for every
          seat. It used to be a bare <div> below the roster, with no card and
          nothing saying it mattered. There is exactly one accent per page and
          this is it. */}
      <Panel tone="accent" title={t("extAccess.title")}>
        <PanelBody>
          <p className="t-caption ext-lead-sub">{t("extAccess.sub")}</p>
          {/* Gate on the role probe itself so the admin-only notice appears only
              once /me has answered — never as a flash while it loads. */}
          <QueryGate query={me}>
            {() =>
              isAdmin ? (
                <InventoryLead query={query} />
              ) : (
                <EmptyState>
                  <p className="t-small">{t("extAccess.adminOnly")}</p>
                </EmptyState>
              )
            }
          </QueryGate>
        </PanelBody>
      </Panel>
      {/* Each unit gets a card of its own, beside the lead card rather than
          inside it: the inventory read's pending and error surfaces belong with
          the heading that names the page, and a unit is a subject in its own
          right. Only reachable with data in hand, which for a non-admin the
          disabled reads never produce. */}
      {/* Gated on the role as well as on the data: `enabled: false` is about the
          next request, and a card rendered from a cache the reader may no longer
          read is the same disclosure the notice above it refuses. */}
      {isAdmin &&
        composed?.extensions.map((unit) => (
          <UnitCard
            key={unit.name}
            unit={unit}
            roles={composed.roles}
            canManage={canMutate}
          />
        ))}
    </div>
  );
}

// The lead card's body: the read's own pending/error surface, plus the sentence
// that speaks for the whole inventory rather than for one unit.
//
// The seat ceiling used to be stated HERE, one card above the controls it
// governs — a reader looking at a row of dead toggles had to leave the card that
// held them to find out why. It moved to the unit card, beside them.
function InventoryLead({
  query,
}: Readonly<{ query: QueryLike<ComposedAccess> }>) {
  const t = useT();
  const units = query.data?.extensions;
  return (
    <QueryStates query={query}>
      {units?.length === 0 ? (
        <EmptyState>
          <p className="t-small">{t("extAccess.empty")}</p>
        </EmptyState>
      ) : null}
    </QueryStates>
  );
}

function UnitCard({
  unit,
  roles,
  canManage,
}: Readonly<{
  unit: ExtensionUnit;
  roles: readonly ExtensionRole[];
  canManage: boolean;
}>) {
  const t = useT();
  // Two sources say which units exist and they can disagree. The inventory
  // above is what /extensions answered — what the RUNNING BINARY composed —
  // while `#/ext/<name>` can only resolve what the SPA's generated registry
  // was built with. A bundle older than the binary lists a unit here whose
  // page App.tsx's ExtensionRoute would answer with its (deliberate, honest)
  // not-found card.
  //
  // So the link is rendered from the registry, never from the API's list. And
  // when the registry misses, the miss is SAID rather than silently omitted:
  // "this unit has no page" and "the page exists but this bundle predates it"
  // are different facts, and an operator hunting a missing screen has to be
  // able to tell them apart — an absent link would leave them debugging the
  // grants below instead of the deploy.
  const page = findExtension(unit.name);
  return (
    <Panel
      className="ext-unit"
      title={unit.name}
      titleAction={
        <div className="ext-unit-actions">
          <Badge>{t("extAccess.version", { version: unit.version })}</Badge>
          {page ? (
            // The unit's name is IN the link text, not only in the heading
            // beside it: several of these sit on one page, and "Open" repeated
            // five times names nothing to anyone reading the links out of
            // context. The hash is built through routeHash and the exported
            // screen token rather than spelled here, so this link and the
            // router cannot drift apart.
            <a
              className="t-small ext-unit-link"
              href={routeHash({ screen: EXTENSION_SCREEN, id: page.name })}
            >
              <ArrowUpRight aria-hidden size={15} />
              {t("extAccess.openUnit", { name: page.name })}
            </a>
          ) : null}
        </div>
      }
    >
      <PanelBody>
        {/* A whole sentence, so it stays in the body where the heading row holds
            only the link it stands in for. */}
        {page ? null : (
          <p className="t-small ext-note ext-unit-nopage">
            <Info aria-hidden size={15} />
            {t("extAccess.noPage", { name: unit.name })}
          </p>
        )}
        {/* One row per registered object, so the thing an operator DECIDES —
            who may do what with this object — sits where every other decision
            in settings sits. The two section headings this replaces named the
            halves of the card; the rows name themselves, and the inventory the
            first heading introduced is reference rather than a decision, so it
            reads last and closed. */}
        <SettingList>
          {/* The seat ceiling, stated in the card that HOLDS the controls it
              governs rather than one card up. Once per unit, not once per
              toggle: every switch below also carries it as its `reason`, which
              is what puts it in the accessibility tree beside the control. */}
          {!canManage && unit.rbac_objects.length > 0 ? (
            <Callout tone="info" className="ext-readonly">
              {t("extAccess.readOnly")}
            </Callout>
          ) : null}
          {unit.rbac_objects.length === 0 ? (
            <p className="t-small ext-note">{t("extAccess.noObjects")}</p>
          ) : (
            unit.rbac_objects.map((object) => (
              <SettingRow
                key={object}
                testId={`ext-object-${object}`}
                // A toggle matrix IS the subject of its row, not an answer that
                // fits beside the question, so it takes the full width below
                // the naming instead of the right column.
                layout="stack"
                label={t("extAccess.matrixCaption", { object })}
                // The function form, because the row's label is now the
                // matrix's accessible NAME: a `<caption>` here would print the
                // same sentence twice, once as the row's naming and once inside
                // the grid it names.
                control={(control) => (
                  <ObjectMatrix
                    object={object}
                    roles={roles}
                    canManage={canManage}
                    labelledBy={control["aria-labelledby"]}
                  />
                )}
              />
            ))
          )}
          {/* What the unit brought, as the card's reference half: an operator
              opens this to check that the object they are granting is the one
              the route they care about is gated on, not to decide anything. */}
          <Disclosure summary={t("extAccess.brings.heading")}>
            <UnitBrings unit={unit} />
          </Disclosure>
        </SettingList>
      </PanelBody>
    </Panel>
  );
}

function UnitBrings({ unit }: Readonly<{ unit: ExtensionUnit }>) {
  const t = useT();
  return (
    <dl className="ext-brings">
      <BringsRow
        icon={<KeyRound aria-hidden size={15} />}
        label={t("extAccess.brings.objects")}
        items={unit.rbac_objects.map((object) => ({
          id: object,
          content: object,
        }))}
      />
      <BringsRow
        icon={<Route aria-hidden size={15} />}
        label={t("extAccess.brings.routes")}
        // Keyed by method AND path: two operations on one path are two
        // entries, and keying by path alone would collapse the pair React
        // has to keep apart — the same collapse the server stopped doing.
        items={unit.routes.map((route) => ({
          id: `${route.method} ${route.path}`,
          content: (
            <>
              <span className="ext-method">{route.method}</span>
              <span className="ext-route-path">{route.path}</span>
            </>
          ),
        }))}
      />
      <BringsRow
        icon={<Timer aria-hidden size={15} />}
        label={t("extAccess.brings.jobs")}
        items={unit.jobs.map((job) => ({ id: job, content: job }))}
      />
    </dl>
  );
}

function BringsRow({
  icon,
  label,
  items,
}: Readonly<{
  icon: ReactNode;
  label: string;
  // A node per chip rather than a string: a route reads as a method and a path,
  // two pieces with different weight, and the row that lists objects and the
  // row that lists routes are otherwise the same row.
  items: readonly { id: string; content: ReactNode }[];
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
              <li key={item.id} className="ext-chip t-mono">
                {item.content}
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
 * is what answers that, and `labelledBy` names which object the whole grid is
 * about — a table announced as "read create update delete" with no subject is
 * unreadable however many ticks it contains. The name is the row's own label
 * rather than a `<caption>`, because the stacked `SettingRow` already draws
 * that sentence directly above the grid and a caption would repeat it.
 *
 * Every cell is a `Switch`, not a `Checkbox`, and the difference is the whole
 * point of this grid: flipping one WRITES an RBAC grant. A checkbox states an
 * intent something later submits, so a reader who was told "checkbox" has been
 * told their next click is safe until they press Save — and there is no Save.
 * `role="switch"` is what says the click IS the change.
 *
 * Each carries its own full sentence as its accessible name ("Allow Rep to read
 * ext_notes_note"), because the header association alone is a hint, not a name:
 * a control has to be identifiable out of context, and it is the only thing a
 * user hears when tabbing straight to it. The name is `labelHidden`, so the
 * visible cell is the track alone.
 *
 * A seat that may not write gets the reason as the switch's own `reason`, which
 * `aria-describedby` attaches to the control — the block above states it once
 * for the eye, this states it to every reader who lands on a single cell.
 */
function ObjectMatrix({
  object,
  roles,
  canManage,
  labelledBy,
}: Readonly<{
  object: string;
  roles: readonly ExtensionRole[];
  canManage: boolean;
  /** The id of the row's label, which is this grid's accessible name. */
  labelledBy: string;
}>) {
  const t = useT();
  const setGrant = useSetGrant();
  // The same reading of a 409 the record edit form makes, through the same
  // helper: only a ProblemError carries a server code, so a rejected fetch can
  // never be mistaken for a concurrent edit.
  const skew =
    setGrant.error instanceof ProblemError &&
    isVersionSkew(setGrant.error.problem);
  // The whole point of the screen: an object no role can read is an extension
  // whose every screen renders "you do not hold access", and that is invisible
  // from anywhere else in the product. Said plainly, next to the toggles that
  // fix it.
  const nobodyReads = roles.every((role) => !grantOf(role, object).read);

  return (
    // `.settingrow-measure` is the stacked row's own contract for a control
    // that owns its width: `.settingrow-control` is a flex row, so without the
    // `min-width: 0` it carries, the matrix's scroll box would grow to the
    // grid's full width and push the card sideways. The inset panel this
    // replaces was separating one object's grants from the next; the
    // `SettingList` hairline between the rows does that now, and two edges
    // around one grid read as a box inside a box.
    <div className="settingrow-measure ext-object">
      {/* `TableScroll` rather than a wrapper of this screen's own: five CRUD
          columns beside a role name overrun a 720px settings column, and the
          box that scrolls sideways — keyboard-reachable, announced — is one
          spelling for every table in the product (atoms.tsx). Named by the
          object rather than by a translated phrase: the object IS the grid's
          subject, which is the same thing `labelledBy` says to the table. */}
      <TableScroll label={object}>
        <table className="ext-matrix" aria-labelledby={labelledBy}>
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
                    <td key={action} className="ext-cell">
                      <Switch
                        labelHidden
                        label={t("extAccess.cell", {
                          role: role.name,
                          action: t(`extAccess.action.${action}`),
                          object,
                        })}
                        checked={grant[action]}
                        disabled={!canManage}
                        // Scoped to the role whose grant is actually being
                        // written. `setGrant` is one mutation for the whole
                        // matrix, so a bare `isPending` set every cell for
                        // every role turning and announcing itself — a claim
                        // about writes nobody made. Role granularity rather
                        // than cell, because the write carries the role's WHOLE
                        // grant record: every action in that row really is in
                        // flight.
                        pending={
                          setGrant.isPending &&
                          setGrant.variables?.roleKey === role.key
                        }
                        // Only the PERMISSION denial gets a reason. A write in
                        // flight is the other way this is disabled, and it
                        // wants no words at all — a sentence that appeared for
                        // 200ms on every toggle would be noise, not an
                        // explanation.
                        reason={canManage ? undefined : t("extAccess.readOnly")}
                        onChange={(next) =>
                          setGrant.mutate({
                            roleKey: role.key,
                            object,
                            grant: { ...grant, [action]: next },
                            // The version of the role THIS cell was read from,
                            // not a version fetched at write time: that is the
                            // whole guarantee — the server compares against
                            // what the operator was looking at.
                            version: role.version,
                          })
                        }
                      />
                    </td>
                  ))}
                </tr>
              );
            })}
          </tbody>
        </table>
      </TableScroll>
      {nobodyReads ? (
        // `warn` is exactly the claim: nothing is broken, and something will go
        // wrong if nobody acts — every screen this unit ships renders "you do
        // not hold access" until a read grant exists. live="status" because the
        // sentence appears and disappears as the last read grant is toggled,
        // and a change nobody is told about is the silence this screen exists
        // to break.
        <Callout
          tone="warn"
          live="status"
          icon={AlertTriangle}
          className="ext-matrix-note"
        >
          {t("extAccess.nobodyReads", { object })}
        </Callout>
      ) : null}
      {setGrant.isError ? (
        <Callout tone="danger" live="alert" className="ext-matrix-note">
          {/* A version skew is not a failure to phrase generically: the
              operator's flip did not apply, someone else's did, and the matrix
              above has just been repainted with theirs. Saying "couldn't save"
              there would leave them staring at a grid that silently changed
              under them. Every other refusal keeps the server's own words. */}
          {skew
            ? t("extAccess.versionSkew")
            : problemMessageOf(setGrant.error, t)}
        </Callout>
      ) : null}
    </div>
  );
}
