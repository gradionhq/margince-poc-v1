import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Trash2 } from "lucide-react";
import { useId, useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { useCanWrite } from "../app/capability";
import { Button, SegmentedControl, TextInput } from "../design-system/atoms";
import { Panel, PanelBody } from "../design-system/panel";
import { useT } from "../i18n";
import { problemMessage, QueryGate } from "./common";

// Pre-capture exclusions: the addresses and domains whose mail the CRM must not
// store at all. Two scopes on one card, because a reader sees both kinds of
// rule that bind their mailbox: the workspace's (admin/ops change those) and
// their own (anyone may keep their own correspondent out of a shared CRM).

type CaptureExclusion = components["schemas"]["CaptureExclusion"];
type Scope = components["schemas"]["CaptureExclusionScope"];
type Kind = components["schemas"]["CaptureExclusionKind"];

const SCOPES: readonly Scope[] = ["user", "workspace"];
const KINDS: readonly Kind[] = ["address", "domain"];

function useExclusions() {
  return useQuery({
    queryKey: ["capture-exclusions"],
    queryFn: async () => {
      const { data, error, response } = await api.GET("/capture/exclusions");
      if (error || !response.ok) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
  });
}

function useAddExclusion() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (body: { scope: Scope; kind: Kind; value: string }) => {
      const { data, error } = await api.POST("/capture/exclusions", { body });
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["capture-exclusions"] });
    },
  });
}

function useRemoveExclusion() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (id: string) => {
      const { error } = await api.DELETE("/capture/exclusions/{id}", {
        params: { path: { id } },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["capture-exclusions"] });
    },
  });
}

export function CaptureExclusionsCard() {
  const t = useT();
  const query = useExclusions();
  return (
    <Panel title={t("captureExclusions.title")}>
      <PanelBody className="form-stack">
        <p className="t-caption">{t("captureExclusions.sub")}</p>
        <QueryGate query={query}>
          {(list) => <ExclusionList list={list.data} />}
        </QueryGate>
      </PanelBody>
    </Panel>
  );
}

function ExclusionList({ list }: Readonly<{ list: CaptureExclusion[] }>) {
  const t = useT();
  const canManageWorkspace = useCanWrite("capture_settings", "update");
  const add = useAddExclusion();
  const remove = useRemoveExclusion();
  const [scope, setScope] = useState<Scope>("user");
  const [kind, setKind] = useState<Kind>("address");
  const [draft, setDraft] = useState("");
  // Said once and pointed at (see own-domains.tsx): a workspace rule is
  // admin/ops work, and the control that refuses it names the reason.
  const denialId = useId();
  const workspaceDenied = scope === "workspace" && !canManageWorkspace;
  const describedBy = workspaceDenied ? denialId : undefined;
  const scopeLabels: Record<Scope, string> = {
    user: t("captureExclusions.scope.user"),
    workspace: t("captureExclusions.scope.workspace"),
  };
  const kindLabels: Record<Kind, string> = {
    address: t("captureExclusions.kind.address"),
    domain: t("captureExclusions.kind.domain"),
  };

  return (
    <div className="form-stack">
      <p className="t-small">{t("captureExclusions.notRetroactive")}</p>
      {list.length === 0 ? (
        <p className="t-small" data-testid="capture-exclusions-empty">
          {t("captureExclusions.empty")}
        </p>
      ) : (
        <ul
          data-testid="capture-exclusions-list"
          style={{ listStyle: "none", margin: 0, padding: 0 }}
        >
          {list.map((rule) => (
            <li
              key={rule.id}
              style={{
                display: "flex",
                alignItems: "center",
                justifyContent: "space-between",
                gap: "var(--space-2)",
                padding: "var(--space-2) 0",
              }}
            >
              <span>
                {rule.value}
                <span
                  className="t-small"
                  style={{ marginLeft: "var(--space-2)" }}
                >
                  {scopeLabels[rule.scope]} · {kindLabels[rule.kind]}
                </span>
              </span>
              <Button
                variant="ghost"
                aria-label={t("captureExclusions.remove", {
                  value: rule.value,
                })}
                disabled={
                  remove.isPending ||
                  (rule.scope === "workspace" && !canManageWorkspace)
                }
                onClick={() => remove.mutate(rule.id)}
              >
                <Trash2 aria-hidden size={16} />
              </Button>
            </li>
          ))}
        </ul>
      )}

      <form
        className="form-stack"
        onSubmit={(event) => {
          event.preventDefault();
          const value = draft.trim();
          if (!value) {
            return;
          }
          add.mutate({ scope, kind, value }, { onSuccess: () => setDraft("") });
        }}
      >
        <div
          style={{ display: "flex", gap: "var(--space-2)", flexWrap: "wrap" }}
        >
          <SegmentedControl
            options={SCOPES}
            value={scope}
            onChange={setScope}
            labels={scopeLabels}
            label={t("captureExclusions.scopeLabel")}
          />
          <SegmentedControl
            options={KINDS}
            value={kind}
            onChange={setKind}
            labels={kindLabels}
            label={t("captureExclusions.kindLabel")}
          />
        </div>
        <div style={{ display: "flex", gap: "var(--space-2)" }}>
          <TextInput
            value={draft}
            aria-label={t("captureExclusions.addLabel")}
            placeholder={
              kind === "address"
                ? t("captureExclusions.placeholder.address")
                : t("captureExclusions.placeholder.domain")
            }
            disabled={workspaceDenied || add.isPending}
            aria-describedby={describedBy}
            onChange={(event) => setDraft(event.target.value)}
          />
          <Button
            type="submit"
            variant="primary"
            disabled={workspaceDenied || add.isPending || draft.trim() === ""}
            aria-describedby={describedBy}
          >
            {t("captureExclusions.add")}
          </Button>
        </div>
      </form>
      {workspaceDenied && (
        <p className="t-small" id={denialId}>
          {t("captureSettings.adminOnly")}
        </p>
      )}
      {(add.isError || remove.isError) && (
        <span role="alert" className="form-error">
          {(add.error ?? remove.error)?.message}
        </span>
      )}
    </div>
  );
}
