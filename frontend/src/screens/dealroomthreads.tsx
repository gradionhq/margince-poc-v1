import { CheckCheck, MessageSquare } from "lucide-react";
import { useState } from "react";
import type { components } from "../api/schema";
import {
  Badge,
  Button,
  Checkbox,
  Field,
  Textarea,
} from "../design-system/atoms";
import { Panel, PanelBody, PanelRow } from "../design-system/panel";
import { Select } from "../design-system/select";
import { useT } from "../i18n";
import "./dealroomthreads.css";

// The conversation, drawn once for both sides. Seller and buyer read the same
// threads in the same shape (the contract serves one projection), so this
// file holds the rendering and takes the verbs as callbacks: who may reply,
// who may resolve, and what a new thread may be about are the caller's
// decisions, and the only things that differ between the two screens.

export type DealRoomThread = components["schemas"]["DealRoomThread"];

export type ThreadVerbs = Readonly<{
  /** Posts a reply; absent when this reader may not write. */
  reply?: (threadId: string, body: string) => Promise<unknown>;
  /** Resolves a thread; absent when this reader may not (the buyer never may). */
  resolve?: (threadId: string) => Promise<unknown>;
  /** Opens a thread; absent when this reader may not write. */
  open?: (input: {
    documentId: string | null;
    body: string;
    requiredChange: boolean;
  }) => Promise<unknown>;
  /** The documents a new thread may be about, by id → title. */
  documents: readonly { id: string; title: string }[];
  /** Whether the composer offers "requires a change" (the buyer's mark). */
  mayRequireChange: boolean;
  /** A sentence saying why writing is refused, when it is. */
  refusal?: string;
}>;

export function ThreadPanel({
  threads,
  verbs,
  documentTitles,
}: Readonly<{
  threads: DealRoomThread[];
  verbs: ThreadVerbs;
  documentTitles: Record<string, string>;
}>) {
  const t = useT();
  return (
    <Panel
      title={t("threads.title")}
      sub={t("threads.sub")}
      titleAction={<Badge>{String(threads.length)}</Badge>}
    >
      {threads.length === 0 ? (
        <PanelBody>
          <p className="t-small">{t("threads.empty")}</p>
        </PanelBody>
      ) : (
        threads.map((thread) => (
          <ThreadRow
            key={thread.id}
            thread={thread}
            verbs={verbs}
            about={
              thread.document_id
                ? documentTitles[thread.document_id]
                : undefined
            }
          />
        ))
      )}
      <PanelBody>
        <ThreadComposer verbs={verbs} />
      </PanelBody>
    </Panel>
  );
}

function ThreadRow({
  thread,
  verbs,
  about,
}: Readonly<{ thread: DealRoomThread; verbs: ThreadVerbs; about?: string }>) {
  const t = useT();
  const [reply, setReply] = useState("");
  const [pending, setPending] = useState<"reply" | "resolve" | null>(null);
  const [error, setError] = useState<string | null>(null);
  const resolved = thread.state === "resolved";
  const act = async (
    kind: "reply" | "resolve",
    run: () => Promise<unknown>,
  ) => {
    setPending(kind);
    setError(null);
    try {
      await run();
      if (kind === "reply") {
        setReply("");
      }
    } catch (failure) {
      setError(failure instanceof Error ? failure.message : String(failure));
    } finally {
      setPending(null);
    }
  };
  return (
    <PanelRow>
      <div className="thread">
        <div className="thread-head">
          <MessageSquare aria-hidden />
          <span className="t-small">
            {about ? t("threads.about", { title: about }) : t("threads.room")}
          </span>
          {thread.required_change ? (
            <Badge>{t("threads.requiredChange")}</Badge>
          ) : null}
          {resolved ? <Badge>{t("threads.resolved")}</Badge> : null}
        </div>
        <ol className="thread-comments">
          {(thread.comments ?? []).map((comment) => (
            <li key={comment.id}>
              <span className="t-small thread-author">
                {comment.author.name} ·{" "}
                {t(
                  comment.author.side === "buyer"
                    ? "threads.sideBuyer"
                    : "threads.sideSeller",
                )}
              </span>
              <p>{comment.body}</p>
            </li>
          ))}
        </ol>
        {!resolved && verbs.reply ? (
          <div className="thread-reply">
            <Field label={t("threads.replyLabel")}>
              {(control) => (
                <Textarea
                  {...control}
                  rows={2}
                  value={reply}
                  onChange={(event) => setReply(event.target.value)}
                />
              )}
            </Field>
            <div className="card-actions">
              <Button
                small
                disabled={reply.trim() === ""}
                pending={pending === "reply"}
                onClick={() => {
                  const run = verbs.reply;
                  if (run) {
                    act("reply", () => run(thread.id, reply.trim()));
                  }
                }}
              >
                {t("threads.reply")}
              </Button>
              {verbs.resolve ? (
                <Button
                  small
                  variant="ghost"
                  pending={pending === "resolve"}
                  onClick={() => {
                    const run = verbs.resolve;
                    if (run) {
                      act("resolve", () => run(thread.id));
                    }
                  }}
                >
                  <CheckCheck aria-hidden />
                  {t("threads.resolve")}
                </Button>
              ) : null}
            </div>
          </div>
        ) : null}
        {!resolved && !verbs.reply && verbs.refusal ? (
          <p className="t-small">{verbs.refusal}</p>
        ) : null}
        {error ? <p className="t-small t-danger">{error}</p> : null}
      </div>
    </PanelRow>
  );
}

function ThreadComposer({ verbs }: Readonly<{ verbs: ThreadVerbs }>) {
  const t = useT();
  const [documentId, setDocumentId] = useState("");
  const [body, setBody] = useState("");
  const [requiredChange, setRequiredChange] = useState(false);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<string | null>(null);
  if (!verbs.open) {
    return verbs.refusal ? <p className="t-small">{verbs.refusal}</p> : null;
  }
  const open = verbs.open;
  const submit = async () => {
    setPending(true);
    setError(null);
    try {
      await open({
        documentId: documentId === "" ? null : documentId,
        body: body.trim(),
        requiredChange: documentId !== "" && requiredChange,
      });
      setBody("");
      setRequiredChange(false);
    } catch (failure) {
      setError(failure instanceof Error ? failure.message : String(failure));
    } finally {
      setPending(false);
    }
  };
  return (
    <div className="thread-composer">
      <Field label={t("threads.aboutLabel")}>
        {(control) => (
          <Select
            id={control.id}
            options={[
              { value: "", label: t("threads.room") },
              ...verbs.documents.map((d) => ({ value: d.id, label: d.title })),
            ]}
            value={documentId}
            onChange={setDocumentId}
          />
        )}
      </Field>
      <Field label={t("threads.newLabel")}>
        {(control) => (
          <Textarea
            {...control}
            rows={3}
            value={body}
            onChange={(event) => setBody(event.target.value)}
          />
        )}
      </Field>
      {verbs.mayRequireChange && documentId !== "" ? (
        <Checkbox
          label={t("threads.requireChangeLabel")}
          checked={requiredChange}
          onChange={(event) => setRequiredChange(event.target.checked)}
        />
      ) : null}
      <div className="card-actions">
        <Button
          small
          disabled={body.trim() === ""}
          pending={pending}
          onClick={submit}
        >
          {t("threads.open")}
        </Button>
      </div>
      {error ? <p className="t-small t-danger">{error}</p> : null}
    </div>
  );
}
