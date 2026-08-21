import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { api } from "../api/client";
import { Button, Checkbox, Field, TextInput } from "../design-system/atoms";
import { Select } from "../design-system/select";
import { useT } from "../i18n";
import { problemMessageOf, throwProblem } from "./common";
import {
  actionLabelKey,
  isDuplicateScope,
  parseRetainDays,
  RETENTION_ACTIONS,
  RETENTION_POLICIES_KEY,
  RETENTION_SCOPES,
  type RetentionAction,
  type RetentionScope,
  scopeLabelKey,
} from "./retention.logic";

// The authoring form for a new retention policy: four inputs committed
// together, so it is the BODY of the dialog the ladder's "Add policy" row opens
// and brings no surface of its own — a card inside a dialog is a box in a box.
//
// The scope select offers the WHOLE authorable enum rather than only the
// unused scopes. Uniqueness is the database's answer, not this form's: another
// admin may have taken a scope since this list was fetched, so the form has to
// carry the duplicate refusal anyway — and a filtered list that could still
// 409 would be two rules for one invariant, the weaker one silently wrong.

export function RetentionPolicyForm({
  onDone,
}: Readonly<{ onDone: () => void }>) {
  const t = useT();
  const queryClient = useQueryClient();
  const [scope, setScope] = useState<RetentionScope>(RETENTION_SCOPES[0]);
  const [retainDays, setRetainDays] = useState("");
  // Archive first, deliberately: the least destructive action is the one a new
  // policy defaults to, so an operator who does not touch this field cannot
  // author an erase by omission.
  const [action, setAction] = useState<RetentionAction>("archive");
  const [lawfulBasis, setLawfulBasis] = useState("");
  const [enabled, setEnabled] = useState(true);

  const days = parseRetainDays(retainDays);

  const create = useMutation({
    mutationFn: async (window: number) => {
      const { data, error } = await api.POST("/retention-policies", {
        body: {
          scope,
          retain_days: window,
          action,
          // An omitted basis is null on the wire, not "" — the auditor reading
          // the row must be able to tell "not stated" from a blank string
          // somebody saved.
          lawful_basis: lawfulBasis.trim() || null,
          enabled,
        },
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: RETENTION_POLICIES_KEY });
      setRetainDays("");
      setLawfulBasis("");
      onDone();
    },
    // A duplicate means the row this form was about to author already exists
    // and this list never saw it. Re-read so the operator's next look shows
    // the row to edit — the message alone would name a row that isn't there.
    onError: (error) => {
      if (isDuplicateScope(error)) {
        queryClient.invalidateQueries({ queryKey: RETENTION_POLICIES_KEY });
      }
    },
  });

  // A stale refusal must not outlive the edit that could fix it (the
  // dismissGrantError idiom every create form on this screen follows).
  function dismissError() {
    if (create.isError) {
      create.reset();
    }
  }

  const errorMessage = !create.isError
    ? null
    : isDuplicateScope(create.error)
      ? t("retention.duplicateScope")
      : // Everything else is the server's own words — the unknown-scope 422 in
        // particular names what IS authorable, which is more useful than
        // anything this form could say about it.
        problemMessageOf(create.error, t);

  return (
    <div className="form-stack">
      <Field label={t("retention.scope")}>
        {(control) => (
          <Select
            {...control}
            options={RETENTION_SCOPES.map((value) => ({
              value,
              label: t(scopeLabelKey(value)),
            }))}
            value={scope}
            onChange={(value) => {
              const picked = RETENTION_SCOPES.find(
                (candidate) => candidate === value,
              );
              if (picked) {
                setScope(picked);
                dismissError();
              }
            }}
          />
        )}
      </Field>

      <Field
        label={t("retention.window")}
        hint={
          days === null && retainDays.trim() !== ""
            ? t("retention.windowInvalid")
            : undefined
        }
      >
        {(control) => (
          <TextInput
            {...control}
            inputMode="numeric"
            value={retainDays}
            onChange={(event) => {
              setRetainDays(event.target.value);
              dismissError();
            }}
          />
        )}
      </Field>

      <Field label={t("retention.action")} hint={t("retention.actionHint")}>
        {(control) => (
          <Select
            {...control}
            options={RETENTION_ACTIONS.map((value) => ({
              value,
              label: t(actionLabelKey(value)),
            }))}
            value={action}
            onChange={(value) => {
              const picked = RETENTION_ACTIONS.find(
                (candidate) => candidate === value,
              );
              if (picked) {
                setAction(picked);
                dismissError();
              }
            }}
          />
        )}
      </Field>

      <Field
        label={t("retention.lawfulBasis")}
        hint={t("retention.lawfulBasisHint")}
      >
        {(control) => (
          <TextInput
            {...control}
            value={lawfulBasis}
            onChange={(event) => {
              setLawfulBasis(event.target.value);
              dismissError();
            }}
          />
        )}
      </Field>

      <Checkbox
        className="t-caption"
        label={t("retention.enabled")}
        checked={enabled}
        onChange={(event) => {
          setEnabled(event.target.checked);
          dismissError();
        }}
      />

      {errorMessage && (
        <p className="t-caption retention-error" role="alert">
          {errorMessage}
        </p>
      )}

      <Button
        small
        variant="primary"
        disabled={days === null || create.isPending}
        onClick={() => days !== null && create.mutate(days)}
      >
        {t("retention.create")}
      </Button>
    </div>
  );
}
