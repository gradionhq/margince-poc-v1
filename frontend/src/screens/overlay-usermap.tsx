// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import {
  type QueryClient,
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { useCallback, useEffect, useId, useMemo, useState } from "react";
import { api, FIRST_PAGE } from "../api/client";
import type { components } from "../api/schema";
import { useCan, useCanWrite } from "../app/capability";
import {
  Badge,
  Button,
  EmptyState,
  Modal,
  SegmentedControl,
} from "../design-system/atoms";
import { Callout } from "../design-system/callout";
import { ConfirmModal } from "../design-system/confirmmodal";
// The rows here stay a real <ul>/<li>: they are a LIST of workspace users, and
// PanelRow draws a <div>. A full-bleed row that costs a screen reader the list
// semantics — how many people, which one of them this is — is a worse trade on
// this card than on a roster of unrelated panels.
import { Panel, PanelBody } from "../design-system/panel";
import {
  RecordPicker,
  type RecordPickerCandidate,
} from "../design-system/recordpicker";
import { SettingList, SettingRow } from "../design-system/settingrow";
import { useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import {
  LoadMoreButton,
  problemCodeOf,
  problemMessageOf,
  throwProblem,
  useMe,
  useSorMode,
} from "./common";
import "./overlay-usermap.css";

// The mirror user-map card (Settings → Integrations): who, in the
// incumbent CRM, each workspace user IS. That mapping is the whole of a
// user's mirror visibility — an unmapped seat sees no mirrored records at
// all, so an unmapped row is a broken account, not a cosmetic gap, and this
// card exists to name it and fix it.
//
// Every line it prints is a server fact. The unmapped REASON is derived
// server-side (usermapservice.go) and rendered verbatim in intent: a
// directory the server could not read answers `directory_unavailable`, which
// this card renders as "we couldn't look", never as "there is no match" —
// those are different findings and only one of them is actionable. The
// incumbent's own noun is looked up from the id the server reports, so a
// Salesforce or Dynamics connection needs a catalog key here and no code
// change at all.
//
// Structure mirrors the neighbouring overlay.tsx: one Card, one SectionHeader,
// server-fact rows with Badge chips, ConfirmModal for anything destructive.

type Entry = components["schemas"]["OverlayUserMapEntry"];
type Owner = components["schemas"]["OverlayOwner"];
type UnmappedReason = Entry["unmapped_reason"];

const VIEWS = ["user", "owner"] as const;
type View = (typeof VIEWS)[number];

// The row and list geometry lives in overlay-usermap.css, not in frozen style
// objects here: five of them said five different things about whether a row
// wraps, and the two that said "never" are what made this card the widest thing
// on a 390px screen.

// The incumbent's own noun, keyed on the id the SERVER reports rather than
// baked into the copy. An incumbent this build has no key for renders the
// generic noun: a wrong brand name is worse than a neutral one.
const PRINCIPAL_LABEL: Partial<Record<string, MessageKey>> = {
  hubspot: "overlay.userMap.principal.hubspot",
};

// Keyed on the contract enum so a reason added upstream is a compile error
// here until this map catches up. `Partial` keeps the gap honest in the type:
// the server is a separately-deployed process and can still send a reason
// this build never saw, which reasonChip renders as the server's own token
// rather than blank — the same rule overlay-health's labelOrRaw follows.
const REASON_CHIP: Partial<Record<UnmappedReason, MessageKey>> = {
  // `none` on an entry with no incumbent user is a contradiction only the
  // server could produce; the row still needs a truthful chip.
  none: "overlay.userMap.notMapped",
  no_email_match: "overlay.userMap.chip.noEmailMatch",
  ambiguous_email: "overlay.userMap.chip.ambiguousEmail",
  blocked_by_admin: "overlay.userMap.chip.blockedByAdmin",
  not_yet_synced: "overlay.userMap.chip.notYetSynced",
  directory_unavailable: "overlay.userMap.chip.directoryUnavailable",
};

const REASON_NOTE: Partial<Record<UnmappedReason, MessageKey>> = {
  no_email_match: "overlay.userMap.reason.noEmailMatch",
  ambiguous_email: "overlay.userMap.reason.ambiguousEmail",
  blocked_by_admin: "overlay.userMap.reason.blockedByAdmin",
  not_yet_synced: "overlay.userMap.reason.notYetSynced",
  directory_unavailable: "overlay.userMap.reason.directoryUnavailable",
};

type Translate = ReturnType<typeof useT>;

function principalNoun(t: Translate, incumbent: string): string {
  const key = PRINCIPAL_LABEL[incumbent];
  return key ? t(key) : t("overlay.userMap.principal.generic");
}

function reasonChip(
  t: Translate,
  reason: UnmappedReason,
  principal: string,
): string {
  const key = REASON_CHIP[reason];
  return key ? t(key, { principal }) : reason;
}

// An explanation exists only where this build has one to give; a reason it
// does not recognize gets the raw chip above and no invented prose.
function reasonNote(
  t: Translate,
  reason: UnmappedReason,
  principal: string,
): string | null {
  const key = REASON_NOTE[reason];
  return key ? t(key, { principal }) : null;
}

// "Ada Lovelace · ada@acme.test" when the incumbent reports both, and
// whichever half it does report otherwise — a directory entry with no name is
// common and must not render as "undefined ·".
function identityLabel(
  name: string | undefined,
  email: string | undefined,
  fallback: string,
): string {
  if (name && email) {
    return `${name} · ${email}`;
  }
  return email || name || fallback;
}

function isMapped(entry: Entry): boolean {
  return (entry.incumbent_user_id ?? "") !== "";
}

// Changing YOUR OWN mapping changes which mirrored records this session can
// see at all, so every cached read becomes suspect — not just this card's.
// Changing someone else's only moves rows in this card.
function invalidateMapping(
  queryClient: QueryClient,
  meId: string | undefined,
  userId: string,
): void {
  if (userId === meId) {
    queryClient.invalidateQueries();
    return;
  }
  queryClient.invalidateQueries({ queryKey: ["overlay"] });
}

// What the directory read produced, threaded to every row that can act on it.
// `failure` is the server's own message when the directory could not be read:
// the picker refuses rather than offering an empty list that would read as
// "this owner does not exist".
type DirectoryState = Readonly<{
  incumbent: string;
  principal: string;
  owners: Owner[];
  truncated: boolean;
  failure: string | null;
}>;

// The mutating half, owned by the card and passed down so a row never holds a
// query client of its own.
//
// `busy` covers BOTH writes, not just the one a given control starts. A PUT and
// a DELETE for the same user are the opposite decision about their whole CRM,
// and nothing orders two in-flight requests: the second to be sent can be the
// first to land, leaving a user mapped after the admin confirmed Unmap. Only
// one mapping write may be outstanding, so every affordance that starts one
// goes inert until it settles.
type MappingActions = Readonly<{
  // The listing and the writes are different questions. A read seat may be
  // entitled to SEE the map (the server gates that on the update grant because
  // the rows carry the incumbent's directory) while the seat ceiling still
  // refuses every PUT and DELETE — so the rows render and their controls do not.
  canMap: boolean;
  picking: string | null;
  onStartPick: (userId: string) => void;
  onCancelPick: () => void;
  onPick: (userId: string, incumbentUserId: string) => void;
  onUnmapRequest: (entry: Entry) => void;
  busy: boolean;
  saveError: string | null;
}>;

// Choosing who somebody IS over there, in the dialog that decision has always
// been in substance: a search over the incumbent's directory, and a commit.
//
// It used to open INSIDE the row, which is what the row language this card now
// speaks does not have room for — a row states one answer and the verb that
// changes it, and a search field with its own candidate list, its own truncation
// caveat and its own failure state is not an answer. As a dialog it also stops
// competing with the roster for width, which is what made this the widest thing
// on a phone.
//
// One instance, owned by the card, mounted only while a row is picking — so it
// cannot carry one person's typed query or half-loaded candidate list onto the
// next person's decision.
function OwnerPickerModal({
  directory,
  actions,
}: Readonly<{ directory: DirectoryState; actions: MappingActions }>) {
  const t = useT();
  const headingId = useId();
  const { owners, principal, failure, truncated } = directory;
  const userId = actions.picking;
  // RecordPicker re-runs its debounced search whenever this callback's
  // identity changes, so it has to be stable — `owners` is memoized upstream
  // for exactly this reason. The directory is one fetch and there is no
  // owner-search endpoint, so the filter runs over what is already loaded.
  const searchOwners = useCallback(
    async (query: string): Promise<RecordPickerCandidate[]> => {
      const needle = query.toLowerCase();
      return owners
        .filter((owner) =>
          `${owner.name ?? ""} ${owner.email}`.toLowerCase().includes(needle),
        )
        .map((owner) => ({
          id: owner.incumbent_user_id,
          name: identityLabel(owner.name, owner.email, owner.incumbent_user_id),
        }));
    },
    [owners],
  );
  if (userId === null) {
    return null;
  }
  return (
    <Modal open onClose={actions.onCancelPick} labelledBy={headingId}>
      <h2 id={headingId} className="t-h2 usermap-pick-title">
        {t("overlay.userMap.pickTitle", { principal })}
      </h2>
      {failure ? (
        <Callout tone="danger" live="alert">
          {failure}
        </Callout>
      ) : (
        <RecordPicker
          label={t("overlay.userMap.pickerLabel", { principal })}
          searchTargets={searchOwners}
          disabled={actions.busy}
          onPick={(candidate) => actions.onPick(userId, candidate.id)}
        />
      )}
      {/* A silently short list reads as "this owner does not exist", which is
          the one wrong conclusion an admin must not draw here. */}
      {truncated && (
        <p className="t-caption">
          {t("overlay.userMap.truncated", { principal })}
        </p>
      )}
      {actions.saveError && (
        <Callout tone="danger" live="alert">
          {actions.saveError}
        </Callout>
      )}
      {/* Cancel alone: picking a candidate IS the commit, so there is no second
          confirm to draw beside it. `.actions` is the shared dialog action band
          every other modal in this tree ends with. */}
      <div className="actions">
        <Button small onClick={actions.onCancelPick} disabled={actions.busy}>
          {t("overlay.userMap.cancel")}
        </Button>
      </div>
    </Modal>
  );
}

// The per-row explanation lines. Split out of UserRow so that function stays
// inside the cognitive-complexity gate.
function RowNotes({
  entry,
  principal,
}: Readonly<{ entry: Entry; principal: string }>) {
  const t = useT();
  const note = isMapped(entry)
    ? null
    : reasonNote(t, entry.unmapped_reason, principal);
  return (
    <>
      {note && <p className="t-caption">{note}</p>}
      {entry.stale_owner_ref && (
        <p className="t-caption">
          {t("overlay.userMap.staleNote", { principal })}
        </p>
      )}
    </>
  );
}

function UserRow({
  entry,
  self,
  directory,
  actions,
}: Readonly<{
  entry: Entry;
  self: boolean;
  directory: DirectoryState;
  actions: MappingActions;
}>) {
  const t = useT();
  const { principal } = directory;
  const mapped = isMapped(entry);
  return (
    // Two columns while both fit, one stack when they do not: a long identity +
    // chip run must not push the actions onto their own right-aligned row,
    // stranded above the explanation they belong to — and the actions must not
    // hold their full nowrap width against an identity squeezed to nothing,
    // which is what a non-wrapping row and a `flex: none` actions column did to
    // every name on a phone.
    <li className="usermap-row">
      <div className="usermap-identity">
        <div className="usermap-facts">
          <span className="usermap-email">{entry.name ?? entry.email}</span>
          {entry.name && (
            <span className="t-small usermap-email">{entry.email}</span>
          )}
          {self && <Badge tone="accent">{t("overlay.userMap.you")}</Badge>}
          {mapped ? (
            <span className="t-small">
              {identityLabel(
                entry.incumbent_user_name,
                entry.incumbent_user_email,
                entry.incumbent_user_id ?? "",
              )}
            </span>
          ) : (
            <Badge tone="warn">
              {reasonChip(t, entry.unmapped_reason, principal)}
            </Badge>
          )}
          {mapped && entry.match_source && (
            <Badge>
              {t(
                entry.match_source === "manual"
                  ? "overlay.userMap.matchManual"
                  : "overlay.userMap.matchEmail",
              )}
            </Badge>
          )}
          {entry.stale_owner_ref && (
            <Badge tone="danger">
              {t("overlay.userMap.staleChip", { principal })}
            </Badge>
          )}
        </div>
        <RowNotes entry={entry} principal={principal} />
      </div>
      {actions.canMap && (
        <div className="usermap-actions">
          <Button
            small
            disabled={actions.busy}
            onClick={() => actions.onStartPick(entry.user_id)}
          >
            {t(mapped ? "overlay.userMap.change" : "overlay.userMap.map")}
          </Button>
          {mapped && (
            <Button
              small
              variant="danger"
              disabled={actions.busy}
              onClick={() => actions.onUnmapRequest(entry)}
            >
              {t("overlay.userMap.unmap")}
            </Button>
          )}
        </div>
      )}
    </li>
  );
}

// One incumbent user and the workspace users pointing at them.
type OwnerGroup = {
  incumbentUserId: string;
  name?: string;
  email?: string;
  users: Entry[];
  missingFromDirectory: boolean;
};

// The by-owner grouping, derived from the SAME two reads — there is no
// third endpoint. It exists because a shared seat (several workspace users
// pointing at ONE incumbent user, which the incumbent permits; the reverse
// never happens) is invisible in the by-user list, where each of those rows
// looks perfectly correct on its own.
//
// Only owners with at least one mapped user are listed: a directory of
// hundreds of never-mapped owners would bury the very thing this view exists
// to show. `missingFromDirectory` is claimed only when the directory was
// genuinely read — a failed read must not accuse a live owner of being gone.
function ownerGroups(
  entries: Entry[],
  directory: Owner[] | null,
): OwnerGroup[] {
  const known = new Map<string, Owner>();
  for (const owner of directory ?? []) {
    known.set(owner.incumbent_user_id, owner);
  }
  const groups = new Map<string, OwnerGroup>();
  for (const entry of entries) {
    const id = entry.incumbent_user_id ?? "";
    if (id === "") {
      continue;
    }
    const existing = groups.get(id);
    if (existing) {
      existing.users.push(entry);
      continue;
    }
    const owner = known.get(id);
    groups.set(id, {
      incumbentUserId: id,
      name: owner?.name ?? entry.incumbent_user_name,
      email: owner?.email ?? entry.incumbent_user_email,
      users: [entry],
      missingFromDirectory: directory !== null && !known.has(id),
    });
  }
  // Shared seats first — they are the finding this view was opened for.
  return [...groups.values()].sort(
    (a, b) =>
      b.users.length - a.users.length ||
      identityLabel(a.name, a.email, a.incumbentUserId).localeCompare(
        identityLabel(b.name, b.email, b.incumbentUserId),
      ),
  );
}

function ByOwnerList({
  entries,
  directory,
  partial,
}: Readonly<{
  entries: Entry[];
  directory: DirectoryState;
  partial: boolean;
}>) {
  const t = useT();
  const { principal } = directory;
  const groups = ownerGroups(
    entries,
    directory.failure === null ? directory.owners : null,
  );
  const unmapped = entries.filter((entry) => !isMapped(entry)).length;
  if (groups.length === 0) {
    return (
      <EmptyState>
        <p>{t("overlay.userMap.ownerEmpty", { principal })}</p>
        {/* "Nobody is mapped" over a partially-loaded list is the same false
            census as an under-count, and a worse one to act on. */}
        {partial && (
          <p className="t-caption">{t("overlay.userMap.partialView")}</p>
        )}
      </EmptyState>
    );
  }
  return (
    <>
      <ul className="usermap-list arrive">
        {groups.map((group) => (
          <li key={group.incumbentUserId}>
            <div className="usermap-facts">
              <span>
                {identityLabel(group.name, group.email, group.incumbentUserId)}
              </span>
              {group.users.length > 1 && (
                <Badge tone="warn">
                  {t("overlay.userMap.sharedSeat", {
                    count: group.users.length,
                  })}
                </Badge>
              )}
              {group.missingFromDirectory && (
                <Badge tone="danger">
                  {t("overlay.userMap.staleChip", { principal })}
                </Badge>
              )}
            </div>
            <ul className="usermap-group-users">
              {group.users.map((user) => (
                <li key={user.user_id} className="t-small">
                  {identityLabel(user.name, user.email, user.user_id)}
                </li>
              ))}
            </ul>
          </li>
        ))}
      </ul>
      {/* This view can only show users who HAVE an owner, so it has to say
          how many it is leaving out rather than read as a full census. */}
      {unmapped > 0 && (
        <p className="t-caption usermap-note">
          {t(
            unmapped === 1
              ? "overlay.userMap.unmappedCountOne"
              : "overlay.userMap.unmappedCount",
            { count: unmapped },
          )}
        </p>
      )}
      {/* Both the grouping and that count are computed over the pages loaded
          so far, so with a page still unread they under-report — a shared seat
          split across pages looks like a solo one. Saying the scope out loud is
          the honest fix; silently counting part of the workspace is not. */}
      {partial && (
        <p className="t-caption usermap-note">
          {t("overlay.userMap.partialView")}
        </p>
      )}
    </>
  );
}

function UserMapBody({
  entries,
  directory,
  meId,
  view,
  onView,
  actions,
  partial,
}: Readonly<{
  entries: Entry[];
  directory: DirectoryState;
  meId: string | undefined;
  view: View;
  onView: (next: View) => void;
  actions: MappingActions;
  partial: boolean;
}>) {
  const t = useT();
  const { principal } = directory;
  if (entries.length === 0) {
    return (
      <EmptyState>
        <p>{t("overlay.userMap.empty")}</p>
      </EmptyState>
    );
  }
  return (
    <>
      {/* The cost of the read this card performs, at the weight a standing
          advisory earns. It was the quietest type on the card — 12px meta grey,
          under everything else — which is the one thing a disclosure about
          somebody else's money must not be.
          Outside the list on purpose: it is not a decision, and a row with no
          answer on its right reads as a setting nobody has set. */}
      <Callout tone="info">{t("overlay.userMap.cost")}</Callout>
      <SettingList>
        {/* Which census, on the row language's own line: one control, one
            answer, in the right column with every other answer on the page.
            SegmentedControl owns its own label, so it is passed as a node
            rather than through the row's ARIA — naming it twice would have a
            screen reader read "Grouping Grouping". */}
        <SettingRow
          label={t("overlay.userMap.view")}
          control={
            <SegmentedControl
              options={VIEWS}
              value={view}
              onChange={onView}
              label={t("overlay.userMap.view")}
              labels={{
                user: t("overlay.userMap.viewByUser"),
                owner: t("overlay.userMap.viewByOwner", { principal }),
              }}
            />
          }
        />
        {/* The roster is the SUBJECT of this card rather than an answer to a
            question beside it, so it takes the full width below its naming.
            The label says which census is on screen, which is the one thing a
            reader scrolled past the toggle can no longer see. */}
        <SettingRow
          label={t(
            view === "user"
              ? "overlay.userMap.viewByUser"
              : "overlay.userMap.viewByOwner",
            { principal },
          )}
          layout="stack"
          control={
            <div className="usermap-stack">
              {view === "user" ? (
                <ul className="usermap-list arrive">
                  {entries.map((entry) => (
                    <UserRow
                      key={entry.user_id}
                      entry={entry}
                      self={entry.user_id === meId}
                      directory={directory}
                      actions={actions}
                    />
                  ))}
                </ul>
              ) : (
                <ByOwnerList
                  entries={entries}
                  directory={directory}
                  partial={partial}
                />
              )}
            </div>
          }
        />
      </SettingList>
    </>
  );
}

export function MirrorUserMapCard() {
  const t = useT();
  const me = useMe();
  const meId = me.data?.user.id;
  // The LISTING is gated on update, not read: a mirror user map exposes the
  // incumbent's directory — names and addresses of people who never consented
  // to appear here — so the server demands the write grant merely to look
  // (overlay/usermapservice.go). Both queries below are gated on it for that
  // reason, not as a convenience.
  const canManage = useCan("overlay_connection", "update");
  // Losing the grant must EVICT the rows, not just stop refetching them: the
  // query keeps its last successful data, and this card's data is PII the
  // principal is no longer entitled to see. The render is gated on canManage
  // above for the same reason — belt and braces, because one of the two is a
  // cache-eviction race and the other is not.
  // Seeing the map is a read the server gates on the update grant; changing a
  // mapping is a real write, so it also needs the seat.
  const canMap = useCanWrite("overlay_connection", "update");
  // There is nothing to map until the workspace actually READS from a mirror.
  // Both endpoints below answer 404 `mode_not_overlay` on a native
  // installation — which is every installation until somebody connects an
  // incumbent — so an admin opening this tab used to spend two round trips
  // provoking a refusal the card then had to explain. The mode is already on
  // /me, so asking it costs nothing and the card says the same sentence
  // without the two requests.
  //
  // This is the user MAP, not the overlay connection itself: the neighbouring
  // OverlayCard is deliberately NOT mode-gated, because it is the only surface
  // that can connect one. Gating that would hide the way in; gating this hides
  // nothing, because a workspace with no mirror has no owners to point at.
  const overlay = useSorMode() === "overlay";
  const readable = canManage && overlay;
  const queryClient = useQueryClient();
  // Evicting, not merely hiding: react-query keeps the last successful page
  // indefinitely, so a principal who loses the grant would otherwise be one
  // re-render away from the directory again.
  useEffect(() => {
    if (!canManage) {
      queryClient.removeQueries({ queryKey: ["overlay", "user-map"] });
      queryClient.removeQueries({ queryKey: ["overlay", "owners"] });
    }
  }, [canManage, queryClient]);
  const [view, setView] = useState<View>("user");
  const [picking, setPicking] = useState<string | null>(null);
  const [unmapping, setUnmapping] = useState<Entry | null>(null);
  // Evicting the queries is not enough on its own: `unmapping` holds a COPY of
  // a row and `picking` drives the owner directory, so either could keep the
  // incumbent's names and addresses on screen after the grant that admitted
  // them was withdrawn. Clear both with the capability.
  useEffect(() => {
    if (!canMap) {
      setPicking(null);
      setUnmapping(null);
    }
  }, [canMap]);

  // Reads are admin/ops-only on the server (usermapservice.go's
  // requireUserMapAdmin): a rep's fetch could only 403, so it is never sent.
  const page = useInfiniteQuery({
    queryKey: ["overlay", "user-map"],
    enabled: readable,
    initialPageParam: FIRST_PAGE,
    queryFn: async ({ pageParam }) => {
      const { data, error } = await api.GET("/overlay/user-map", {
        params: { query: { cursor: pageParam ?? undefined } },
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
    getNextPageParam: (last) => last.next_cursor ?? null,
  });

  const directoryQuery = useQuery({
    queryKey: ["overlay", "owners"],
    enabled: readable,
    queryFn: async () => {
      const { data, error } = await api.GET("/overlay/owners", {});
      if (error) {
        throwProblem(error);
      }
      return data;
    },
  });

  const invalidateAfterMapping = useCallback(
    (userId: string) => invalidateMapping(queryClient, meId, userId),
    [queryClient, meId],
  );

  const setMapping = useMutation({
    mutationFn: async (input: { userId: string; incumbentUserId: string }) => {
      const { error } = await api.PUT("/overlay/user-map/{id}", {
        params: { path: { id: input.userId } },
        body: { incumbent_user_id: input.incumbentUserId },
      });
      if (error) {
        throwProblem(error);
      }
      return input.userId;
    },
    onSuccess: (userId) => {
      setPicking(null);
      invalidateAfterMapping(userId);
    },
  });

  const unmap = useMutation({
    mutationFn: async (userId: string) => {
      const { error } = await api.DELETE("/overlay/user-map/{id}", {
        params: { path: { id: userId } },
      });
      if (error) {
        throwProblem(error);
      }
      return userId;
    },
    onSuccess: (userId) => {
      setUnmapping(null);
      invalidateAfterMapping(userId);
    },
  });

  // A directory read failure degrades the picker; it never fails the page.
  // The server already downgrades every reason that needed the directory to
  // `directory_unavailable` in that case — the ones it holds locally, such as
  // an admin's block, still say what they are — so the rows stay honest and the
  // picker just has nothing truthful to offer, and says so.
  const owners = useMemo(
    () => directoryQuery.data?.owners ?? [],
    [directoryQuery.data],
  );
  const incumbent =
    page.data?.pages[0]?.incumbent ?? directoryQuery.data?.incumbent ?? "";
  const principal = principalNoun(t, incumbent);
  const directory: DirectoryState = {
    incumbent,
    principal,
    owners,
    truncated: directoryQuery.data?.truncated ?? false,
    failure: directoryQuery.isError
      ? problemMessageOf(
          directoryQuery.error,
          t,
          t("overlay.userMap.directoryFailed", { principal }),
        )
      : null,
  };

  const busy = setMapping.isPending || unmap.isPending;

  // Every dialog open and close clears the previous attempt's failure. A
  // mutation error outlives the dialog it happened in, so without this the
  // next row's picker — or the confirm for a different person — opens already
  // showing a refusal that was never about them.
  const actions: MappingActions = {
    canMap,
    picking,
    onStartPick: (userId) => {
      setMapping.reset();
      setPicking(userId);
    },
    onCancelPick: () => {
      setMapping.reset();
      setPicking(null);
    },
    // Re-read at the write, not at the render that offered it: an open picker
    // outlives a grant withdrawn by the next /me refetch.
    onPick: (userId, incumbentUserId) => {
      if (!canMap) {
        return;
      }
      setMapping.mutate({ userId, incumbentUserId });
    },
    onUnmapRequest: (entry) => {
      unmap.reset();
      setUnmapping(entry);
    },
    busy,
    saveError: setMapping.isError
      ? problemMessageOf(setMapping.error, t)
      : null,
  };

  return (
    <Panel title={t("overlay.userMap.title")}>
      <PanelBody>
        <p className="settings-panel-sub">
          {t("overlay.userMap.sub", { principal: directory.principal })}
        </p>
        <UserMapNotice
          canManage={canManage}
          overlay={overlay}
          rolesKnown={!me.isPending}
          pending={page.isPending}
          failed={page.isError}
          error={page.error}
        />
        {/* One body, not two stacked: the panel's inner interval is paid once,
            so the table opens under the sentence that introduces it rather than
            across a gap that reads as a missing element. */}
        {canManage && page.isSuccess && (
          <UserMapBody
            entries={page.data.pages.flatMap((one) => one.entries)}
            directory={directory}
            meId={meId}
            view={view}
            onView={setView}
            actions={actions}
            partial={page.hasNextPage}
          />
        )}
        {canManage && page.isSuccess && <LoadMoreButton query={page} />}
      </PanelBody>
      {/* Both dialogs hang off the card rather than off a row, and both are
          gated on the grant a second time: `picking` and `unmapping` are already
          cleared when it drops, and this is the belt to that braces — a dialog
          holding the incumbent's directory must not be one stale render away
          from a reader who is no longer entitled to it. */}
      {canManage && (
        <OwnerPickerModal directory={directory} actions={actions} />
      )}
      <UnmapConfirm
        entry={canManage ? unmapping : null}
        self={unmapping?.user_id === meId}
        pending={busy}
        error={unmap.isError ? problemMessageOf(unmap.error, t) : null}
        onClose={() => {
          unmap.reset();
          setUnmapping(null);
        }}
        onConfirm={(userId) => {
          if (!canMap) {
            return;
          }
          unmap.mutate(userId);
        }}
      />
    </Panel>
  );
}

// Everything the card shows INSTEAD of the mapping table. The two calm states
// are not failures and must not read as ones: a 501 means this deployment
// never wired overlay mode, and a workspace reading from native tables has
// nothing to map. Anything else is a real failure and keeps the server's own
// detail.
//
// The native case is answered from /me rather than from a 404 the card had to
// provoke. `mode_not_overlay` is still recognised below, because /me and this
// endpoint are two reads of a workspace that can be flipped between them.
function UserMapNotice({
  canManage,
  overlay,
  rolesKnown,
  pending,
  failed,
  error,
}: Readonly<{
  canManage: boolean;
  overlay: boolean;
  rolesKnown: boolean;
  pending: boolean;
  failed: boolean;
  error: unknown;
}>) {
  const t = useT();
  if (!rolesKnown) {
    return null;
  }
  if (!canManage) {
    return <p className="t-small">{t("overlay.userMap.adminOnly")}</p>;
  }
  const code = problemCodeOf(error);
  if (!overlay || code === "mode_not_overlay") {
    return (
      <EmptyState>
        <p>{t("overlay.userMap.notOverlay")}</p>
      </EmptyState>
    );
  }
  if (code === "not_implemented") {
    return (
      <EmptyState>
        <p>{t("overlay.userMap.notConfigured")}</p>
      </EmptyState>
    );
  }
  if (failed) {
    return (
      <Callout tone="danger" live="alert">
        {problemMessageOf(error, t, t("overlay.userMap.loadFailed"))}
      </Callout>
    );
  }
  if (pending) {
    return <p className="t-small">{t("overlay.userMap.loading")}</p>;
  }
  return null;
}

// Unmapping blanks somebody's CRM, so it is confirm-first either way — but
// unmapping YOURSELF is the case that surprises, and it gets copy that says
// so. It is survivable (this tab stays reachable and you can map yourself
// back), which is why it is a confirm and not a block.
function UnmapConfirm({
  entry,
  self,
  pending,
  error,
  onClose,
  onConfirm,
}: Readonly<{
  entry: Entry | null;
  self: boolean;
  pending: boolean;
  error: string | null;
  onClose: () => void;
  onConfirm: (userId: string) => void;
}>) {
  const t = useT();
  return (
    <ConfirmModal
      open={entry !== null}
      onClose={onClose}
      title={t(
        self ? "overlay.userMap.unmapSelfTitle" : "overlay.userMap.unmapTitle",
      )}
      confirmLabel={t("overlay.userMap.unmap")}
      confirmVariant="danger"
      pending={pending}
      error={error}
      onConfirm={() => {
        if (entry) {
          onConfirm(entry.user_id);
        }
      }}
    >
      <p className="t-small">
        {self
          ? t("overlay.userMap.unmapSelfBody")
          : t("overlay.userMap.unmapBody", {
              user: entry?.name ?? entry?.email ?? "",
            })}
      </p>
    </ConfirmModal>
  );
}
