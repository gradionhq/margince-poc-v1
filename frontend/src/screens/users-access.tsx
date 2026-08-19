import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Trash2 } from "lucide-react";
import { useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { Button, TextInput } from "../design-system/atoms";
import { Panel, PanelBody } from "../design-system/panel";
import { useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { problemMessage, QueryGate, throwProblem } from "./common";

// What a seat will see, said by the server. The invite form asks before the
// invite goes out; the answer is the evaluated policy — the same grants,
// masks and read classes the gates read — so the screen never interprets a
// role a second way.

type AccessPreview = components["schemas"]["AccessPreview"];
type Role = components["schemas"]["AccessPreviewRequest"]["role"];
type Team = components["schemas"]["Team"];

// The objects worth a line in the preview: the record kinds a rep works.
const PREVIEW_OBJECTS = [
  "person",
  "organization",
  "lead",
  "deal",
  "project",
] as const;

function useAccessPreview(role: Role, teamIds: string[]) {
  return useQuery({
    queryKey: ["access-preview", role, [...teamIds].sort().join(",")],
    queryFn: async (): Promise<AccessPreview> => {
      const { data, error } = await api.POST("/users/access-preview", {
        body: { role, team_ids: teamIds },
      });
      if (error) throwProblem(error);
      return data;
    },
  });
}

export function AccessPreviewPanel({
  role,
  teamIds,
}: Readonly<{ role: Role; teamIds: string[] }>) {
  const t = useT();
  const preview = useAccessPreview(role, teamIds);
  return (
    <div className="users-access-preview" aria-live="polite">
      <p className="t-caption">{t("users.access.title")}</p>
      <QueryGate query={preview}>
        {(access) => <AccessSummary access={access} />}
      </QueryGate>
    </div>
  );
}

function AccessSummary({ access }: Readonly<{ access: AccessPreview }>) {
  const t = useT();
  const verbs = (object: string): string => {
    const grant = access.objects?.[object];
    if (!grant || !grant.read) {
      return t("users.access.none");
    }
    const parts = [t("users.access.read")];
    if (grant.create || grant.update) parts.push(t("users.access.write"));
    if (grant.delete) parts.push(t("users.access.delete"));
    return parts.join(" · ");
  };
  const teams = (access.teams ?? []).map((team) => team.name).join(", ");
  return (
    <ul className="t-small users-access-list">
      <li>{t("users.access.identity")}</li>
      <li>
        {access.row_scope === "all"
          ? t("users.access.writesAll")
          : access.row_scope === "team"
            ? teams
              ? t("users.access.writesTeam", { teams })
              : t("users.access.writesTeamNone")
            : t("users.access.writesOwn")}
      </li>
      {PREVIEW_OBJECTS.map((object) => (
        <li key={object}>
          {t(`users.access.object.${object}` satisfies MessageKey)}:{" "}
          {verbs(object)}
        </li>
      ))}
      {(access.field_masks ?? []).map((mask) => (
        <li key={`${mask.object}.${mask.field}`}>
          {t("users.access.mask", {
            field: `${mask.object}.${mask.field}`,
            when:
              mask.condition === "always"
                ? t("users.access.maskAlways")
                : t("users.access.maskOutside"),
          })}
        </li>
      ))}
    </ul>
  );
}

// The teams card: the workspace's teams, and the verbs that change them.
// Membership is what resolves who may EDIT whose records now that customer
// identity is readable by every seat, so this is where that is administered.

function useTeams() {
  return useQuery({
    queryKey: ["teams"],
    queryFn: async (): Promise<Team[]> => {
      const { data, error } = await api.GET("/teams", {
        params: { query: { limit: 200 } },
      });
      if (error) throw new Error(problemMessage(error));
      return data.data;
    },
  });
}

export function TeamsCard() {
  const t = useT();
  const qc = useQueryClient();
  const teams = useTeams();
  const [draft, setDraft] = useState("");
  const create = useMutation({
    mutationFn: async (name: string) => {
      const { data, error } = await api.POST("/teams", { body: { name } });
      if (error) throw new Error(problemMessage(error));
      return data;
    },
    onSuccess: () => {
      setDraft("");
      qc.invalidateQueries({ queryKey: ["teams"] });
    },
  });
  const archive = useMutation({
    mutationFn: async (id: string) => {
      const { error } = await api.PATCH("/teams/{id}", {
        params: { path: { id } },
        body: { archived: true },
      });
      if (error) throw new Error(problemMessage(error));
    },
    onSuccess: () => qc.invalidateQueries({ queryKey: ["teams"] }),
  });
  return (
    <Panel title={t("users.teamsTitle")}>
      <PanelBody className="form-stack">
        <p className="t-caption">{t("users.teamsSub")}</p>
        <QueryGate query={teams}>
          {(list) =>
            list.length === 0 ? (
              <p className="t-small">{t("users.noTeamsYet")}</p>
            ) : (
              <ul style={{ listStyle: "none", margin: 0, padding: 0 }}>
                {list.map((team) => (
                  <li
                    key={team.id}
                    style={{
                      display: "flex",
                      alignItems: "center",
                      justifyContent: "space-between",
                      gap: "var(--space-2)",
                      padding: "var(--space-2) 0",
                    }}
                  >
                    <span>
                      {team.name}
                      <span
                        className="t-small"
                        style={{ marginLeft: "var(--space-2)" }}
                      >
                        {t("users.teamMembers", {
                          count: team.member_count ?? 0,
                        })}
                      </span>
                    </span>
                    <Button
                      variant="ghost"
                      aria-label={t("users.archiveTeam", { name: team.name })}
                      disabled={archive.isPending}
                      onClick={() => archive.mutate(team.id)}
                    >
                      <Trash2 aria-hidden size={16} />
                    </Button>
                  </li>
                ))}
              </ul>
            )
          }
        </QueryGate>
        <form
          style={{ display: "flex", gap: "var(--space-2)" }}
          onSubmit={(event) => {
            event.preventDefault();
            const name = draft.trim();
            if (name) create.mutate(name);
          }}
        >
          <TextInput
            value={draft}
            aria-label={t("users.newTeamLabel")}
            placeholder={t("users.newTeamPlaceholder")}
            disabled={create.isPending}
            onChange={(event) => setDraft(event.target.value)}
          />
          <Button
            type="submit"
            variant="primary"
            disabled={create.isPending || draft.trim() === ""}
          >
            {t("users.createTeam")}
          </Button>
        </form>
        {(create.isError || archive.isError) && (
          <span role="alert" className="form-error">
            {(create.error ?? archive.error)?.message}
          </span>
        )}
      </PanelBody>
    </Panel>
  );
}
