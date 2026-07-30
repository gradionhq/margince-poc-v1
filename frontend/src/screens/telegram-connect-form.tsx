// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useEffect, useId, useRef, useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { Badge, Button, Modal, TextInput } from "../design-system/atoms";
import { useT } from "../i18n";
import { ProblemError, problemMessage, throwProblem } from "./common";
import { statusLabel, statusTone } from "./connector-status";

// The Telegram connector (Task 17, design §9.1/§9.2): one bot connects for
// the WHOLE workspace, not per-user — there is no OAuth handshake, so
// first-connect is the same "paste a credential, submit" shape
// imap-connect-form.tsx already established. Unlike the mail providers, a
// live channel connection stays EDITABLE: replacing the token goes through
// PATCH and returns to `connected` via `pending`, so captured history and
// every person_channel_identity binding survive the rotation instead of a
// disconnect-reconnect cycle that would discard them. This one form serves
// both first-connect (no `connection` prop) and that in-place edit (a
// `connection` prop supplies the id PATCH targets).

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

  // A fresh open never carries a previous attempt's token — nor, on a
  // repeat edit, the token of whichever earlier attempt preceded it.
  const wasOpen = useRef(false);
  useEffect(() => {
    if (open && !wasOpen.current) {
      setBotToken("");
    }
    wasOpen.current = open;
  }, [open]);

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

  const ready = botToken.trim() !== "";
  // RFC 7807 `detail` carries the actionable reason (e.g. the webhook-
  // conflict refusal names the other installation) — surfaced verbatim
  // rather than flattened into a generic "failed to connect" (§5).
  let errorMessage: string | null = null;
  if (connect.isError) {
    errorMessage =
      connect.error instanceof ProblemError
        ? problemMessage(connect.error.problem, t)
        : connect.error.message;
  }
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
          {/* The connection's CURRENT status stays visible while replacing
              its token — a pending row must read as pending here too, not
              silently as "connected" just because an edit form opened on
              it (§9.1). */}
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
