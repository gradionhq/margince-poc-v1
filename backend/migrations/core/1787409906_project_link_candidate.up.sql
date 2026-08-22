-- The uncertain rung of the project attribution ladder (PROJ-FORM-4): when no
-- deterministic rung files a captured message, the ladder may still find ONE
-- plausible project — the sole live project of the account the message reaches,
-- or the best-ranked of several by embedding similarity. It never writes that
-- guess as a link. It writes a candidate here and stages a project_attribution
-- approval; a human decides, and only the confirm writes the activity_link.
--
-- The row holds NO subject data. Its evidence is a character-offset range into
-- the activity's own subject or body, never a copied snippet: a snippet would be
-- a second home for correspondence that an erasure anonymizing the activity in
-- place never reaches. Offsets say nothing on their own, and they stop meaning
-- anything the moment the body they index is blanked — which is the right
-- outcome, not a gap.
--
-- ON DELETE CASCADE on both parents: a candidate for a project that was deleted,
-- or for an activity that was, is a question about nothing. In-place
-- anonymization leaves the activity row standing, and the candidate with it —
-- harmless, because the candidate carries nothing of the subject's.
SET LOCAL lock_timeout = '5s';

CREATE TABLE project_link_candidate (
    id uuid DEFAULT uuidv7() NOT NULL,
    activity_id uuid NOT NULL,
    project_id uuid NOT NULL,
    -- How the ladder arrived at this project. sole_live_project: the account
    -- the message reaches has exactly one live project. ranked_similarity: it
    -- has several, and this one's embedding is nearest the message's.
    method text NOT NULL,
    -- 1.0 for a sole project; the cosine similarity for a ranked one.
    confidence numeric(5,4) NOT NULL,
    -- The offset range (in characters, half-open, 0-based) of the span that
    -- names the project, in the field named — or NULL when nothing in the text
    -- names it and the evidence is the account reach alone.
    evidence_field text,
    evidence_start integer,
    evidence_end integer,
    -- The approval a human decides this through. SET NULL rather than
    -- CASCADE: the approvals engine may expire or withdraw its row, and the
    -- candidate remains the record that the question was asked.
    proposal_id uuid,
    -- expired: the window closed with nobody answering. It frees the
    -- live-row index below, so the question may be asked again; only a
    -- rejection is remembered as a refusal.
    status text DEFAULT 'pending'::text NOT NULL,
    decided_at timestamptz,
    version bigint DEFAULT 1 NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT project_link_candidate_pkey PRIMARY KEY (id),
    CONSTRAINT project_link_candidate_activity_id_fkey FOREIGN KEY (activity_id)
        REFERENCES activity(id) ON DELETE CASCADE,
    CONSTRAINT project_link_candidate_project_id_fkey FOREIGN KEY (project_id)
        REFERENCES project(id) ON DELETE CASCADE,
    CONSTRAINT project_link_candidate_proposal_id_fkey FOREIGN KEY (proposal_id)
        REFERENCES approval(id) ON DELETE SET NULL,
    CONSTRAINT project_link_candidate_method_check
        CHECK (method IN ('sole_live_project', 'ranked_similarity')),
    CONSTRAINT project_link_candidate_status_check
        CHECK (status IN ('pending', 'confirmed', 'rejected', 'expired')),
    CONSTRAINT project_link_candidate_confidence_check
        CHECK (confidence >= 0 AND confidence <= 1),
    -- Evidence is all three columns or none; a half-set range is unreadable.
    CONSTRAINT project_link_candidate_evidence_shape CHECK (
        (evidence_field IS NULL AND evidence_start IS NULL AND evidence_end IS NULL)
        OR (evidence_field IN ('subject', 'body')
            AND evidence_start IS NOT NULL AND evidence_end IS NOT NULL
            AND evidence_start >= 0 AND evidence_end > evidence_start)),
    CONSTRAINT project_link_candidate_decided CHECK (
        (status = 'pending' AND decided_at IS NULL)
        OR (status <> 'pending' AND decided_at IS NOT NULL))
);

-- One open question per message at a time.
CREATE UNIQUE INDEX uq_project_link_candidate_pending
    ON project_link_candidate (activity_id) WHERE status = 'pending';
-- The coverage number on the project page counts the pending rows per project.
CREATE INDEX idx_project_link_candidate_project
    ON project_link_candidate (project_id) WHERE status = 'pending';
-- The confirm and the decline find the candidate by the approval it rode on.
CREATE INDEX idx_project_link_candidate_proposal
    ON project_link_candidate (proposal_id) WHERE proposal_id IS NOT NULL;

CREATE TRIGGER project_link_candidate_touch BEFORE UPDATE ON project_link_candidate
    FOR EACH ROW EXECUTE FUNCTION set_updated_at_bump_version();

GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE project_link_candidate TO margince_app;
