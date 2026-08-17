import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { Button, Field, TextInput } from "../design-system/atoms";
import { Callout } from "../design-system/callout";
import { Panel, PanelBody } from "../design-system/panel";
import { usePasswordReveal } from "../design-system/passwordreveal";
import { useT } from "../i18n";
import { problemMessageOf, throwProblem } from "./common";
import { isTooShort } from "./passwordrule";

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

  // One per field, because they toggle independently: a reader checking what
  // they typed in the confirm box has no reason to expose the new password
  // above it at the same time.
  const revealLabels = {
    show: t("auth.showPassword"),
    hide: t("auth.hidePassword"),
  };
  const current = usePasswordReveal(revealLabels);
  const next = usePasswordReveal(revealLabels);
  const confirm = usePasswordReveal(revealLabels);

  const tooShort = isTooShort(fields.next);
  const mismatch = fields.confirm.length > 0 && fields.confirm !== fields.next;
  const ready =
    fields.current !== "" &&
    fields.next !== "" &&
    !tooShort &&
    fields.confirm === fields.next;

  return (
    <Panel title={t("password.title")}>
      {/* A real form, so Enter submits it. Three password fields that could only
          be committed by reaching for the button is not how anyone types a
          credential, and the button carried no `type` at all — Button defaults
          to `type="button"`, so even inside a form it would not have. */}
      <form
        onSubmit={(event) => {
          event.preventDefault();
          if (ready && !change.isPending) change.mutate(fields);
        }}
      >
        <PanelBody className="form-stack">
          <p className="t-small settings-panel-sub">{t("password.body")}</p>
          {/* The outcome is a Callout in its own tone, not a grey line. Both
              halves used to be `.t-small` — `--textMeta` — so a refused change
              and a successful one were the same colour, one element apart, and
              the only thing telling them apart was reading the sentence. */}
          {done && (
            <Callout tone="success" live="status">
              {t("password.done")}
            </Callout>
          )}
          {change.isError && (
            <Callout tone="danger" live="alert">
              {problemMessageOf(change.error, t, t("password.errorGeneric"))}
            </Callout>
          )}
          {/* Every password field carries a reveal, this one included. A
              mistyped CURRENT password is the cheapest of the three to
              diagnose — the server refuses it in one round trip — but being
              refused without being able to see what you typed is how a reader
              concludes they have forgotten a password they know. */}
          <Field
            label={t("password.current")}
            required
            trailing={current.trailing}
          >
            {(control) => (
              <TextInput
                {...control}
                type={current.type}
                name="current-password"
                autoComplete="current-password"
                value={fields.current}
                onChange={(event) => set("current")(event.target.value)}
              />
            )}
          </Field>
          {/* The new pair has the strongest claim on it: a mistyped current
              password is refused, while a mistyped NEW one simply becomes the
              password — with a twelve-character floor and a confirm field that
              agreed with it. */}
          <Field
            label={t("password.next")}
            required
            error={tooShort ? t("password.tooShort") : undefined}
            // The rule, until the rule is being broken — at which point the
            // refusal restates it in the danger tone and a second grey copy of
            // the same sentence underneath is noise.
            hint={tooShort ? undefined : t("password.hint")}
            trailing={next.trailing}
          >
            {(control) => (
              <TextInput
                {...control}
                type={next.type}
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
            error={mismatch ? t("password.mismatch") : undefined}
            trailing={confirm.trailing}
          >
            {(control) => (
              <TextInput
                {...control}
                type={confirm.type}
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
          <div className="form-actions">
            {/* Two facts, two props. `!ready` is a form that is not filled in
                yet and `change.isPending` is a write already on its way, and
                folding them into one `disabled` drew them the same: the reader
                could not tell "I still have to type something" from "it is
                going". */}
            <Button
              type="submit"
              variant="primary"
              disabled={!ready}
              pending={change.isPending}
            >
              {t("password.submit")}
            </Button>
          </div>
        </PanelBody>
      </form>
    </Panel>
  );
}
