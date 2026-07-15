import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useId, useState } from "react";
import { api } from "../api/client";
import {
  Badge,
  Button,
  SectionHeader,
  TextInput,
} from "../design-system/atoms";
import { formatDate } from "../format/format";
import { useLocale, useT } from "../i18n";
import { problemMessage, QueryGate } from "./common";
import { dsrKindTone } from "./privacy.logic";
import "./privacy.css";

// The two settings/privacy surfaces, extracted out of the 1309-line
// settings.tsx (the audit.tsx extraction precedent): the consent-purpose
// catalogue (G-3 adds create — POST /consent-purposes already routed, but
// nothing in this app called it) and the DSR inbox. GET + POST only — there
// is no PATCH or DELETE on /consent-purposes, so a purpose is append-only by
// contract, not by convention; the create form says so up front.

// G-3: the inline purpose-create form, toggled by "Add purpose" — the
// share.tsx precedent (create is an inline card, never a modal; only the
// destructive revoke there uses one). A stale create error must not outlive
// the edit that could fix it, so every field's onChange clears it first
// (share.tsx:432's dismissGrantError idiom).
function PurposeCreateForm({ onDone }: Readonly<{ onDone: () => void }>) {
  const t = useT();
  const queryClient = useQueryClient();
  const [key, setKey] = useState("");
  const [label, setLabel] = useState("");
  const [requiresDoi, setRequiresDoi] = useState(false);
  const keyId = useId();
  const labelId = useId();

  const create = useMutation({
    mutationFn: async () => {
      const { data, error } = await api.POST("/consent-purposes", {
        body: {
          key: key.trim(),
          label: label.trim(),
          requires_double_opt_in: requiresDoi,
        },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["consent-purposes"] });
      setKey("");
      setLabel("");
      setRequiresDoi(false);
      onDone();
    },
  });

  // share.tsx:417's honestMessage idiom — this form has only one error
  // source (the create mutation), so it renders the message directly rather
  // than distinguishing an approval-required case the way share.tsx does.
  function honestMessage(error: unknown): string | null {
    return error instanceof Error ? error.message : null;
  }

  function dismissCreateError() {
    if (create.isError) {
      create.reset();
    }
  }

  return (
    <div className="card card-inset purpose-form">
      <p className="t-caption purpose-form-warning">
        {t("privacy.purposeAppendOnly")}
      </p>
      <div className="form-stack">
        <div className="field">
          <label className="t-label" htmlFor={keyId}>
            {t("privacy.purposeKey")}
          </label>
          <TextInput
            id={keyId}
            value={key}
            onChange={(event) => {
              setKey(event.target.value);
              dismissCreateError();
            }}
          />
        </div>
        <div className="field">
          <label className="t-label" htmlFor={labelId}>
            {t("privacy.purposeLabel")}
          </label>
          <TextInput
            id={labelId}
            value={label}
            onChange={(event) => {
              setLabel(event.target.value);
              dismissCreateError();
            }}
          />
        </div>
        <label className="t-caption purpose-doi-check">
          <input
            type="checkbox"
            checked={requiresDoi}
            onChange={(event) => {
              setRequiresDoi(event.target.checked);
              dismissCreateError();
            }}
          />
          {t("privacy.purposeDoi")}
        </label>
        {create.isError && (
          <p className="t-caption purpose-form-error">
            {honestMessage(create.error)}
          </p>
        )}
        <Button
          small
          variant="primary"
          disabled={!key.trim() || !label.trim() || create.isPending}
          onClick={() => create.mutate()}
        >
          {t("privacy.purposeCreate")}
        </Button>
      </div>
    </div>
  );
}

export function ConsentPurposesCard() {
  const t = useT();
  const [adding, setAdding] = useState(false);
  const query = useQuery({
    queryKey: ["consent-purposes"],
    queryFn: async () => {
      const { data, error } = await api.GET("/consent-purposes");
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
  });
  return (
    <section className="card" style={{ marginBottom: 14 }}>
      <div className="list-head">
        <SectionHeader title={t("settings.purposes")} />
        <Button small onClick={() => setAdding((value) => !value)}>
          {t("privacy.addPurpose")}
        </Button>
      </div>
      {adding && <PurposeCreateForm onDone={() => setAdding(false)} />}
      <QueryGate query={query} empty={(page) => page.data.length === 0}>
        {(page) => (
          <div
            style={{
              display: "flex",
              gap: 8,
              flexWrap: "wrap",
              marginTop: adding ? 10 : 0,
            }}
          >
            {page.data.map((purpose) => (
              <Badge
                key={purpose.id}
                tone={purpose.requires_double_opt_in ? "warn" : undefined}
              >
                {purpose.label}
                {purpose.requires_double_opt_in ? " · DOI" : ""}
              </Badge>
            ))}
          </div>
        )}
      </QueryGate>
    </section>
  );
}

export function PrivacyInboxCard() {
  const t = useT();
  const { locale } = useLocale();
  const query = useQuery({
    queryKey: ["dsrs"],
    queryFn: async () => {
      const { data, error } = await api.GET("/data-subject-requests", {
        params: { query: { limit: 50 } },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
  });
  return (
    <section className="card">
      <SectionHeader
        title={t("settings.privacy")}
        sub={t("settings.privacySub")}
      />
      <QueryGate query={query} empty={(page) => page.data.length === 0}>
        {(page) => (
          <ul
            style={{
              listStyle: "none",
              display: "flex",
              flexDirection: "column",
              gap: 6,
            }}
          >
            {page.data.map((dsr) => (
              <li
                key={dsr.id}
                style={{ display: "flex", gap: 8, alignItems: "center" }}
              >
                <Badge tone={dsrKindTone(dsr.kind)}>{dsr.kind}</Badge>
                <span className="t-mono">{dsr.subject_ref}</span>
                <Badge
                  tone={dsr.status === "fulfilled" ? "success" : undefined}
                >
                  {dsr.status}
                </Badge>
                <span className="t-small">
                  {t("settings.due", {
                    date: formatDate(dsr.due_at, locale, "Europe/Berlin"),
                  })}
                </span>
              </li>
            ))}
          </ul>
        )}
      </QueryGate>
    </section>
  );
}
