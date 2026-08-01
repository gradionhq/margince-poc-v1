import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Upload } from "lucide-react";
import { useRef, useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { Button, SectionHeader } from "../design-system/atoms";
import { useT } from "../i18n";
import { problemMessage } from "./common";
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

type ImportSummary = {
  rows: number;
  imported: number;
  skipped: number;
  confirmed: number;
  suggested: number;
};

function useImportConnections() {
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
        throw new Error(problemMessage(payload));
      }
      return payload as ImportSummary;
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
        throw new Error(problemMessage(error));
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
        throw new Error(problemMessage(error));
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
function LinkedInProfileSection() {
  const t = useT();
  const account = useLinkedInAccount();
  const save = useSaveLinkedInAccount();
  const [draft, setDraft] = useState<string | null>(null);

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

  if (account.isError) {
    return (
      <p role="alert" className="co-error">
        {account.error.message}
      </p>
    );
  }

  return (
    <div className="li-import-profile">
      <label className="li-import-field" htmlFor="linkedin-profile-url">
        {t("linkedinImport.profileLabel")}
        <input
          id="linkedin-profile-url"
          type="url"
          inputMode="url"
          data-testid="linkedin-profile-url"
          placeholder={t("linkedinImport.profilePlaceholder")}
          value={value}
          onChange={(e) => setDraft(e.target.value)}
        />
      </label>
      <p className="co-muted">
        {account.data?.connected
          ? t("linkedinImport.connectedNote")
          : t("linkedinImport.notConnectedNote")}
      </p>
      <div className="li-import-picker">
        <Button
          small
          variant="primary"
          disabled={!dirty || save.isPending}
          onClick={() => save.mutate(value.trim())}
        >
          {t("linkedinImport.saveProfile")}
        </Button>
        {save.isError && (
          <span role="alert" className="co-error">
            {save.error.message}
          </span>
        )}
      </div>
    </div>
  );
}

export function LinkedInImportCard() {
  const t = useT();
  const input = useRef<HTMLInputElement>(null);
  const [fileName, setFileName] = useState<string | null>(null);
  const importer = useImportConnections();

  return (
    <section className="card li-import">
      <SectionHeader
        title={t("linkedinImport.title")}
        sub={t("linkedinImport.sub")}
      />
      <p className="co-muted li-import-explainer">
        {t("linkedinImport.explainer")}
      </p>
      <LinkedInProfileSection />
      {/* The file is named, and named twice: LinkedIn's export archive holds
          a dozen CSVs and picking the wrong one fails with a parse error that
          explains nothing. */}
      <p className="li-import-which">
        <Upload size={16} aria-hidden />
        <span>{t("linkedinImport.whichFile")}</span>
      </p>
      <div className="li-import-picker">
        <label className="li-import-button" htmlFor="linkedin-import-file">
          {t("linkedinImport.choose")}
        </label>
        <input
          id="linkedin-import-file"
          ref={input}
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

      {importer.isPending && (
        <p className="co-muted">{t("linkedinImport.working")}</p>
      )}
      {importer.isError && (
        <p
          role="alert"
          className="co-error"
          data-testid="linkedin-import-error"
        >
          {importer.error.message}
        </p>
      )}
      {importer.isSuccess && <ImportResult summary={importer.data} />}
    </section>
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
