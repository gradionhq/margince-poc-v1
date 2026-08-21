import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useId, useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { useCan, useCanUpsert } from "../app/capability";
import {
  Button,
  DataTable,
  EmptyState,
  Field,
  Modal,
  TextInput,
} from "../design-system/atoms";
import { Callout } from "../design-system/callout";
import { Panel, PanelBody } from "../design-system/panel";
import { SettingList, SettingRow } from "../design-system/settingrow";
import { useT } from "../i18n";
import { problemMessageOf, QueryGate, throwProblem, useMe } from "./common";
import "./rates.css";

type FxRate = components["schemas"]["FxRate"];
type AiModelRate = components["schemas"]["AiModelRate"];

function today(): string {
  return new Date().toISOString().slice(0, 10);
}

// trimDecimal drops trailing zeros (and a bare trailing dot) so a
// numeric(20,10) value like "0.9200000000" reads as "0.92".
function trimDecimal(value: string): string {
  if (!value.includes(".")) {
    return value;
  }
  return value.replace(/0+$/, "").replace(/\.$/, "");
}

// RefreshFromSources enqueues an async refresh that stages proposals into the
// approvals inbox (the job runs in the background — nothing to poll here). The
// button stays available so an admin can trigger another refresh; success and
// failure both surface next to it.
function RefreshFromSources({
  path,
}: Readonly<{
  path: "/fx-rates/propose-refresh" | "/ai-model-rates/propose-refresh";
}>) {
  const t = useT();
  const refresh = useMutation({
    mutationFn: async () => {
      const { error } = await api.POST(path);
      if (error) {
        throwProblem(error);
      }
    },
  });
  return (
    <span className="rates-refresh">
      <Button
        variant="ghost"
        small
        onClick={() => refresh.mutate()}
        disabled={refresh.isPending}
      >
        {t("settings.rates.refresh")}
      </Button>
      {refresh.isSuccess ? (
        <span className="t-small" role="status">
          {t("settings.rates.refreshEnqueued")}
        </span>
      ) : null}
      {/* The failure is spoken, and the retry is the button it sits beside —
          which stays enabled, so the reader does not need a second control that
          would do the same thing. */}
      {refresh.isError ? (
        <span className="rates-error" role="alert">
          {problemMessageOf(refresh.error, t)}
        </span>
      ) : null}
    </span>
  );
}

// Withheld, not absent (design-system README, "Absent, disabled, or withheld"):
// a PERMISSION is what denies a price sheet — fx_rate and ai_model_rate grants
// exist only for admin and ops — so the card keeps its place and says so. Both
// sheets sit on settings pages other roles open for their other cards (currency
// rates on Organization, model prices on AI), and a sheet that vanished there
// would read as "this installation has no rates" rather than "not yours to see".
//
// It asks the server for NOTHING: each caller keeps `enabled: canRead` on its
// list query, because the answer is already known and fetching the 403 in order
// to render it turns a settled denial into red error text with a Retry that can
// only be refused again. Gated on the /me probe so the notice waits for the
// grants instead of flashing at every reader while they are still in flight.
function WithheldRateCard({
  title,
  reason,
}: Readonly<{ title: string; reason: string }>) {
  const me = useMe();
  return (
    <Panel title={title}>
      <PanelBody>
        <QueryGate query={me}>
          {() => (
            <EmptyState>
              <p className="t-small">{reason}</p>
            </EmptyState>
          )}
        </QueryGate>
      </PanelBody>
    </Panel>
  );
}

// ---- FX rates ----

export function FxRatesCard() {
  const t = useT();
  // Reading the sheet and authoring it are separate grants, so the card asks
  // for each one where it needs it.
  //
  // `useCan` for the read and `useCanUpsert` for the write, which additionally
  // folds the licensing seat — a difference that decides the read-seat case:
  // an admin on a read seat still holds fx_rate:read, so they get the table and
  // the read-only caption below, never the withheld body. Their denial is about
  // WRITING, and saying "only an admin or ops can see this" to an admin who is
  // looking at it would be a lie.
  const canRead = useCan("fx_rate", "read");
  // Either write grant, because the endpoint is one upsert: setting a rate for
  // a (currency, day) inserts under fx_rate:create and replaces an existing one
  // under fx_rate:update, and which it will be is only known once the server
  // has read the row. It admits on either and demands the specific verb inside
  // the transaction, so asking for one here would hide the editor from a
  // principal the server would have let write.
  const canManage = useCanUpsert("fx_rate");
  const [open, setOpen] = useState(false);
  const query = useQuery({
    queryKey: ["fx-rates"],
    enabled: canRead,
    queryFn: async () => {
      const { data, error } = await api.GET("/fx-rates", {
        params: { query: {} },
      });
      if (error) {
        throwProblem(error);
      }
      return data.data;
    },
  });

  // After every hook, so the number of hooks a render runs stays the same. A
  // principal holding a write verb but not the read lands here too: without the
  // sheet there is nothing to author a new rate against, so the read decides
  // whether this card exists at all.
  if (!canRead) {
    return (
      <WithheldRateCard
        title={t("settings.rates.fxTitle")}
        reason={t("settings.rates.fxWithheld")}
      />
    );
  }

  return (
    // The two verbs ride in the panel's own action band under the sheet they
    // change, not beside the title. Beside the title they were one unwrappable
    // row sized to its max content: at 390px the pair measured 353px inside a
    // 324px card, pushed the page 12px past the viewport and squeezed the
    // title to nothing. The band wraps, so the same two controls cannot widen
    // the card at any viewport.
    <Panel
      title={t("settings.rates.fxTitle")}
      actions={
        canManage ? (
          <>
            <RefreshFromSources path="/fx-rates/propose-refresh" />
            <Button variant="primary" small onClick={() => setOpen(true)}>
              {t("settings.rates.fxAdd")}
            </Button>
          </>
        ) : null
      }
    >
      <PanelBody className="form-stack">
        <p className="settings-panel-sub">{t("settings.rates.fxIntro")}</p>
        {/* A sheet whose write affordances are all withheld says so ONCE, here,
            rather than annotating each absent control. The rule (design-system
            README): a permission-withheld SURFACE states it, while individual
            write affordances inside a readable surface may simply be absent —
            provided the surface has said what a reader is looking at. Without
            this the page was a rate table with no editor and no reason given,
            which reads as a bug rather than as a permission.
            This is the READ-GRANTED case alone: the withheld branch has already
            returned, so `!canManage` here means the reader may see the sheet and
            not change it — no write verb on the object, or a read licensing seat.
            On the withheld body these two lines would explain one denial twice,
            in two different ways. */}
        {!canManage && (
          <p className="t-caption">{t("settings.rates.readOnly")}</p>
        )}
        <SettingList>
          {/* The sheet IS this card's subject rather than an answer that fits
              beside a label, so it takes the full width below the naming
              (design-system README, SettingRow: what picks `stack` is
              complexity, not size). The row names what a reader is looking at
              — the rates currently in force — which the table's own column
              headers do not say. */}
          <SettingRow
            label={t("settings.rates.fxTableLabel")}
            layout="stack"
            control={
              <QueryGate query={query}>
                {(rows) =>
                  rows.length === 0 ? (
                    <EmptyState>
                      <b>{t("settings.rates.fxEmpty")}</b>
                    </EmptyState>
                  ) : (
                    <DataTable<FxRate>
                      label={t("settings.rates.fxTableLabel")}
                      rows={rows}
                      rowKey={(row) => row.from_currency}
                      columns={[
                        {
                          key: "from",
                          header: t("settings.rates.colFrom"),
                          render: (row) => row.from_currency,
                        },
                        {
                          key: "rate",
                          header: t("settings.rates.colRate", {
                            base: rows[0]?.to_currency ?? "",
                          }),
                          render: (row) => trimDecimal(row.rate),
                        },
                        {
                          key: "effective",
                          header: t("settings.rates.colEffective"),
                          render: (row) => row.effective_date,
                        },
                      ]}
                    />
                  )
                }
              </QueryGate>
            }
          />
        </SettingList>
        {open ? <FxRateModal onClose={() => setOpen(false)} /> : null}
      </PanelBody>
    </Panel>
  );
}

function FxRateModal({ onClose }: Readonly<{ onClose: () => void }>) {
  const t = useT();
  const qc = useQueryClient();
  const labelId = useId();
  const [from, setFrom] = useState("");
  const [rate, setRate] = useState("");
  const [effectiveDate, setEffectiveDate] = useState(today());
  const [error, setError] = useState<string | null>(null);

  const save = useMutation({
    mutationFn: async () => {
      const { error: err } = await api.POST("/fx-rates", {
        body: {
          from_currency: from.trim().toUpperCase(),
          rate: rate.trim(),
          effective_date: effectiveDate,
        },
      });
      if (err) {
        throwProblem(err);
      }
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["fx-rates"] });
      onClose();
    },
    onError: (err: Error) => setError(problemMessageOf(err, t)),
  });

  return (
    <Modal open onClose={onClose} labelledBy={labelId}>
      {/* A dialog is portalled to the body, so it is its own region and its
          title starts the outline at level 2 — the spelling ConfirmModal uses
          for every other dialog in the tree. `.modal-title` is the catalog's
          own name for the interval under it. */}
      <h2 id={labelId} className="t-h2 modal-title">
        {t("settings.rates.fxModalTitle")}
      </h2>
      {/* `Field` owns each box's id and hands it to the input, so the label a
          reader sees and the name the control announces are one string written
          once. The stack owns the interval between them: a `.field` sets no
          margin of its own, deliberately, and a dialog is not the place to
          invent a second answer to that. */}
      <div className="form-stack">
        <Field label={t("settings.rates.colFrom")}>
          {(control) => (
            <TextInput
              {...control}
              value={from}
              maxLength={3}
              onChange={(e) => setFrom(e.target.value)}
            />
          )}
        </Field>
        <Field label={t("settings.rates.rateToBase")}>
          {(control) => (
            <TextInput
              {...control}
              value={rate}
              inputMode="decimal"
              placeholder="0.92"
              onChange={(e) => setRate(e.target.value)}
            />
          )}
        </Field>
        <Field label={t("settings.rates.colEffective")}>
          {(control) => (
            <TextInput
              {...control}
              type="date"
              min={today()}
              value={effectiveDate}
              onChange={(e) => setEffectiveDate(e.target.value)}
            />
          )}
        </Field>
        {/* The failure is a live region, not tinted text: a message that
            appears after the reader has pressed Save is one they are not
            looking at. The dialog stays open behind it, so the retry is the
            same button. */}
        {error ? (
          <Callout tone="danger" live="alert">
            {error}
          </Callout>
        ) : null}
        <div className="form-actions">
          <Button small variant="ghost" onClick={onClose}>
            {t("create.cancel")}
          </Button>
          <Button
            small
            variant="primary"
            onClick={() => {
              setError(null);
              save.mutate();
            }}
            disabled={
              save.isPending || from.trim() === "" || rate.trim() === ""
            }
          >
            {t("settings.rates.setRate")}
          </Button>
        </div>
      </div>
    </Modal>
  );
}

// ---- AI model costs ----

export function ModelCostsCard() {
  const t = useT();
  // Read and write asked separately, for the same reasons as FxRatesCard above —
  // including the read seat, which withholds the WRITE from an admin who plainly
  // still reads the sheet.
  const canRead = useCan("ai_model_rate", "read");
  // Either write grant, for the same reason as FxRatesCard above: one endpoint,
  // insert or replace, the specific verb resolved inside the transaction.
  const canManage = useCanUpsert("ai_model_rate");
  const [open, setOpen] = useState(false);
  const query = useQuery({
    queryKey: ["ai-model-rates"],
    enabled: canRead,
    queryFn: async () => {
      const { data, error } = await api.GET("/ai-model-rates", {
        params: { query: {} },
      });
      if (error) {
        throwProblem(error);
      }
      return data.data;
    },
  });

  // After every hook, as in FxRatesCard.
  if (!canRead) {
    return (
      <WithheldRateCard
        title={t("settings.rates.modelTitle")}
        reason={t("settings.rates.modelWithheld")}
      />
    );
  }

  return (
    // The verbs in the action band, for the reason spelled out on FxRatesCard:
    // beside the title they were an unwrappable row that widened the card past
    // a 390px viewport.
    <Panel
      title={t("settings.rates.modelTitle")}
      actions={
        canManage ? (
          <>
            <RefreshFromSources path="/ai-model-rates/propose-refresh" />
            <Button variant="primary" small onClick={() => setOpen(true)}>
              {t("settings.rates.modelAdd")}
            </Button>
          </>
        ) : null
      }
    >
      <PanelBody className="form-stack">
        <p className="settings-panel-sub">{t("settings.rates.modelIntro")}</p>
        {/* A sheet whose write affordances are all withheld says so ONCE, here,
            rather than annotating each absent control. The rule (design-system
            README): a permission-withheld SURFACE states it, while individual
            write affordances inside a readable surface may simply be absent —
            provided the surface has said what a reader is looking at. Without
            this the page was a rate table with no editor and no reason given,
            which reads as a bug rather than as a permission.
            This is the READ-GRANTED case alone: the withheld branch has already
            returned, so `!canManage` here means the reader may see the sheet and
            not change it — no write verb on the object, or a read licensing seat.
            On the withheld body these two lines would explain one denial twice,
            in two different ways. */}
        {!canManage && (
          <p className="t-caption">{t("settings.rates.readOnly")}</p>
        )}
        <SettingList>
          {/* Stacked for the reason spelled out on FxRatesCard: the price sheet
              is the subject, and this row names which prices they are. */}
          <SettingRow
            label={t("settings.rates.modelTableLabel")}
            layout="stack"
            control={
              <QueryGate query={query}>
                {(rows) =>
                  rows.length === 0 ? (
                    <EmptyState>
                      <b>{t("settings.rates.modelEmpty")}</b>
                    </EmptyState>
                  ) : (
                    <DataTable<AiModelRate>
                      label={t("settings.rates.modelTableLabel")}
                      rows={rows}
                      rowKey={(row) => `${row.provider}/${row.model_id}`}
                      columns={[
                        {
                          key: "provider",
                          header: t("settings.rates.colProvider"),
                          render: (row) => row.provider,
                        },
                        {
                          key: "model",
                          header: t("settings.rates.colModel"),
                          render: (row) => row.model_id,
                        },
                        {
                          key: "in",
                          header: t("settings.rates.colInput"),
                          render: (row) => row.input_per_mtok,
                        },
                        {
                          key: "out",
                          header: t("settings.rates.colOutput"),
                          render: (row) => row.output_per_mtok,
                        },
                        {
                          key: "cr",
                          header: t("settings.rates.colCacheRead"),
                          render: (row) => row.cache_read_per_mtok,
                        },
                        {
                          key: "cw",
                          header: t("settings.rates.colCacheWrite"),
                          render: (row) => row.cache_write_per_mtok,
                        },
                        {
                          key: "effective",
                          header: t("settings.rates.colEffective"),
                          render: (row) => row.effective_date,
                        },
                      ]}
                    />
                  )
                }
              </QueryGate>
            }
          />
        </SettingList>
        {open ? <ModelCostModal onClose={() => setOpen(false)} /> : null}
      </PanelBody>
    </Panel>
  );
}

function ModelCostModal({ onClose }: Readonly<{ onClose: () => void }>) {
  const t = useT();
  const qc = useQueryClient();
  const labelId = useId();
  const [provider, setProvider] = useState("");
  const [modelId, setModelId] = useState("");
  const [input, setInput] = useState("");
  const [output, setOutput] = useState("");
  const [cacheRead, setCacheRead] = useState("0");
  const [cacheWrite, setCacheWrite] = useState("0");
  const [effectiveDate, setEffectiveDate] = useState(today());
  const [error, setError] = useState<string | null>(null);

  const save = useMutation({
    mutationFn: async () => {
      const { error: err } = await api.POST("/ai-model-rates", {
        body: {
          provider: provider.trim(),
          model_id: modelId.trim(),
          input_per_mtok: input.trim(),
          output_per_mtok: output.trim(),
          cache_read_per_mtok: cacheRead.trim() || "0",
          cache_write_per_mtok: cacheWrite.trim() || "0",
          effective_date: effectiveDate,
        },
      });
      if (err) {
        throwProblem(err);
      }
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["ai-model-rates"] });
      onClose();
    },
    onError: (err: Error) => setError(problemMessageOf(err, t)),
  });

  // One box of this dialog. `Field` owns the id and hands it to the input, so
  // what a call site passes is only what differs between the seven: the label,
  // the value, where it goes, and whether it takes words or a number.
  const field = (
    label: string,
    value: string,
    set: (v: string) => void,
    opts: Readonly<{
      inputMode?: "text" | "decimal";
      placeholder?: string;
    }> = {},
  ) => (
    <Field label={label}>
      {(control) => (
        <TextInput
          {...control}
          value={value}
          inputMode={opts.inputMode ?? "decimal"}
          placeholder={opts.placeholder ?? ""}
          onChange={(e) => set(e.target.value)}
        />
      )}
    </Field>
  );

  return (
    <Modal open onClose={onClose} labelledBy={labelId}>
      <h2 id={labelId} className="t-h2 modal-title">
        {t("settings.rates.modelModalTitle")}
      </h2>
      <div className="form-stack">
        {field(t("settings.rates.colProvider"), provider, setProvider, {
          inputMode: "text",
        })}
        {field(t("settings.rates.colModel"), modelId, setModelId, {
          inputMode: "text",
        })}
        {field(t("settings.rates.colInput"), input, setInput, {
          placeholder: "5.00",
        })}
        {field(t("settings.rates.colOutput"), output, setOutput, {
          placeholder: "25.00",
        })}
        {field(t("settings.rates.colCacheRead"), cacheRead, setCacheRead)}
        {field(t("settings.rates.colCacheWrite"), cacheWrite, setCacheWrite)}
        <Field label={t("settings.rates.colEffective")}>
          {(control) => (
            <TextInput
              {...control}
              type="date"
              min={today()}
              value={effectiveDate}
              onChange={(e) => setEffectiveDate(e.target.value)}
            />
          )}
        </Field>
        {/* The failure is a live region, not tinted text: a message that
            appears after the reader has pressed Save is one they are not
            looking at. The dialog stays open behind it, so the retry is the
            same button. */}
        {error ? (
          <Callout tone="danger" live="alert">
            {error}
          </Callout>
        ) : null}
        <div className="form-actions">
          <Button small variant="ghost" onClick={onClose}>
            {t("create.cancel")}
          </Button>
          <Button
            small
            variant="primary"
            onClick={() => {
              setError(null);
              save.mutate();
            }}
            disabled={
              save.isPending ||
              provider.trim() === "" ||
              modelId.trim() === "" ||
              input.trim() === "" ||
              output.trim() === ""
            }
          >
            {t("settings.rates.setRate")}
          </Button>
        </div>
      </div>
    </Modal>
  );
}
