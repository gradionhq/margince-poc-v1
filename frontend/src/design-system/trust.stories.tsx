import type { Meta, StoryObj } from "@storybook/react-vite";
import { type CSSProperties, type ReactNode, useState } from "react";
import { LocaleProvider } from "../i18n";
import {
  ApprovalGate,
  AutonomyDot,
  ConfidenceMeter,
  EvidenceChip,
  FieldDiff,
  PassportChip,
  type Proposal,
  ProvenanceTag,
  type Resolution,
  StagedProposal,
  StagingCard,
} from "./trust";

// The whole trust vocabulary in one catalog (design-language §4): where a
// value came from, how sure the system is, and whether it is real yet. These
// primitives only mean something next to each other — a confidence dot alone
// says nothing, three of them side by side say what the scale is.
//
// Every component here reads its copy through useT, so the locale is pinned
// rather than left to the reviewing machine's browser: the catalog has to say
// the same words on every screenshot.
const meta: Meta = {
  title: "Design System/Trust",
  parameters: { layout: "padded" },
  decorators: [
    (Story) => (
      <LocaleProvider initial="en">
        <Story />
      </LocaleProvider>
    ),
  ],
};
export default meta;

type Story = StoryObj;

const stack: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: "1rem",
};
const row: CSSProperties = {
  display: "flex",
  gap: "1rem",
  alignItems: "center",
  flexWrap: "wrap",
};

// Each primitive is a bare glyph or chip, so the catalog names it — otherwise
// the two autonomy dots are two dots.
function Specimen({
  caption,
  children,
}: Readonly<{ caption: string; children: ReactNode }>) {
  return (
    <div style={{ display: "flex", alignItems: "center", gap: "0.5rem" }}>
      {children}
      <span className="t-caption">{caption}</span>
    </div>
  );
}

// The two glyphs a reader scans before reading anything: what the system is
// allowed to do on its own, and how much it trusts what it found. Low
// confidence is shown as low, never hidden (§4.2) — there is no prop to
// suppress it, which is why all three levels belong in one row.
export const Signals: Story = {
  render: () => (
    <div style={stack}>
      <div style={row}>
        <Specimen caption="auto — executes without asking">
          <AutonomyDot tier="auto" />
        </Specimen>
        <Specimen caption="confirm — stages for approval">
          <AutonomyDot tier="confirm" />
        </Specimen>
      </div>
      <div style={row}>
        <ConfidenceMeter level="high" />
        <ConfidenceMeter level="med" />
        <ConfidenceMeter level="low" />
      </div>
    </div>
  ),
};

const DEMO_USERS: Record<string, string> = { usr_7f2: "Carol Wagner" };

// Every shape `captured_by` can take, including the two the tag exists to keep
// apart: a value the reader typed themselves and one a colleague typed. Both
// used to read as the reader's own handiwork, which is a false statement about
// who to ask. The unattributed row says so plainly rather than guessing.
export const Provenance: Story = {
  render: () => (
    <div style={stack}>
      <div style={row}>
        <ProvenanceTag provenance={{ kind: "agent", agent: "capture" }} />
        <PassportChip id="psp_7Q3fa91" />
      </div>
      <div style={row}>
        <ProvenanceTag provenance={{ kind: "connector", connector: "gmail" }} />
      </div>
      <div style={row}>
        <ProvenanceTag provenance={{ kind: "human", self: true }} />
        <ProvenanceTag
          provenance={{ kind: "human", self: false, userId: "usr_7f2" }}
          renderUser={(userId) => (
            <strong>{DEMO_USERS[userId] ?? userId}</strong>
          )}
        />
        {/* No renderUser: the design system has no record lookups, so the tag
            says a person entered it without claiming which one. */}
        <ProvenanceTag
          provenance={{ kind: "human", self: false, userId: "usr_7f2" }}
        />
      </div>
      <div style={row}>
        <ProvenanceTag provenance={{ kind: "unknown" }} />
      </div>
    </div>
  ),
};

const WEB_EVIDENCE = {
  snippet: "Series B led by Atlas Ventures, closed 14 May.",
  source: "https://www.example.com/press/2026/series-b-funding-announcement",
};
const INBOX_EVIDENCE = {
  snippet: "We are moving the renewal to October.",
  source: "email 12 Jun",
};

// The three forms of the chip, and the source-shortening with them: the
// collapsed chip strips the scheme and the leading www and truncates in the
// middle, so two snippets from the same site still read apart.
function EvidenceDemo() {
  const [opened, setOpened] = useState<string | null>(null);
  return (
    <div style={stack}>
      <EvidenceChip evidence={WEB_EVIDENCE} />
      <EvidenceChip
        evidence={INBOX_EVIDENCE}
        onOpen={() => setOpened(INBOX_EVIDENCE.source)}
      />
      {opened && <span className="t-caption">Opened source: {opened}</span>}
      <div style={row}>
        <EvidenceChip evidence={WEB_EVIDENCE} collapsed />
        <EvidenceChip evidence={INBOX_EVIDENCE} collapsed />
      </div>
    </div>
  );
}

export const Evidence: Story = {
  render: () => <EvidenceDemo />,
};

// A stored value can be a whole jsonb document, so the long side is capped and
// scrolls — and it becomes a tab stop, because a scroll container a keyboard
// reader cannot reach is content they cannot read at all.
const LONG_VALUE =
  "Renewal terms agreed on the call: 36 months, 8 percent uplift at each " +
  "anniversary, payment 30 days net, and a mutual notice period of 90 days " +
  "before the end of the term. Legal review pending.";

// A null side is an honest marker, never a blank and never a guessed value:
// created and cleared are different facts, and a diff that renders both as an
// empty cell loses the one the reader needs.
export const Diffs: Story = {
  render: () => (
    <div style={stack}>
      <FieldDiff
        oldValue="Globex Renewal"
        newValue="Globex Renewal (updated)"
      />
      <FieldDiff oldValue={null} newValue="Carol Wagner" />
      <FieldDiff oldValue="draft" newValue={null} />
      <FieldDiff oldValue={LONG_VALUE} newValue="Signed" />
    </div>
  ),
};

const TRIAD_OUTCOMES: Record<string, string> = {
  accept: "Accepted — the value keeps its agent provenance.",
  edit: "Edited — the value is human-typed, the evidence stays attached.",
  dismiss: "Dismissed — nothing was written.",
};

// The gate is three buttons and no state of its own, so the story owns the
// outcome and says what each verb means. The universal triad is Accept / Edit
// / Dismiss (§4.4) and it never varies by surface.
function StagingDemo() {
  const [outcome, setOutcome] = useState<string | null>(null);
  return (
    <StagingCard>
      <div style={row}>
        <ProvenanceTag provenance={{ kind: "agent", agent: "enrich" }} />
        <ConfidenceMeter level="med" />
      </div>
      <p style={{ marginTop: "var(--space-2)" }}>
        Headquarters: <span className="staged-value">Munich, Germany</span>
      </p>
      <EvidenceChip evidence={WEB_EVIDENCE} />
      <ApprovalGate
        onAccept={() => setOutcome(TRIAD_OUTCOMES.accept)}
        onEdit={() => setOutcome(TRIAD_OUTCOMES.edit)}
        onDismiss={() => setOutcome(TRIAD_OUTCOMES.dismiss)}
      />
      {outcome && <p className="t-caption">{outcome}</p>}
    </StagingCard>
  );
}

export const Staging: Story = {
  render: () => <StagingDemo />,
};

const EVIDENCED_PROPOSAL: Proposal = {
  description: "Employee count",
  value: "1,200",
  agent: "enrich",
  confidence: "high",
  evidence: WEB_EVIDENCE,
};

// Evidence is optional on a proposal, and the version WITHOUT it is the one
// worth cataloguing: a low-confidence value with nothing behind it is exactly
// what a reader must be able to spot before accepting it.
const BARE_PROPOSAL: Proposal = {
  description: "Industry",
  value: "Logistics",
  agent: "capture",
  confidence: "low",
};

// StagedProposal drives one proposal through the triad itself — accept, edit
// or dismiss it in the canvas and the card resolves in place. Persisting the
// outcome is the caller's job, which is what onResolve reports here.
function ProposalDemo() {
  const [resolution, setResolution] = useState<Resolution | null>(null);
  return (
    <div style={stack}>
      <StagedProposal proposal={EVIDENCED_PROPOSAL} onResolve={setResolution} />
      {resolution && (
        <span className="t-caption">
          onResolve fired: {resolution.outcome}
          {resolution.outcome === "dismissed" ? "" : ` (${resolution.value})`}
        </span>
      )}
      {/* No onResolve: the callback is optional and the card still runs its
          own state machine. */}
      <StagedProposal proposal={BARE_PROPOSAL} />
    </div>
  );
}

export const Proposals: Story = {
  render: () => <ProposalDemo />,
};
