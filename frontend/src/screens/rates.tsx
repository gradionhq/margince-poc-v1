import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useId, useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import {
  Button,
  DataTable,
  EmptyState,
  Modal,
  SectionHeader,
  TextInput,
} from "../design-system/atoms";
import { useT } from "../i18n";
import { canManageRates, problemMessage, QueryGate, useMe } from "./common";
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

export function RatesScreen() {
  return (
    <>
      <FxRatesCard />
      <ModelCostsCard />
    </>
  );
}

// ---- FX rates ----

export function FxRatesCard() {
  const t = useT();
  const me = useMe();
  const canManage = canManageRates(me.data?.roles);
  const [open, setOpen] = useState(false);
  const query = useQuery({
    queryKey: ["fx-rates"],
    queryFn: async () => {
      const { data, error } = await api.GET("/fx-rates", {
        params: { query: {} },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data.data;
    },
  });

  return (
    <section className="card" style={{ marginBottom: "var(--space-4)" }}>
      <div className="rates-head">
        <SectionHeader title={t("settings.rates.fxTitle")} />
        {canManage ? (
          <Button variant="primary" small onClick={() => setOpen(true)}>
            {t("settings.rates.fxAdd")}
          </Button>
        ) : null}
      </div>
      <p className="t-small" style={{ marginBottom: "var(--space-3)" }}>
        {t("settings.rates.fxIntro")}
      </p>
      <QueryGate query={query}>
        {(rows) =>
          rows.length === 0 ? (
            <EmptyState>
              <b>{t("settings.rates.fxEmpty")}</b>
            </EmptyState>
          ) : (
            <DataTable<FxRate>
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
      {open ? <FxRateModal onClose={() => setOpen(false)} /> : null}
    </section>
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
        throw new Error(problemMessage(err));
      }
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["fx-rates"] });
      onClose();
    },
    onError: (err: Error) => setError(err.message),
  });

  return (
    <Modal open onClose={onClose} labelledBy={labelId}>
      <h3 id={labelId}>{t("settings.rates.fxModalTitle")}</h3>
      <label className="t-label" htmlFor={`${labelId}-from`}>
        {t("settings.rates.colFrom")}
      </label>
      <TextInput
        id={`${labelId}-from`}
        value={from}
        maxLength={3}
        placeholder="USD"
        onChange={(e) => setFrom(e.target.value)}
      />
      <label className="t-label" htmlFor={`${labelId}-rate`}>
        {t("settings.rates.rateToBase")}
      </label>
      <TextInput
        id={`${labelId}-rate`}
        value={rate}
        inputMode="decimal"
        placeholder="0.92"
        onChange={(e) => setRate(e.target.value)}
      />
      <label className="t-label" htmlFor={`${labelId}-date`}>
        {t("settings.rates.colEffective")}
      </label>
      <TextInput
        id={`${labelId}-date`}
        type="date"
        min={today()}
        value={effectiveDate}
        onChange={(e) => setEffectiveDate(e.target.value)}
      />
      {error ? (
        <p className="t-small" style={{ color: "var(--danger)" }}>
          {error}
        </p>
      ) : null}
      <div
        style={{
          display: "flex",
          gap: "var(--space-2)",
          justifyContent: "flex-end",
          marginTop: "var(--space-3)",
        }}
      >
        <Button variant="ghost" onClick={onClose}>
          {t("create.cancel")}
        </Button>
        <Button
          variant="primary"
          onClick={() => {
            setError(null);
            save.mutate();
          }}
          disabled={save.isPending || from.trim() === "" || rate.trim() === ""}
        >
          {t("settings.rates.setRate")}
        </Button>
      </div>
    </Modal>
  );
}

// ---- AI model costs ----

export function ModelCostsCard() {
  const t = useT();
  const me = useMe();
  const canManage = canManageRates(me.data?.roles);
  const [open, setOpen] = useState(false);
  const query = useQuery({
    queryKey: ["ai-model-rates"],
    queryFn: async () => {
      const { data, error } = await api.GET("/ai-model-rates", {
        params: { query: {} },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data.data;
    },
  });

  return (
    <section className="card" style={{ marginBottom: "var(--space-4)" }}>
      <div className="rates-head">
        <SectionHeader title={t("settings.rates.modelTitle")} />
        {canManage ? (
          <Button variant="primary" small onClick={() => setOpen(true)}>
            {t("settings.rates.modelAdd")}
          </Button>
        ) : null}
      </div>
      <p className="t-small" style={{ marginBottom: "var(--space-3)" }}>
        {t("settings.rates.modelIntro")}
      </p>
      <QueryGate query={query}>
        {(rows) =>
          rows.length === 0 ? (
            <EmptyState>
              <b>{t("settings.rates.modelEmpty")}</b>
            </EmptyState>
          ) : (
            <DataTable<AiModelRate>
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
      {open ? <ModelCostModal onClose={() => setOpen(false)} /> : null}
    </section>
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
        throw new Error(problemMessage(err));
      }
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["ai-model-rates"] });
      onClose();
    },
    onError: (err: Error) => setError(err.message),
  });

  const field = (
    key: string,
    label: string,
    value: string,
    set: (v: string) => void,
    placeholder = "",
  ) => (
    <>
      <label className="t-label" htmlFor={`${labelId}-${key}`}>
        {label}
      </label>
      <TextInput
        id={`${labelId}-${key}`}
        value={value}
        inputMode={key === "provider" || key === "model" ? "text" : "decimal"}
        placeholder={placeholder}
        onChange={(e) => set(e.target.value)}
      />
    </>
  );

  return (
    <Modal open onClose={onClose} labelledBy={labelId}>
      <h3 id={labelId}>{t("settings.rates.modelModalTitle")}</h3>
      {field(
        "provider",
        t("settings.rates.colProvider"),
        provider,
        setProvider,
        "anthropic",
      )}
      {field(
        "model",
        t("settings.rates.colModel"),
        modelId,
        setModelId,
        "claude-opus-4-8",
      )}
      {field("in", t("settings.rates.colInput"), input, setInput, "5.00")}
      {field("out", t("settings.rates.colOutput"), output, setOutput, "25.00")}
      {field("cr", t("settings.rates.colCacheRead"), cacheRead, setCacheRead)}
      {field(
        "cw",
        t("settings.rates.colCacheWrite"),
        cacheWrite,
        setCacheWrite,
      )}
      <label className="t-label" htmlFor={`${labelId}-date`}>
        {t("settings.rates.colEffective")}
      </label>
      <TextInput
        id={`${labelId}-date`}
        type="date"
        min={today()}
        value={effectiveDate}
        onChange={(e) => setEffectiveDate(e.target.value)}
      />
      {error ? (
        <p className="t-small" style={{ color: "var(--danger)" }}>
          {error}
        </p>
      ) : null}
      <div
        style={{
          display: "flex",
          gap: "var(--space-2)",
          justifyContent: "flex-end",
          marginTop: "var(--space-3)",
        }}
      >
        <Button variant="ghost" onClick={onClose}>
          {t("create.cancel")}
        </Button>
        <Button
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
    </Modal>
  );
}
