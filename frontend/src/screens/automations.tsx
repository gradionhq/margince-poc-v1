import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useId, useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import {
  Badge,
  Button,
  SectionHeader,
  TextInput,
} from "../design-system/atoms";
import { AutonomyDot } from "../design-system/trust";
import { useT } from "../i18n";
import { problemMessage, QueryGate } from "./common";

// The automations editor (B-EP09.15): a management UI over the CLOSED
// catalog (E15/ADR-0035). The anti-DSL invariant of features/10 §1 holds by
// construction — every form field derives from the catalog entry's
// params_schema plus the instance name; there is no free-form rule body and
// no user-defined trigger anywhere on this surface, and a test pins that.
// Instances render from the Automation wire schema alone, so an
// agent-authored instance is indistinguishable from a catalog-authored one.

type CatalogEntry = components["schemas"]["AutomationCatalogEntry"];
type Automation = components["schemas"]["Automation"];

export type ParamField = {
  key: string;
  kind: "integer" | "string";
  min?: number;
  max?: number;
  initial: string;
};

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

// Catalog defaults and stored params are JSON scalars; anything non-scalar
// has no honest single-line rendering, so it collapses to empty.
function scalarText(value: unknown): string {
  if (value === undefined || value === null || typeof value === "object") {
    return "";
  }
  return String(value);
}

function paramKind(type: unknown): ParamField["kind"] | null {
  if (type === "integer" || type === "number") {
    return "integer";
  }
  if (type === "string") {
    return "string";
  }
  return null;
}

// The ONLY source of editable parameters: the catalog entry's JSON schema.
export function paramFields(schema: Record<string, unknown>): ParamField[] {
  const properties = isRecord(schema.properties) ? schema.properties : {};
  return Object.entries(properties).flatMap(([key, raw]) => {
    if (!isRecord(raw)) {
      return [];
    }
    const kind = paramKind(raw.type);
    if (kind === null) {
      return [];
    }
    return [
      {
        key,
        kind,
        min: typeof raw.minimum === "number" ? raw.minimum : undefined,
        max: typeof raw.maximum === "number" ? raw.maximum : undefined,
        initial: scalarText(raw.default),
      },
    ];
  });
}

function paramsFromValues(
  fields: ParamField[],
  values: Record<string, string>,
): Record<string, unknown> {
  return Object.fromEntries(
    fields.map((field) => {
      const value = values[field.key] ?? field.initial;
      return [field.key, field.kind === "integer" ? Number(value) : value];
    }),
  );
}

// Pick-a-template + fill-parameters (B-E15.7b1). Also serves the edit flow:
// initial values arrive from the instance instead of the schema defaults.
function AutomationForm({
  entry,
  initialName,
  initialParams,
  submitLabel,
  pending,
  onSubmit,
  onCancel,
}: Readonly<{
  entry: CatalogEntry;
  initialName: string;
  initialParams?: Automation["params"];
  submitLabel: string;
  pending: boolean;
  onSubmit: (name: string, params: Record<string, unknown>) => void;
  onCancel: () => void;
}>) {
  const t = useT();
  const formId = useId();
  const fields = paramFields(entry.params_schema);
  const [name, setName] = useState(initialName);
  const [values, setValues] = useState<Record<string, string>>(() =>
    Object.fromEntries(
      fields.map((field) => {
        const configured = initialParams?.[field.key];
        return [
          field.key,
          configured === undefined ? field.initial : scalarText(configured),
        ];
      }),
    ),
  );

  return (
    <form
      className="card card-inset"
      style={{ marginTop: 10 }}
      onSubmit={(event) => {
        event.preventDefault();
        onSubmit(name.trim() || entry.name, paramsFromValues(fields, values));
      }}
    >
      <p className="t-label">{entry.name}</p>
      <p className="t-mono t-small" style={{ marginTop: 2 }}>
        {entry.trigger} {"->"} {entry.action}
      </p>
      <div className="field" style={{ marginTop: 8 }}>
        <span className="t-label" id={`${formId}-name`}>
          {t("auto.name")}
        </span>
        <TextInput
          aria-labelledby={`${formId}-name`}
          value={name}
          onChange={(event) => setName(event.target.value)}
        />
      </div>
      {fields.map((field) => (
        <div className="field" key={field.key} style={{ marginTop: 8 }}>
          <span className="t-label" id={`${formId}-${field.key}`}>
            {field.key}
          </span>
          <TextInput
            type={field.kind === "integer" ? "number" : "text"}
            aria-labelledby={`${formId}-${field.key}`}
            min={field.min}
            max={field.max}
            required
            value={values[field.key] ?? ""}
            onChange={(event) =>
              setValues((current) => ({
                ...current,
                [field.key]: event.target.value,
              }))
            }
          />
        </div>
      ))}
      <div className="approval-gate" style={{ marginTop: 10 }}>
        <Button type="submit" variant="primary" small disabled={pending}>
          {submitLabel}
        </Button>
        <Button small onClick={onCancel}>
          {t("deals.cancel")}
        </Button>
      </div>
    </form>
  );
}

// One instance row, rendered from the Automation wire schema alone — no
// origin field exists on the wire, so authorship cannot change the render.
export function AutomationRow({
  automation,
  entry,
}: Readonly<{
  automation: Automation;
  entry?: CatalogEntry;
}>) {
  const t = useT();
  const queryClient = useQueryClient();
  const [editing, setEditing] = useState(false);

  const patch = useMutation({
    mutationFn: async (body: {
      name?: string;
      params?: Record<string, unknown>;
      status?: "enabled" | "paused";
    }) => {
      const { data, error } = await api.PATCH("/automations/{id}", {
        params: {
          path: { id: automation.id },
          header:
            automation.version === undefined
              ? {}
              : { "If-Match": String(automation.version) },
        },
        body,
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
    onSuccess: () => {
      setEditing(false);
      queryClient.invalidateQueries({ queryKey: ["automations"] });
    },
  });

  const remove = useMutation({
    mutationFn: async () => {
      const { error } = await api.DELETE("/automations/{id}", {
        params: { path: { id: automation.id } },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["automations"] });
    },
  });

  const enabled = automation.status === "enabled";

  let mutationError: string | null = null;
  if (patch.error instanceof Error) {
    mutationError = patch.error.message;
  } else if (remove.error instanceof Error) {
    mutationError = remove.error.message;
  }

  return (
    <li style={{ marginBottom: 12 }} data-automation={automation.id}>
      <div
        style={{
          display: "flex",
          gap: 8,
          alignItems: "center",
          flexWrap: "wrap",
        }}
      >
        {entry?.tier && (
          <AutonomyDot tier={entry.tier === "green" ? "auto" : "confirm"} />
        )}
        <strong>{automation.name}</strong>
        <span className="t-mono t-small">{automation.key}</span>
        <Badge tone={enabled ? "success" : "warn"}>
          {enabled ? t("auto.statusEnabled") : t("auto.statusPaused")}
        </Badge>
        <span className="t-mono t-small">
          {Object.entries(automation.params)
            .map(([key, value]) => `${key}=${scalarText(value)}`)
            .join(" ")}
        </span>
        <span style={{ flexGrow: 1 }} />
        <Button
          small
          disabled={patch.isPending}
          onClick={() =>
            patch.mutate({ status: enabled ? "paused" : "enabled" })
          }
        >
          {enabled ? t("auto.pause") : t("auto.enable")}
        </Button>
        {entry && (
          <Button small onClick={() => setEditing((open) => !open)}>
            {t("trust.edit")}
          </Button>
        )}
        <Button
          small
          variant="danger"
          disabled={remove.isPending}
          onClick={() => remove.mutate()}
        >
          {t("auto.delete")}
        </Button>
      </div>
      {editing && entry && (
        <AutomationForm
          entry={entry}
          initialName={automation.name}
          initialParams={automation.params}
          submitLabel={t("trust.save")}
          pending={patch.isPending}
          onSubmit={(name, params) => patch.mutate({ name, params })}
          onCancel={() => setEditing(false)}
        />
      )}
      {(patch.isError || remove.isError) && (
        <p
          className="t-caption"
          style={{ color: "var(--danger)", marginTop: 8 }}
        >
          {mutationError}
        </p>
      )}
    </li>
  );
}

export function AutomationsScreen() {
  const t = useT();
  const queryClient = useQueryClient();
  const [template, setTemplate] = useState<CatalogEntry | null>(null);

  const catalog = useQuery({
    queryKey: ["automation-catalog"],
    queryFn: async () => {
      const { data, error } = await api.GET("/automations/catalog");
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
  });

  const instances = useQuery({
    queryKey: ["automations"],
    queryFn: async () => {
      const { data, error } = await api.GET("/automations", {
        params: { query: { limit: 50 } },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
  });

  const create = useMutation({
    mutationFn: async (input: {
      key: string;
      name: string;
      params: Record<string, unknown>;
    }) => {
      const { data, error } = await api.POST("/automations", {
        body: input,
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
    onSuccess: () => {
      setTemplate(null);
      queryClient.invalidateQueries({ queryKey: ["automations"] });
    },
  });

  const entryFor = (key: string): CatalogEntry | undefined =>
    catalog.data?.data.find((entry) => entry.key === key);

  return (
    <div className="wrap">
      <SectionHeader title={t("nav.automations")} sub={t("auto.sub")} />
      <div style={{ display: "flex", gap: 14, flexWrap: "wrap" }}>
        <section className="card" style={{ flex: "1 1 260px", minWidth: 240 }}>
          <SectionHeader title={t("auto.catalog")} sub={t("auto.catalogSub")} />
          <QueryGate query={catalog} empty={(page) => page.data.length === 0}>
            {(page) => (
              <ul
                style={{
                  listStyle: "none",
                  display: "flex",
                  flexDirection: "column",
                  gap: 10,
                }}
              >
                {page.data.map((entry) => (
                  <li key={entry.key}>
                    <div
                      style={{
                        display: "flex",
                        gap: 8,
                        alignItems: "center",
                        flexWrap: "wrap",
                      }}
                    >
                      {entry.tier && (
                        <AutonomyDot
                          tier={entry.tier === "green" ? "auto" : "confirm"}
                        />
                      )}
                      <strong>{entry.name}</strong>
                      <Button small onClick={() => setTemplate(entry)}>
                        {t("auto.use")}
                      </Button>
                    </div>
                    {entry.description && (
                      <p className="t-caption" style={{ marginTop: 2 }}>
                        {entry.description}
                      </p>
                    )}
                    <p className="t-mono t-small" style={{ marginTop: 2 }}>
                      {entry.trigger} {"->"} {entry.action}
                    </p>
                  </li>
                ))}
              </ul>
            )}
          </QueryGate>
          {template && (
            <AutomationForm
              key={template.key}
              entry={template}
              initialName={template.name}
              submitLabel={t("auto.create")}
              pending={create.isPending}
              onSubmit={(name, params) =>
                create.mutate({ key: template.key, name, params })
              }
              onCancel={() => setTemplate(null)}
            />
          )}
          {create.isSuccess && (
            <p className="t-caption" style={{ marginTop: 8 }}>
              {t("auto.createdPaused")}
            </p>
          )}
          {create.isError && (
            <p
              className="t-caption"
              style={{ color: "var(--danger)", marginTop: 8 }}
            >
              {create.error instanceof Error ? create.error.message : null}
            </p>
          )}
        </section>
        <section className="card" style={{ flex: "2 1 320px", minWidth: 280 }}>
          <SectionHeader title={t("auto.instances")} />
          <QueryGate query={instances} empty={(page) => page.data.length === 0}>
            {(page) => (
              <ul style={{ listStyle: "none" }}>
                {page.data.map((automation) => (
                  <AutomationRow
                    key={automation.id}
                    automation={automation}
                    entry={entryFor(automation.key)}
                  />
                ))}
              </ul>
            )}
          </QueryGate>
        </section>
      </div>
    </div>
  );
}
