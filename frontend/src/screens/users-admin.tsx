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
import { useT } from "../i18n";
import { problemMessage, QueryGate } from "./common";
import "./users-admin.css";

type User = components["schemas"]["User"];
type Role = components["schemas"]["ChangeUserRoleRequest"]["role"];

const ROLES: readonly Role[] = ["admin", "manager", "rep", "read_only", "ops"];

// Admin member management (org settings). The roster includes inactive members
// (include_inactive, honored server-side only for an admin) so a deactivated
// member can be reactivated. Every write is admin-only and re-checked server-side.
function useMembers() {
  return useQuery({
    queryKey: ["users-admin"],
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
  const members = useMembers();
  return (
    <section className="card">
      <SectionHeader title={t("users.title")} sub={t("users.sub")} />
      <InviteForm />
      <QueryGate query={members}>
        {(list) =>
          list.length === 0 ? (
            <EmptyState>
              <p className="t-small">{t("users.empty")}</p>
            </EmptyState>
          ) : (
            <ul className="users-list" style={{ marginTop: "var(--space-3)" }}>
              {list.map((u) => (
                <MemberRow key={u.id} member={u} />
              ))}
            </ul>
          )
        }
      </QueryGate>
    </section>
  );
}

function InviteForm() {
  const t = useT();
  const qc = useQueryClient();
  const [email, setEmail] = useState("");
  const [name, setName] = useState("");
  const [role, setRole] = useState<Role>("rep");
  const [error, setError] = useState<string | null>(null);

  const invite = useMutation({
    mutationFn: async () => {
      const { error: err } = await api.POST("/users", {
        body: { email: email.trim(), display_name: name.trim(), role },
      });
      if (err) {
        throw new Error(problemMessage(err));
      }
    },
    onSuccess: () => {
      setEmail("");
      setName("");
      setRole("rep");
      setError(null);
      qc.invalidateQueries({ queryKey: ["users-admin"] });
    },
    onError: (e: Error) => setError(e.message),
  });

  const canInvite =
    email.trim().length > 0 && name.trim().length > 0 && !invite.isPending;

  return (
    <div className="users-invite">
      <TextInput
        placeholder={t("users.emailPlaceholder")}
        type="email"
        value={email}
        onChange={(e) => setEmail(e.target.value)}
      />
      <TextInput
        placeholder={t("users.namePlaceholder")}
        value={name}
        onChange={(e) => setName(e.target.value)}
      />
      <select
        className="input"
        aria-label={t("users.roleLabel")}
        value={role}
        onChange={(e) => setRole(e.target.value as Role)}
      >
        {ROLES.map((r) => (
          <option key={r} value={r}>
            {t(`users.role.${r}`)}
          </option>
        ))}
      </select>
      <Button
        variant="primary"
        small
        disabled={!canInvite}
        onClick={() => invite.mutate()}
      >
        <UserPlus aria-hidden /> {t("users.invite")}
      </Button>
      {error && (
        <p className="t-small" style={{ flexBasis: "100%" }}>
          {error}
        </p>
      )}
    </div>
  );
}

function MemberRow({ member }: Readonly<{ member: User }>) {
  const t = useT();
  const qc = useQueryClient();
  const [error, setError] = useState<string | null>(null);
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
    onSuccess: refresh,
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
  const active = member.status === "active";

  return (
    <li className="users-row">
      <span className="users-who">
        <b>{member.display_name}</b>
        <span className="t-small">{member.email}</span>
      </span>
      <Badge tone={active ? "success" : "warn"}>
        {t(`users.status.${member.status}`)}
      </Badge>
      <select
        className="input"
        aria-label={t("users.setRoleFor", { name: member.display_name })}
        defaultValue=""
        disabled={pending}
        onChange={(e) => {
          if (e.target.value) {
            setRole.mutate(e.target.value as Role);
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
      {active ? (
        <Button small disabled={pending} onClick={() => deactivate.mutate()}>
          {t("users.deactivate")}
        </Button>
      ) : (
        <Button small disabled={pending} onClick={() => reactivate.mutate()}>
          {t("users.reactivate")}
        </Button>
      )}
      {error && (
        <span className="t-small" style={{ flexBasis: "100%" }}>
          {error}
        </span>
      )}
    </li>
  );
}
