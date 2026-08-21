// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useCallback, useId, useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { useCanWrite } from "../app/capability";
import { isOption } from "../app/options";
import {
  Badge,
  Button,
  DataTable,
  EmptyState,
  Field,
  Modal,
  TextInput,
} from "../design-system/atoms";
import { Callout } from "../design-system/callout";
import { CountLine } from "../design-system/listsurface";
import { Panel, PanelBody } from "../design-system/panel";
import { Select } from "../design-system/select";
import { SettingList, SettingRow } from "../design-system/settingrow";
import { formatDate } from "../format/format";
import { viewerZone } from "../format/timezone";
import { type Locale, useLocale, useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { problemMessageOf, QueryGate, throwProblem } from "./common";

// The domains this installation refuses a company (ADR-0072). A vendor the
// business merely USES has a real corporate website, so every piece of evidence
// a crawl can gather says "company" — only a standing decision says otherwise,
// which is why the refusal lives on the domain rather than on any sender or
// read.
//
// The card exists for one question an operator cannot otherwise answer: a
// company that never appeared — was it refused, and by whom? So `source` is a
// first-class column, not a detail: a bulk-sender verdict and somebody's
// deliberate call look identical in the outcome and are completely different
// facts. Every human role reads the list (`organization:read`); changing an
// entry demands `organization:update`, so the verb is refused rather than
// hidden, like the capture cards beside it.
//
// The write is a PUT that is idempotent on the normalized domain: there is no
// version to quote and none to send, so no `ifMatch` here — an entry for a
// domain already on the list REPLACES it, which is also how a refusal is undone.

// The row shape comes from the generated contract rather than being restated
// here: a hand-written copy would drift the first time the contract gains a
// field, and drift silently, since nothing compares the two.
type BlockedDomain = components["schemas"]["BlockedDomain"];

// The two decisions an entry can carry, as ONE list: the type is derived from
// it and the Select's options are built from it, so the offered choices, their
// labels and the runtime narrowing cannot drift apart (the shape
// consumer-mail-domains.tsx uses for the same reason).
const ADMISSIONS = ["suppressed", "admitted"] as const;
type Admission = (typeof ADMISSIONS)[number];

const ADMISSION_LABEL: Record<Admission, MessageKey> = {
  suppressed: "blockedDomains.admission.suppressed",
  admitted: "blockedDomains.admission.admitted",
};

// The contract's own ceiling on `reason` (SetBlockedDomainRequest.maxLength).
// Held on the control so the reader stops at the limit rather than typing past
// it and losing the sentence to a 422 — the server enforces it either way.
const REASON_MAX = 500;

const SOURCE_LABEL: Record<BlockedDomain["source"], MessageKey> = {
  verdict: "blockedDomains.source.verdict",
  heuristic: "blockedDomains.source.heuristic",
  human: "blockedDomains.source.human",
};

/** One standing decision, as the dialog receives it before anybody types. */
type Decision = Readonly<{
  domain: string;
  admission: Admission;
  reason: string;
}>;

const BLANK_DECISION: Decision = {
  domain: "",
  admission: "suppressed",
  reason: "",
};

function useBlockedDomains() {
  return useQuery({
    queryKey: ["blocked-domains"],
    queryFn: async () => {
      const { data, error, response } = await api.GET(
        "/capture/blocked-domains",
      );
      if (error || !response.ok) {
        throwProblem(error);
      }
      return data;
    },
  });
}

function useSetBlockedDomain() {
  const queryClient = useQueryClient();
  return useMutation({
    // Every input arrives as a variable rather than through a closure: the
    // handler belongs to the committed render, so what it passes cannot be
    // older than the control the operator pressed.
    mutationFn: async (decision: Decision) => {
      const { data, error } = await api.PUT("/capture/blocked-domains", {
        body: decision,
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["blocked-domains"] });
    },
  });
}

/** Derived from the hook rather than restated, so the two cannot drift. */
type SetDecision = ReturnType<typeof useSetBlockedDomain>;

export function BlockedDomainsCard() {
  const t = useT();
  const { locale } = useLocale();
  // An operator investigating a company that never appeared reads the decision
  // time on their own wall clock, the same choice the audit trail makes — a
  // fixed installation zone would put the moment they are correlating against
  // an hour they were not working.
  const zone = viewerZone();
  const canManage = useCanWrite("organization", "update");
  const query = useBlockedDomains();
  const set = useSetBlockedDomain();
  // The decision being written, and the dialog's own open state: one piece of
  // state rather than two, because a dialog that is open with nothing in it is
  // a state this card cannot be in.
  const [editing, setEditing] = useState<Decision | null>(null);
  // The denial, said once and POINTED AT — `Button`'s `reasonId` refuses the
  // control and names the one sentence already on the page, which is what a
  // screen reader hears from the control it left them on. The id is minted
  // unconditionally, because a hook may not depend on a permission.
  const denialId = useId();
  const refusal = canManage ? undefined : denialId;

  // Revising a standing decision starts on its row and finishes in the dialog.
  // The contract requires a reason for every write, so a one-click flip has
  // nowhere to get one — and a refusal nobody can explain is one nobody can
  // review, which is the whole point of the column. The row therefore opens
  // the dialog on the domain with the OPPOSITE decision and no reason, so what
  // is left to do is type the sentence: that is the McKinsey case, a newsletter
  // publisher that became a client.
  const revise = useCallback((entry: BlockedDomain) => {
    setEditing({
      domain: entry.domain,
      admission: entry.admission === "suppressed" ? "admitted" : "suppressed",
      reason: "",
    });
  }, []);

  return (
    <Panel
      title={t("blockedDomains.title")}
      // The card's one create verb rides in the header, not in a row: a row
      // states a setting and its answer, and this row's LABEL was its own
      // button's words. It also keeps the verb still while the list below it
      // grows. Refused rather than hidden, like every other control on this
      // card — the sentence under the list is what `reasonId` names.
      titleAction={
        <Button
          small
          reasonId={refusal}
          onClick={() => setEditing(BLANK_DECISION)}
        >
          {t("blockedDomains.recordOpen")}
        </Button>
      }
    >
      {/* `form-stack` stays: the denial sentence and the stored-callout below
          the list are non-row children, and the list owns only the intervals
          BETWEEN its rows. */}
      <PanelBody className="form-stack">
        <p className="settings-panel-sub">{t("blockedDomains.sub")}</p>
        <SettingList>
          {/* The decisions are the subject of this card rather than an answer
              to a question beside them, so they take the row's full width. */}
          <SettingRow
            label={t("blockedDomains.listTitle")}
            layout="stack"
            control={
              <QueryGate query={query}>
                {(list) =>
                  list.data.length === 0 ? (
                    // `empty`, and only `empty`: no decision has been recorded,
                    // which is a fact about the installation rather than a read
                    // that failed. The states that are not this one are the
                    // query gate's above.
                    <EmptyState>
                      <p className="t-small">{t("blockedDomains.none")}</p>
                    </EmptyState>
                  ) : (
                    <div className="form-stack settingrow-measure">
                      <DataTable
                        label={t("blockedDomains.listTitle")}
                        columns={decisionColumns({
                          t,
                          locale,
                          zone,
                          revise,
                          refusal,
                          set,
                        })}
                        rows={list.data}
                        rowKey={(row) => row.domain}
                      />
                      {/* Refusals accumulate on their own from every
                          bulk-sender verdict, so the server pages the list and
                          answers with how many decisions EXIST. An operator
                          hunting a company that never appeared has to be able
                          to tell "not refused" from "past the end of this
                          page". */}
                      <p className="t-small">
                        <CountLine
                          unit={t("blockedDomains.unit")}
                          first={1}
                          last={list.data.length}
                          total={list.total}
                        />
                      </p>
                    </div>
                  )
                }
              </QueryGate>
            }
          />
        </SettingList>
        {!canManage && (
          <p className="t-small" id={denialId}>
            {t("blockedDomains.adminOnly")}
          </p>
        )}
        {/* What LANDED, named, and on the CARD rather than in the dialog: the
            server normalizes the domain to its registrable form and the write
            replaces any entry already on it, so without this a sub-domain
            silently became its parent and a second decision on a domain
            already listed looked like nothing had happened at all. The dialog
            is gone by the time it is true. */}
        {set.data && (
          <Callout tone="success" live="status">
            {t("blockedDomains.stored", {
              domain: set.data.domain,
              admission: t(ADMISSION_LABEL[set.data.admission]),
            })}
          </Callout>
        )}
        {editing !== null && (
          <DecisionDialog
            initial={editing}
            set={set}
            onClose={() => setEditing(null)}
          />
        )}
      </PanelBody>
    </Panel>
  );
}

/**
 * The table's columns.
 *
 * A function of what they need rather than a constant, because two of them
 * render translated copy and one renders a control whose refusal is the
 * reader's — none of which is knowable at module scope.
 */
function decisionColumns({
  t,
  locale,
  zone,
  revise,
  refusal,
  set,
}: Readonly<{
  t: ReturnType<typeof useT>;
  locale: Locale;
  zone: string;
  revise: (entry: BlockedDomain) => void;
  refusal: string | undefined;
  set: SetDecision;
}>) {
  return [
    {
      key: "domain",
      header: t("blockedDomains.col.domain"),
      render: (row: BlockedDomain) => (
        <>
          <span className="t-mono">{row.domain}</span>
          {/* The company an admitted domain produced, when there is one. A
              link rather than the id it is built from: the payload carries no
              name, and printing a UUID at an operator is not a fact they can
              use. */}
          {row.organization_id != null && (
            <>
              {" "}
              <a href={`#/companies/${row.organization_id}`}>
                {t("blockedDomains.openCompany")}
              </a>
            </>
          )}
        </>
      ),
    },
    {
      key: "admission",
      header: t("blockedDomains.col.admission"),
      render: (row: BlockedDomain) => (
        <Badge tone={row.admission === "admitted" ? "success" : "warn"}>
          {t(ADMISSION_LABEL[row.admission])}
        </Badge>
      ),
    },
    {
      key: "source",
      header: t("blockedDomains.col.source"),
      // A human decision is the one an operator is hunting for, so it is the
      // one the eye finds: the machine sources read as plain pills beside it.
      render: (row: BlockedDomain) => (
        <Badge tone={row.source === "human" ? "accent" : undefined}>
          {t(SOURCE_LABEL[row.source])}
        </Badge>
      ),
    },
    {
      key: "reason",
      header: t("blockedDomains.col.reason"),
      render: (row: BlockedDomain) => row.reason,
    },
    {
      key: "decided",
      header: t("blockedDomains.col.decided"),
      render: (row: BlockedDomain) => (
        <time dateTime={row.decided_at}>
          {formatDate(row.decided_at, locale, zone)}
        </time>
      ),
    },
    {
      key: "revise",
      header: t("blockedDomains.col.revise"),
      render: (row: BlockedDomain) => (
        <Button
          small
          variant="ghost"
          disabled={set.isPending}
          reasonId={refusal}
          onClick={() => revise(row)}
        >
          {t(
            row.admission === "suppressed"
              ? "blockedDomains.rowAdmit"
              : "blockedDomains.rowRefuse",
          )}
        </Button>
      ),
    },
  ];
}

/**
 * The write, in a dialog.
 *
 * It is only ever opened by a seat holding `organization:update` — both verbs
 * that open it are refused without the grant — so nothing in here restates the
 * denial. The mutation belongs to the CARD rather than to this component,
 * because what landed is reported after the dialog has closed.
 */
function DecisionDialog({
  initial,
  set,
  onClose,
}: Readonly<{ initial: Decision; set: SetDecision; onClose: () => void }>) {
  const t = useT();
  const headingId = useId();
  const [domain, setDomain] = useState(initial.domain);
  const [admission, setAdmission] = useState<Admission>(initial.admission);
  const [reason, setReason] = useState(initial.reason);
  const trimmedDomain = domain.trim();
  const trimmedReason = reason.trim();
  const ready = trimmedDomain !== "" && trimmedReason !== "";
  return (
    <Modal open onClose={onClose} labelledBy={headingId}>
      <h2 id={headingId} className="t-h2 modal-title">
        {t("blockedDomains.record")}
      </h2>
      <form
        className="form-stack"
        onSubmit={(event) => {
          event.preventDefault();
          if (!ready) {
            return;
          }
          set.mutate(
            {
              domain: trimmedDomain,
              admission,
              reason: trimmedReason,
            },
            { onSuccess: onClose },
          );
        }}
      >
        <div className="form-row">
          <Field label={t("blockedDomains.domainLabel")} required>
            {(control) => (
              <TextInput
                {...control}
                data-testid="blocked-domain-input"
                placeholder={t("blockedDomains.domainPlaceholder")}
                value={domain}
                onChange={(event) => setDomain(event.target.value)}
              />
            )}
          </Field>
          <Field label={t("blockedDomains.admissionLabel")}>
            {(control) => (
              <Select
                {...control}
                value={admission}
                onChange={(value) => {
                  if (isOption(value, ADMISSIONS)) {
                    setAdmission(value);
                  }
                }}
                options={ADMISSIONS.map((value) => ({
                  value,
                  label: t(ADMISSION_LABEL[value]),
                }))}
              />
            )}
          </Field>
        </div>
        <Field
          label={t("blockedDomains.reasonLabel")}
          hint={t("blockedDomains.reasonHint")}
          required
        >
          {(control) => (
            <TextInput
              {...control}
              data-testid="blocked-domain-reason"
              maxLength={REASON_MAX}
              placeholder={t("blockedDomains.reasonPlaceholder")}
              value={reason}
              onChange={(event) => setReason(event.target.value)}
            />
          )}
        </Field>
        {set.isError && (
          <Callout tone="danger" live="alert">
            {problemMessageOf(set.error, t)}
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
            disabled={set.isPending || !ready}
          >
            {t("blockedDomains.save")}
          </Button>
        </div>
      </form>
    </Modal>
  );
}
