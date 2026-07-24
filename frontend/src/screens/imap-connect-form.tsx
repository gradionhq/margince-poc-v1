// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useEffect, useId, useRef, useState } from "react";
import { api } from "../api/client";
import { Button, Modal, TextInput } from "../design-system/atoms";
import { useT } from "../i18n";
import { ProblemError, problemCode, throwProblem } from "./common";

// The IMAP connect flavor (RC-8/Task 6): the credential providers' first-
// connect and reconnect both happen through this one form, in Settings —
// there is no OAuth redirect to bounce through, so the standing connect
// (Task 1's `{imap:{...}}` shape) IS the whole act. The typed client only;
// the transient one-shot `/connectors/imap/connect` fetch this used to be
// (onboarding-connect-panels.tsx) is a separate, still-unmigrated surface.

type ImapConnectRequest = {
  host: string;
  port: number;
  username: string;
  secret: string;
  mailbox: string;
  max_messages: number;
};

const DEFAULT_PORT = "993";
const DEFAULT_MAILBOX = "INBOX";
const DEFAULT_MAX_MESSAGES = "50";

// The two IMAP-specific server conditions get their own honest sentence;
// anything else falls through to the mutation's generic message. Neither
// sentence ever echoes the submitted host — the server doesn't send it back
// either, so there is nothing here to leak. Exported so the onboarding IMAP
// panel (onboarding-connect-panels.tsx) reads the same two sentences off the
// same server codes, rather than growing its own copy of this mapping.
export function imapErrorMessage(
  error: unknown,
  t: (
    key: "connectors.imapLoginRejected" | "connectors.imapUnreachable",
  ) => string,
): string | null {
  if (!(error instanceof ProblemError)) {
    return error instanceof Error ? error.message : null;
  }
  const code = problemCode(error.problem);
  if (code === "imap_login_rejected") {
    return t("connectors.imapLoginRejected");
  }
  if (code === "imap_unreachable") {
    return t("connectors.imapUnreachable");
  }
  return error.message;
}

export function ImapConnectForm({
  open,
  onClose,
  onConnected,
}: Readonly<{
  open: boolean;
  onClose: () => void;
  // Called after the server has confirmed the connection — never before.
  // The caller's own row list (GET /connectors, invalidated below) is what
  // actually proves it; this callback just closes the caller's affordance.
  onConnected?: () => void;
}>) {
  const t = useT();
  const headingId = useId();
  const queryClient = useQueryClient();
  const [host, setHost] = useState("");
  const [port, setPort] = useState(DEFAULT_PORT);
  const [username, setUsername] = useState("");
  const [secret, setSecret] = useState("");
  const [mailbox, setMailbox] = useState(DEFAULT_MAILBOX);
  const [maxMessages, setMaxMessages] = useState(DEFAULT_MAX_MESSAGES);

  // A fresh open never carries a previous attempt's values — least of all
  // the secret, which is never retained across opens either.
  const wasOpen = useRef(false);
  useEffect(() => {
    if (open && !wasOpen.current) {
      setHost("");
      setPort(DEFAULT_PORT);
      setUsername("");
      setSecret("");
      setMailbox(DEFAULT_MAILBOX);
      setMaxMessages(DEFAULT_MAX_MESSAGES);
    }
    wasOpen.current = open;
  }, [open]);

  const connect = useMutation({
    mutationFn: async (request: ImapConnectRequest) => {
      const { data, error } = await api.POST("/connectors/{provider}/connect", {
        params: { path: { provider: "imap" } },
        body: { imap: request },
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
    onSuccess: () => {
      // Never claim a connection the server did not confirm: the row list
      // is the proof, so invalidate it and let the card's own re-read of
      // GET /connectors drive whatever it shows next.
      queryClient.invalidateQueries({ queryKey: ["connectors"] });
      setSecret("");
      onConnected?.();
    },
    onError: () => {
      // The secret is never retained after a failed submit.
      setSecret("");
    },
  });

  const parsedPort = port.trim() === "" ? 993 : Number(port);
  const parsedMax = maxMessages.trim() === "" ? 50 : Number(maxMessages);
  const ready =
    host.trim() !== "" &&
    username.trim() !== "" &&
    secret !== "" &&
    Number.isInteger(parsedPort) &&
    parsedPort >= 1 &&
    parsedPort <= 65535 &&
    Number.isInteger(parsedMax) &&
    parsedMax >= 1 &&
    parsedMax <= 200;

  const errorMessage = connect.isError
    ? imapErrorMessage(connect.error, t)
    : null;

  return (
    <Modal open={open} onClose={onClose} labelledBy={headingId}>
      <h2
        id={headingId}
        className="t-h2"
        style={{ marginBottom: "var(--space-3)" }}
      >
        {t("connectors.imapModalTitle")}
      </h2>
      <form
        className="form-stack"
        onSubmit={(event) => {
          event.preventDefault();
          if (!ready) {
            return;
          }
          connect.mutate({
            host: host.trim(),
            port: parsedPort,
            username: username.trim(),
            secret,
            mailbox: mailbox.trim() || DEFAULT_MAILBOX,
            max_messages: parsedMax,
          });
        }}
      >
        <div className="field">
          <label className="t-label" htmlFor={`${headingId}-host`}>
            {t("connectors.imapHost")}
          </label>
          <TextInput
            id={`${headingId}-host`}
            value={host}
            required
            onChange={(event) => setHost(event.target.value)}
          />
        </div>
        <div className="field">
          <label className="t-label" htmlFor={`${headingId}-port`}>
            {t("connectors.imapPort")}
          </label>
          <TextInput
            id={`${headingId}-port`}
            type="number"
            min={1}
            max={65535}
            value={port}
            onChange={(event) => setPort(event.target.value)}
          />
        </div>
        <div className="field">
          <label className="t-label" htmlFor={`${headingId}-username`}>
            {t("connectors.imapUsername")}
          </label>
          <TextInput
            id={`${headingId}-username`}
            type="email"
            autoComplete="email"
            value={username}
            required
            onChange={(event) => setUsername(event.target.value)}
          />
        </div>
        <div className="field">
          <label className="t-label" htmlFor={`${headingId}-secret`}>
            {t("connectors.imapSecret")}
          </label>
          <TextInput
            id={`${headingId}-secret`}
            type="password"
            autoComplete="off"
            value={secret}
            required
            onChange={(event) => setSecret(event.target.value)}
          />
        </div>
        <div className="field">
          <label className="t-label" htmlFor={`${headingId}-mailbox`}>
            {t("connectors.imapMailbox")}
          </label>
          <TextInput
            id={`${headingId}-mailbox`}
            value={mailbox}
            onChange={(event) => setMailbox(event.target.value)}
          />
        </div>
        <div className="field">
          <label className="t-label" htmlFor={`${headingId}-max-messages`}>
            {t("connectors.imapMaxMessages")}
          </label>
          <TextInput
            id={`${headingId}-max-messages`}
            type="number"
            min={1}
            max={200}
            value={maxMessages}
            onChange={(event) => setMaxMessages(event.target.value)}
          />
        </div>
        <p className="t-caption">{t("connectors.imapSecretHint")}</p>
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
              : t("connectors.imapSubmitCta")}
          </Button>
        </div>
      </form>
    </Modal>
  );
}
