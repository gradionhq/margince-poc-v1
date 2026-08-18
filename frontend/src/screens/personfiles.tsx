import { useInfiniteQuery } from "@tanstack/react-query";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { Badge } from "../design-system/atoms";
import { Panel, PanelRow } from "../design-system/panel";
import { type SectionState, SurfaceState } from "../design-system/surfacestate";
import { formatDateAbbrev } from "../format/format";
import { useLocale, useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { LoadMoreButton, throwProblem } from "./common";
import { RECORD_ZONE } from "./company360";
import "./person360.css";

// The person's own files: unlike the sibling tabs in persontabs.tsx, this one
// is not a read of the 360 composite — the 360 does not carry attachments —
// so it fetches its own page and classifies its own state rather than reusing
// `sectionState`, which exists for a section of a payload this tab never
// receives.

type Attachment = components["schemas"]["Attachment"];
type Category = NonNullable<Attachment["category"]>;

// The most a reader is asked to scan at once on a record page: enough to
// recognise a person that has been busy, and the rest is one button away.
const PAGE_LIMIT = 20;

const CATEGORY_LABELS: Record<Category, MessageKey> = {
  contract: "docs.category.contract",
  offer: "docs.category.offer",
  legal: "docs.category.legal",
  email_attachment: "docs.category.email",
  other: "docs.category.other",
};

export function PersonFilesTab({ personId }: Readonly<{ personId: string }>) {
  const t = useT();
  const { locale } = useLocale();

  const query = useInfiniteQuery({
    queryKey: ["attachments", "person", personId],
    initialPageParam: null as string | null,
    queryFn: async ({ pageParam }) => {
      const { data, error } = await api.GET("/attachments", {
        params: {
          query: {
            entity_type: "person",
            entity_id: personId,
            limit: PAGE_LIMIT,
            ...(pageParam ? { cursor: pageParam } : {}),
          },
        },
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
    getNextPageParam: (last) => last.page.next_cursor ?? null,
  });

  const files = query.data?.pages.flatMap((page) => page.data) ?? [];
  // The truncation is the SERVER's statement, read off the newest page the
  // walk has reached — not `hasNextPage`, which answers the narrower question
  // of whether a cursor came back to walk with. A page that reports more rows
  // without handing over a cursor is still a cut list, and saying so with no
  // button beats claiming completeness this tab cannot verify.
  const hasMore = query.data?.pages.at(-1)?.page.has_more ?? false;

  // Its own endpoint, so its own state — a failed read (`failed`) and an
  // empty library (`empty`) are different sentences and only one is about
  // this person. `partial` is what the reader is owed while rows remain
  // unread: the ones fetched so far, the sentence that says so, and the
  // button that fetches the rest. Once the walk reaches the end the state is
  // `ready` and the footer goes silent, so silence means "all of them" and
  // never "the first twenty".
  //
  // A page that fails PART-WAY through the walk keeps the rows already read:
  // the error belongs to one page, not to the library, and replacing twenty
  // files a reader is looking at with "this section did not load" loses more
  // than it explains. The button stays, so a retry is the same click again.
  let state: SectionState;
  if (query.isPending) {
    state = "loading";
  } else if (files.length === 0) {
    state = query.isError ? "failed" : "empty";
  } else {
    state = hasMore ? "partial" : "ready";
  }

  return (
    <Panel
      title={t("tab.documents")}
      // The walk's control belongs to the SECTION, not to any file in it, so it
      // takes the panel's own foot band rather than trailing the rows as one
      // more of them. Passed only where another page exists: the band draws
      // itself whenever it is given anything, and an empty one is a strip of
      // chrome a reader can see and cannot use.
      footer={query.hasNextPage ? <LoadMoreButton query={query} /> : undefined}
    >
      <SurfaceState
        state={state}
        emptyLabel={t("person.documents.empty")}
        detail={
          state === "failed"
            ? { onRetry: () => void query.refetch() }
            : undefined
        }
      >
        {files.map((file) => (
          <PanelRow className="pe-row" key={file.id}>
            {/* The NAME is the download, as it is on the account's own
              document list: a separate action word at the far end of the row
              is a second thing to find for the only thing this row does. The
              title if somebody gave it one, else the filename — a display
              name is what a reader looks for, and the filename is what the
              saved file is called. */}
            <span className="pe-row-label">
              {formatDateAbbrev(file.created_at, locale, RECORD_ZONE)}
            </span>
            <span className="pe-row-value">
              <a
                className="link-button"
                href={`/v1/attachments/${file.id}`}
                download={file.filename}
              >
                {file.title || file.filename}
              </a>
            </span>
            {file.category && (
              <Badge>{t(CATEGORY_LABELS[file.category])}</Badge>
            )}
          </PanelRow>
        ))}
      </SurfaceState>
    </Panel>
  );
}
