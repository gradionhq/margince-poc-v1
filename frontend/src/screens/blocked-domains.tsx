import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useCallback, useId, useRef, useState } from "react";
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
  TextInput,
} from "../design-system/atoms";
import { Callout } from "../design-system/callout";
import { CountLine } from "../design-system/listsurface";
import { Panel, PanelBody } from "../design-system/panel";
import { Select } from "../design-system/select";
import { formatDate } from "../format/format";
import { useLocale, useT } from "../i18n";
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
// entry demands `organization:update`, so the controls disable rather than hide,
// like the capture cards beside it.
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
    mutationFn: async (
      decision: Readonly<{
        domain: string;
        admission: Admission;
        reason: string;
      }>,
    ) => {
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

/**
 * Every description a control here can carry, in one attribute.
 *
 * `Field` names its own hint in `aria-describedby`, and this card also has one
 * sentence saying the seat may not write. Overriding the attribute would pick
 * which of the two a screen reader gets; a caller that has both owes the
 * control both.
 */
function describedBy(
  ...ids: readonly (string | undefined)[]
): string | undefined {
  return (
    ids.filter((id): id is string => id !== undefined).join(" ") || undefined
  );
}

export function BlockedDomainsCard() {
  const t = useT();
  const { locale } = useLocale();
  // An operator investigating a company that never appeared reads the decision
  // time on their own wall clock, the same choice the audit trail makes — a
  // fixed workspace zone would put the moment they are correlating against an
  // hour they were not working.
  const zone = Intl.DateTimeFormat().resolvedOptions().timeZone;
  const canManage = useCanWrite("organization", "update");
  const query = useBlockedDomains();
  const set = useSetBlockedDomain();
  const [domain, setDomain] = useState("");
  const [admission, setAdmission] = useState<Admission>("suppressed");
  const [reason, setReason] = useState("");
  const reasonInput = useRef<HTMLInputElement>(null);
  // The denial, said once and POINTED AT — the wiring `Switch`'s `reason` prop
  // does for a toggle (design-system README), spelled with an id and
  // `aria-describedby` for the controls that are not switches. A reason
  // floating below the form is one a screen reader never reads out with the
  // control it explains. The id is minted unconditionally, because a hook may
  // not depend on a permission.
  const denialId = useId();
  const denial = canManage ? undefined : denialId;

  // Revising a standing decision starts on its row and finishes in the form.
  // The contract requires a reason for every write, so a one-click flip has
  // nowhere to get one — and a refusal nobody can explain is one nobody can
  // review, which is the whole point of the column. The row therefore hands
  // the form the domain and the OPPOSITE decision, clears the reason, and puts
  // the cursor where the operator now has to type: that is the McKinsey case, a
  // newsletter publisher that became a client.
  const revise = useCallback((entry: BlockedDomain) => {
    setDomain(entry.domain);
    setAdmission(entry.admission === "suppressed" ? "admitted" : "suppressed");
    setReason("");
    reasonInput.current?.focus();
  }, []);

  const columns = [
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
          disabled={!canManage || set.isPending}
          aria-describedby={denial}
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

  const trimmedDomain = domain.trim();
  const trimmedReason = reason.trim();

  return (
    <Panel title={t("blockedDomains.title")}>
      <PanelBody className="form-stack">
        <p className="t-caption">{t("blockedDomains.sub")}</p>
        <form
          className="form-stack"
          onSubmit={(event) => {
            event.preventDefault();
            if (!canManage || trimmedDomain === "" || trimmedReason === "") {
              return;
            }
            set.mutate({
              domain: trimmedDomain,
              admission,
              reason: trimmedReason,
            });
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
                  disabled={!canManage}
                  aria-describedby={describedBy(
                    control["aria-describedby"],
                    denial,
                  )}
                  onChange={(event) => setDomain(event.target.value)}
                />
              )}
            </Field>
            <Field label={t("blockedDomains.admissionLabel")}>
              {(control) => (
                <Select
                  {...control}
                  value={admission}
                  disabled={!canManage}
                  aria-describedby={describedBy(
                    control["aria-describedby"],
                    denial,
                  )}
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
                ref={reasonInput}
                data-testid="blocked-domain-reason"
                maxLength={REASON_MAX}
                placeholder={t("blockedDomains.reasonPlaceholder")}
                value={reason}
                disabled={!canManage}
                aria-describedby={describedBy(
                  control["aria-describedby"],
                  denial,
                )}
                onChange={(event) => setReason(event.target.value)}
              />
            )}
          </Field>
          <div className="form-actions">
            <Button
              type="submit"
              variant="primary"
              disabled={
                !canManage ||
                set.isPending ||
                trimmedDomain === "" ||
                trimmedReason === ""
              }
              aria-describedby={denial}
            >
              {t("blockedDomains.save")}
            </Button>
          </div>
        </form>
        {!canManage && (
          <p className="t-small" id={denialId}>
            {t("blockedDomains.adminOnly")}
          </p>
        )}
        {set.isError && (
          <Callout tone="danger" live="alert">
            {problemMessageOf(set.error, t)}
          </Callout>
        )}
        {/* What LANDED, named. The server normalizes the domain to its
            registrable form and the write replaces any entry already on it, so
            without this a sub-domain silently became its parent and a second
            decision on a domain already listed looked like nothing had
            happened at all. */}
        {set.data && (
          <Callout tone="success" live="status">
            {t("blockedDomains.stored", {
              domain: set.data.domain,
              admission: t(ADMISSION_LABEL[set.data.admission]),
            })}
          </Callout>
        )}
        <QueryGate query={query}>
          {(list) =>
            list.data.length === 0 ? (
              // `empty`, and only `empty`: no decision has been recorded, which
              // is a fact about the installation rather than a read that failed.
              // The states that are not this one are the query gate's above.
              <EmptyState>
                <p className="t-small">{t("blockedDomains.none")}</p>
              </EmptyState>
            ) : (
              <>
                <DataTable
                  columns={columns}
                  rows={list.data}
                  rowKey={(row) => row.domain}
                />
                {/* Refusals accumulate on their own from every bulk-sender
                    verdict, so the server pages the list and answers with how
                    many decisions EXIST. An operator hunting a company that
                    never appeared has to be able to tell "not refused" from
                    "past the end of this page". */}
                <p className="t-small">
                  <CountLine
                    unit={t("blockedDomains.unit")}
                    first={1}
                    last={list.data.length}
                    total={list.total}
                  />
                </p>
              </>
            )
          }
        </QueryGate>
      </PanelBody>
    </Panel>
  );
}
