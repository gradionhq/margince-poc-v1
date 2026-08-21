// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useId, useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { Button, Field, Modal, TextInput } from "../design-system/atoms";
import { Callout } from "../design-system/callout";
import { Panel, PanelBody } from "../design-system/panel";
import { SettingList, SettingRow } from "../design-system/settingrow";
import { useT } from "../i18n";
import { problemMessageOf, throwProblem } from "./common";
import "./linkedin-import.css";

// The LinkedIn connections import (ADR-0078 §2.1b).
//
// Your own export, not an integration: LinkedIn hands every member a
// Connections.csv under Settings → Data privacy, and this reads it. No app
// approval, no OAuth, nothing to configure.
//
// The copy says what happens to the file, because a user uploading their
// personal address book into a company system deserves to know before they
// click rather than after. The imported rows never become contacts.
//
// Two decisions, so two rows: which profile the imported network is attributed
// to, and which file to read. The URL is an ANSWER — a value with a verb that
// changes it — so the field that edits it lives in a modal and the row prints
// what is set now. What the import PRODUCED is not an answer to anything and
// gets no row: it is a report, and it renders under the list.

type ImportSummary = {
  rows: number;
  imported: number;
  skipped: number;
  confirmed: number;
  suggested: number;
};

function useImportConnections() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: async (file: File): Promise<ImportSummary> => {
      // Sent as multipart by hand rather than through the typed client: the
      // generated client serializes JSON bodies, and this endpoint takes a
      // file part.
      const body = new FormData();
      body.append("file", file);
      const response = await fetch("/v1/me/linkedin-connections", {
        method: "POST",
        body,
        credentials: "include",
      });
      const payload = await response.json().catch(() => undefined);
      if (!response.ok) {
        throwProblem(payload);
      }
      return payload as ImportSummary;
    },
    // An import changes what all three cards on this tab read, and they hold
    // separate cache keys. Without this the summary reports new matches while
    // the queue beside it still shows the pre-import "nothing waiting" it
    // cached on load — the page contradicts itself and only a reload fixes it.
    onSuccess: async () => {
      await client.invalidateQueries({ queryKey: ACCOUNT_KEY });
      await client.invalidateQueries({ queryKey: ["linkedin-connections"] });
      await client.invalidateQueries({ queryKey: ["linkedin-reach"] });
    },
  });
}

type LinkedInAccount = components["schemas"]["LinkedInAccount"];

const ACCOUNT_KEY = ["linkedin-account"] as const;

function useLinkedInAccount() {
  return useQuery({
    queryKey: ACCOUNT_KEY,
    queryFn: async (): Promise<LinkedInAccount> => {
      const { data, error } = await api.GET("/me/linkedin-account", {});
      if (error) {
        throwProblem(error);
      }
      return data;
    },
  });
}

function useSaveLinkedInAccount() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: async (profileUrl: string): Promise<LinkedInAccount> => {
      const { data, error } = await api.PUT("/me/linkedin-account", {
        // connected:false never revokes — the store keeps an existing
        // authorization. Editing a URL is not disconnecting.
        body: { profile_url: profileUrl, connected: false },
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
    onSuccess: (data) => client.setQueryData(ACCOUNT_KEY, data),
  });
}

// Your own LinkedIn account: the profile the onboarding act recorded, shown
// back so it can be corrected. A member is the only authority on their own
// profile URL, so this is the caller's row and nobody else's — the API has no
// path to another member's.
//
// The row is the brief's value-and-verb shape: the URL that is stored reads on
// the left, the verb that changes it sits at the same x as every other answer on
// the page, and the field itself is in a modal. One input is not what put it
// there — a URL with an Edit beside it is a row that says what it is set to,
// while a text box in the card says only that something is editable.
function LinkedInProfileRow() {
  const t = useT();
  const account = useLinkedInAccount();
  const save = useSaveLinkedInAccount();
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState<string | null>(null);
  const headingId = useId();

  const stored = account.data?.profile_url ?? "";
  // Adopt the server value until the member starts typing, so a save or a
  // refetch is reflected without discarding an edit in progress. Adjusted
  // during render rather than in an effect: an effect would paint the stale
  // value first and then correct it.
  const [seen, setSeen] = useState(stored);
  if (seen !== stored) {
    setSeen(stored);
    setDraft(null);
  }
  const value = draft ?? stored;
  const dirty = value.trim() !== stored;

  // Closing is abandoning: the draft goes and so does the previous attempt's
  // refusal, which would otherwise be on screen the next time the dialog opens
  // and read as a fresh failure of an edit nobody has made yet.
  const close = () => {
    setEditing(false);
    setDraft(null);
    save.reset();
  };

  if (account.isError) {
    // The row keeps its place and says the read failed. `SettingList` rules
    // between whatever it holds, so a surface standing in for a row lines up
    // with the rows around it.
    return (
      <Callout tone="danger" live="alert">
        {problemMessageOf(account.error, t)}
      </Callout>
    );
  }

  return (
    <>
      <SettingRow
        label={t("linkedinImport.profileLabel")}
        // The answer the row holds. An empty URL is not a blank cell — it is
        // "we have not recorded one", which is a different claim from "the
        // member has no profile".
        value={stored === "" ? t("linkedinImport.profileNotSet") : stored}
        // What the setting DOES, which is what a description is for. The
        // not-connected arm used to open "Not connected yet." — the same fact
        // the `value` beside it already states as "Not recorded yet", said
        // twice on one row in two different words.
        description={
          account.data?.connected
            ? t("linkedinImport.connectedNote")
            : t("linkedinImport.notConnectedNote")
        }
        control={
          <Button small variant="ghost" onClick={() => setEditing(true)}>
            {t("linkedinImport.editProfile")}
          </Button>
        }
      />
      <Modal open={editing} onClose={close} labelledBy={headingId}>
        <h2 id={headingId} className="t-h2">
          {t("linkedinImport.editProfileTitle")}
        </h2>
        <form
          className="form-stack li-import-profile-form"
          onSubmit={(event) => {
            event.preventDefault();
            if (dirty) {
              save.mutate(value.trim(), { onSuccess: () => setEditing(false) });
            }
          }}
        >
          {/* Field + TextInput, not a hand-rolled label wrapping a bare <input>:
              this one box had its own label type, its own padding, its own border
              and its own focus ring, none of which agreed with the field beside it
              in any other dialog on the tab. */}
          <Field label={t("linkedinImport.profileLabel")}>
            {(control) => (
              <TextInput
                {...control}
                type="url"
                inputMode="url"
                data-testid="linkedin-profile-url"
                placeholder={t("linkedinImport.profilePlaceholder")}
                value={value}
                onChange={(e) => setDraft(e.target.value)}
              />
            )}
          </Field>
          {save.isError && (
            <Callout tone="danger" live="alert">
              {problemMessageOf(save.error, t)}
            </Callout>
          )}
          <div className="actions">
            <Button
              small
              type="button"
              onClick={close}
              disabled={save.isPending}
            >
              {t("create.cancel")}
            </Button>
            {/* An unchanged URL and a save in flight are two different
                unavailabilities, and the design system draws them differently:
                `disabled` for the precondition the reader can fix by typing,
                `pending` for the write they have already started, which keeps
                the button focusable so the wait is announced from it. */}
            <Button
              small
              variant="primary"
              type="submit"
              disabled={!dirty}
              pending={save.isPending}
            >
              {t("linkedinImport.saveProfile")}
            </Button>
          </div>
        </form>
      </Modal>
    </>
  );
}

export function LinkedInImportCard() {
  const t = useT();
  const [fileName, setFileName] = useState<string | null>(null);
  const importer = useImportConnections();

  return (
    // No per-card bottom margin: the tab owns the rhythm between its cards.
    <Panel title={t("linkedinImport.title")}>
      <PanelBody>
        {/* ONE description, in the spacing contract's spelling. A second
            full-width paragraph under it — where to get the file, and what
            happens to it — was a wall of prose before the card's first row, and
            both halves of it belong to the import row rather than to the card:
            they say what that one setting does. */}
        <p className="settings-panel-sub">{t("linkedinImport.sub")}</p>
        <SettingList>
          <LinkedInProfileRow />
          {/* The file is NAMED on the left, which is the row language doing the
              job an icon used to: LinkedIn's export archive holds a dozen CSVs
              and picking the wrong one fails with a parse error that explains
              nothing, so the name of the file sits in the description the picker
              is announced with rather than beside a glyph above it. The
              description also carries what happens to the file — a member
              uploading their own address book into a company system is owed that
              before they press, and beside the picker is where they read it. */}
          <SettingRow
            label={t("linkedinImport.importLabel")}
            description={t("linkedinImport.whichFile")}
            control={
              <div className="li-import-picker">
                <label
                  className="li-import-button"
                  htmlFor="linkedin-import-file"
                >
                  {t("linkedinImport.choose")}
                </label>
                <input
                  id="linkedin-import-file"
                  type="file"
                  accept=".csv,text/csv"
                  data-testid="linkedin-import-file"
                  onChange={(e) => {
                    const file = e.target.files?.[0];
                    setFileName(file?.name ?? null);
                    if (file) {
                      importer.mutate(file);
                    }
                  }}
                />
                {fileName && <span className="co-muted">{fileName}</span>}
              </div>
            }
          />
        </SettingList>

        {/* What the import DID. Not a row: a row is an answer to a question the
            card asks, and this is a report of an act that has already happened —
            it has no setting to line up with and no verb of its own. */}
        {importer.isPending && (
          <p className="co-muted">{t("linkedinImport.working")}</p>
        )}
        {importer.isError && (
          <div data-testid="linkedin-import-error">
            <Callout tone="danger" live="alert">
              {problemMessageOf(importer.error, t)}
            </Callout>
          </div>
        )}
        {importer.isSuccess && <ImportResult summary={importer.data} />}
      </PanelBody>
    </Panel>
  );
}

// ImportResult states what happened in the terms someone checking the import
// would ask. Skipped rows are shown rather than hidden: a file half-ignored
// under a success message is worse than a refusal.
function ImportResult({ summary }: Readonly<{ summary: ImportSummary }>) {
  const t = useT();
  return (
    <>
      <dl data-testid="linkedin-import-result" className="li-import-result">
        <div>
          <dt>{t("linkedinImport.imported")}</dt>
          <dd>{summary.imported}</dd>
        </div>
        <div>
          <dt>{t("linkedinImport.confirmed")}</dt>
          <dd>{summary.confirmed}</dd>
        </div>
        <div>
          <dt>{t("linkedinImport.suggested")}</dt>
          <dd>{summary.suggested}</dd>
        </div>
        {summary.skipped > 0 && (
          <div>
            <dt>{t("linkedinImport.skipped")}</dt>
            <dd>{summary.skipped}</dd>
          </div>
        )}
      </dl>
      {/* Zero matches on a new workspace is expected rather than wrong: the
          contacts an export matches arrive with mail capture over the hours
          after it. Saying so stops it reading as a broken import. */}
      {summary.confirmed + summary.suggested === 0 && summary.imported > 0 && (
        <p className="co-muted">{t("linkedinImport.noMatchesYet")}</p>
      )}
    </>
  );
}
