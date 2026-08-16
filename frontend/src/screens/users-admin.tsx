import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { UserPlus } from "lucide-react";
import { useRef, useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { Badge, Button, EmptyState, TextInput } from "../design-system/atoms";
import { Callout } from "../design-system/callout";
import { ConfirmModal } from "../design-system/confirmmodal";
import { Panel, PanelBody } from "../design-system/panel";
import { Select, type SelectOption } from "../design-system/select";
import { useT } from "../i18n";
import { problemMessageOf, QueryGate, throwProblem, useMe } from "./common";
import "./users-admin.css";
import { useHoldsAdminRole } from "../app/capability";
import { isOption } from "../app/options";
import { PasswordLinkModal, usePasswordLink } from "./users-password-link";

type User = components["schemas"]["User"];
type Role = components["schemas"]["ChangeUserRoleRequest"]["role"];

// Wire keys, not product names: `manager` shows as Team Lead, `rep` as Member
// (ADR-0110). The catalog carries the display names.
const ROLES: readonly Role[] = [
  "admin",
  "management",
  "manager",
  "rep",
  "read_only",
  "ops",
];

// roleLabel names a held role key. The catalog covers the six system roles;
// a workspace-defined key has no translation, so it reads as itself rather
// than as a missing-translation marker — the admin still learns what is held.
const roleLabel = (t: ReturnType<typeof useT>) => (key: string) =>
  isOption(key, ROLES) ? t(`users.role.${key}`) : key;

// The five system roles as pickable options — shared by the invite form and
// every roster row so the two lists cannot drift apart.
const roleOptions = (t: ReturnType<typeof useT>): SelectOption[] =>
  ROLES.map((role) => ({ value: role, label: t(`users.role.${role}`) }));

// The member roster (org settings). Every user-management WRITE is admin-only
// server-side, but the read is not: `GET /users` answers 200 to any authenticated
// principal, so the list is fetched for everyone and only the controls that
// change a member are withheld. The read opts into inactive members
// (include_inactive, honored server-side only for an admin) so a deactivated
// member can be reactivated. First page only in V1; larger member lists paginate
// in a follow-up.
function useMembers() {
  return useQuery({
    queryKey: ["users-admin"],
    queryFn: async (): Promise<User[]> => {
      const { data, error } = await api.GET("/users", {
        params: { query: { include_inactive: true } },
      });
      if (error) {
        throwProblem(error);
      }
      return data.data;
    },
  });
}

export function UsersAdminCard() {
  const t = useT();
  const me = useMe();
  const isAdmin = useHoldsAdminRole();
  const members = useMembers();
  // The server answers whether THIS caller can mint set-password links: admin,
  // on an installation with no email channel and a configured base URL. Where
  // email works, the invite mail carries the link and this action would only
  // ever 409 — so it is not rendered at all.
  const canIssueLink = me.data?.admin_password_link ?? false;
  // The ROSTER is not admin surface: `GET /users` answers 200 to any
  // authenticated principal, and "who is on my team and what may they do" is not
  // an admin's private question. So every member gets the member list, and only
  // the two things the server refuses them — inviting somebody, and changing a
  // role or a status — are the admin's.
  //
  // Ordered roster-FIRST for everyone, admin included. The common reason to open
  // this page is to look somebody up, and it used to open on an empty invite
  // form: three blank fields ahead of the answer, for a task most visits are not
  // about.
  //
  // Gating on the role probe itself is what keeps the invite form from flashing
  // into view while /me is still in flight.
  return (
    <div className="users-stack">
      <MembersCard
        members={members}
        canIssueLink={canIssueLink}
        canAdminister={isAdmin}
      />
      {isAdmin ? (
        <InviteForm canIssueLink={canIssueLink} />
      ) : (
        <QueryGate query={me}>
          {() => (
            // Withheld, not absent: the page opens for every seat now, so an
            // invite form that simply were not there would leave a reader to
            // wonder whether this installation can add people at all.
            <Panel title={t("users.inviteTitle")}>
              <PanelBody>
                <p className="t-caption">{t("users.inviteSub")}</p>
                <EmptyState>
                  <p className="t-small">{t("users.adminOnly")}</p>
                </EmptyState>
              </PanelBody>
            </Panel>
          )}
        </QueryGate>
      )}
    </div>
  );
}

function MembersCard({
  members,
  canIssueLink,
  canAdminister,
}: Readonly<{
  members: ReturnType<typeof useMembers>;
  canIssueLink: boolean;
  canAdminister: boolean;
}>) {
  const t = useT();
  const roster = members.data;
  return (
    <Panel
      className="users-members"
      title={t("users.membersTitle")}
      // The count states what the roster holds, deactivated members included —
      // the read opts into them, and a roster of twelve with three switched off
      // is not a roster of nine. Nothing to count is said by the empty state
      // below instead, so a "0 members" badge never doubles it.
      titleAction={
        roster && roster.length > 0 ? (
          <Badge>
            {t(
              roster.length === 1
                ? "users.memberCount.one"
                : "users.memberCount.other",
              { count: roster.length },
            )}
          </Badge>
        ) : undefined
      }
    >
      <PanelBody className="users-intro">
        <p className="t-caption">{t("users.membersSub")}</p>
      </PanelBody>
      {/* The rows run full-bleed against the panel's edges, so this body carries
          no padding of its own and the sheet gives it back to everything the
          gate can put here INSTEAD of rows — the skeletons, a failure, the
          empty state — none of which should touch the panel's edge. */}
      <PanelBody className="users-roster">
        <QueryGate query={members}>
          {(list) =>
            list.length === 0 ? (
              <EmptyState>
                <p className="t-small">{t("users.empty")}</p>
              </EmptyState>
            ) : (
              // A roster is a list, so it stays a <ul> of <li> rather than
              // becoming PanelRow's <div>s — the rows take the panel row's look
              // from this screen's sheet instead of losing their semantics to it.
              <ul className="users-list">
                {list.map((u) => (
                  <MemberRow
                    key={u.id}
                    member={u}
                    canIssueLink={canIssueLink}
                    canAdminister={canAdminister}
                  />
                ))}
              </ul>
            )
          }
        </QueryGate>
      </PanelBody>
    </Panel>
  );
}

function InviteForm({ canIssueLink }: Readonly<{ canIssueLink: boolean }>) {
  const t = useT();
  const qc = useQueryClient();
  const [email, setEmail] = useState("");
  const [name, setName] = useState("");
  const [role, setRole] = useState<Role>("rep");
  const [error, setError] = useState<string | null>(null);
  // Where no email channel exists the invite alone leaves a member who cannot
  // sign in, so the dialog opens straight away and mints the link. The member
  // row keeps its own action, which is what makes a dismissed dialog
  // recoverable.
  const [invited, setInvited] = useState<{ id: string; name: string } | null>(
    null,
  );
  const passwordLink = usePasswordLink();

  const invite = useMutation({
    mutationFn: async (): Promise<string> => {
      const { data, error: err } = await api.POST("/users", {
        body: { email: email.trim(), display_name: name.trim(), role },
      });
      if (err) {
        throwProblem(err);
      }
      return data.id;
    },
    onSuccess: (newUserId) => {
      const invitedName = name.trim();
      setEmail("");
      setName("");
      setRole("rep");
      setError(null);
      qc.invalidateQueries({ queryKey: ["users-admin"] });
      if (canIssueLink) {
        setInvited({ id: newUserId, name: invitedName });
        void passwordLink.mint(newUserId);
      }
    },
    onError: (e: Error) => setError(problemMessageOf(e, t)),
  });

  const canInvite =
    email.trim().length > 0 && name.trim().length > 0 && !invite.isPending;

  return (
    // The Panel is the surface and the <form> inside it is the form: a Panel is
    // a <section>, so the element the browser associates the Enter key with has
    // to be its own rather than the one carrying the heading.
    <Panel title={t("users.inviteTitle")}>
      <PanelBody>
        <p className="t-caption">{t("users.inviteSub")}</p>
        <form
          className="users-invite"
          onSubmit={(e) => {
            e.preventDefault();
            if (canInvite) {
              invite.mutate();
            }
          }}
        >
          <TextInput
            aria-label={t("users.emailLabel")}
            placeholder={t("users.emailPlaceholder")}
            type="email"
            value={email}
            onChange={(e) => setEmail(e.target.value)}
          />
          <TextInput
            aria-label={t("users.nameLabel")}
            placeholder={t("users.namePlaceholder")}
            value={name}
            onChange={(e) => setName(e.target.value)}
          />
          <Select
            aria-label={t("users.roleLabel")}
            value={role}
            onChange={(value) => {
              if (isOption(value, ROLES)) setRole(value);
            }}
            options={roleOptions(t)}
          />
          <Button variant="primary" small type="submit" disabled={!canInvite}>
            <UserPlus aria-hidden /> {t("users.invite")}
          </Button>
          {/* A refused invite is the surface saying something is wrong, which
              is what Callout's `danger` tone is; a bare tinted paragraph with a
              role on it was the same claim, hand-drawn, and it took its
              emphasis from nothing at all. `alert` because the reader pressed
              the button and has to act on the answer. */}
          {error && (
            <Callout tone="danger" live="alert" className="users-formerror">
              {error}
            </Callout>
          )}
        </form>
      </PanelBody>
      {invited && (
        <PasswordLinkModal
          memberName={invited.name}
          link={passwordLink.state.link}
          pending={passwordLink.state.pending}
          error={passwordLink.state.error}
          onRetry={() => void passwordLink.mint(invited.id)}
          onClose={() => {
            // Drop the credential with the dialog, never merely hide it.
            passwordLink.clear();
            setInvited(null);
          }}
        />
      )}
    </Panel>
  );
}

// The role cell: what a member's role reads as, and the control that changes it.
//
// An agent seat has neither, and gets the reason in their place. Its authority
// is the passport granting it intersected with the person that passport names,
// so a role on its own row grants nothing — the server refuses to write one, and
// a control here would promise otherwise. A line rather than a disabled select
// or a gap: the absence is a rule about what the seat IS, not a permission this
// admin happens to lack.
function RoleCell({
  member,
  pending,
  inFlight,
  onPick,
}: Readonly<{
  member: User;
  pending: boolean;
  inFlight?: Role;
  onPick: (role: Role) => void;
}>) {
  const t = useT();
  if (member.is_agent) {
    return <span className="t-small">{t("users.agentSeatRole")}</span>;
  }
  // `roles` arrives only for an admin caller — which this card always is — and
  // normally holds exactly one key. No key (an unassigned seat) and several keys
  // both leave the select on its placeholder, because neither has one current
  // role to show.
  const heldRoles = member.roles ?? [];
  const currentRole =
    heldRoles.length === 1 && isOption(heldRoles[0], ROLES) ? heldRoles[0] : "";
  // A member holding SEVERAL roles is the case worth naming: any choice here
  // replaces the whole set, so a neutral "Set role…" would let an admin strip
  // privileges they never saw. The placeholder says what is held instead.
  const placeholder =
    heldRoles.length > 1
      ? t("users.rolesHeld", { roles: heldRoles.map(roleLabel(t)).join(", ") })
      : t("users.setRole");
  return (
    // The unset state is the select's PLACEHOLDER, not an option: picking it
    // back would set no role, so it belongs on the closed face and nowhere in
    // the list. It is only ever seen when there is no single role to show.
    <Select
      aria-label={t("users.setRoleFor", { name: member.display_name })}
      value={inFlight ?? currentRole}
      placeholder={placeholder}
      disabled={pending}
      onChange={(value) => {
        if (isOption(value, ROLES)) {
          onPick(value);
        }
      }}
      options={roleOptions(t)}
    />
  );
}

function MemberRow({
  member,
  canIssueLink,
  canAdminister,
}: Readonly<{
  member: User;
  canIssueLink: boolean;
  canAdminister: boolean;
}>) {
  const t = useT();
  const qc = useQueryClient();
  const [error, setError] = useState<string | null>(null);
  const [confirmOff, setConfirmOff] = useState(false);
  const [linkOpen, setLinkOpen] = useState(false);
  // Where the deactivate confirm hands focus back. The Deactivate button it was
  // opened from is gone by then — Reactivate has taken its place — but the row
  // itself stays, because the roster is read with include_inactive.
  const row = useRef<HTMLLIElement | null>(null);
  const passwordLink = usePasswordLink();
  const openLink = () => {
    setLinkOpen(true);
    void passwordLink.mint(member.id);
  };
  // Returns the refetch so each onSuccess can hand it back to react-query,
  // which then keeps the mutation pending until the new roster lands. Without
  // that the mutation settles first and the row renders the pre-change roster
  // it still has cached — the member's OLD role, briefly, right after a
  // successful change.
  const refresh = () => {
    setError(null);
    return qc.invalidateQueries({ queryKey: ["users-admin"] });
  };
  const onError = (e: Error) => setError(problemMessageOf(e, t));

  const setRole = useMutation({
    mutationFn: async (role: Role) => {
      const { error: err } = await api.PATCH("/users/{id}/role", {
        params: { path: { id: member.id } },
        body: { role },
      });
      if (err) {
        throwProblem(err);
      }
    },
    onSuccess: refresh,
    onError,
  });

  const deactivate = useMutation({
    mutationFn: async () => {
      const { error: err } = await api.POST("/users/{id}/deactivate", {
        params: { path: { id: member.id } },
      });
      if (err) {
        throwProblem(err);
      }
    },
    onSuccess: async () => {
      // The refreshed roster FIRST, then the dialog: closing it hands focus back
      // to the row, and a row still showing "Active" beside a Deactivate button
      // would announce the state this confirm just ended.
      await refresh();
      setConfirmOff(false);
    },
    onError,
  });

  const reactivate = useMutation({
    mutationFn: async () => {
      const { error: err } = await api.POST("/users/{id}/reactivate", {
        params: { path: { id: member.id } },
      });
      if (err) {
        throwProblem(err);
      }
    },
    onSuccess: refresh,
    onError,
  });

  const pending =
    setRole.isPending || deactivate.isPending || reactivate.isPending;

  return (
    // tabIndex -1 makes the row reachable by focus() without putting a
    // container into anybody's Tab order — see the deactivate confirm below,
    // which is the only thing that focuses it.
    <li className="users-row" ref={row} tabIndex={-1}>
      <span className="users-who">
        <b>{member.display_name}</b>
        <span className="t-small">{member.email}</span>
      </span>
      <Badge tone={member.status === "active" ? "success" : "warn"}>
        {t(`users.status.${member.status}`)}
      </Badge>
      {/* The workspace's agent identity sits in this roster because it OWNS
          records — a client resolving an owner has to find it — so the row says
          what it is rather than passing for a colleague. */}
      {member.is_agent && <Badge tone="ai">{t("users.agentSeat")}</Badge>}
      {/* A role is a FACT about a member to anybody who may not change it, so a
          reader without the grant gets the role in words and not a picker that
          could only be refused. The one control becomes the one badge. */}
      {canAdminister ? (
        <RoleCell
          member={member}
          pending={pending}
          // While a change is in flight the cell shows the role being applied —
          // and it stays in flight until the refreshed roster lands (see refresh),
          // so the row never renders the replaced role. A FAILED change leaves it
          // on the role still held, which is what keeps a retry live: re-picking
          // the same target still fires onChange.
          inFlight={setRole.isPending ? setRole.variables : undefined}
          onPick={(role) => setRole.mutate(role)}
        />
      ) : (
        <span className="t-small">
          {(member.roles ?? []).map(roleLabel(t)).join(", ")}
        </span>
      )}
      {/* Only an ACTIVE member can redeem a link — redemption updates an active
          account and refuses otherwise — so offering one on a deactivated row
          would hand the admin a link that is dead on arrival. The agent seat is
          excluded for a different reason: it holds no password by construction,
          which is what makes it a thing that signs in nowhere, and the server
          refuses to mint it one. */}
      {canAdminister &&
        canIssueLink &&
        !member.is_agent &&
        member.status === "active" && (
          <Button small disabled={pending} onClick={openLink}>
            {t("users.link.action")}
          </Button>
        )}
      {canAdminister && member.status === "active" && (
        <Button small disabled={pending} onClick={() => setConfirmOff(true)}>
          {t("users.deactivate")}
        </Button>
      )}
      {canAdminister && member.status === "deactivated" && (
        <Button small disabled={pending} onClick={() => reactivate.mutate()}>
          {t("users.reactivate")}
        </Button>
      )}
      {/* Same vocabulary as the invite form's refusal, one row down: a failed
          role change or deactivation is the surface saying something is wrong,
          and it takes the whole row rather than wedging itself between the
          controls that caused it. */}
      {error && (
        <Callout tone="danger" live="alert" className="users-formerror">
          {error}
        </Callout>
      )}
      <ConfirmModal
        open={confirmOff}
        onClose={() => setConfirmOff(false)}
        title={t("users.deactivateConfirmTitle", { name: member.display_name })}
        confirmLabel={t("users.deactivate")}
        confirmVariant="danger"
        pending={deactivate.isPending}
        error={deactivate.error ? problemMessageOf(deactivate.error, t) : null}
        onConfirm={() => deactivate.mutate()}
        // The member's own row, which reads back their name, address and the
        // status this confirm just changed — the outcome, at the place the
        // operator was working. The button they pressed is not an option: a
        // deactivated row offers Reactivate instead, so the opener is gone.
        returnFocusTo={() => row.current}
      >
        {/* Deactivating the agent seat is a posture an operator is entitled to
            take, so it stays offered — but what stops when they take it is
            invisible from this screen, and the generic body (signed out, sessions
            revoked) describes a person rather than what actually happens. */}
        <p className="t-small">
          {t(
            member.is_agent
              ? "users.deactivateAgentConfirmBody"
              : "users.deactivateConfirmBody",
          )}
        </p>
      </ConfirmModal>
      {linkOpen && (
        <PasswordLinkModal
          memberName={member.display_name}
          link={passwordLink.state.link}
          pending={passwordLink.state.pending}
          error={passwordLink.state.error}
          onRetry={openLink}
          onClose={() => {
            // Drop the credential with the dialog, never merely hide it.
            passwordLink.clear();
            setLinkOpen(false);
          }}
        />
      )}
    </li>
  );
}
