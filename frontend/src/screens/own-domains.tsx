import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Trash2 } from "lucide-react";
import { useId, useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { useCanWrite } from "../app/capability";
import { Button, TextInput } from "../design-system/atoms";
import { Panel, PanelBody } from "../design-system/panel";
import { useT } from "../i18n";
import { problemMessage, QueryGate } from "./common";

// The workspace own-domain surface (CAP-WIRE-2a, ADR-0082/A127): which domains
// this installation treats as its own, and therefore whose mail it does not
// store. Every role reads it — a rep should be able to see why a thread is
// missing — and only admin/ops may change it, so the controls are disabled
// rather than hidden, like the capture-settings card beside it.
//
// Two cards, because the two lists answer to different owners: the company
// profile claims the first set and this screen cannot touch them, while the
// second is curated here. One card put a read-only list and an editable one
// under a single heading, which reads as one list with an inconsistent remove
// button.

// The row shape comes from the generated contract rather than being restated
// here: a hand-written copy would drift the first time the contract gains a
// field, and drift silently, since nothing compares the two.
type WorkspaceEmailDomain = components["schemas"]["WorkspaceEmailDomain"];

function useOwnDomains() {
  return useQuery({
    queryKey: ["workspace-email-domains"],
    queryFn: async () => {
      const { data, error, response } = await api.GET("/capture/email-domains");
      if (error || !response.ok) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
  });
}

function useAddOwnDomain() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (domain: string) => {
      const { data, error } = await api.POST("/capture/email-domains", {
        body: { domain },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["workspace-email-domains"] });
    },
  });
}

function useRemoveOwnDomain() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (domain: string) => {
      const { error } = await api.DELETE("/capture/email-domains/{domain}", {
        params: { path: { domain } },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["workspace-email-domains"] });
    },
  });
}

export function OwnDomainsCard() {
  const t = useT();
  const canManage = useCanWrite("capture_settings", "update");
  const query = useOwnDomains();
  // The company card reads the anchor list off the same query the curated card
  // gates on — one request feeds both. It stays out of the gate because a card
  // whose whole content is a list nobody may edit has nothing to say while the
  // read is in flight, and nothing at all when the company claims no domain.
  const anchors = query.data?.anchor_domains ?? [];
  return (
    // Panel rather than Card, and no per-card bottom margin: the settings page
    // owns the gap between its surfaces in one place, so a card cannot space
    // itself differently from the one beside it.
    <>
      {anchors.length > 0 && (
        <Panel title={t("ownDomains.companyTitle")}>
          <PanelBody className="form-stack">
            <p className="t-caption">{t("ownDomains.fromCompany")}</p>
            <ul
              className="t-small"
              data-testid="own-domains-from-company"
              style={{ margin: 0, paddingLeft: "var(--space-4)" }}
            >
              {anchors.map((domain) => (
                <li key={domain}>{domain}</li>
              ))}
            </ul>
            {/* Copy that sends the reader somewhere has to take them there. The
                line above says these are changed on the company profile, which
                is a different settings entry — so without this the instruction
                was a destination the reader had to go and find, on a page whose
                whole point is that it cannot be edited here. */}
            <p className="t-small">
              <a href="#/settings/general">{t("ownDomains.openCompany")}</a>
            </p>
          </PanelBody>
        </Panel>
      )}
      <Panel title={t("ownDomains.title")}>
        <PanelBody className="form-stack">
          <p className="t-caption">{t("ownDomains.sub")}</p>
          <QueryGate query={query}>
            {(list) => (
              <CuratedDomains list={list.data} canManage={canManage} />
            )}
          </QueryGate>
        </PanelBody>
      </Panel>
    </>
  );
}

// The curated half: the domains this screen owns, plus the two verbs that
// change them. The irreversibility note lives here rather than beside the
// company list because it describes what adding and removing do — the acts
// only this card offers.
function CuratedDomains({
  list,
  canManage,
}: Readonly<{ list: WorkspaceEmailDomain[]; canManage: boolean }>) {
  const t = useT();
  const add = useAddOwnDomain();
  const remove = useRemoveOwnDomain();
  const [draft, setDraft] = useState("");
  // The denial, said once and POINTED AT. A control that is disabled with its
  // reason floating somewhere further down the card has told a screen reader
  // nothing: the reason is read only if the reader happens to arrive at that
  // paragraph, which is not where the disabled control left them. `Switch`'s
  // `reason` prop is the pattern this copies (design-system README) — it
  // renders the explanation AND names it on the control — and an id plus
  // `aria-describedby` is that same wiring for the controls that are not
  // switches. The id is minted unconditionally, because a hook may not depend
  // on a permission, and named only where the paragraph is really rendered.
  const denialId = useId();
  const denied = !canManage;
  const describedBy = denied ? denialId : undefined;

  return (
    <div className="form-stack">
      <p className="t-small">{t("ownDomains.irreversible")}</p>

      {list.length === 0 ? (
        <p className="t-small" data-testid="own-domains-empty">
          {t("ownDomains.empty")}
        </p>
      ) : (
        <ul
          data-testid="own-domains-list"
          style={{ listStyle: "none", margin: 0, padding: 0 }}
        >
          {list.map((domain) => (
            <DomainRow
              key={domain.domain}
              domain={domain}
              disabled={!canManage || remove.isPending}
              describedBy={describedBy}
              onRemove={() => remove.mutate(domain.domain)}
            />
          ))}
        </ul>
      )}

      <form
        style={{ display: "flex", gap: "var(--space-2)" }}
        onSubmit={(event) => {
          event.preventDefault();
          const domain = draft.trim();
          if (!domain) {
            return;
          }
          add.mutate(domain, { onSuccess: () => setDraft("") });
        }}
      >
        <TextInput
          value={draft}
          aria-label={t("ownDomains.addLabel")}
          placeholder={t("ownDomains.placeholder")}
          disabled={!canManage || add.isPending}
          aria-describedby={describedBy}
          onChange={(event) => setDraft(event.target.value)}
        />
        <Button
          type="submit"
          variant="primary"
          disabled={!canManage || add.isPending || draft.trim() === ""}
          aria-describedby={describedBy}
        >
          {t("ownDomains.add")}
        </Button>
      </form>

      {denied && (
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

function DomainRow({
  domain,
  disabled,
  describedBy,
  onRemove,
}: Readonly<{
  domain: WorkspaceEmailDomain;
  disabled: boolean;
  /** The id of the one paragraph saying why removal is refused, when it is. */
  describedBy?: string;
  onRemove: () => void;
}>) {
  const t = useT();
  return (
    <li
      style={{
        display: "flex",
        alignItems: "center",
        justifyContent: "space-between",
        gap: "var(--space-2)",
        padding: "var(--space-2) 0",
      }}
    >
      <span>
        {domain.domain}
        <span className="t-small" style={{ marginLeft: "var(--space-2)" }}>
          {domain.verified
            ? t("ownDomains.confirmed")
            : t("ownDomains.candidate")}
        </span>
      </span>
      <Button
        variant="ghost"
        aria-label={t("ownDomains.remove", { domain: domain.domain })}
        disabled={disabled}
        aria-describedby={describedBy}
        onClick={onRemove}
      >
        <Trash2 aria-hidden size={16} />
      </Button>
    </li>
  );
}
