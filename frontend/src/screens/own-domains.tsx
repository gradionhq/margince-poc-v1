// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Trash2 } from "lucide-react";
import { useId, useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { useCanWrite } from "../app/capability";
import { Button, EmptyState, Modal, TextInput } from "../design-system/atoms";
import { Callout } from "../design-system/callout";
import { Panel, PanelBody } from "../design-system/panel";
import { SettingList, SettingRow } from "../design-system/settingrow";
import { useT } from "../i18n";
import { problemMessageOf, QueryGate, throwProblem } from "./common";

// The own-domain surface (CAP-WIRE-2a, ADR-0082/A127): which domains this
// installation treats as its own, and therefore whose mail it does not store.
// Every role reads it — a rep should be able to see why a thread is missing —
// and only admin/ops may change it, so the verbs are refused rather than
// hidden, like the capture-settings card beside it.
//
// One card with two named rows, because the two lists answer the same question
// — which domains are ours — and differ only in who owns the answer: the
// company profile claims the first set and this screen cannot touch them, the
// second is curated here. As two cards they read as two subjects; as two rows
// with their own labels the difference in ownership is the thing the reader
// sees, which is what it actually is.

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
        throwProblem(error);
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
        throwProblem(error);
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
        throwProblem(error);
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
  const remove = useRemoveOwnDomain();
  const [adding, setAdding] = useState(false);
  // The denial, said once and POINTED AT. A control that is refused with its
  // reason floating somewhere further down the card has told a screen reader
  // nothing: the reason is read only if the reader happens to arrive at that
  // paragraph, which is not where the refused control left them. `Button`'s
  // `reasonId` is that wiring — it refuses the control AND names the one
  // sentence already on the page — so several refused verbs say it once. The
  // id is minted unconditionally, because a hook may not depend on a
  // permission.
  const denialId = useId();
  const refusal = canManage ? undefined : denialId;
  // The anchor list rides on the same query the curated row gates on — one
  // request feeds both. It stays out of the gate because a row whose whole
  // content is a list nobody may edit has nothing to say while the read is in
  // flight, and no row at all when the company claims no domain.
  const anchors = query.data?.anchor_domains ?? [];

  // Panel rather than Card, and no per-card bottom margin: the settings page
  // owns the gap between its surfaces in one place, so a card cannot space
  // itself differently from the one beside it.
  return (
    <Panel
      title={t("ownDomains.title")}
      // A create form behind one verb, so the rows below stay answers — and the
      // verb rides in the header rather than in a row of its own, because a row
      // states a setting and its answer while a row whose label repeats its own
      // button says the same thing twice a hand apart. Refused, never hidden:
      // `reasonId` names the one sentence under the rows.
      titleAction={
        <Button small reasonId={refusal} onClick={() => setAdding(true)}>
          {t("ownDomains.addOpen")}
        </Button>
      }
    >
      {/* `form-stack` stays: the denial sentence and the failure Callout under
          the rows are non-row children, and the list owns only the intervals
          BETWEEN its rows. */}
      <PanelBody className="form-stack">
        <p className="settings-panel-sub">{t("ownDomains.sub")}</p>
        <SettingList>
          {anchors.length > 0 && (
            <SettingRow
              testId="own-domains-company-row"
              label={t("ownDomains.companyTitle")}
              // Copy that sends the reader somewhere has to take them there.
              // The line says these are changed on the company profile, which
              // is a different settings entry — so without the link the
              // instruction was a destination the reader had to go and find,
              // on a row whose whole point is that it cannot be edited here.
              description={
                <>
                  {t("ownDomains.fromCompany")}{" "}
                  <a href="#/settings/admin/general">
                    {t("ownDomains.openCompany")}
                  </a>
                </>
              }
              layout="stack"
              // One row per domain, in the same row language as the curated
              // half below — it was a bulleted `<ul>` with an inline margin and
              // an inline left padding, a third layout on a card that already
              // had two. `control={null}` because there is nothing to press: a
              // domain the company profile claims is changed there, and the
              // link in the description is what takes the reader.
              control={
                <SettingList testId="own-domains-from-company">
                  {anchors.map((domain) => (
                    <SettingRow key={domain} label={domain} control={null} />
                  ))}
                </SettingList>
              }
            />
          )}
          {/* The irreversibility note lives on THIS row rather than beside the
              company list, because it describes what adding and removing do —
              the acts only this half offers. */}
          <SettingRow
            testId="own-domains-curated-row"
            label={t("ownDomains.curatedTitle")}
            description={t("ownDomains.irreversible")}
            layout="stack"
            control={
              <QueryGate query={query}>
                {(list) => (
                  <CuratedDomains
                    list={list.data}
                    refusal={refusal}
                    pending={remove.isPending}
                    onRemove={(domain) => remove.mutate(domain)}
                  />
                )}
              </QueryGate>
            }
          />
        </SettingList>
        {!canManage && (
          <p className="t-small" id={denialId}>
            {t("captureSettings.adminOnly")}
          </p>
        )}
        {remove.isError && (
          <Callout tone="danger" live="alert">
            {problemMessageOf(remove.error, t)}
          </Callout>
        )}
        {adding && <AddOwnDomainDialog onClose={() => setAdding(false)} />}
      </PanelBody>
    </Panel>
  );
}

/**
 * The curated half: one row per domain, whether it is confirmed as the row's
 * answer, and the verb that takes it back at the right.
 *
 * It was a hand-rolled `<ul>` with an inline-styled `<li>` and an inline
 * `marginLeft` on the state beside each domain — the same list
 * capture-exclusions.tsx had copied, which is the signal the shape belongs to
 * the row language rather than to either screen.
 */
function CuratedDomains({
  list,
  refusal,
  pending,
  onRemove,
}: Readonly<{
  list: WorkspaceEmailDomain[];
  /** The id of the one sentence saying why removal is refused, when it is. */
  refusal: string | undefined;
  pending: boolean;
  onRemove: (domain: string) => void;
}>) {
  const t = useT();
  if (list.length === 0) {
    // `empty`, and only `empty`: no further domain is registered, which is a
    // fact about the installation rather than a read that failed. The row caps
    // and left-aligns it already (settingrow.css).
    return (
      <EmptyState>
        <p className="t-small" data-testid="own-domains-empty">
          {t("ownDomains.empty")}
        </p>
      </EmptyState>
    );
  }
  return (
    <SettingList testId="own-domains-list">
      {list.map((domain) => (
        <SettingRow
          key={domain.domain}
          label={domain.domain}
          value={
            domain.verified
              ? t("ownDomains.confirmed")
              : t("ownDomains.candidate")
          }
          control={
            <Button
              small
              variant="ghost"
              aria-label={t("ownDomains.remove", { domain: domain.domain })}
              disabled={pending}
              reasonId={refusal}
              onClick={() => onRemove(domain.domain)}
            >
              <Trash2 aria-hidden size={16} />
            </Button>
          }
        />
      ))}
    </SettingList>
  );
}

// Mounted only while it is open, so the draft a reader abandoned is gone the
// next time they open it rather than waiting there as a half-typed domain.
function AddOwnDomainDialog({ onClose }: Readonly<{ onClose: () => void }>) {
  const t = useT();
  const add = useAddOwnDomain();
  const headingId = useId();
  const [draft, setDraft] = useState("");
  const domain = draft.trim();
  return (
    <Modal open onClose={onClose} labelledBy={headingId}>
      <h2 id={headingId} className="t-h2 modal-title">
        {t("ownDomains.addLabel")}
      </h2>
      <form
        className="form-stack"
        onSubmit={(event) => {
          event.preventDefault();
          if (domain === "") {
            return;
          }
          add.mutate(domain, { onSuccess: onClose });
        }}
      >
        <TextInput
          value={draft}
          aria-label={t("ownDomains.addLabel")}
          placeholder={t("ownDomains.placeholder")}
          onChange={(event) => setDraft(event.target.value)}
        />
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
            disabled={add.isPending || domain === ""}
          >
            {t("ownDomains.add")}
          </Button>
        </div>
      </form>
    </Modal>
  );
}
