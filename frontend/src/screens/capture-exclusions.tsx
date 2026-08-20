import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Trash2 } from "lucide-react";
import { useId, useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { useCanWrite } from "../app/capability";
import {
  Button,
  Modal,
  SegmentedControl,
  TextInput,
} from "../design-system/atoms";
import { Callout } from "../design-system/callout";
import { Panel, PanelBody } from "../design-system/panel";
import { SettingList, SettingRow } from "../design-system/settingrow";
import { useT } from "../i18n";
import { problemMessageOf, QueryGate, throwProblem } from "./common";

// Pre-capture exclusions: the addresses and domains whose mail the CRM must not
// store at all. Two scopes on one card, because a reader sees both kinds of
// rule that bind their mailbox: the organization's (admin/ops change those) and
// their own (anyone may keep their own correspondent out of a shared CRM).

type CaptureExclusion = components["schemas"]["CaptureExclusion"];
type Scope = components["schemas"]["CaptureExclusionScope"];
type Kind = components["schemas"]["CaptureExclusionKind"];

const SCOPES: readonly Scope[] = ["user", "workspace"];
const KINDS: readonly Kind[] = ["address", "domain"];

/** The organization-wide rules, which are the ones a plain seat may not touch. */
function bindsEveryone(rule: CaptureExclusion): boolean {
  return rule.scope === "workspace";
}

function useExclusions() {
  return useQuery({
    queryKey: ["capture-exclusions"],
    queryFn: async () => {
      const { data, error, response } = await api.GET("/capture/exclusions");
      if (error || !response.ok) {
        throwProblem(error);
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
        throwProblem(error);
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
        throwProblem(error);
      }
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["capture-exclusions"] });
    },
  });
}

/** What each scope and kind is called, in the reader's language. */
function useRuleWords() {
  const t = useT();
  const scope: Record<Scope, string> = {
    user: t("captureExclusions.scope.user"),
    workspace: t("captureExclusions.scope.workspace"),
  };
  const kind: Record<Kind, string> = {
    address: t("captureExclusions.kind.address"),
    domain: t("captureExclusions.kind.domain"),
  };
  return { scope, kind };
}

export function CaptureExclusionsCard() {
  const t = useT();
  const canManageWorkspace = useCanWrite("capture_settings", "update");
  const query = useExclusions();
  const remove = useRemoveExclusion();
  const [excluding, setExcluding] = useState(false);
  // Said once and pointed at (see own-domains.tsx): an organization-wide rule
  // is admin/ops work, and `Button`'s `reasonId` refuses the verb AND names
  // the sentence, so every refused row points at one line rather than
  // printing it per row. The id is minted unconditionally, because a hook may
  // not depend on a permission.
  const denialId = useId();
  const rules = query.data?.data ?? [];
  // Named only when something on this card is actually refused: a standing
  // sentence about a permission with nothing behind it is a claim about the
  // reader that no row on the page bears out.
  const refusesARow =
    !canManageWorkspace && rules.some((rule) => bindsEveryone(rule));

  return (
    <Panel title={t("captureExclusions.title")}>
      <PanelBody className="form-stack">
        <p className="t-small settings-panel-sub">
          {t("captureExclusions.sub")}
        </p>
        <SettingList>
          {/* The rules are the subject of this card, not an answer beside a
              question, so they take the row's full width. */}
          <SettingRow
            label={t("captureExclusions.current")}
            layout="stack"
            control={
              <QueryGate query={query}>
                {(list) => (
                  <ExclusionList
                    list={list.data}
                    canManageWorkspace={canManageWorkspace}
                    denialId={denialId}
                    pending={remove.isPending}
                    onRemove={(id) => remove.mutate(id)}
                  />
                )}
              </QueryGate>
            }
          />
          {/* Three inputs submitted together — who it binds, what kind of rule
              it is, and the value — so the form lives behind this verb. */}
          <SettingRow
            label={t("captureExclusions.addLabel")}
            description={t("captureExclusions.notRetroactive")}
            control={
              <Button variant="ghost" onClick={() => setExcluding(true)}>
                {t("captureExclusions.add")}
              </Button>
            }
          />
        </SettingList>
        {refusesARow && (
          <p className="t-small" id={denialId}>
            {t("captureSettings.adminOnly")}
          </p>
        )}
        {remove.isError && (
          <Callout tone="danger" live="alert">
            {problemMessageOf(remove.error, t)}
          </Callout>
        )}
        {excluding && (
          <ExcludeDialog
            canManageWorkspace={canManageWorkspace}
            onClose={() => setExcluding(false)}
          />
        )}
      </PanelBody>
    </Panel>
  );
}

function ExclusionList({
  list,
  canManageWorkspace,
  denialId,
  pending,
  onRemove,
}: Readonly<{
  list: CaptureExclusion[];
  canManageWorkspace: boolean;
  /** The one sentence saying why an organization-wide rule is not this reader's. */
  denialId: string;
  pending: boolean;
  onRemove: (id: string) => void;
}>) {
  const t = useT();
  const words = useRuleWords();
  if (list.length === 0) {
    return (
      <p className="t-small" data-testid="capture-exclusions-empty">
        {t("captureExclusions.empty")}
      </p>
    );
  }
  return (
    <ul
      className="settingrow-measure"
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
            <span className="t-small" style={{ marginLeft: "var(--space-2)" }}>
              {words.scope[rule.scope]} · {words.kind[rule.kind]}
            </span>
          </span>
          <Button
            variant="ghost"
            aria-label={t("captureExclusions.remove", { value: rule.value })}
            disabled={pending}
            reasonId={
              bindsEveryone(rule) && !canManageWorkspace ? denialId : undefined
            }
            onClick={() => onRemove(rule.id)}
          >
            <Trash2 aria-hidden size={16} />
          </Button>
        </li>
      ))}
    </ul>
  );
}

// Mounted only while it is open, so a half-typed address is gone the next time
// the dialog opens rather than waiting there under a scope nobody re-chose.
function ExcludeDialog({
  canManageWorkspace,
  onClose,
}: Readonly<{ canManageWorkspace: boolean; onClose: () => void }>) {
  const t = useT();
  const words = useRuleWords();
  const add = useAddExclusion();
  const headingId = useId();
  const denialId = useId();
  const [scope, setScope] = useState<Scope>("user");
  const [kind, setKind] = useState<Kind>("address");
  const [draft, setDraft] = useState("");
  // Anyone may keep their own correspondent out of a shared CRM; a rule that
  // binds everybody is admin/ops work. So the refusal follows the SCOPE the
  // reader has picked, and the sentence sits with the controls it refuses.
  const refused = scope === "workspace" && !canManageWorkspace;
  const value = draft.trim();
  return (
    <Modal open onClose={onClose} labelledBy={headingId}>
      <h2 id={headingId} className="t-h2 modal-title">
        {t("captureExclusions.addLabel")}
      </h2>
      <form
        className="form-stack"
        onSubmit={(event) => {
          event.preventDefault();
          if (refused || value === "") {
            return;
          }
          add.mutate({ scope, kind, value }, { onSuccess: onClose });
        }}
      >
        <SegmentedControl
          options={SCOPES}
          value={scope}
          onChange={setScope}
          labels={words.scope}
          label={t("captureExclusions.scopeLabel")}
        />
        <SegmentedControl
          options={KINDS}
          value={kind}
          onChange={setKind}
          labels={words.kind}
          label={t("captureExclusions.kindLabel")}
        />
        <TextInput
          value={draft}
          aria-label={t("captureExclusions.addLabel")}
          placeholder={
            kind === "address"
              ? t("captureExclusions.placeholder.address")
              : t("captureExclusions.placeholder.domain")
          }
          disabled={refused}
          aria-describedby={refused ? denialId : undefined}
          onChange={(event) => setDraft(event.target.value)}
        />
        {refused && (
          <p className="t-small" id={denialId}>
            {t("captureSettings.adminOnly")}
          </p>
        )}
        {add.isError && (
          <Callout tone="danger" live="alert">
            {problemMessageOf(add.error, t)}
          </Callout>
        )}
        <div className="form-actions">
          <Button small type="button" onClick={onClose}>
            {t("create.cancel")}
          </Button>
          <Button
            small
            type="submit"
            variant="primary"
            disabled={add.isPending || value === ""}
            reasonId={refused ? denialId : undefined}
          >
            {t("captureExclusions.add")}
          </Button>
        </div>
      </form>
    </Modal>
  );
}
