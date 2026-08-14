-- 0246: the lead score keeps the reasoning that produced it (ADR-0105/A156).
--
-- Scoring already computes a weighted-factor breakdown and throws it away, so
-- AC-S7's promised decomposition reaches no client and the number on screen
-- cannot be interrogated. These two tables are what make it answerable.
--
-- Neither carries workspace_id and neither has RLS, matching every table added
-- since ADR-0091: one installation serves one organization (A107), so there is
-- no second tenant for a policy to isolate these from. What protects them is
-- the lead's own row-scope gate on every read path that reaches them.

-- LEADSCORE-DDL-2. The retained series behind "Explain This Score".
--
-- The distinction this table exists to hold: while a Commercial Judgement
-- override is in force (A68/ADR-0053) the DISPLAYED score is the human's number
-- and the machine keeps computing its own beside it, so the factors reconcile
-- to score_computed and NOT to score. Storing one number would make every
-- overridden lead's explanation a lie about which value it explains.
CREATE TABLE lead_score_history (
  id             uuid PRIMARY KEY DEFAULT uuidv7(),

  -- CASCADE: a score history of a deleted lead has nothing left to be a
  -- history OF. Note this does NOT reach the ordinary retention path, which
  -- ANONYMIZES an unconverted lead in place at 365 days (DM-SEED-1) and fires
  -- no ON DELETE — the privacy engine owns that reach explicitly (ADR-0105).
  lead_id        uuid NOT NULL REFERENCES lead(id) ON DELETE CASCADE,

  -- The displayed score: the human's number under an override, else the
  -- machine value.
  score          smallint NOT NULL CHECK (score BETWEEN 0 AND 100),
  -- The machine value the factors below reconcile to. Equals score unless an
  -- override was in force at this point.
  score_computed smallint NOT NULL CHECK (score_computed BETWEEN 0 AND 100),
  -- The Commercial Judgement reason in force here; NULL when machine-computed.
  -- A blank string would be a reason nobody wrote, which A68 rejects.
  override_reason text NULL CHECK (override_reason IS NULL OR length(btrim(override_reason)) > 0),

  -- The weighted-factor breakdown that summed to score_computed, and the two
  -- intermediate values that let a reader reconcile it: raw_sum is fractional,
  -- rounded_sum is it rounded half-up, and score_computed is that clamped to
  -- 0..100. Rounding and clamping are separate steps and a client that cannot
  -- tell them apart reports 100.6 as a clamp source when the clamp actually
  -- acted on 101.
  factors        jsonb NOT NULL,
  raw_sum        numeric(8,3) NOT NULL,
  rounded_sum    smallint NOT NULL,

  computed_at    timestamptz NOT NULL DEFAULT now(),

  -- An override is exactly a score that differs from the machine's by human
  -- decision. Without this a row can claim no override while showing two
  -- different numbers, and the surface rendering it has to guess which half
  -- to believe.
  CONSTRAINT lead_score_history_override_shape CHECK (
    (override_reason IS NOT NULL) OR (score = score_computed)
  )
);

-- The read's own query: the newest entry for the lead on screen, then back
-- through the series under a keyset cursor.
CREATE INDEX idx_lead_score_history_series
  ON lead_score_history (lead_id, computed_at DESC, id DESC);

-- LEADSCORE-DDL-1. What a rep knows and capture cannot fetch (S-E13.6).
--
-- A superseded row is RETAINED rather than deleted: when the auto value
-- arrives it wins, and the rep's estimate stays visible so their number never
-- vanishes without explanation (ADR-0105 §4). Deletion is the rep's own
-- explicit clear, never a side effect of enrichment.
CREATE TABLE lead_manual_signal (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  lead_id       uuid NOT NULL REFERENCES lead(id) ON DELETE CASCADE,

  factor        text NOT NULL CHECK (factor IN ('web_traffic','employees','budget_hint')),
  band          text NOT NULL CHECK (length(btrim(band)) > 0),
  points        smallint NOT NULL,
  signal_kind   text NOT NULL CHECK (signal_kind IN ('fact','assumption','judgement')),
  confidence    numeric(4,3) NULL CHECK (confidence IS NULL OR confidence BETWEEN 0 AND 1),

  -- Always a written reason: a scoring input nobody can account for is the
  -- thing this whole feature exists to end.
  reason        text NOT NULL CHECK (length(btrim(reason)) > 0),

  set_by        uuid NOT NULL REFERENCES app_user(id),
  set_at        timestamptz NOT NULL DEFAULT now(),

  -- Set when an auto source takes the factor over. superseded_by names WHAT
  -- replaced the estimate, so the rep sees more than the fact that it went.
  superseded_at timestamptz NULL,
  superseded_by text NULL CHECK (superseded_by IS NULL OR length(btrim(superseded_by)) > 0),
  CONSTRAINT lead_manual_signal_superseded_shape
    CHECK ((superseded_at IS NULL) = (superseded_by IS NULL))
);

-- One LIVE band per factor. Partial on purpose: a full unique key over
-- (lead, factor) would let a single superseded estimate block the rep from
-- ever entering that factor again.
CREATE UNIQUE INDEX uq_lead_manual_signal_live
  ON lead_manual_signal (lead_id, factor)
  WHERE superseded_at IS NULL;

-- The decomposition reads every signal for the lead, live and superseded
-- alike, newest first.
CREATE INDEX idx_lead_manual_signal_lead
  ON lead_manual_signal (lead_id, set_at DESC);
