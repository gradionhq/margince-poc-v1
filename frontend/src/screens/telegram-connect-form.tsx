// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useEffect, useId, useRef, useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { Badge, Button, Modal, TextInput } from "../design-system/atoms";
import { useT } from "../i18n";
import { problemMessageOf, throwProblem } from "./common";
import { statusLabel, statusTone } from "./connector-status";

// The Telegram connector: one bot connects for the WHOLE workspace, not
// per-user — there is no OAuth handshake, so first-connect is the same "paste a
// credential, submit" shape imap-connect-form.tsx already established, and it
// asks for nothing else: the installation polls the provider for messages, so
// there is no address of our own for anyone to supply. Unlike the mail
// providers, a live channel connection stays EDITABLE: replacing the token goes
// through PATCH and stays `connected` throughout, so captured history and every
// channel identity binding survive the rotation instead of a
// disconnect-reconnect cycle that would discard them. This one form serves both
// first-connect (no `connection` prop) and that in-place edit (a `connection`
// prop supplies the id PATCH targets).

type ChannelConnection = components["schemas"]["ChannelConnection"];

export function TelegramConnectForm({
  open,
  onClose,
  connection,
  onConnected,
}: Readonly<{
  open: boolean;
  onClose: () => void;
  // Present only when replacing an existing connection's token; absent for
  // the first connect. Its presence alone decides PATCH vs POST below —
  // there is no separate "mode" flag to drift out of sync with it.
  connection?: ChannelConnection;
  onConnected?: () => void;
}>) {
  const t = useT();
  const headingId = useId();
  const queryClient = useQueryClient();
  const [botToken, setBotToken] = useState("");

  const connect = useMutation({
    mutationFn: async (token: string): Promise<ChannelConnection> => {
      if (connection) {
        const { data, error } = await api.PATCH("/channel-connections/{id}", {
          params: { path: { id: connection.id } },
          body: { botToken: token },
        });
        if (error) {
          throwProblem(error, t);
        }
        return data;
      }
      const { data, error } = await api.POST("/channel-connections", {
        body: { provider: "telegram", botToken: token },
      });
      if (error) {
        throwProblem(error, t);
      }
      return data;
    },
    onSuccess: () => {
      // Never claim a connection the server did not confirm: the list is
      // the proof, so invalidate it and let the panel's own re-read of
      // GET /channel-connections drive whatever it shows next.
      queryClient.invalidateQueries({ queryKey: ["channel-connections"] });
      setBotToken("");
    },
    onError: () => {
      // The token is never retained after a failed submit — the same
      // posture the IMAP form holds for its secret.
      setBotToken("");
    },
  });

  // A fresh open starts from nothing: neither a previous attempt's token nor
  // the outcome it reached. This form is mounted for the life of the Settings
  // card and only shown or hidden, so a retained success view is what the
  // admin meets on the next rotation — a bot username and a Done button,
  // with no way back to the token field.
  const { reset: resetConnect } = connect;
  const wasOpen = useRef(false);
  useEffect(() => {
    if (open && !wasOpen.current) {
      setBotToken("");
      resetConnect();
    }
    wasOpen.current = open;
  }, [open, resetConnect]);

  const ready = botToken.trim() !== "";
  // RFC 7807 `detail` carries the actionable reason (e.g. which of the two
  // conflicts a 409 is — this bot is bound elsewhere, or this workspace already
  // has one) — surfaced verbatim rather than flattened into a generic "failed
  // to connect".
  const errorMessage = connect.isError
    ? problemMessageOf(connect.error, t)
    : null;
  const resolved = connect.isSuccess ? connect.data : null;

  return (
    <Modal open={open} onClose={onClose} labelledBy={headingId}>
      <h2
        id={headingId}
        className="t-h2"
        style={{ marginBottom: "var(--space-3)" }}
      >
        {connection
          ? t("connectors.telegramEditTitle")
          : t("connectors.telegramModalTitle")}
      </h2>
      {resolved ? (
        <div className="form-stack">
          <p className="t-body">
            {t("connectors.telegramConnectedAs", {
              username: resolved.channelLabel,
            })}
          </p>
          <div>
            <Badge tone={statusTone(resolved.status)}>
              {t(statusLabel(resolved.status))}
            </Badge>
          </div>
          <div className="actions">
            <Button
              small
              variant="primary"
              onClick={() => {
                onConnected?.();
                onClose();
              }}
            >
              {t("webhooks.secret.done")}
            </Button>
          </div>
        </div>
      ) : (
        <form
          className="form-stack"
          onSubmit={(event) => {
            event.preventDefault();
            if (!ready) {
              return;
            }
            connect.mutate(botToken.trim());
          }}
        >
          {/* The connection's CURRENT status stays visible while replacing its
              token — a binding ingress has parked (error / reauth_required)
              must read that way here too, not silently as "connected" just
              because an edit form opened on it. */}
          {connection && (
            <div>
              <Badge tone={statusTone(connection.status)}>
                {t(statusLabel(connection.status))}
              </Badge>
            </div>
          )}
          <div className="field">
            <label className="t-label" htmlFor={`${headingId}-token`}>
              {t("connectors.telegramBotToken")}
            </label>
            <TextInput
              id={`${headingId}-token`}
              type="password"
              autoComplete="off"
              value={botToken}
              required
              onChange={(event) => setBotToken(event.target.value)}
            />
          </div>
          <p className="t-caption">{t("connectors.telegramBotTokenHint")}</p>
          {errorMessage && (
            <p
              role="alert"
              className="t-caption"
              style={{ color: "var(--danger)" }}
            >
              {errorMessage}
            </p>
          )}
          <div className="actions">
            <Button
              small
              type="button"
              onClick={onClose}
              disabled={connect.isPending}
            >
              {t("create.cancel")}
            </Button>
            <Button
              small
              variant="primary"
              type="submit"
              disabled={!ready || connect.isPending}
            >
              {connect.isPending
                ? t("create.saving")
                : connection
                  ? t("connectors.telegramReplaceCta")
                  : t("connectors.telegramSubmitCta")}
            </Button>
          </div>
        </form>
      )}
    </Modal>
  );
}
