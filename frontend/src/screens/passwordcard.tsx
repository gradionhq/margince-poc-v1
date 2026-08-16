import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { Button, Field } from "../design-system/atoms";
import { Panel, PanelBody } from "../design-system/panel";
import { useT } from "../i18n";
import { problemMessageOf, throwProblem } from "./common";

// Changing your own password, from the account settings page.
//
// The product could set a password three ways before this — a reset token
// mailed to the account, an admin minting a set-password link for someone else,
// and an operator running the CLI against the database — and none of them was
// "I am signed in and I would like a different password". On an installation
// with no outbound email that left a member with no way to rotate their own
// credential at all.
//
// The current password is what authorizes the change, not the session. So this
// card asks for it, and the server verifies it: a session is what a stolen
// laptop already has.

type ChangeFields = { current: string; next: string; confirm: string };

const EMPTY: ChangeFields = { current: "", next: "", confirm: "" };

/** The floor the server applies, restated so the form can say it first. */
const MIN_PASSWORD = 12;

export function ChangePasswordCard({
  onChanged,
}: Readonly<{
  // Called after a successful change. The settings page needs nothing — the
  // sign-out below is the whole outcome there. The forced-change boundary uses
  // it to re-probe, because the refusal that sent the user here is exactly what
  // the change resolves.
  onChanged?: () => void;
}> = {}) {
  const t = useT();
  const [fields, setFields] = useState<ChangeFields>(EMPTY);
  const [done, setDone] = useState(false);

  const queryClient = useQueryClient();
  const change = useMutation({
    // Cleared before the attempt, not after it: without this a second attempt
    // that fails renders the success line and the error line together, telling
    // the reader both that the password changed and that it did not.
    onMutate: () => setDone(false),
    // Takes what it needs as a variable rather than closing over render state:
    // the click belongs to the committed render, so a variable it passes cannot
    // be older than the control that carried it.
    mutationFn: async (values: ChangeFields) => {
      const response = await fetch("/v1/auth/change-password", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        credentials: "same-origin",
        body: JSON.stringify({
          current_password: values.current,
          new_password: values.next,
        }),
      });
      if (!response.ok) {
        // The house error path: the server's problem detail is what says
        // whether the current password was wrong or the new one was refused,
        // and a generic message here would throw that away.
        throwProblem(await response.json().catch(() => null), t);
      }
    },
    onSuccess: async () => {
      setFields(EMPTY);
      setDone(true);
      // The server revoked every credential for this account, this session
      // included, and cleared the cookie. Without dropping the cached identity
      // the app would keep rendering the signed-in shell against a session that
      // no longer exists, and every later request would 401 — a success message
      // followed by unexplained failures, which is exactly what the warning
      // above this button exists to prevent.
      queryClient.removeQueries({
        predicate: (query) => query.queryKey[0] !== "me",
      });
      await queryClient.resetQueries({ queryKey: ["me"] });
      onChanged?.();
    },
  });

  const set = (key: keyof ChangeFields) => (value: string) =>
    setFields((current) => ({ ...current, [key]: value }));

  const tooShort =
    fields.next.length > 0 && [...fields.next].length < MIN_PASSWORD;
  const mismatch = fields.confirm.length > 0 && fields.confirm !== fields.next;
  const ready =
    fields.current !== "" &&
    fields.next !== "" &&
    !tooShort &&
    fields.confirm === fields.next;

  return (
    <Panel title={t("password.title")}>
      <PanelBody>
        <p className="t-small">{t("password.body")}</p>
        {done && (
          <p className="t-small" role="status">
            {t("password.done")}
          </p>
        )}
        {change.isError && (
          <p className="t-small" role="alert">
            {problemMessageOf(change.error, t, t("password.errorGeneric"))}
          </p>
        )}
        <Field label={t("password.current")} required>
          {(control) => (
            <input
              {...control}
              type="password"
              name="current-password"
              autoComplete="current-password"
              value={fields.current}
              onChange={(event) => set("current")(event.target.value)}
            />
          )}
        </Field>
        <Field
          label={t("password.next")}
          required
          hint={tooShort ? t("password.tooShort") : t("password.hint")}
        >
          {(control) => (
            <input
              {...control}
              type="password"
              name="new-password"
              autoComplete="new-password"
              value={fields.next}
              onChange={(event) => set("next")(event.target.value)}
            />
          )}
        </Field>
        <Field
          label={t("password.confirm")}
          required
          hint={mismatch ? t("password.mismatch") : undefined}
        >
          {(control) => (
            <input
              {...control}
              type="password"
              name="confirm-password"
              autoComplete="new-password"
              value={fields.confirm}
              onChange={(event) => set("confirm")(event.target.value)}
            />
          )}
        </Field>
        {/* Said before the button is pressed, not after: the change ends every
            session including this one, so the next thing that happens is a
            sign-in screen. A person who is not told that reads it as being
            kicked out. */}
        <p className="t-small">{t("password.signsYouOut")}</p>
        <Button
          variant="primary"
          disabled={!ready || change.isPending}
          onClick={() => change.mutate(fields)}
        >
          {change.isPending ? t("password.changing") : t("password.submit")}
        </Button>
      </PanelBody>
    </Panel>
  );
}
