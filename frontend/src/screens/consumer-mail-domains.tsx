import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Mail, Trash2 } from "lucide-react";
import { useEffect, useId, useState } from "react";
import { api } from "../api/client";
import { useCanUpsert, useCanWrite } from "../app/capability";
import { isOption } from "../app/options";
import { Button, Field, TextInput } from "../design-system/atoms";
import { Callout } from "../design-system/callout";
import { Eyebrow } from "../design-system/eyebrow";
import { Panel, PanelBody } from "../design-system/panel";
import { Select } from "../design-system/select";
import { useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { problemMessageOf, QueryGate, throwProblem } from "./common";
import { SEARCH_DEBOUNCE_MS } from "./listquery";
import "./consumer-mail-domains.css";

// The workspace's own consumer-mail list (CAP-PARAM-5). Mail from a consumer
// domain still creates the person; what it never creates is a company. The
// shipped baseline is a third-party dataset of some 8 700 domains, right far
// more often than a hand-typed list and still wrong sometimes in both
// directions — so this is where an operator adds what it missed and takes back
// what it wrongly claimed. Every role reads it, and every role may search the
// shipped baseline itself, so the capture posture stays legible to the people
// whose mail it governs. The write split mirrors the server's: any seat with
// capture_settings:create adds a consumer domain the baseline missed (`extra`),
// while `never` carve-outs and removal stay on capture_settings:update
// (admin/ops) — those controls disable rather than hide.

// The two things an entry can say, as ONE list: the type is derived from it and
// the control's options are built from it, so the offered choices, their labels
// and the runtime narrowing cannot drift apart (same shape as overlay.tsx's
// region list).
const KINDS = ["extra", "never"] as const;
type Kind = (typeof KINDS)[number];
const kindLabel: Record<Kind, MessageKey> = {
  extra: "consumerMail.kind.extra",
  never: "consumerMail.kind.never",
};

function useConsumerMailBaseline(q: string) {
  return useQuery({
    queryKey: ["consumer-mail-baseline", q],
    queryFn: async () => {
      const { data, error, response } = await api.GET(
        "/capture/consumer-mail-baseline",
        { params: { query: q ? { q } : {} } },
      );
      if (error || !response.ok) {
        throwProblem(error);
      }
      return data;
    },
  });
}

function useConsumerMailDomains() {
  return useQuery({
    queryKey: ["consumer-mail-domains"],
    queryFn: async () => {
      const { data, error, response } = await api.GET(
        "/capture/consumer-mail-domains",
      );
      if (error || !response.ok) {
        throwProblem(error);
      }
      return data.data;
    },
  });
}

function useAddConsumerMailDomain() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (entry: { domain: string; kind: Kind }) => {
      const { data, error } = await api.POST("/capture/consumer-mail-domains", {
        body: entry,
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
    onSuccess: () => {
      void queryClient.invalidateQueries({
        queryKey: ["consumer-mail-domains"],
      });
    },
  });
}

function useRemoveConsumerMailDomain() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (id: string) => {
      const { error } = await api.DELETE(
        "/capture/consumer-mail-domains/{id}",
        { params: { path: { id } } },
      );
      if (error) {
        throwProblem(error);
      }
    },
    onSuccess: () => {
      void queryClient.invalidateQueries({
        queryKey: ["consumer-mail-domains"],
      });
    },
  });
}

/** A typed value held back until the typing stops, so it can be a query key. */
function useSettledSearch(typed: string): string {
  const [settled, setSettled] = useState(typed);
  useEffect(() => {
    const timer = setTimeout(() => setSettled(typed), SEARCH_DEBOUNCE_MS);
    return () => clearTimeout(timer);
  }, [typed]);
  return settled;
}

// The shipped baseline, searchable in place: an operator deciding whether a
// domain needs an entry first sees what the shipped list already says about
// it. Results render only once a filter is typed — the first 50 of 8 700
// alphabetical rows answer no question anyone is asking.
function BaselineSection() {
  const t = useT();
  const [q, setQ] = useState("");
  // The field shows what is being typed; the SEARCH is what has settled. `q`
  // is the query key, so without this every character was its own request over
  // a list of 8 700 domains — and the answers could land out of order, leaving
  // the results of a prefix under the word the reader had finished typing. The
  // shared list surface settles on the same constant.
  const needle = useSettledSearch(q.trim());
  const query = useConsumerMailBaseline(needle);
  const result = query.data;
  return (
    <PanelBody className="consumer-mail-baseline">
      {/* A real heading, one level under the panel's own title, rather than a
          paragraph styled to look like one — a reader navigating by heading can
          reach the shipped list without scrolling the card. */}
      <Eyebrow as="h3">{t("consumerMail.baselineTitle")}</Eyebrow>
      {result && (
        <p className="t-small">
          {t("consumerMail.baselineCount", { total: result.total })}
        </p>
      )}
      {/* Field, not a bare aria-label: it owns the id and draws a real
          `<label for>`, so the words above the box are also the box's click
          target and its accessible name. */}
      <Field label={t("consumerMail.baselineSearchLabel")}>
        {(control) => (
          <TextInput
            {...control}
            data-testid="consumer-mail-baseline-search"
            placeholder={t("consumerMail.baselinePlaceholder")}
            value={q}
            onChange={(e) => setQ(e.target.value)}
          />
        )}
      </Field>
      {needle !== "" && result && result.matched === 0 && (
        <p className="t-small">{t("consumerMail.baselineNone")}</p>
      )}
      {needle !== "" && result && result.matched > 0 && (
        <>
          <ul
            className="consumer-mail-baseline-list"
            data-testid="consumer-mail-baseline-list"
          >
            {result.data.map((domain) => (
              <li key={domain} className="t-mono t-small">
                {domain}
              </li>
            ))}
          </ul>
          {result.matched > result.data.length && (
            <p className="t-small">
              {t("consumerMail.baselineMore", {
                shown: result.data.length,
                matched: result.matched,
              })}
            </p>
          )}
        </>
      )}
    </PanelBody>
  );
}

export function ConsumerMailDomainsCard() {
  const t = useT();
  // The write split mirrors the server's two demands. `canManage`
  // (capture_settings:update) covers what rewrites workspace posture: the
  // `never` carve-out, overwriting an entry, removal. `canAdd` mirrors the
  // server's upsert admission (create OR update) — a rep holding only create
  // may still contribute a new `extra` domain, and the server demands the
  // specific grant once it knows which half the write is.
  const canManage = useCanWrite("capture_settings", "update");
  const canAdd = useCanUpsert("capture_settings");
  const query = useConsumerMailDomains();
  const add = useAddConsumerMailDomain();
  const remove = useRemoveConsumerMailDomain();
  const [domain, setDomain] = useState("");
  const [kind, setKind] = useState<Kind>("extra");
  // The denial, said once and POINTED AT — the same wiring `Switch`'s `reason`
  // prop does for a toggle (design-system README), spelled with an id and
  // `aria-describedby` for the controls that are not switches. A reason
  // floating below the form is a reason a screen reader never reads out with
  // the control it explains.
  //
  // Two grants, so two sentences, and a reader gets exactly one of them: no
  // create at all means nothing on this card writes, while create-without-
  // update means the carve-out and removal are what is refused. The id is
  // minted unconditionally, because a hook may not depend on a permission.
  const denialId = useId();
  const denial = !canAdd
    ? t("consumerMail.adminOnly")
    : canManage
      ? undefined
      : t("consumerMail.addOnly");
  // Named only on the controls the denial actually disables: pointing an
  // enabled field at "you may only add" describes a refusal that is not
  // happening to it.
  const addDescribedBy = canAdd ? undefined : denialId;
  const manageDescribedBy = canManage ? undefined : denialId;

  return (
    <Panel title={t("consumerMail.title")}>
      <PanelBody className="form-stack">
        <p className="t-caption">{t("consumerMail.sub")}</p>
        <form
          className="consumer-mail-add"
          onSubmit={(e) => {
            e.preventDefault();
            if (!canAdd || domain.trim() === "") return;
            add.mutate(
              { domain: domain.trim(), kind },
              { onSuccess: () => setDomain("") },
            );
          }}
        >
          <Field label={t("consumerMail.domainLabel")}>
            {(control) => (
              <TextInput
                {...control}
                data-testid="consumer-mail-domain-input"
                placeholder={t("consumerMail.domainPlaceholder")}
                value={domain}
                disabled={!canAdd}
                aria-describedby={addDescribedBy}
                onChange={(e) => setDomain(e.target.value)}
              />
            )}
          </Field>
          {/* The kind stays on the update grant: `never` overrides the shipped
              baseline for the whole workspace, so a create-only seat submits the
              initial `extra` and never reaches the carve-out. */}
          <Field label={t("consumerMail.kindLabel")}>
            {(control) => (
              <Select
                {...control}
                className="consumer-mail-kind"
                value={kind}
                disabled={!canManage}
                aria-describedby={manageDescribedBy}
                onChange={(value) => {
                  if (isOption(value, KINDS)) {
                    setKind(value);
                  }
                }}
                options={KINDS.map((value) => ({
                  value,
                  label: t(kindLabel[value]),
                }))}
              />
            )}
          </Field>
          <Button
            type="submit"
            variant="primary"
            disabled={!canAdd || add.isPending}
            aria-describedby={addDescribedBy}
          >
            {t("consumerMail.add")}
          </Button>
        </form>
        {denial && (
          <p className="t-small" id={denialId}>
            {denial}
          </p>
        )}
        {add.isError && (
          <Callout tone="danger" live="alert">
            {problemMessageOf(add.error, t)}
          </Callout>
        )}
        <QueryGate query={query}>
          {(entries) =>
            entries.length === 0 ? (
              <p className="t-small">{t("consumerMail.none")}</p>
            ) : (
              <ul
                className="consumer-mail-list"
                data-testid="consumer-mail-domain-list"
              >
                {entries.map((entry) => (
                  <li
                    key={entry.id}
                    className="consumer-mail-row"
                    data-kind={entry.kind}
                  >
                    <Mail aria-hidden size={16} />
                    <span className="consumer-mail-row-domain">
                      {entry.domain}
                    </span>
                    <span className="t-small">
                      {entry.kind === "never"
                        ? t("consumerMail.kind.never")
                        : t("consumerMail.kind.extra")}
                    </span>
                    <Button
                      variant="ghost"
                      small
                      aria-label={t("consumerMail.remove")}
                      disabled={!canManage || remove.isPending}
                      aria-describedby={manageDescribedBy}
                      onClick={() => remove.mutate(entry.id)}
                    >
                      <Trash2 aria-hidden size={16} />
                    </Button>
                  </li>
                ))}
              </ul>
            )
          }
        </QueryGate>
        {remove.isError && (
          <Callout tone="danger" live="alert">
            {problemMessageOf(remove.error, t)}
          </Callout>
        )}
      </PanelBody>
      {/* The shipped list is a SECOND section of this card, not a footnote
          inside the first: what the baseline already says, against what this
          workspace adds to it. Its own body, so the boundary is the full-bleed
          seam panel.css draws between two of them rather than a line that stops
          20px short of the card's edges. */}
      <BaselineSection />
    </Panel>
  );
}
