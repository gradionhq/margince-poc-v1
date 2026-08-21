import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Trash2 } from "lucide-react";
import { useEffect, useId, useState } from "react";
import { api } from "../api/client";
import { useCanUpsert, useCanWrite } from "../app/capability";
import { isOption } from "../app/options";
import {
  Button,
  Disclosure,
  EmptyState,
  Field,
  Modal,
  TextInput,
} from "../design-system/atoms";
import { Callout } from "../design-system/callout";
import { Panel, PanelBody } from "../design-system/panel";
import { Select } from "../design-system/select";
import { SettingList, SettingRow } from "../design-system/settingrow";
import { useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { problemMessageOf, QueryGate, throwProblem } from "./common";
import { SEARCH_DEBOUNCE_MS } from "./listquery";
import "./consumer-mail-domains.css";

// This installation's own consumer-mail list (CAP-PARAM-5). Mail from a consumer
// domain still creates the person; what it never creates is a company. The
// shipped baseline is a third-party dataset of some 8 700 domains, right far
// more often than a hand-typed list and still wrong sometimes in both
// directions — so this is where an operator adds what it missed and takes back
// what it wrongly claimed. Every role reads it, and every role may search the
// shipped baseline itself, so the capture posture stays legible to the people
// whose mail it governs. The write split mirrors the server's: any seat with
// capture_settings:create adds a consumer domain the baseline missed (`extra`),
// while `never` carve-outs and removal stay on capture_settings:update
// (admin/ops) — those controls are refused rather than hidden.

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
// it. It is a lookup rather than a setting, so it sits in the card's
// disclosure — one line until asked for — and results render only once a filter
// is typed, because the first 50 of 8 700 alphabetical rows answer no question
// anyone is asking.
function BaselineRow() {
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
    <SettingList>
      <SettingRow
        label={t("consumerMail.baselineSearchLabel")}
        description={
          result && t("consumerMail.baselineCount", { total: result.total })
        }
        layout="stack"
        // The function form, so the words the row draws are also the name the
        // box announces — one string, written once.
        control={(control) => (
          <div className="consumer-mail-baseline settingrow-measure">
            <TextInput
              {...control}
              data-testid="consumer-mail-baseline-search"
              placeholder={t("consumerMail.baselinePlaceholder")}
              value={q}
              onChange={(e) => setQ(e.target.value)}
            />
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
          </div>
        )}
      />
    </SettingList>
  );
}

export function ConsumerMailDomainsCard() {
  const t = useT();
  // The write split mirrors the server's two demands. `canManage`
  // (capture_settings:update) covers what rewrites the installation's posture:
  // the `never` carve-out, overwriting an entry, removal. `canAdd` mirrors the
  // server's upsert admission (create OR update) — a rep holding only create
  // may still contribute a new `extra` domain, and the server demands the
  // specific grant once it knows which half the write is.
  const canManage = useCanWrite("capture_settings", "update");
  const canAdd = useCanUpsert("capture_settings");
  const query = useConsumerMailDomains();
  const remove = useRemoveConsumerMailDomain();
  const [adding, setAdding] = useState(false);
  // The denial, said once and POINTED AT — `Button`'s `reasonId` refuses the
  // control and names the one sentence already on the page, so several refused
  // verbs say it once instead of printing it beside each of them.
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

  return (
    <Panel
      title={t("consumerMail.title")}
      // Two inputs submitted together — the domain and what it IS — so the form
      // lives behind this verb, and the verb rides in the header rather than in
      // a row of its own: a row states a setting and its answer, and a row whose
      // label repeats its own button says the same thing twice a hand apart.
      // Refused, never hidden — `reasonId` names the sentence under the rows,
      // which is the one that knows WHICH of the two grants is missing.
      titleAction={
        <Button
          small
          reasonId={canAdd ? undefined : denialId}
          onClick={() => setAdding(true)}
        >
          {t("consumerMail.addOpen")}
        </Button>
      }
    >
      {/* `form-stack` stays: the denial sentence and the failure Callout under
          the rows are non-row children, and the list owns only the intervals
          BETWEEN its rows. */}
      <PanelBody className="form-stack">
        <p className="settings-panel-sub">{t("consumerMail.sub")}</p>
        <SettingList>
          {/* The entries are the subject of this card rather than an answer to
              a question beside them, so they take the row's full width. */}
          <SettingRow
            label={t("consumerMail.addedTitle")}
            layout="stack"
            control={
              <QueryGate query={query}>
                {(entries) =>
                  entries.length === 0 ? (
                    // `empty`, and only `empty`: nothing has been added, so the
                    // shipped list decides every domain. The row caps and
                    // left-aligns it already (settingrow.css).
                    <EmptyState>
                      <p className="t-small">{t("consumerMail.none")}</p>
                    </EmptyState>
                  ) : (
                    // One entry per row, in the row language the rest of this
                    // tab speaks: the domain names itself on the left, what it
                    // IS stands as the row's answer, and the verb that takes it
                    // back sits at one x down the list. It was a hand-rolled
                    // `<ul>` of four-item flex rows — and the mail glyph on
                    // every one of them distinguished nothing, since every row
                    // on this card is a mail domain.
                    <SettingList testId="consumer-mail-domain-list">
                      {entries.map((entry) => (
                        <SettingRow
                          key={entry.id}
                          label={entry.domain}
                          value={t(kindLabel[entry.kind])}
                          control={
                            <Button
                              variant="ghost"
                              small
                              aria-label={t("consumerMail.remove")}
                              disabled={remove.isPending}
                              reasonId={canManage ? undefined : denialId}
                              onClick={() => remove.mutate(entry.id)}
                            >
                              <Trash2 aria-hidden size={16} />
                            </Button>
                          }
                        />
                      ))}
                    </SettingList>
                  )
                }
              </QueryGate>
            }
          />
          {/* The shipped list is the SECOND question this card answers — what
              the baseline already says, against what this installation adds to
              it — and a reader consults it while deciding, not on every visit.
              A disclosure, so it costs one line until it is asked for. */}
          <Disclosure summary={t("consumerMail.baselineTitle")}>
            <BaselineRow />
          </Disclosure>
        </SettingList>
        {denial && (
          <p className="t-small" id={denialId}>
            {denial}
          </p>
        )}
        {remove.isError && (
          <Callout tone="danger" live="alert">
            {problemMessageOf(remove.error, t)}
          </Callout>
        )}
        {adding && (
          <AddConsumerMailDialog
            canManage={canManage}
            onClose={() => setAdding(false)}
          />
        )}
      </PanelBody>
    </Panel>
  );
}

// Mounted only while it is open, so a domain somebody started typing and
// abandoned is gone the next time the dialog opens rather than waiting there.
function AddConsumerMailDialog({
  canManage,
  onClose,
}: Readonly<{ canManage: boolean; onClose: () => void }>) {
  const t = useT();
  const add = useAddConsumerMailDomain();
  const headingId = useId();
  // The kind stays on the update grant: `never` overrides the shipped baseline
  // for the whole installation, so a create-only seat submits the initial
  // `extra` and never reaches the carve-out. The sentence saying so lives in
  // here with the control it refuses — a reason outside the dialog is a reason
  // a reader inside it never gets.
  const carveOutDenialId = useId();
  const [domain, setDomain] = useState("");
  const [kind, setKind] = useState<Kind>("extra");
  const typed = domain.trim();
  return (
    <Modal open onClose={onClose} labelledBy={headingId}>
      <h2 id={headingId} className="t-h2 modal-title">
        {t("consumerMail.addTitle")}
      </h2>
      <form
        className="form-stack"
        onSubmit={(e) => {
          e.preventDefault();
          if (typed === "") {
            return;
          }
          add.mutate({ domain: typed, kind }, { onSuccess: onClose });
        }}
      >
        <Field label={t("consumerMail.domainLabel")}>
          {(control) => (
            <TextInput
              {...control}
              data-testid="consumer-mail-domain-input"
              placeholder={t("consumerMail.domainPlaceholder")}
              value={domain}
              onChange={(e) => setDomain(e.target.value)}
            />
          )}
        </Field>
        <Field label={t("consumerMail.kindLabel")}>
          {(control) => (
            <Select
              {...control}
              value={kind}
              disabled={!canManage}
              aria-describedby={canManage ? undefined : carveOutDenialId}
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
        {!canManage && (
          <p className="t-small" id={carveOutDenialId}>
            {t("consumerMail.addOnly")}
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
            disabled={add.isPending || typed === ""}
          >
            {t("consumerMail.add")}
          </Button>
        </div>
      </form>
    </Modal>
  );
}
