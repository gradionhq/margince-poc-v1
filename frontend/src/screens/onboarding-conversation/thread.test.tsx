/** @vitest-environment jsdom */
import "@testing-library/jest-dom/vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { LocaleProvider } from "../../i18n";
import type { ThreadEntry } from "./conversation-machine";
import { entityQuestion } from "./test-fixtures";
import { ConversationThread } from "./thread";

// The thread's presentation duties: live narration reveals word by word
// while restored entries render instantly, the one live question card
// claims keyboard focus only when no text field has a better claim, and the
// full candidate list renders ONLY for the machine's current pending
// question instance. Every other question entry renders nothing: a settled
// one's record is the answer turn beside it (a plain UserTurn), and a
// superseded one has no record to show at all.

afterEach(cleanup);

const restoredNarration: ThreadEntry = {
  kind: "narration",
  id: "0:recap",
  i18nKey: "ob.conv.recap.back",
};

const liveNarration: ThreadEntry = {
  kind: "narration",
  id: "1:read:started",
  i18nKey: "ob.conv.read.started",
  params: { host: "gradion.com" },
};

const questionEntry: ThreadEntry = {
  kind: "question",
  id: "2:question:clarify-entity",
  question: entityQuestion,
};

type ThreadProps = Readonly<{
  entries: readonly ThreadEntry[];
  pendingQuestionId?: string | null;
  composerValue?: string;
  onAnswer?: (questionId: string, value: string) => void;
  onDismiss?: (questionId: string) => void;
}>;

// The composer guard walks the DOM the workbench provides: the panel class
// around the thread and the composer beside it.
function Harness({
  entries,
  pendingQuestionId = null,
  composerValue = "",
  onAnswer = () => {},
  onDismiss,
}: ThreadProps) {
  // A reader's own turn wears their monogram, read from the ["me"] cache the
  // rest of the app already fills, so the thread needs a query client even
  // where this suite never resolves an identity — an unresolved one is a state
  // the turn has to render anyway.
  return (
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <LocaleProvider initial="en">
        <section className="ob-workbench-panel">
          <ConversationThread
            entries={entries}
            pendingQuestionId={pendingQuestionId}
            onAnswer={onAnswer}
            onDismiss={onDismiss}
          />
          <div className="mw-composer">
            <textarea aria-label="composer" defaultValue={composerValue} />
          </div>
        </section>
      </LocaleProvider>
    </QueryClientProvider>
  );
}

describe("word-by-word reveal", () => {
  it("renders entries present at mount instantly, without reveal markup", () => {
    const { container } = render(<Harness entries={[restoredNarration]} />);
    expect(container.querySelector(".ob-conv-reveal")).toBeNull();
    expect(screen.getByText(/Welcome back\./)).toBeTruthy();
  });

  it("reveals narration that arrives after mount, keeping the full sentence readable", () => {
    const { container, rerender } = render(
      <Harness entries={[restoredNarration]} />,
    );
    rerender(<Harness entries={[restoredNarration, liveNarration]} />);
    const reveal = container.querySelector(".ob-conv-reveal");
    expect(reveal).not.toBeNull();
    // The animated copy is presentation only; the coherent sentence is the
    // visually hidden source next to it.
    expect(reveal?.getAttribute("aria-hidden")).toBe("true");
    expect(screen.getByText(/Reading gradion\.com now/)).toBeTruthy();
    // The restored entry stays plain.
    const bubbles = container.querySelectorAll(".ob-conv-narration");
    expect(bubbles[0]?.querySelector(".ob-conv-reveal")).toBeNull();
  });
});

describe("live question focus", () => {
  it("moves focus to the live question's first option when the composer is idle", () => {
    render(
      <Harness
        entries={[questionEntry]}
        pendingQuestionId={entityQuestion.id}
      />,
    );
    const first = screen.getByRole("button", { name: "Acme GmbH" });
    expect(document.activeElement).toBe(first);
  });

  it("leaves focus alone while the composer holds a draft", () => {
    render(
      <Harness
        entries={[questionEntry]}
        pendingQuestionId={entityQuestion.id}
        composerValue="https://gradion"
      />,
    );
    const first = screen.getByRole("button", { name: "Acme GmbH" });
    expect(document.activeElement).not.toBe(first);
  });

  it("leaves focus alone while a text field is focused", () => {
    const { rerender } = render(<Harness entries={[]} />);
    screen.getByLabelText("composer").focus();
    rerender(
      <Harness
        entries={[questionEntry]}
        pendingQuestionId={entityQuestion.id}
      />,
    );
    expect(document.activeElement).toBe(screen.getByLabelText("composer"));
  });

  it("renders nothing to focus once the question is no longer pending", () => {
    render(<Harness entries={[questionEntry]} pendingQuestionId={null} />);
    expect(
      screen.queryByRole("button", { name: "Acme GmbH" }),
    ).not.toBeInTheDocument();
  });
});

describe("question card interactivity", () => {
  const dismissibleQuestion = {
    ...entityQuestion,
    dismissLabelKey: "ob.conv.clarify.dismiss" as const,
  };
  const dismissibleEntry: ThreadEntry = {
    kind: "question",
    id: "2:question:clarify-entity",
    question: dismissibleQuestion,
  };

  it("a question that is no longer pending renders no card at all, and clicking through is impossible", () => {
    const onAnswer = vi.fn();
    const onDismiss = vi.fn();
    // The machine advanced (a dismissal or answer cleared the pending
    // question); the card's moment passed even though no answer turn for it
    // exists in the thread yet.
    render(
      <Harness
        entries={[dismissibleEntry]}
        pendingQuestionId={null}
        onAnswer={onAnswer}
        onDismiss={onDismiss}
      />,
    );

    expect(
      screen.queryByRole("button", { name: "Acme GmbH" }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", {
        name: "Skip this - I will set it myself",
      }),
    ).not.toBeInTheDocument();
    expect(onAnswer).not.toHaveBeenCalled();
    expect(onDismiss).not.toHaveBeenCalled();
  });

  it("the live pending card stays interactive and answering works", async () => {
    const onAnswer = vi.fn();
    render(
      <Harness
        entries={[dismissibleEntry]}
        pendingQuestionId={entityQuestion.id}
        onAnswer={onAnswer}
      />,
    );

    const option = screen.getByRole("button", {
      name: "Acme GmbH",
    }) as HTMLButtonElement;
    expect(option.disabled).toBe(false);
    await userEvent.click(option);
    expect(onAnswer).toHaveBeenCalledWith(entityQuestion.id, "acme-gmbh");
  });

  // The guarantee: a settled decision is the one-line answer turn the
  // reducer already appends beside it — never a replay of the candidate
  // list it was chosen from.
  it("renders a resolved question as the chosen value alone, with none of the rejected candidate labels", () => {
    const answerTurn: ThreadEntry = {
      kind: "user",
      id: "3:answer:clarify-entity",
      text: "Acme GmbH",
    };
    render(
      <Harness
        entries={[questionEntry, answerTurn]}
        pendingQuestionId={null}
      />,
    );

    expect(screen.getByText("Acme GmbH")).toBeInTheDocument();
    expect(screen.queryByText("Acme Holding SE")).not.toBeInTheDocument();
    // The record is static text, not a button — nothing here is a control
    // the human could press to re-trigger the choice.
    expect(
      screen.queryByRole("button", { name: "Acme GmbH" }),
    ).not.toBeInTheDocument();
  });

  // The skip escape belongs to the live decision; a settled record — chosen
  // or dismissed — is never a control the human can press again.
  it("carries no skip control once a question is settled, dismissed or answered", () => {
    const dismissTurn: ThreadEntry = {
      kind: "user",
      id: "3:answer:clarify-entity",
      i18nKey: "ob.conv.clarify.dismiss",
    };
    render(
      <Harness
        entries={[dismissibleEntry, dismissTurn]}
        pendingQuestionId={null}
        onDismiss={() => {}}
      />,
    );

    expect(
      screen.queryByRole("button", {
        name: "Skip this - I will set it myself",
      }),
    ).not.toBeInTheDocument();
    // The dismissal is still an honest record: it says what happened, as
    // the ordinary user turn it always was.
    expect(
      screen.getByText("Skip this - I will set it myself"),
    ).toBeInTheDocument();
  });
});

describe("the lead", () => {
  it("puts the act's opening turn inside the scrolling log, above the entries", () => {
    // A greeting mounted as a SIBLING of the log cannot shrink and cannot
    // scroll away, so it permanently reduces the transcript to what is left
    // below it. Inside the log it is just the first turn.
    render(
      <LocaleProvider initial="en">
        <ConversationThread
          entries={[liveNarration]}
          pendingQuestionId={null}
          onAnswer={() => {}}
          lead={<p>Hi, I am Margince.</p>}
        />
      </LocaleProvider>,
    );

    const log = screen.getByRole("log");
    const lead = screen.getByText("Hi, I am Margince.");
    expect(log.contains(lead)).toBe(true);
    expect(
      lead.compareDocumentPosition(screen.getByText(/Reading gradion\.com/)) &
        Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy();
  });
});
