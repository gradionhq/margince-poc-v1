import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { UserPlus } from "lucide-react";
import { useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import {
  Badge,
  Button,
  EmptyState,
  SectionHeader,
  TextInput,
} from "../design-system/atoms";
import { ConfirmModal } from "../design-system/confirmmodal";
import { useT } from "../i18n";
import { problemMessage, QueryGate, useMe } from "./common";
import "./users-admin.css";
import { isOption } from "../app/options";
import { PasswordLinkDialog } from "./users-password-link";

type User = components["schemas"]["User"];
type Role = components["schemas"]["ChangeUserRoleRequest"]["role"];

const ROLES: readonly Role[] = ["admin", "manager", "rep", "read_only", "ops"];

// Admin member management (org settings). Every user-management write is
// admin-only server-side, so the whole card is admin-only here — an ops seat in
// the Organization group would otherwise see controls that only 403. The roster
// includes inactive members (include_inactive, honored server-side only for an
// admin) so a deactivated member can be reactivated. First page only in V1;
// larger member lists paginate in a follow-up.
function useMembers(enabled: boolean) {
  return useQuery({
    queryKey: ["users-admin"],
    enabled,
    queryFn: async (): Promise<User[]> => {
      const { data, error } = await api.GET("/users", {
        params: { query: { include_inactive: true } },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data.data;
    },
  });
}

export function UsersAdminCard() {
  const t = useT();
  const me = useMe();
  const isAdmin = (me.data?.roles ?? []).includes("admin");
  const members = useMembers(isAdmin);
  // The server answers whether THIS caller can mint set-password links: admin,
  // on an installation with no email channel and a configured base URL. Where
  // email works, the invite mail carries the link and this action would only
  // ever 409 — so it is not rendered at all.
  const canIssueLink = me.data?.admin_password_link ?? false;
  return (
    <section className="card">
      <SectionHeader title={t("users.title")} sub={t("users.sub")} />
      {/* Gate on the role probe itself so the admin-only notice appears only
          once /me has actually answered — never as a flash while it loads. */}
      <QueryGate query={me}>
        {() =>
          isAdmin ? (
            <>
              <InviteForm canIssueLink={canIssueLink} />
              <QueryGate query={members}>
                {(list) =>
                  list.length === 0 ? (
                    <EmptyState>
                      <p className="t-small">{t("users.empty")}</p>
                    </EmptyState>
                  ) : (
                    <ul className="users-list">
                      {list.map((u) => (
                        <MemberRow
                          key={u.id}
                          member={u}
                          canIssueLink={canIssueLink}
                        />
                      ))}
                    </ul>
                  )
                }
              </QueryGate>
            </>
          ) : (
            <EmptyState>
              <p className="t-small">{t("users.adminOnly")}</p>
            </EmptyState>
          )
        }
      </QueryGate>
    </section>
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

  const invite = useMutation({
    mutationFn: async (): Promise<string> => {
      const { data, error: err } = await api.POST("/users", {
        body: { email: email.trim(), display_name: name.trim(), role },
      });
      if (err) {
        throw new Error(problemMessage(err));
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
      }
    },
    onError: (e: Error) => setError(e.message),
  });

  const canInvite =
    email.trim().length > 0 && name.trim().length > 0 && !invite.isPending;

  return (
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
      <select
        className="input"
        aria-label={t("users.roleLabel")}
        value={role}
        onChange={(e) => {
          const value = e.target.value;
          if (isOption(value, ROLES)) setRole(value);
        }}
      >
        {ROLES.map((r) => (
          <option key={r} value={r}>
            {t(`users.role.${r}`)}
          </option>
        ))}
      </select>
      <Button variant="primary" small type="submit" disabled={!canInvite}>
        <UserPlus aria-hidden /> {t("users.invite")}
      </Button>
      {error && (
        <p className="t-small" role="alert" style={{ flexBasis: "100%" }}>
          {error}
        </p>
      )}
      {invited && (
        <PasswordLinkDialog
          userId={invited.id}
          memberName={invited.name}
          onClose={() => setInvited(null)}
        />
      )}
    </form>
  );
}

function MemberRow({
  member,
  canIssueLink,
}: Readonly<{ member: User; canIssueLink: boolean }>) {
  const t = useT();
  const qc = useQueryClient();
  const [error, setError] = useState<string | null>(null);
  const [confirmOff, setConfirmOff] = useState(false);
  const [linkOpen, setLinkOpen] = useState(false);
  const refresh = () => {
    setError(null);
    qc.invalidateQueries({ queryKey: ["users-admin"] });
  };
  const onError = (e: Error) => setError(e.message);

  const setRole = useMutation({
    mutationFn: async (role: Role) => {
      const { error: err } = await api.PATCH("/users/{id}/role", {
        params: { path: { id: member.id } },
        body: { role },
      });
      if (err) {
        throw new Error(problemMessage(err));
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
        throw new Error(problemMessage(err));
      }
    },
    onSuccess: () => {
      setConfirmOff(false);
      refresh();
    },
    onError,
  });

  const reactivate = useMutation({
    mutationFn: async () => {
      const { error: err } = await api.POST("/users/{id}/reactivate", {
        params: { path: { id: member.id } },
      });
      if (err) {
        throw new Error(problemMessage(err));
      }
    },
    onSuccess: refresh,
    onError,
  });

  const pending =
    setRole.isPending || deactivate.isPending || reactivate.isPending;

  return (
    <li className="users-row">
      <span className="users-who">
        <b>{member.display_name}</b>
        <span className="t-small">{member.email}</span>
      </span>
      <Badge tone={member.status === "active" ? "success" : "warn"}>
        {t(`users.status.${member.status}`)}
      </Badge>
      {/* Controlled at "" so the label always resets — re-selecting the same
          role after a failed change still fires onChange. */}
      <select
        className="input"
        aria-label={t("users.setRoleFor", { name: member.display_name })}
        value=""
        disabled={pending}
        onChange={(e) => {
          const value = e.target.value;
          if (isOption(value, ROLES)) {
            setRole.mutate(value);
          }
        }}
      >
        <option value="">{t("users.setRole")}</option>
        {ROLES.map((r) => (
          <option key={r} value={r}>
            {t(`users.role.${r}`)}
          </option>
        ))}
      </select>
      {/* Only an ACTIVE member can redeem a link — redemption updates an active
          account and refuses otherwise — so offering one on a deactivated row
          would hand the admin a link that is dead on arrival. */}
      {canIssueLink && member.status === "active" && (
        <Button small disabled={pending} onClick={() => setLinkOpen(true)}>
          {t("users.link.action")}
        </Button>
      )}
      {member.status === "active" && (
        <Button small disabled={pending} onClick={() => setConfirmOff(true)}>
          {t("users.deactivate")}
        </Button>
      )}
      {member.status === "deactivated" && (
        <Button small disabled={pending} onClick={() => reactivate.mutate()}>
          {t("users.reactivate")}
        </Button>
      )}
      {error && (
        <span className="t-small" role="alert" style={{ flexBasis: "100%" }}>
          {error}
        </span>
      )}
      <ConfirmModal
        open={confirmOff}
        onClose={() => setConfirmOff(false)}
        title={t("users.deactivateConfirmTitle", { name: member.display_name })}
        confirmLabel={t("users.deactivate")}
        confirmVariant="danger"
        pending={deactivate.isPending}
        error={deactivate.error?.message}
        onConfirm={() => deactivate.mutate()}
      >
        <p className="t-small">{t("users.deactivateConfirmBody")}</p>
      </ConfirmModal>
      {/* Mounted only while open, so closing discards the credential rather
          than parking it behind a hidden dialog. */}
      {linkOpen && (
        <PasswordLinkDialog
          userId={member.id}
          memberName={member.display_name}
          onClose={() => setLinkOpen(false)}
        />
      )}
    </li>
  );
}
