// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Database, Plug, Trash2 } from "lucide-react";
import { useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import {
  Badge,
  Button,
  Card,
  Checkbox,
  EmptyState,
  Field,
  TextInput,
} from "../design-system/atoms";
import { Callout } from "../design-system/callout";
import { ConfirmModal } from "../design-system/confirmmodal";
import { Meter } from "../design-system/readings";
import { useT } from "../i18n";
import {
  problemCode,
  problemMessageOf,
  QueryGate,
  throwProblem,
} from "./common";
import { connectionLabel, connectionTone } from "./provider-status";

// The licensed-data-provider card (ADR-0101, PI-WIRE-1..5): connect a key,
// see what the provider says is left, decide whether new contacts are
// enriched automatically, and — separately — stop the flow or destroy what
// was bought.
//
// Disconnect and delete-data are two buttons because they are two decisions.
// Disconnecting stops new lookups and destroys the key; the data already paid
// for stays on the records. A customer may want either without the other, and
// a single button would make one of them a surprise.

type ProviderConnection = components["schemas"]["ProviderConnection"];

type ConnectionsResult = {
  /** True when this build carries no adapter at all. Not an error: it is the
   *  supported "no provider" configuration, and the card says so plainly
   *  rather than showing a broken control (PI-AC-9). */
  notConfigured: boolean;
  connections: ProviderConnection[];
};

function useProviderConnections() {
  return useQuery({
    queryKey: ["provider-connections"],
    queryFn: async (): Promise<ConnectionsResult> => {
      const { data, error, response } = await api.GET("/provider-connections");
      // 501 is a deployment fact, not a failure — the same shape connectors.tsx
      // uses for a connector nobody configured.
      if (response.status === 501 && problemCode(error) === "not_implemented") {
        return { notConfigured: true, connections: [] };
      }
      if (error || !response.ok) {
        throwProblem(error);
      }
      return { notConfigured: false, connections: data?.data ?? [] };
    },
  });
}

export function ProviderCard() {
  const t = useT();
  const query = useProviderConnections();
  return (
    <Card>
      <h2>{t("provider.title")}</h2>
      <p className="muted">{t("provider.sub")}</p>
      <QueryGate query={query}>
        {(result) =>
          // An empty list means the same thing a 501 does: no adapter is
          // compiled in. The server returns a row for every REGISTERED
          // provider — including one nobody has connected yet, which is how
          // the key field appears at all — so "no rows" cannot mean "not
          // connected", and both cases read as the honest no-provider state.
          result.notConfigured || result.connections.length === 0 ? (
            <EmptyState>{t("provider.notConfigured")}</EmptyState>
          ) : (
            <>
              {result.connections.map((connection) => (
                <ProviderConnectionRow
                  key={connection.provider}
                  connection={connection}
                />
              ))}
            </>
          )
        }
      </QueryGate>
    </Card>
  );
}

function ProviderConnectionRow({
  connection,
}: Readonly<{ connection: ProviderConnection }>) {
  const t = useT();
  return (
    <section className="pe-card">
      <header
        style={{ display: "flex", alignItems: "center", gap: "var(--space-2)" }}
      >
        <Database aria-hidden />
        <strong>{connection.provider}</strong>
        <Badge tone={connectionTone(connection.status)}>
          {t(connectionLabel(connection.status))}
        </Badge>
      </header>
      <CreditsBlock connection={connection} />
      <SpendBlock connection={connection} />
      <PolicyBlock connection={connection} />
      <CredentialBlock connection={connection} />
    </section>
  );
}

// What the PROVIDER says is left — their number, never ours. A customer may
// spend the same credits through the provider's own app, so this is a reading
// of their ledger and the card never presents it as our accounting.
// What THIS installation consumed, directly under what the provider says is
// left — the two are the questions a customer asks together, and seeing them
// side by side is what stops either being mistaken for the other. The label
// says whose number it is; the hint says why they can legitimately differ.
function SpendBlock({
  connection,
}: Readonly<{ connection: ProviderConnection }>) {
  const t = useT();
  const months = connection.spend?.months ?? [];
  if (months.length === 0) {
    return (
      <div>
        <h3>{t("provider.spend")}</h3>
        <p className="muted">{t("provider.spend.none")}</p>
      </div>
    );
  }
  // The series arrives newest first, so the current month heads the list.
  const current = months[0].month;
  return (
    <div>
      <h3>{t("provider.spend")}</h3>
      <p className="muted">{t("provider.spend.hint")}</p>
      {months.map((month) => (
        <div key={`${month.month}-${month.pool}`}>
          <span className="muted">
            {month.month === current
              ? t("provider.spend.thisMonth")
              : month.month}
          </span>{" "}
          <span>
            {t("provider.spend.charged", {
              pool: month.pool,
              credits: month.charged_credits,
              count: month.runs,
            })}
          </span>
          {month.held_credits > 0 && (
            // Never folded into the charge: the platform does not know
            // whether those credits were spent, and a total that quietly
            // counted them either way would assert something it cannot
            // support. This is the figure a human reconciles against the
            // provider's invoice.
            <p className="muted">
              {t("provider.spend.held", { credits: month.held_credits })}
            </p>
          )}
        </div>
      ))}
    </div>
  );
}

function CreditsBlock({
  connection,
}: Readonly<{ connection: ProviderConnection }>) {
  const t = useT();
  // Iterated, never hardcoded to email/mobile: the pool names are the
  // PROVIDER's own vocabulary, and a second provider meters different ones.
  const pools = Object.entries(connection.credits?.pools ?? {});
  if (pools.length === 0) {
    return <p className="muted">{t("provider.credits.none")}</p>;
  }
  const highest = Math.max(1, ...pools.map(([, balance]) => balance ?? 0));
  return (
    <div>
      <h3>{t("provider.credits")}</h3>
      {pools.map(([pool, balance]) => (
        <div key={pool}>
          <span>{t("provider.credits.pool", { pool })}</span>
          <Meter
            value={balance ?? 0}
            max={highest}
            label={String(balance ?? 0)}
          />
        </div>
      ))}
      {(connection.effective_constraints ?? []).length > 0 && (
        <p className="muted">
          {t("provider.constraints")}:{" "}
          {(connection.effective_constraints ?? []).join(", ")}
        </p>
      )}
    </div>
  );
}

function usePatchConfiguration(
  provider: ProviderConnection["provider"],
  version: number,
) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (automaticIndividualCreate: boolean) => {
      const { data, error } = await api.PATCH(
        "/provider-connections/{provider}",
        {
          params: {
            path: { provider },
            // The saved policy carries a version, and a blind write would
            // silently overwrite a colleague's edit. A 409 is version skew,
            // which the refetch below resolves by showing what is actually
            // stored.
            header: { "If-Match": String(version) },
          },
          body: {
            configuration: {
              automatic_individual_create: automaticIndividualCreate,
            },
          },
        },
      );
      if (error) {
        throwProblem(error);
      }
      return data;
    },
    onSuccess: (updated) => {
      queryClient.setQueryData(
        ["provider-connections"],
        (current: ConnectionsResult | undefined) =>
          current
            ? {
                ...current,
                connections: current.connections.map((c) =>
                  c.provider === updated?.provider ? updated : c,
                ),
              }
            : current,
      );
    },
    onError: () => {
      void queryClient.invalidateQueries({
        queryKey: ["provider-connections"],
      });
    },
  });
}

function PolicyBlock({
  connection,
}: Readonly<{ connection: ProviderConnection }>) {
  const t = useT();
  const patch = usePatchConfiguration(connection.provider, connection.version);
  const configuration = connection.configuration;
  return (
    <div>
      <Checkbox
        checked={configuration.automatic_individual_create ?? false}
        disabled={patch.isPending || connection.status !== "connected"}
        onChange={(event) => patch.mutate(event.target.checked)}
        label={t("provider.autoEnrich")}
      />
      <p className="muted">{t("provider.autoEnrichHint")}</p>
      {patch.error && (
        <Callout tone="danger">{problemMessageOf(patch.error, t)}</Callout>
      )}
    </div>
  );
}

function CredentialBlock({
  connection,
}: Readonly<{ connection: ProviderConnection }>) {
  const t = useT();
  const queryClient = useQueryClient();
  const [key, setKey] = useState("");
  const [confirming, setConfirming] = useState(false);
  const [disconnecting, setDisconnecting] = useState(false);
  const [deleting, setDeleting] = useState(false);
  const [typed, setTyped] = useState("");

  const connect = useMutation({
    mutationFn: async () => {
      const { error } = await api.PUT("/provider-connections/{provider}", {
        params: { path: { provider: connection.provider } },
        body: { api_key: key },
      });
      if (error) {
        throwProblem(error);
      }
    },
    onSuccess: () => {
      // The key never lives in this component longer than the request: it is
      // sealed server-side and never returned, so holding it would keep a
      // secret in the page for no purpose.
      setKey("");
      setConfirming(false);
      void queryClient.invalidateQueries({
        queryKey: ["provider-connections"],
      });
    },
  });

  const disconnect = useMutation({
    mutationFn: async () => {
      const { error } = await api.DELETE("/provider-connections/{provider}", {
        params: { path: { provider: connection.provider } },
      });
      if (error) {
        throwProblem(error);
      }
    },
    onSuccess: () => {
      setDisconnecting(false);
      void queryClient.invalidateQueries({
        queryKey: ["provider-connections"],
      });
    },
  });

  const deleteData = useMutation({
    mutationFn: async () => {
      const { error } = await api.DELETE(
        "/provider-connections/{provider}/data",
        {
          params: { path: { provider: connection.provider } },
        },
      );
      if (error) {
        throwProblem(error);
      }
    },
    onSuccess: () => {
      setDeleting(false);
      setTyped("");
      void queryClient.invalidateQueries({
        queryKey: ["provider-connections"],
      });
    },
  });

  const connected = connection.credential_present;
  return (
    <div>
      <form
        className="form-stack"
        onSubmit={(event) => {
          event.preventDefault();
          if (key.trim() !== "") {
            setConfirming(true);
          }
        }}
      >
        <Field label={t("provider.apiKey")} hint={t("provider.apiKeyHint")}>
          {(control) => (
            <TextInput
              {...control}
              type="password"
              autoComplete="off"
              value={key}
              required
              onChange={(event) => setKey(event.target.value)}
            />
          )}
        </Field>
        <div style={{ display: "flex", gap: "var(--space-2)" }}>
          <Button
            small
            variant="primary"
            type="submit"
            disabled={key.trim() === ""}
          >
            <Plug aria-hidden />{" "}
            {connected ? t("provider.reconnect") : t("provider.connect")}
          </Button>
          {connected && (
            <>
              <Button
                small
                variant="danger"
                type="button"
                onClick={() => setDisconnecting(true)}
              >
                {t("provider.disconnect")}
              </Button>
              <Button
                small
                variant="danger"
                type="button"
                onClick={() => setDeleting(true)}
              >
                <Trash2 aria-hidden /> {t("provider.deleteData")}
              </Button>
            </>
          )}
        </div>
      </form>
      {connect.error && (
        <Callout tone="danger">{problemMessageOf(connect.error, t)}</Callout>
      )}

      <ConfirmModal
        open={confirming}
        title={t("provider.connectConfirm.title")}
        confirmLabel={t("provider.connect")}
        onConfirm={() => connect.mutate()}
        onClose={() => setConfirming(false)}
        pending={connect.isPending}
      >
        {t("provider.connectConfirm.body")}
      </ConfirmModal>

      <ConfirmModal
        open={disconnecting}
        confirmVariant="danger"
        title={t("provider.disconnectConfirm.title")}
        confirmLabel={t("provider.disconnect")}
        onConfirm={() => disconnect.mutate()}
        onClose={() => setDisconnecting(false)}
        pending={disconnect.isPending}
      >
        {t("provider.disconnectConfirm.body")}
      </ConfirmModal>

      {/* Typed confirmation, like the data reset: this destroys purchased
          data on every contact, and a misclick must not be able to do it. */}
      <ConfirmModal
        open={deleting}
        confirmVariant="danger"
        title={t("provider.deleteDataConfirm.title")}
        confirmLabel={t("provider.deleteData")}
        confirmDisabled={typed !== connection.provider}
        onConfirm={() => deleteData.mutate()}
        pending={deleteData.isPending}
        onClose={() => {
          setDeleting(false);
          setTyped("");
        }}
      >
        <p>{t("provider.deleteDataConfirm.body")}</p>
        <Field label={t("provider.deleteDataConfirm.typed")}>
          {(control) => (
            <TextInput
              {...control}
              value={typed}
              onChange={(event) => setTyped(event.target.value)}
            />
          )}
        </Field>
      </ConfirmModal>
    </div>
  );
}
