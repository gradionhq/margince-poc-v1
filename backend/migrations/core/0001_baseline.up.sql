-- SPDX-License-Identifier: BUSL-1.1
-- SPDX-FileCopyrightText: 2026 Gradion

-- The schema core builds, as the shape it arrives at rather than the path it
-- took. Everything below is one transaction: dbmigrate applies a migration and
-- its ledger row together, so a fresh database either has this whole schema or
-- none of it.
--
-- THE ORDER IS A DEPENDENCY ORDER, not a filing system, and it is the one thing
-- to preserve when editing:
--
--   1. extensions, and the `ext` schema every extension unit's tables live in
--      (ADR-0069)
--   2. the functions a column default can call — uuidv7(), the updated_at and
--      version stamps, and the trigger bodies that enforce what a CHECK cannot
--      express. plpgsql bodies are not resolved at creation, so one may name a
--      table that arrives below it
--   3. every table, with its sequences and column defaults
--   4. the functions that READ a table, which therefore cannot precede one: a
--      `LANGUAGE sql` body IS resolved when the function is created, so
--      last_activity_of_deal naming activity_link fails until activity_link
--      exists
--   5. everything needing every table to already exist — primary keys, unique
--      and foreign-key constraints, indexes, triggers, views, grants, and the
--      reference rows core ships. A foreign key names two tables and
--      person/lead reference each other, so there is no table order that lets
--      these sit with their own table
--
-- A new migration does NOT edit this file. It goes after it, named for the unix
-- second it was written (`make migrate-create`), and it changes
-- testdata/head_catalog.txt in the same commit — which is the diff that shows a
-- reviewer what the schema effect was.

CREATE SCHEMA IF NOT EXISTS ext;

CREATE EXTENSION IF NOT EXISTS btree_gist WITH SCHEMA public;

CREATE EXTENSION IF NOT EXISTS pg_trgm WITH SCHEMA public;

CREATE EXTENSION IF NOT EXISTS unaccent WITH SCHEMA public;

CREATE EXTENSION IF NOT EXISTS vector WITH SCHEMA public;

CREATE FUNCTION activity_ts_config(lang text) RETURNS regconfig
    LANGUAGE sql IMMUTABLE PARALLEL SAFE
    RETURN CASE lang WHEN 'de'::text THEN 'german'::regconfig WHEN 'en'::text THEN 'english'::regconfig ELSE 'simple'::regconfig END;

CREATE FUNCTION comms_outbound_attachments_well_formed(files jsonb) RETURNS boolean
    LANGUAGE sql IMMUTABLE
    AS $$
  SELECT jsonb_typeof(files) = 'array'
     AND NOT EXISTS (
       SELECT 1 FROM jsonb_array_elements(files) AS f
        WHERE jsonb_typeof(f) <> 'object'
           OR f->>'attachment_id' IS NULL
           OR f->>'filename' IS NULL
     );
$$;

CREATE FUNCTION f_unaccent(text) RETURNS text
    LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE
    RETURN unaccent('public.unaccent'::regdictionary, $1);

CREATE FUNCTION f_fold_apostrophes(text) RETURNS text
    LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE
    RETURN replace(f_unaccent($1), ''''::text, ''::text);

CREATE FUNCTION organization_geocode_goes_stale() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
  -- Only when the coordinates exist and the writer did not speak for them:
  -- stamping 'stale' on a row that was never resolved would say the
  -- coordinates are out of date rather than absent, and overriding a writer
  -- that set the status deliberately would undo the worker's own write.
  IF NEW.geocode_status IS DISTINCT FROM OLD.geocode_status THEN
    RETURN NEW;
  END IF;
  IF OLD.geocode_status IS NULL THEN
    RETURN NEW;
  END IF;
  NEW.geocode_status := 'stale';
  RETURN NEW;
END;
$$;

CREATE FUNCTION set_updated_at() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN NEW.updated_at = now(); RETURN NEW; END;
$$;

CREATE FUNCTION set_updated_at_bump_version() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
  IF current_setting('margince.last_activity_move', true) = 'on' THEN
    RETURN NEW;
  END IF;
  NEW.updated_at = now();
  NEW.version = OLD.version + 1;
  RETURN NEW;
END;
$$;

CREATE FUNCTION trg_activity_link_last_activity() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
  IF TG_OP IN ('DELETE', 'UPDATE') THEN
    PERFORM refresh_last_activity_for_link(OLD.person_id, OLD.deal_id, OLD.organization_id);
  END IF;
  IF TG_OP IN ('INSERT', 'UPDATE') THEN
    PERFORM refresh_last_activity_for_link(NEW.person_id, NEW.deal_id, NEW.organization_id);
  END IF;
  RETURN NULL;
END;
$$;

CREATE FUNCTION trg_activity_link_project_last_activity() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
  IF TG_OP IN ('DELETE', 'UPDATE') THEN
    PERFORM move_project_last_activity(OLD.project_id);
  END IF;
  IF TG_OP IN ('INSERT', 'UPDATE') THEN
    PERFORM move_project_last_activity(NEW.project_id);
  END IF;
  RETURN NULL;
END;
$$;

CREATE FUNCTION uuidv7() RETURNS uuid
    LANGUAGE sql
    AS $$
  SELECT encode(
    set_byte(
      set_byte(
        overlay(r PLACING substring(int8send((extract(epoch FROM clock_timestamp()) * 1000)::bigint) FROM 3) FROM 1 FOR 6),
        6, (get_byte(r, 6) & 15) | 112),
      8, (get_byte(r, 8) & 63) | 128),
    'hex')::uuid
  FROM (SELECT uuid_send(gen_random_uuid()) AS r) AS gen
$$;


SET default_tablespace = '';

SET default_table_access_method = heap;

CREATE TABLE activity (
    id uuid DEFAULT uuidv7() NOT NULL,
    kind text NOT NULL,
    subject text,
    body text,
    occurred_at timestamptz DEFAULT now() NOT NULL,
    due_at timestamptz,
    assignee_id uuid,
    is_done boolean DEFAULT false NOT NULL,
    done_at timestamptz,
    duration_seconds integer,
    direction text,
    meeting_status text,
    source_system text,
    source_id text,
    source text NOT NULL,
    captured_by text NOT NULL,
    raw jsonb,
    version bigint DEFAULT 1 NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    archived_at timestamptz,
    host_user_id uuid,
    remind_at timestamptz,
    language text,
    search_tsv tsvector GENERATED ALWAYS AS ((((setweight(to_tsvector('simple'::regconfig, f_unaccent(COALESCE(subject, ''::text))), 'A'::"char") || setweight(to_tsvector('simple'::regconfig, f_fold_apostrophes(COALESCE(subject, ''::text))), 'A'::"char")) || setweight(to_tsvector(activity_ts_config(language), f_unaccent(((COALESCE(subject, ''::text) || ' '::text) || COALESCE(body, ''::text)))), 'B'::"char")) || setweight(to_tsvector('simple'::regconfig, f_unaccent(COALESCE(body, ''::text))), 'C'::"char"))) STORED,
    thread_key text,
    capture_label text,
    capture_labeled_at timestamptz,
    counterparty_email text,
    counterparty_outbound_attested boolean DEFAULT false NOT NULL,
    bulk_mail_attested boolean DEFAULT false NOT NULL,
    channel_provider text,
    retention_class text,
    retention_class_at timestamptz,
    restricted_at timestamptz,
    restricted_reason text,
    restricted_until timestamptz,
    redacted_fields text[] DEFAULT '{}'::text[] NOT NULL,
    audience text DEFAULT 'workspace'::text NOT NULL,
    CONSTRAINT activity_audience_check CHECK (audience IN ('workspace', 'participants', 'selected')),
    CONSTRAINT activity_capture_label_check CHECK (capture_label IS NULL OR capture_label IN ('commitment', 'meeting', 'noise')),
    CONSTRAINT activity_direction_check CHECK (direction IS NULL OR direction IN ('inbound', 'outbound')),
    CONSTRAINT activity_done_at CHECK (((is_done = false) OR (done_at IS NOT NULL))),
    CONSTRAINT activity_language_check CHECK (language IS NULL OR language IN ('de', 'en')),
    CONSTRAINT activity_meeting_host CHECK (((host_user_id IS NULL) OR (kind = 'meeting'::text))),
    CONSTRAINT activity_meeting_status_check CHECK (meeting_status IS NULL OR meeting_status IN ('booked', 'held', 'no_show', 'canceled')),
    CONSTRAINT activity_message_has_provider CHECK (((kind = 'message'::text) = (channel_provider IS NOT NULL))),
    CONSTRAINT activity_restricted_is_archived CHECK (((restricted_at IS NULL) OR (archived_at IS NOT NULL))),
    CONSTRAINT activity_restriction_complete CHECK ((((restricted_at IS NULL) AND (restricted_reason IS NULL) AND (restricted_until IS NULL)) OR ((restricted_at IS NOT NULL) AND (restricted_reason IS NOT NULL) AND (restricted_until IS NOT NULL)))),
    CONSTRAINT activity_restriction_needs_class CHECK (((restricted_at IS NULL) OR (retention_class IS NOT NULL))),
    CONSTRAINT activity_restriction_window CHECK (((restricted_until IS NULL) OR (restricted_until > restricted_at))),
    CONSTRAINT activity_retention_class_known CHECK (((retention_class IS NULL) OR (retention_class = 'commercial_correspondence'::text))),
    CONSTRAINT activity_retention_class_stamped CHECK (((retention_class IS NULL) = (retention_class_at IS NULL))),
    CONSTRAINT activity_task_fields CHECK (((kind = 'task'::text) OR ((due_at IS NULL) AND (assignee_id IS NULL) AND (is_done = false) AND (remind_at IS NULL))))
);

CREATE TABLE activity_audience_member (
    activity_id uuid NOT NULL,
    subject_type text NOT NULL,
    subject_id uuid NOT NULL,
    created_by text NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT activity_audience_member_subject_type_check CHECK (subject_type IN ('user', 'team'))
);

CREATE TABLE activity_link (
    id uuid DEFAULT uuidv7() NOT NULL,
    activity_id uuid NOT NULL,
    entity_type text NOT NULL,
    person_id uuid,
    organization_id uuid,
    deal_id uuid,
    created_at timestamptz DEFAULT now() NOT NULL,
    lead_id uuid,
    project_id uuid,
    CONSTRAINT activity_link_entity_type_check CHECK (entity_type IN ('person', 'organization', 'deal', 'lead', 'project')),
    CONSTRAINT activity_link_shape CHECK ((((entity_type = 'person'::text) AND (person_id IS NOT NULL) AND (organization_id IS NULL) AND (deal_id IS NULL) AND (lead_id IS NULL) AND (project_id IS NULL)) OR ((entity_type = 'organization'::text) AND (organization_id IS NOT NULL) AND (person_id IS NULL) AND (deal_id IS NULL) AND (lead_id IS NULL) AND (project_id IS NULL)) OR ((entity_type = 'deal'::text) AND (deal_id IS NOT NULL) AND (person_id IS NULL) AND (organization_id IS NULL) AND (lead_id IS NULL) AND (project_id IS NULL)) OR ((entity_type = 'lead'::text) AND (lead_id IS NOT NULL) AND (person_id IS NULL) AND (organization_id IS NULL) AND (deal_id IS NULL) AND (project_id IS NULL)) OR ((entity_type = 'project'::text) AND (project_id IS NOT NULL) AND (person_id IS NULL) AND (organization_id IS NULL) AND (deal_id IS NULL) AND (lead_id IS NULL))))
);

CREATE TABLE activity_participant (
    id uuid DEFAULT uuidv7() NOT NULL,
    activity_id uuid NOT NULL,
    user_id uuid,
    person_id uuid,
    address text,
    role text NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT activity_participant_identity CHECK (((user_id IS NOT NULL) OR (person_id IS NOT NULL) OR (address IS NOT NULL))),
    CONSTRAINT activity_participant_role_check CHECK (role IN ('from', 'to', 'cc', 'bcc', 'attendee', 'organizer'))
);

CREATE TABLE activity_retention_evidence (
    id uuid DEFAULT uuidv7() NOT NULL,
    activity_id uuid NOT NULL,
    basis text NOT NULL,
    qualified_at timestamptz NOT NULL,
    deal_id uuid,
    deal_name text,
    decided_by uuid,
    decided_by_name text,
    reason text,
    created_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT activity_retention_evidence_basis_check CHECK (basis IN ('deal_won', 'offer_beyond_draft', 'controller_pin')),
    CONSTRAINT are_deal_name_with_id CHECK (((deal_id IS NULL) OR (deal_name IS NOT NULL))),
    CONSTRAINT are_derived_names_its_deal CHECK (((basis = 'controller_pin'::text) OR ((deal_name IS NOT NULL) AND (decided_by IS NULL) AND (decided_by_name IS NULL) AND (reason IS NULL)))),
    CONSTRAINT are_pin_is_attributed CHECK (((basis <> 'controller_pin'::text) OR ((length(btrim(decided_by_name)) > 0) AND (length(btrim(reason)) > 0))))
);

CREATE TABLE attachment (
    id uuid DEFAULT uuidv7() NOT NULL,
    entity_type text NOT NULL,
    entity_id uuid NOT NULL,
    filename text NOT NULL,
    content_type text,
    byte_size bigint,
    storage_key text NOT NULL,
    checksum text,
    source text NOT NULL,
    captured_by text NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    archived_at timestamptz,
    category text DEFAULT 'other'::text NOT NULL,
    title text,
    doc_state text DEFAULT 'current'::text NOT NULL,
    pinned boolean DEFAULT false NOT NULL,
    supersedes_id uuid,
    organization_id uuid,
    deal_id uuid,
    project_id uuid,
    activity_id uuid,
    external_source_id text,
    external_part_id text,
    declared_type text,
    contract_id uuid,
    CONSTRAINT attachment_category_check CHECK (category IN ('contract', 'offer', 'legal', 'email_attachment', 'message_attachment', 'other')),
    CONSTRAINT attachment_doc_state_check CHECK (doc_state IN ('draft', 'current', 'final', 'superseded')),
    CONSTRAINT attachment_entity_type_check CHECK (entity_type IN ('person', 'organization', 'deal', 'lead', 'activity', 'project', 'relationship')),
    CONSTRAINT attachment_external_identity_complete CHECK (((external_source_id IS NULL) = (external_part_id IS NULL))),
    CONSTRAINT attachment_supersedes_not_self CHECK (((supersedes_id IS NULL) OR (supersedes_id <> id)))
);

CREATE TABLE attachment_extraction (
    id uuid DEFAULT uuidv7() NOT NULL,
    attachment_id uuid NOT NULL,
    status text DEFAULT 'queued'::text NOT NULL,
    status_detail text,
    fields jsonb DEFAULT '[]'::jsonb NOT NULL,
    requested_by text NOT NULL,
    started_at timestamptz,
    finished_at timestamptz,
    created_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT attachment_extraction_fields_shape CHECK ((jsonb_typeof(fields) = 'array'::text)),
    CONSTRAINT attachment_extraction_started_shape CHECK ((((status = 'queued'::text) AND (started_at IS NULL)) OR ((status <> 'queued'::text) AND (started_at IS NOT NULL)))),
    CONSTRAINT attachment_extraction_status CHECK (status IN ('queued', 'running', 'done', 'failed')),
    CONSTRAINT attachment_extraction_terminal_shape CHECK ((((status IN ('done', 'failed')) AND (finished_at IS NOT NULL)) OR ((status IN ('queued', 'running')) AND (finished_at IS NULL))))
);

CREATE TABLE booking_page (
    id uuid DEFAULT uuidv7() NOT NULL,
    host_user_id uuid NOT NULL,
    slug text NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    revoked_at timestamptz
);

CREATE TABLE scheduled_send (
    id uuid NOT NULL,
    status text DEFAULT 'scheduled'::text NOT NULL,
    scheduled_at timestamptz NOT NULL,
    scheduled_tz text NOT NULL,
    origin_kind text NOT NULL,
    anchor_activity_id uuid,
    origin_links jsonb,
    payload jsonb NOT NULL,
    payload_version integer DEFAULT 1 NOT NULL,
    scheduled_by uuid NOT NULL,
    principal_kind text NOT NULL,
    activity_id uuid,
    delivery_id uuid,
    held_reason text,
    version bigint DEFAULT 1 NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    agent_actor_id text,
    agent_passport_id uuid,
    agent_on_behalf_of uuid,
    CONSTRAINT scheduled_send_agent_provenance_shape CHECK ((((principal_kind = 'agent'::text) AND (agent_actor_id IS NOT NULL)) OR ((agent_actor_id IS NULL) AND (agent_passport_id IS NULL) AND (agent_on_behalf_of IS NULL)))),
    CONSTRAINT scheduled_send_held_shape CHECK ((((status = 'held'::text) AND (held_reason IS NOT NULL)) OR ((status <> 'held'::text) AND (held_reason IS NULL)))),
    CONSTRAINT scheduled_send_origin_kind CHECK (origin_kind IN ('reply', 'account')),
    CONSTRAINT scheduled_send_origin_links_shape CHECK (((origin_links IS NULL) OR (jsonb_typeof(origin_links) = 'array'::text))),
    CONSTRAINT scheduled_send_origin_shape CHECK ((((origin_kind = 'reply'::text) AND (anchor_activity_id IS NOT NULL) AND (origin_links IS NULL)) OR ((origin_kind = 'account'::text) AND (anchor_activity_id IS NULL) AND (origin_links IS NOT NULL)))),
    CONSTRAINT scheduled_send_principal_kind CHECK (principal_kind IN ('human', 'agent')),
    CONSTRAINT scheduled_send_released_shape CHECK ((((status = 'released'::text) AND (activity_id IS NOT NULL) AND (delivery_id IS NOT NULL)) OR ((status <> 'released'::text) AND (activity_id IS NULL) AND (delivery_id IS NULL)))),
    CONSTRAINT scheduled_send_status CHECK (status IN ('scheduled', 'released', 'cancelled', 'held'))
);

CREATE TABLE transcript_read (
    id uuid NOT NULL,
    activity_id uuid NOT NULL,
    status text DEFAULT 'queued'::text NOT NULL,
    status_detail text,
    line_count integer DEFAULT 0 NOT NULL,
    proposal_ids uuid[] DEFAULT '{}'::uuid[] NOT NULL,
    requested_by text NOT NULL,
    started_at timestamptz,
    finished_at timestamptz,
    created_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT transcript_read_started_shape CHECK ((((status = 'queued'::text) AND (started_at IS NULL)) OR ((status <> 'queued'::text) AND (started_at IS NOT NULL)))),
    CONSTRAINT transcript_read_status CHECK (status IN ('queued', 'running', 'done', 'failed')),
    CONSTRAINT transcript_read_terminal_shape CHECK ((((status IN ('done', 'failed')) AND (finished_at IS NOT NULL)) OR ((status IN ('queued', 'running')) AND (finished_at IS NULL))))
);

CREATE TABLE agent_run (
    id uuid DEFAULT uuidv7() NOT NULL,
    agent_spec text NOT NULL,
    goal text NOT NULL,
    trigger_ref text NOT NULL,
    passport_id uuid,
    status text DEFAULT 'running'::text NOT NULL,
    approval_id uuid,
    pending jsonb,
    result jsonb,
    trace jsonb DEFAULT '[]'::jsonb NOT NULL,
    degrade_reason text,
    steps_used integer DEFAULT 0 NOT NULL,
    output_tokens integer DEFAULT 0 NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    finished_at timestamptz,
    CONSTRAINT agent_run_awaiting_shape CHECK (((status <> 'awaiting_approval'::text) OR ((approval_id IS NOT NULL) AND (pending IS NOT NULL)))),
    CONSTRAINT agent_run_status_check CHECK (status IN ('running', 'awaiting_approval', 'completed', 'degraded', 'failed'))
);

CREATE TABLE runner_job (
    id uuid DEFAULT uuidv7() NOT NULL,
    agent_spec text NOT NULL,
    trigger_ref text NOT NULL,
    passport_id uuid,
    due_at timestamptz NOT NULL,
    status text DEFAULT 'queued'::text NOT NULL,
    attempts integer DEFAULT 0 NOT NULL,
    last_error text,
    agent_run_id uuid,
    created_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT runner_job_status_check CHECK (status IN ('queued', 'running', 'done', 'failed'))
);

CREATE TABLE ai_call (
    id uuid DEFAULT uuidv7() NOT NULL,
    correlation_id uuid,
    task text NOT NULL,
    tier text DEFAULT ''::text NOT NULL,
    provider text DEFAULT ''::text NOT NULL,
    model_id text DEFAULT ''::text NOT NULL,
    request_fingerprint text NOT NULL,
    tokens_in bigint DEFAULT 0 NOT NULL,
    tokens_out bigint DEFAULT 0 NOT NULL,
    reasoning_tokens bigint DEFAULT 0 NOT NULL,
    cached_tokens bigint DEFAULT 0 NOT NULL,
    latency_ms bigint DEFAULT 0 NOT NULL,
    cache_hit boolean DEFAULT false NOT NULL,
    degraded boolean DEFAULT false NOT NULL,
    error_sentinel text,
    agent_run_id uuid,
    occurred_at timestamptz DEFAULT now() NOT NULL,
    logical_call_id uuid NOT NULL,
    attempt integer DEFAULT 1 NOT NULL,
    is_terminal boolean DEFAULT true NOT NULL,
    attempt_reason text DEFAULT ''::text NOT NULL,
    kind text DEFAULT 'completion'::text NOT NULL,
    served_model text DEFAULT ''::text NOT NULL,
    served_identity_source text DEFAULT 'configured'::text NOT NULL,
    cache_off boolean DEFAULT false NOT NULL,
    config_hash text,
    context_scopes text[] DEFAULT '{}'::text[] NOT NULL,
    context_fingerprint text DEFAULT ''::text NOT NULL,
    context_bytes bigint DEFAULT 0 NOT NULL,
    context_tokens_estimate bigint DEFAULT 0 NOT NULL,
    cache_write_tokens bigint DEFAULT 0 NOT NULL,
    CONSTRAINT ai_call_context_bytes_check CHECK ((context_bytes >= 0)),
    CONSTRAINT ai_call_context_fingerprint_check CHECK (((context_fingerprint = ''::text) OR (context_fingerprint ~ '^[0-9a-f]{64}$'::text))),
    CONSTRAINT ai_call_context_scopes_check CHECK ((context_scopes <@ ARRAY['identity'::text, 'positioning'::text, 'sales'::text, 'offer'::text, 'market'::text, 'proof'::text, 'administrative'::text])),
    CONSTRAINT ai_call_context_tokens_estimate_check CHECK ((context_tokens_estimate >= 0)),
    CONSTRAINT ai_call_kind_check CHECK (kind IN ('completion', 'embedding')),
    CONSTRAINT ai_call_source_check CHECK (served_identity_source IN ('response', 'echo', 'configured'))
);

CREATE TABLE ai_call_config (
    hash text NOT NULL,
    task_contract_hash text NOT NULL,
    routing_config_hash text NOT NULL,
    prompt_version text DEFAULT ''::text NOT NULL,
    provider_params jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL
);

CREATE TABLE ai_call_payload (
    id uuid DEFAULT uuidv7() NOT NULL,
    ai_call_id uuid NOT NULL,
    request_payload jsonb NOT NULL,
    response_payload jsonb NOT NULL,
    occurred_at timestamptz DEFAULT now() NOT NULL
);

CREATE TABLE ai_feedback (
    id uuid DEFAULT uuidv7() NOT NULL,
    subject_type text NOT NULL,
    subject_id uuid NOT NULL,
    claim_kind text NOT NULL,
    claim_key text NOT NULL,
    verdict text NOT NULL,
    corrected_value text,
    note text,
    source text NOT NULL,
    captured_by text NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT ai_feedback_claim_kind_check CHECK (claim_kind IN ('profile_field', 'inferred_kpi', 'next_step', 'signal', 'research_claim')),
    CONSTRAINT ai_feedback_corrected_carries_a_value CHECK (((verdict = 'corrected'::text) = (corrected_value IS NOT NULL))),
    CONSTRAINT ai_feedback_subject_type_check CHECK (subject_type IN ('organization', 'person', 'deal', 'lead')),
    CONSTRAINT ai_feedback_verdict_check CHECK (verdict IN ('corrected', 'suppressed', 'confirmed'))
);

CREATE TABLE ai_model_rate (
    id uuid DEFAULT uuidv7() NOT NULL,
    provider text NOT NULL,
    model_id text NOT NULL,
    input_per_mtok_microusd bigint NOT NULL,
    output_per_mtok_microusd bigint NOT NULL,
    cache_read_per_mtok_microusd bigint DEFAULT 0 NOT NULL,
    cache_write_per_mtok_microusd bigint DEFAULT 0 NOT NULL,
    effective_date date NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT ai_model_rate_cache_read_per_mtok_microusd_check CHECK ((cache_read_per_mtok_microusd >= 0)),
    CONSTRAINT ai_model_rate_cache_write_per_mtok_microusd_check CHECK ((cache_write_per_mtok_microusd >= 0)),
    CONSTRAINT ai_model_rate_input_per_mtok_microusd_check CHECK ((input_per_mtok_microusd >= 0)),
    CONSTRAINT ai_model_rate_output_per_mtok_microusd_check CHECK ((output_per_mtok_microusd >= 0))
);

CREATE TABLE ai_usage (
    day date NOT NULL,
    task text NOT NULL,
    tier text NOT NULL,
    calls bigint DEFAULT 0 NOT NULL,
    cached_hits bigint DEFAULT 0 NOT NULL,
    tokens_in bigint DEFAULT 0 NOT NULL,
    tokens_out bigint DEFAULT 0 NOT NULL,
    reasoning_tokens bigint DEFAULT 0 NOT NULL,
    cached_tokens bigint DEFAULT 0 NOT NULL,
    cache_write_tokens bigint DEFAULT 0 NOT NULL
);

CREATE TABLE voice_build (
    id uuid DEFAULT uuidv7() NOT NULL,
    voice_profile_id uuid NOT NULL,
    requested_by uuid,
    reason text NOT NULL,
    status text NOT NULL,
    stage text,
    source_hash text NOT NULL,
    source_count integer NOT NULL,
    result_version integer,
    candidate_action text DEFAULT 'none'::text NOT NULL,
    status_code text,
    status_detail text,
    next_attempt_at timestamptz,
    started_at timestamptz,
    completed_at timestamptz,
    version bigint DEFAULT 1 NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz,
    archived_at timestamptz,
    CONSTRAINT voice_build_candidate_action_check CHECK (candidate_action IN ('none', 'auto_activated', 'review_required')),
    CONSTRAINT voice_build_deferral_check CHECK ((((status = 'deferred'::text) AND (status_code = 'budget_deferred'::text) AND (next_attempt_at IS NOT NULL)) OR ((status <> 'deferred'::text) AND (status_code IS DISTINCT FROM 'budget_deferred'::text) AND (next_attempt_at IS NULL)))),
    CONSTRAINT voice_build_reason_check CHECK (reason IN ('onboarding', 'manual', 'automatic')),
    CONSTRAINT voice_build_result_version_check CHECK ((result_version >= 1)),
    CONSTRAINT voice_build_source_count_check CHECK ((source_count >= 0)),
    CONSTRAINT voice_build_stage_check CHECK (stage IN ('snapshot', 'extract', 'evaluate', 'activate')),
    CONSTRAINT voice_build_status_check CHECK (status IN ('queued', 'deferred', 'running', 'succeeded', 'failed')),
    CONSTRAINT voice_build_status_code_check CHECK (status_code IN ('budget_deferred', 'model_unavailable', 'invalid_output', 'quality_regression', 'material_drift', 'internal'))
);

CREATE TABLE voice_corpus_source (
    id uuid DEFAULT uuidv7() NOT NULL,
    voice_profile_id uuid NOT NULL,
    kind text NOT NULL,
    register text NOT NULL,
    weight numeric(4,3) DEFAULT 1.0 NOT NULL,
    source_label text NOT NULL,
    source_ref text NOT NULL,
    content text,
    word_count integer NOT NULL,
    excluded boolean DEFAULT false NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz,
    origin text DEFAULT 'manual'::text NOT NULL,
    content_hash text NOT NULL,
    exclusion_reason text,
    extractor_version text DEFAULT 'voice-v1'::text NOT NULL,
    occurred_at timestamptz NOT NULL,
    retention_until timestamptz,
    content_erased_at timestamptz,
    source text NOT NULL,
    captured_by text NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    archived_at timestamptz,
    CONSTRAINT voice_corpus_source_exclusion_check CHECK (((excluded AND (exclusion_reason IS NOT NULL)) OR (NOT excluded))),
    CONSTRAINT voice_corpus_source_kind_check CHECK (kind IN ('email', 'linkedin', 'proposal', 'transcript', 'document', 'other')),
    CONSTRAINT voice_corpus_source_origin_check CHECK (origin IN ('manual', 'capture', 'draft_signal')),
    CONSTRAINT voice_corpus_source_register_check CHECK (register IN ('email', 'social', 'long_form', 'spoken', 'general')),
    CONSTRAINT voice_corpus_source_weight_check CHECK (((weight >= (0)::numeric) AND (weight <= (2)::numeric))),
    CONSTRAINT voice_corpus_source_word_count_check CHECK ((word_count >= 0))
);

CREATE TABLE voice_learning_signal (
    id uuid DEFAULT uuidv7() NOT NULL,
    voice_profile_id uuid NOT NULL,
    profile_version integer,
    draft_ref_hash bytea NOT NULL,
    outcome text NOT NULL,
    generated_original text,
    final_text text,
    final_captured_by text,
    qualifies_as_source boolean DEFAULT false NOT NULL,
    similarity numeric(5,4),
    transformations jsonb DEFAULT '[]'::jsonb NOT NULL,
    retention_until timestamptz NOT NULL,
    content_erased_at timestamptz,
    source text NOT NULL,
    captured_by text NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz,
    archived_at timestamptz,
    CONSTRAINT voice_learning_signal_outcome_check CHECK (outcome IN ('drafted', 'accepted', 'edited_sent', 'rejected')),
    CONSTRAINT voice_learning_signal_profile_version_check CHECK ((profile_version >= 1)),
    CONSTRAINT voice_learning_signal_qualifies_check CHECK (((NOT qualifies_as_source) OR ((outcome = 'edited_sent'::text) AND (final_text IS NOT NULL) AND (final_captured_by ~~ 'human:%'::text)))),
    CONSTRAINT voice_learning_signal_similarity_check CHECK (((similarity >= (0)::numeric) AND (similarity <= (1)::numeric)))
);

CREATE TABLE voice_profile (
    id uuid DEFAULT uuidv7() NOT NULL,
    owner_id uuid,
    scope text DEFAULT 'user'::text NOT NULL,
    model_ref text,
    status text DEFAULT 'collecting'::text NOT NULL,
    voice_profile_md text DEFAULT ''::text NOT NULL,
    profile_version integer DEFAULT 0 NOT NULL,
    personality_md text DEFAULT ''::text NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz,
    archived_at timestamptz,
    team_id uuid,
    auto_learning_enabled boolean DEFAULT false NOT NULL,
    active_source_hash text,
    last_built_at timestamptz,
    source text NOT NULL,
    captured_by text NOT NULL,
    CONSTRAINT voice_profile_derived_versioned CHECK (((voice_profile_md = ''::text) OR (profile_version >= 1))),
    CONSTRAINT voice_profile_scope_check CHECK (scope IN ('user', 'team', 'workspace')),
    CONSTRAINT voice_profile_scope_owner_check CHECK ((((scope = 'user'::text) AND (owner_id IS NOT NULL) AND (team_id IS NULL)) OR ((scope = 'team'::text) AND (owner_id IS NULL) AND (team_id IS NOT NULL)) OR ((scope = 'workspace'::text) AND (owner_id IS NULL) AND (team_id IS NULL)))),
    CONSTRAINT voice_profile_status_check CHECK (status IN ('collecting', 'ready', 'stale')),
    CONSTRAINT voice_profile_version_nonnegative CHECK ((profile_version >= 0))
);

CREATE TABLE voice_profile_delta (
    id uuid DEFAULT uuidv7() NOT NULL,
    voice_profile_id uuid NOT NULL,
    from_version integer,
    to_version integer NOT NULL,
    classification text NOT NULL,
    activation_outcome text NOT NULL,
    delta_json jsonb NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz,
    archived_at timestamptz,
    CONSTRAINT voice_profile_delta_activation_outcome_check CHECK (activation_outcome IN ('auto_activated', 'review_required', 'manually_activated', 'rejected', 'rollback')),
    CONSTRAINT voice_profile_delta_classification_check CHECK (classification IN ('routine', 'material')),
    CONSTRAINT voice_profile_delta_from_version_check CHECK ((from_version >= 1)),
    CONSTRAINT voice_profile_delta_to_version_check CHECK ((to_version >= 1))
);

CREATE TABLE voice_profile_version (
    id uuid DEFAULT uuidv7() NOT NULL,
    voice_profile_id uuid NOT NULL,
    profile_version integer NOT NULL,
    status text NOT NULL,
    voice_profile_md text NOT NULL,
    profile_json jsonb NOT NULL,
    stats_json jsonb NOT NULL,
    source_hash text NOT NULL,
    source_count integer NOT NULL,
    reason text NOT NULL,
    predecessor_version integer,
    model_provider text NOT NULL,
    model_name text NOT NULL,
    builder_version text NOT NULL,
    activation_policy_version text NOT NULL,
    evaluation_json jsonb NOT NULL,
    review_reasons text[] DEFAULT '{}'::text[] NOT NULL,
    activated_at timestamptz,
    source text NOT NULL,
    captured_by text NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz,
    archived_at timestamptz,
    CONSTRAINT voice_profile_version_predecessor_version_check CHECK ((predecessor_version >= 1)),
    CONSTRAINT voice_profile_version_profile_version_check CHECK ((profile_version >= 1)),
    CONSTRAINT voice_profile_version_reason_check CHECK (reason IN ('onboarding', 'manual', 'automatic', 'rollback')),
    CONSTRAINT voice_profile_version_source_count_check CHECK ((source_count >= 0)),
    CONSTRAINT voice_profile_version_status_check CHECK (status IN ('candidate', 'active', 'superseded', 'rejected'))
);

CREATE TABLE approval (
    id uuid DEFAULT uuidv7() NOT NULL,
    kind text NOT NULL,
    status text DEFAULT 'pending'::text NOT NULL,
    proposed_by text NOT NULL,
    on_behalf_of uuid,
    passport_id uuid,
    target_entity_type text,
    target_entity_id uuid,
    target_version bigint,
    summary text,
    proposed_change jsonb NOT NULL,
    diff_hash text NOT NULL,
    expires_at timestamptz NOT NULL,
    decided_by uuid,
    decided_at timestamptz,
    decision_reason text,
    consumed_at timestamptz,
    version bigint DEFAULT 1 NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    bundle_id uuid,
    evidence jsonb,
    CONSTRAINT approval_decided CHECK ((((status = 'pending'::text) AND (decided_at IS NULL)) OR (status = 'expired'::text) OR ((status IN ('approved', 'rejected')) AND (decided_at IS NOT NULL)))),
    CONSTRAINT approval_status_check CHECK (status IN ('pending', 'approved', 'rejected', 'expired'))
);

CREATE TABLE signing_key (
    kid text NOT NULL,
    alg text DEFAULT 'EdDSA'::text NOT NULL,
    private_key bytea NOT NULL,
    public_key bytea NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    retired_at timestamptz,
    CONSTRAINT workspace_signing_key_alg_check CHECK ((alg = 'EdDSA'::text))
);

CREATE TABLE automation (
    id uuid DEFAULT uuidv7() NOT NULL,
    key text NOT NULL,
    name text NOT NULL,
    origin text DEFAULT 'catalog'::text NOT NULL,
    trigger jsonb NOT NULL,
    action jsonb NOT NULL,
    params jsonb DEFAULT '{}'::jsonb NOT NULL,
    owner_id uuid,
    enabled boolean DEFAULT false NOT NULL,
    tier text DEFAULT 'confirmation_required'::text NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz,
    archived_at timestamptz,
    CONSTRAINT automation_origin_check CHECK (origin IN ('catalog', 'agent_authored')),
    CONSTRAINT automation_tier_check CHECK (tier IN ('auto_execute', 'confirmation_required'))
);

CREATE TABLE workflow_run (
    id uuid DEFAULT uuidv7() NOT NULL,
    handler text NOT NULL,
    idempotency_key text NOT NULL,
    trigger_event uuid NOT NULL,
    status text DEFAULT 'applied'::text NOT NULL,
    planned jsonb NOT NULL,
    applied jsonb,
    error text,
    created_at timestamptz DEFAULT now() NOT NULL,
    detail jsonb,
    CONSTRAINT workflow_run_status_check CHECK (status IN ('applied', 'skipped', 'failed', 'requires_approval', 'blocked'))
);

CREATE TABLE capture_auto_enrich_budget (
    budget_date date NOT NULL,
    enqueued integer DEFAULT 0 NOT NULL
);

CREATE TABLE capture_auto_enrich_state (
    organization_id uuid NOT NULL,
    attempts integer DEFAULT 0 NOT NULL,
    last_attempt_at timestamptz,
    next_attempt_at timestamptz DEFAULT now(),
    last_outcome text,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT capture_auto_enrich_state_last_outcome_check CHECK (last_outcome IS NULL OR last_outcome IN ('queued', 'applied', 'empty', 'failed', 'exhausted'))
);

CREATE TABLE capture_backfill (
    id uuid DEFAULT uuidv7() NOT NULL,
    connection_id uuid NOT NULL,
    window_months integer NOT NULL,
    after_date date NOT NULL,
    status text DEFAULT 'queued'::text NOT NULL,
    cursor jsonb,
    total_estimate integer,
    scanned integer DEFAULT 0 NOT NULL,
    captured integer DEFAULT 0 NOT NULL,
    skipped integer DEFAULT 0 NOT NULL,
    people_created integer DEFAULT 0 NOT NULL,
    organizations_created integer DEFAULT 0 NOT NULL,
    dedupe_candidates integer DEFAULT 0 NOT NULL,
    started_at timestamptz,
    completed_at timestamptz,
    last_error_class text,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    consecutive_failures integer DEFAULT 0 NOT NULL,
    inflight_scanned integer DEFAULT 0 NOT NULL,
    inflight_captured integer DEFAULT 0 NOT NULL,
    inflight_skipped integer DEFAULT 0 NOT NULL,
    CONSTRAINT capture_backfill_status_check CHECK (status IN ('queued', 'running', 'done', 'error', 'cancelled')),
    CONSTRAINT capture_backfill_window_months_check CHECK (window_months IN (3, 6, 12, 24, 60))
);

CREATE TABLE capture_connection (
    id uuid DEFAULT uuidv7() NOT NULL,
    provider text NOT NULL,
    user_id uuid NOT NULL,
    scopes text[] DEFAULT '{}'::text[] NOT NULL,
    status text DEFAULT 'disconnected'::text NOT NULL,
    auth bytea,
    sync_cursor jsonb,
    created_at timestamptz DEFAULT now() NOT NULL,
    credential_ref text,
    watch_expires_at timestamptz,
    archived_at timestamptz,
    account_label text,
    provider_scopes text[],
    generation integer DEFAULT 0 NOT NULL,
    account_bound_at timestamptz,
    share_acknowledged_at timestamptz,
    CONSTRAINT capture_connection_provider_check CHECK (provider IN ('gmail', 'gcal', 'imap', 'graph', 'whatsapp', 'telegram', 'offline_demo')),
    CONSTRAINT capture_connection_status_check CHECK (status IN ('connected', 'disconnected', 'error', 'reauth_required'))
);

CREATE TABLE capture_digest (
    id uuid DEFAULT uuidv7() NOT NULL,
    user_id uuid NOT NULL,
    digest_date date NOT NULL,
    payload jsonb NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL
);

CREATE TABLE capture_exclusion (
    id uuid DEFAULT uuidv7() NOT NULL,
    scope text NOT NULL,
    user_id uuid,
    kind text NOT NULL,
    value text NOT NULL,
    created_by text NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT capture_exclusion_kind_check CHECK (kind IN ('address', 'domain')),
    CONSTRAINT capture_exclusion_scope_check CHECK (scope IN ('workspace', 'user')),
    CONSTRAINT capture_exclusion_scope_user CHECK (((scope = 'user'::text) = (user_id IS NOT NULL))),
    CONSTRAINT capture_exclusion_value_check CHECK (((value = lower(value)) AND (value <> ''::text)))
);

CREATE TABLE capture_freemail_domain (
    id uuid DEFAULT uuidv7() NOT NULL,
    domain text NOT NULL,
    kind text NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    created_by uuid,
    CONSTRAINT capture_freemail_domain_domain_check CHECK (((domain = lower(domain)) AND (domain <> ''::text))),
    CONSTRAINT capture_freemail_domain_kind_check CHECK (kind IN ('extra', 'never'))
);

CREATE TABLE capture_pending_counterparty (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    email text NOT NULL,
    domain text,
    display_name text,
    activity_id uuid NOT NULL,
    owner_id uuid NOT NULL,
    status text DEFAULT 'pending'::text NOT NULL,
    disposition_reason text,
    claimed_until timestamptz,
    claimed_by uuid,
    attempts integer DEFAULT 0 NOT NULL,
    next_attempt_at timestamptz,
    proposal_id uuid,
    resolved_at timestamptz,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    kind text,
    CONSTRAINT capture_pending_counterparty_kind_check CHECK (kind IS NULL OR kind IN ('person', 'role_mailbox', 'organization_sender', 'newsletter', 'transactional', 'spam')),
    CONSTRAINT capture_pending_counterparty_status_check CHECK (status IN ('pending', 'unsure', 'real', 'noise', 'suppressed', 'rejected'))
);

CREATE TABLE capture_sync_state (
    connection_id uuid NOT NULL,
    next_sync_at timestamptz DEFAULT now() NOT NULL,
    consecutive_failures integer DEFAULT 0 NOT NULL,
    last_synced_at timestamptz,
    last_success_at timestamptz,
    last_error_class text,
    backfill_active boolean DEFAULT false NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT capture_sync_state_last_error_class_check CHECK (last_error_class IS NULL OR last_error_class IN ('rate_limited', 'unreachable', 'auth', 'history_gone', 'internal'))
);

CREATE TABLE capture_trace (
    id uuid DEFAULT uuidv7() NOT NULL,
    user_id uuid,
    connector text NOT NULL,
    source_system text NOT NULL,
    source_id text NOT NULL,
    outcome text NOT NULL,
    reason text,
    activity_id uuid,
    counterparty text,
    subject text,
    occurred_at timestamptz DEFAULT now() NOT NULL,
    stage text NOT NULL,
    CONSTRAINT capture_trace_connector_check CHECK (((connector ~ '^[a-z][a-z0-9_:.-]*$'::text) AND (char_length(connector) <= 96))),
    CONSTRAINT capture_trace_counterparty_check CHECK (((counterparty IS NULL) OR (char_length(counterparty) <= 320))),
    CONSTRAINT capture_trace_source_id_check CHECK (((length(source_id) > 0) AND (char_length(source_id) <= 512))),
    CONSTRAINT capture_trace_source_system_check CHECK (((length(source_system) > 0) AND (char_length(source_system) <= 128))),
    CONSTRAINT capture_trace_stage_outcome_check CHECK ((((stage = 'internal_drop'::text) AND (outcome IN ('internal', 'suppressed'))) OR ((stage = 'activity_write'::text) AND (outcome = 'fault'::text)) OR ((stage = 'tier_ladder'::text) AND (outcome IN ('captured', 'suppressed', 'deferred', 'fault'))))),
    CONSTRAINT capture_trace_subject_check CHECK (((subject IS NULL) OR (char_length(subject) <= 300)))
);

CREATE TABLE channel_connection (
    id uuid DEFAULT uuidv7() NOT NULL,
    provider text NOT NULL,
    channel_id text NOT NULL,
    channel_label text NOT NULL,
    credential_ref text NOT NULL,
    status text NOT NULL,
    poll_offset bigint DEFAULT 0 NOT NULL,
    connected_by uuid NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    archived_at timestamptz,
    CONSTRAINT channel_connection_provider_check CHECK ((provider = 'telegram'::text)),
    CONSTRAINT channel_connection_status_check CHECK (status IN ('connected', 'disconnected', 'error', 'reauth_required'))
);

CREATE TABLE raw_capture (
    id uuid DEFAULT uuidv7() NOT NULL,
    source_system text NOT NULL,
    source_id text NOT NULL,
    payload jsonb NOT NULL,
    received_at timestamptz DEFAULT now() NOT NULL
);

CREATE TABLE workspace_email_domain (
    domain text NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    source text DEFAULT 'mailbox'::text NOT NULL,
    verified boolean DEFAULT false NOT NULL,
    CONSTRAINT workspace_email_domain_domain_check CHECK (((domain = lower(domain)) AND (domain <> ''::text))),
    CONSTRAINT workspace_email_domain_source_check CHECK (source IN ('admin', 'mailbox'))
);

CREATE TABLE list (
    id uuid DEFAULT uuidv7() NOT NULL,
    name text NOT NULL,
    entity_type text NOT NULL,
    list_type text DEFAULT 'static'::text NOT NULL,
    definition jsonb,
    owner_id uuid,
    team_id uuid,
    version bigint DEFAULT 1 NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    archived_at timestamptz,
    CONSTRAINT list_entity_type_check CHECK (entity_type IN ('person', 'organization', 'deal', 'lead', 'project')),
    CONSTRAINT list_list_type_check CHECK (list_type IN ('static', 'dynamic'))
);

CREATE TABLE list_member (
    id uuid DEFAULT uuidv7() NOT NULL,
    list_id uuid NOT NULL,
    entity_type text NOT NULL,
    entity_id uuid NOT NULL,
    added_by text NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT list_member_entity_type_check CHECK (entity_type IN ('person', 'organization', 'deal', 'lead', 'project'))
);

CREATE TABLE saved_view (
    id uuid DEFAULT uuidv7() NOT NULL,
    owner_id uuid NOT NULL,
    shared_scope text DEFAULT 'private'::text NOT NULL,
    resource text NOT NULL,
    name text NOT NULL,
    query jsonb NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    archived_at timestamptz,
    CONSTRAINT saved_view_resource_check CHECK (resource IN ('people', 'organizations', 'deals', 'activities', 'leads', 'partners')),
    CONSTRAINT saved_view_shared_scope_check CHECK (shared_scope IN ('private', 'team', 'workspace'))
);

CREATE TABLE tag (
    id uuid DEFAULT uuidv7() NOT NULL,
    name text NOT NULL,
    color text,
    version bigint DEFAULT 1 NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    archived_at timestamptz
);

CREATE TABLE taggable (
    id uuid DEFAULT uuidv7() NOT NULL,
    tag_id uuid NOT NULL,
    entity_type text NOT NULL,
    entity_id uuid NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT taggable_entity_type_check CHECK (entity_type IN ('person', 'organization', 'deal', 'lead', 'project'))
);

CREATE TABLE commission_entry (
    id uuid NOT NULL,
    deal_id uuid NOT NULL,
    partner_org_id uuid NOT NULL,
    status text DEFAULT 'accrued'::text NOT NULL,
    trigger_event_id uuid,
    attribution_at_accrual text NOT NULL,
    margin_tier_at_accrual text,
    rate_bps integer NOT NULL,
    basis_amount_minor bigint NOT NULL,
    currency text NOT NULL,
    fx_rate_to_base numeric(20,10),
    amount_minor bigint NOT NULL,
    reversal_of uuid,
    void_reason text,
    captured_by text NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT commission_entry_amount_minor_check CHECK ((amount_minor >= 0)),
    CONSTRAINT commission_entry_attribution_at_accrual_check CHECK (attribution_at_accrual IN ('sourced', 'influenced')),
    CONSTRAINT commission_entry_basis_amount_minor_check CHECK ((basis_amount_minor >= 0)),
    CONSTRAINT commission_entry_currency_check CHECK ((currency ~ '^[A-Z]{3}$'::text)),
    CONSTRAINT commission_entry_rate_bps_check CHECK (((rate_bps >= 0) AND (rate_bps <= 10000))),
    CONSTRAINT commission_entry_status_check CHECK (status IN ('accrued', 'approved', 'paid', 'void')),
    CONSTRAINT commission_reversal_is_void CHECK (((reversal_of IS NULL) OR (status = 'void'::text))),
    CONSTRAINT commission_void_has_reason CHECK (((status <> 'void'::text) OR (void_reason IS NOT NULL)))
);

CREATE TABLE comms_outbound (
    id uuid NOT NULL,
    activity_id uuid NOT NULL,
    user_id uuid NOT NULL,
    provider text NOT NULL,
    message_id text,
    recipients jsonb,
    cc jsonb DEFAULT '[]'::jsonb,
    subject text,
    body text NOT NULL,
    consent_purpose text NOT NULL,
    in_reply_to text,
    references_chain jsonb DEFAULT '[]'::jsonb,
    thread_key text,
    list_unsubscribe text,
    status text DEFAULT 'pending'::text NOT NULL,
    attempts integer DEFAULT 0 NOT NULL,
    reason text,
    provider_message_id text,
    sent_at timestamptz,
    created_at timestamptz DEFAULT now() NOT NULL,
    channel_user_id text,
    inflight_at timestamptz,
    attachments jsonb DEFAULT '[]'::jsonb NOT NULL,
    html_body text,
    from_name text,
    bcc jsonb,
    redacted_fields text[] DEFAULT '{}'::text[] NOT NULL,
    CONSTRAINT comms_outbound_attachments_shape CHECK (comms_outbound_attachments_well_formed(attachments)),
    CONSTRAINT comms_outbound_cc_array CHECK ((jsonb_typeof(cc) = 'array'::text)),
    CONSTRAINT comms_outbound_inflight_is_channel CHECK (((inflight_at IS NULL) OR (channel_user_id IS NOT NULL))),
    CONSTRAINT comms_outbound_recipients_array CHECK ((jsonb_typeof(recipients) = 'array'::text)),
    CONSTRAINT comms_outbound_references_array CHECK ((jsonb_typeof(references_chain) = 'array'::text)),
    CONSTRAINT comms_outbound_shape CHECK ((((channel_user_id IS NULL) AND (message_id IS NOT NULL) AND (recipients IS NOT NULL) AND (cc IS NOT NULL) AND (subject IS NOT NULL) AND (references_chain IS NOT NULL)) OR ((channel_user_id IS NOT NULL) AND (message_id IS NULL) AND (recipients IS NULL) AND (cc IS NULL) AND (subject IS NULL) AND (references_chain IS NULL) AND (thread_key IS NULL) AND (list_unsubscribe IS NULL) AND ((channel_user_id <> ''::text) OR (status <> 'pending'::text))))),
    CONSTRAINT comms_outbound_status CHECK (status IN ('pending', 'sent', 'parked'))
);

CREATE TABLE consent_doi_token (
    id uuid DEFAULT uuidv7() NOT NULL,
    person_id uuid NOT NULL,
    purpose_id uuid NOT NULL,
    token_hash text NOT NULL,
    issued_at timestamptz DEFAULT now() NOT NULL,
    expires_at timestamptz NOT NULL,
    consumed_at timestamptz
);

CREATE TABLE consent_event (
    id uuid DEFAULT uuidv7() NOT NULL,
    person_id uuid,
    purpose_id uuid NOT NULL,
    new_state text NOT NULL,
    lawful_basis text,
    source text NOT NULL,
    policy_text text NOT NULL,
    policy_version text NOT NULL,
    double_opt_in_confirmed_at timestamptz,
    captured_at timestamptz NOT NULL,
    captured_by text NOT NULL,
    lead_id uuid,
    issuance_trigger text,
    confirm_ip text,
    confirm_user_agent text,
    CONSTRAINT consent_event_new_state_check CHECK (new_state IN ('granted', 'withdrawn')),
    CONSTRAINT consent_event_subject CHECK (((person_id IS NOT NULL) OR (lead_id IS NOT NULL)))
);

CREATE TABLE consent_existing_customer_flag (
    person_id uuid NOT NULL,
    sale_reference text NOT NULL,
    collected_at timestamptz NOT NULL,
    similar_goods_note text NOT NULL,
    optout_notice_given boolean NOT NULL,
    set_by_user_id uuid,
    created_at timestamptz DEFAULT now() NOT NULL,
    revoked_at timestamptz,
    revoked_reason text,
    CONSTRAINT consent_existing_customer_notice CHECK (optout_notice_given)
);

CREATE TABLE consent_purpose (
    id uuid DEFAULT uuidv7() NOT NULL,
    key text NOT NULL,
    label text NOT NULL,
    requires_double_opt_in boolean DEFAULT false NOT NULL,
    archived_at timestamptz,
    created_at timestamptz DEFAULT now() NOT NULL,
    class text DEFAULT 'marketing'::text NOT NULL,
    CONSTRAINT consent_purpose_class_check CHECK (class IN ('business_correspondence', 'transactional', 'marketing', 'phone_outreach'))
);

CREATE TABLE consent_qualifying_event (
    id uuid DEFAULT uuidv7() NOT NULL,
    person_id uuid NOT NULL,
    kind text NOT NULL,
    source_entity_type text,
    source_entity_id uuid,
    note text,
    occurred_at timestamptz NOT NULL,
    source text NOT NULL,
    captured_by text NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT consent_qualifying_event_evidence CHECK ((((kind = 'in_person'::text) AND (note IS NOT NULL)) OR ((kind <> 'in_person'::text) AND (source_entity_type IS NOT NULL) AND (source_entity_id IS NOT NULL)))),
    CONSTRAINT consent_qualifying_event_kind_check CHECK (kind IN ('inbound_message', 'inquiry', 'active_deal', 'in_person')),
    CONSTRAINT consent_qualifying_event_source_entity_type_check CHECK (source_entity_type IN ('activity', 'deal'))
);

CREATE TABLE data_subject_request (
    id uuid DEFAULT uuidv7() NOT NULL,
    kind text NOT NULL,
    status text DEFAULT 'open'::text NOT NULL,
    subject_ref text NOT NULL,
    assignee_id uuid,
    due_at timestamptz NOT NULL,
    resolution text,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT data_subject_request_kind_check CHECK (kind IN ('access', 'rectify', 'erasure')),
    CONSTRAINT data_subject_request_status_check CHECK (status IN ('open', 'in_progress', 'fulfilled', 'rejected')),
    CONSTRAINT dsr_resolution_shape CHECK (((status NOT IN ('fulfilled', 'rejected')) OR (resolution IS NOT NULL)))
);

CREATE TABLE person_consent (
    id uuid DEFAULT uuidv7() NOT NULL,
    person_id uuid,
    purpose_id uuid NOT NULL,
    state text DEFAULT 'unknown'::text NOT NULL,
    lawful_basis text,
    captured_at timestamptz,
    source text,
    policy_version text,
    lead_id uuid,
    CONSTRAINT person_consent_state_check CHECK (state IN ('unknown', 'granted', 'withdrawn')),
    CONSTRAINT person_consent_subject CHECK (((person_id IS NOT NULL) OR (lead_id IS NOT NULL)))
);

CREATE TABLE preference_token (
    id uuid DEFAULT uuidv7() NOT NULL,
    person_id uuid NOT NULL,
    token text NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    revoked_at timestamptz,
    expires_at timestamptz NOT NULL
);

CREATE TABLE contract (
    id uuid DEFAULT uuidv7() NOT NULL,
    organization_id uuid NOT NULL,
    deal_id uuid,
    project_id uuid,
    contract_number text,
    title text NOT NULL,
    value_minor bigint,
    currency char(3),
    value_basis text DEFAULT 'total'::text NOT NULL,
    fx_rate_to_base numeric(20,10),
    fx_rate_date date,
    starts_on date,
    ends_on date,
    renewal_on date,
    auto_renew boolean DEFAULT false NOT NULL,
    notice_period_days integer,
    status text DEFAULT 'draft'::text NOT NULL,
    signed_on date,
    cancellation_notice_on date,
    cancellation_effective_on date,
    superseded_by_id uuid,
    source text DEFAULT 'manual'::text NOT NULL,
    captured_by text NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    archived_at timestamptz,
    CONSTRAINT contract_cancellation_order CHECK (((cancellation_notice_on IS NULL) OR (cancellation_effective_on IS NULL) OR (cancellation_effective_on >= cancellation_notice_on))),
    CONSTRAINT contract_cancellation_within_term CHECK (((cancellation_effective_on IS NULL) OR (ends_on IS NULL) OR (cancellation_effective_on <= ends_on))),
    CONSTRAINT contract_fx_pair CHECK (((fx_rate_to_base IS NULL) = (fx_rate_date IS NULL))),
    CONSTRAINT contract_notice_period_days_check CHECK (((notice_period_days IS NULL) OR (notice_period_days >= 0))),
    CONSTRAINT contract_status_check CHECK (status IN ('draft', 'active', 'expired', 'cancelled', 'superseded')),
    CONSTRAINT contract_superseded_agrees CHECK (((status = 'superseded'::text) = (superseded_by_id IS NOT NULL))),
    CONSTRAINT contract_supersedes_not_self CHECK (((superseded_by_id IS NULL) OR (superseded_by_id <> id))),
    CONSTRAINT contract_term_order CHECK (((starts_on IS NULL) OR (ends_on IS NULL) OR (ends_on >= starts_on))),
    CONSTRAINT contract_value_basis_check CHECK (value_basis IN ('total', 'annualized_12m')),
    CONSTRAINT contract_value_pair CHECK (((value_minor IS NULL) = (currency IS NULL)))
);

CREATE TABLE custom_field (
    id uuid DEFAULT uuidv7() NOT NULL,
    object text NOT NULL,
    slug text NOT NULL,
    label text NOT NULL,
    type text NOT NULL,
    status text DEFAULT 'active'::text NOT NULL,
    column_name text NOT NULL,
    currency char(3),
    options jsonb,
    created_by uuid NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    archived_at timestamptz,
    CONSTRAINT custom_field_currency_check CHECK (((currency IS NULL) OR (currency ~ '^[A-Z]{3}$'::text))),
    CONSTRAINT custom_field_object_check CHECK (object IN ('person', 'organization', 'deal', 'lead', 'activity', 'project', 'relationship')),
    CONSTRAINT custom_field_status_check CHECK (status IN ('active', 'retired')),
    CONSTRAINT custom_field_type_check CHECK (type IN ('text', 'number', 'date', 'currency', 'picklist', 'boolean'))
);

CREATE TABLE deal (
    id uuid DEFAULT uuidv7() NOT NULL,
    name text NOT NULL,
    amount_minor bigint,
    currency char(3),
    fx_rate_to_base numeric(20,10),
    fx_rate_date date,
    pipeline_id uuid NOT NULL,
    stage_id uuid NOT NULL,
    organization_id uuid,
    owner_id uuid,
    partner_org_id uuid,
    status text DEFAULT 'open'::text NOT NULL,
    lost_reason text,
    expected_close_date date,
    closed_at timestamptz,
    forecast_category text,
    wait_until date,
    last_activity_at timestamptz,
    source text NOT NULL,
    captured_by text NOT NULL,
    raw jsonb,
    version bigint DEFAULT 1 NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    archived_at timestamptz,
    legal_hold boolean DEFAULT false NOT NULL,
    close_date_provisional boolean DEFAULT false NOT NULL,
    amount_minor_base bigint GENERATED ALWAYS AS ((round(((amount_minor)::numeric * fx_rate_to_base)))::bigint) STORED,
    search_tsv tsvector GENERATED ALWAYS AS ((setweight(to_tsvector('simple'::regconfig, f_unaccent(COALESCE(name, ''::text))), 'A'::"char") || setweight(to_tsvector('simple'::regconfig, f_fold_apostrophes(COALESCE(name, ''::text))), 'A'::"char"))) STORED,
    project_id uuid,
    won_without_contract_reason text,
    won_without_contract_detail text,
    partner_attribution text,
    CONSTRAINT deal_amount_currency_pair CHECK (((amount_minor IS NULL) = (currency IS NULL))),
    CONSTRAINT deal_closed_at CHECK (((status = 'open'::text) OR (closed_at IS NOT NULL))),
    CONSTRAINT deal_closed_fx CHECK (((status = 'open'::text) OR (amount_minor IS NULL) OR (fx_rate_to_base IS NOT NULL))),
    CONSTRAINT deal_currency_check CHECK (((currency IS NULL) OR (currency ~ '^[A-Z]{3}$'::text))),
    CONSTRAINT deal_forecast_category_check CHECK (forecast_category IS NULL OR forecast_category IN ('commit', 'best_case', 'pipeline', 'omitted')),
    CONSTRAINT deal_lost_reason CHECK (((status <> 'lost'::text) OR (lost_reason IS NOT NULL))),
    CONSTRAINT deal_lost_reason_only_when_lost CHECK (((lost_reason IS NULL) OR (status = 'lost'::text))),
    CONSTRAINT deal_partner_attribution_check CHECK (partner_attribution IS NULL OR partner_attribution IN ('sourced', 'influenced')),
    CONSTRAINT deal_partner_attribution_pairing CHECK (((partner_org_id IS NULL) = (partner_attribution IS NULL))),
    CONSTRAINT deal_status_check CHECK (status IN ('open', 'won', 'lost')),
    CONSTRAINT deal_won_without_contract_detail CHECK (((won_without_contract_reason IS DISTINCT FROM 'other'::text) OR ((won_without_contract_detail IS NOT NULL) AND (btrim(won_without_contract_detail) <> ''::text)))),
    CONSTRAINT deal_won_without_contract_only_when_won CHECK (((won_without_contract_reason IS NULL) OR (status = 'won'::text))),
    CONSTRAINT deal_won_without_contract_reason CHECK (won_without_contract_reason IS NULL OR won_without_contract_reason IN ('imported', 'purchase_order', 'verbal', 'renewal_by_email', 'other'))
);

CREATE TABLE deal_forecast_history (
    id uuid DEFAULT uuidv7() NOT NULL,
    deal_id uuid NOT NULL,
    changed_by text NOT NULL,
    changed_at timestamptz DEFAULT now() NOT NULL,
    amount_minor_at_change bigint,
    currency_at_change char(3),
    close_date_at_change date
);

CREATE TABLE deal_stage_history (
    id uuid DEFAULT uuidv7() NOT NULL,
    deal_id uuid NOT NULL,
    from_stage_id uuid,
    to_stage_id uuid NOT NULL,
    changed_by text NOT NULL,
    changed_at timestamptz DEFAULT now() NOT NULL,
    amount_minor_at_change bigint,
    currency_at_change char(3),
    win_probability_at_change smallint,
    CONSTRAINT deal_stage_history_win_probability_at_change_check CHECK (((win_probability_at_change IS NULL) OR ((win_probability_at_change >= 0) AND (win_probability_at_change <= 100))))
);

CREATE TABLE fx_rate (
    id uuid DEFAULT uuidv7() NOT NULL,
    from_currency char(3) NOT NULL,
    to_currency char(3) NOT NULL,
    rate numeric(20,10) NOT NULL,
    rate_date date NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL
);

CREATE TABLE offer (
    id uuid DEFAULT uuidv7() NOT NULL,
    deal_id uuid NOT NULL,
    offer_number text NOT NULL,
    revision integer DEFAULT 1 NOT NULL,
    status text DEFAULT 'draft'::text NOT NULL,
    currency char(3) NOT NULL,
    buyer_org_id uuid,
    buyer_snapshot jsonb,
    issuer_snapshot jsonb,
    valid_until date,
    intro_text text,
    terms_text text,
    net_minor bigint DEFAULT 0 NOT NULL,
    tax_minor bigint DEFAULT 0 NOT NULL,
    gross_minor bigint DEFAULT 0 NOT NULL,
    fx_rate_to_base numeric(20,10),
    fx_rate_date date,
    pdf_asset_ref text,
    accepted_at timestamptz,
    source text NOT NULL,
    captured_by text NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    archived_at timestamptz,
    template_id uuid,
    CONSTRAINT offer_accepted_at CHECK (((status <> 'accepted'::text) OR (accepted_at IS NOT NULL))),
    CONSTRAINT offer_currency_check CHECK ((currency ~ '^[A-Z]{3}$'::text)),
    CONSTRAINT offer_revision_check CHECK ((revision >= 1)),
    CONSTRAINT offer_status_check CHECK (status IN ('draft', 'sent', 'accepted', 'rejected', 'expired', 'superseded'))
);

CREATE TABLE offer_line_item (
    id uuid DEFAULT uuidv7() NOT NULL,
    offer_id uuid NOT NULL,
    "position" integer NOT NULL,
    product_id uuid,
    description text NOT NULL,
    unit text DEFAULT 'unit'::text NOT NULL,
    quantity numeric(14,3) NOT NULL,
    unit_price_minor bigint NOT NULL,
    discount_pct numeric(5,2) DEFAULT 0 NOT NULL,
    tax_rate numeric(5,2) DEFAULT 0 NOT NULL,
    evidence jsonb,
    version bigint DEFAULT 1 NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    proposal_state text DEFAULT 'accepted'::text NOT NULL,
    price_grounded boolean DEFAULT true NOT NULL,
    CONSTRAINT offer_line_item_discount_pct_check CHECK (((discount_pct >= (0)::numeric) AND (discount_pct <= (100)::numeric))),
    CONSTRAINT offer_line_item_position_check CHECK (("position" >= 1)),
    CONSTRAINT offer_line_item_proposal_state_check CHECK (proposal_state IN ('staged', 'accepted')),
    CONSTRAINT offer_line_item_quantity_check CHECK ((quantity > (0)::numeric)),
    CONSTRAINT offer_line_item_tax_rate_check CHECK ((tax_rate >= (0)::numeric)),
    CONSTRAINT offer_line_item_ungrounded_price_zero CHECK ((price_grounded OR (unit_price_minor = 0))),
    CONSTRAINT offer_line_item_unit_price_minor_check CHECK ((unit_price_minor >= 0))
);

CREATE TABLE offer_template (
    id uuid DEFAULT uuidv7() NOT NULL,
    name text NOT NULL,
    locale text DEFAULT 'de-DE'::text NOT NULL,
    is_default boolean DEFAULT false NOT NULL,
    layout jsonb NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    archived_at timestamptz
);

CREATE TABLE pipeline (
    id uuid DEFAULT uuidv7() NOT NULL,
    name text NOT NULL,
    is_default boolean DEFAULT false NOT NULL,
    "position" integer DEFAULT 0 NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    archived_at timestamptz
);

CREATE TABLE product (
    id uuid DEFAULT uuidv7() NOT NULL,
    name text NOT NULL,
    sku text,
    description text,
    unit text DEFAULT 'unit'::text NOT NULL,
    unit_price_minor bigint NOT NULL,
    currency char(3) NOT NULL,
    default_tax_rate numeric(5,2) DEFAULT 0 NOT NULL,
    active boolean DEFAULT true NOT NULL,
    source text NOT NULL,
    captured_by text NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    archived_at timestamptz,
    CONSTRAINT product_currency_check CHECK ((currency ~ '^[A-Z]{3}$'::text)),
    CONSTRAINT product_default_tax_rate_check CHECK ((default_tax_rate >= (0)::numeric)),
    CONSTRAINT product_unit_price_minor_check CHECK ((unit_price_minor >= 0))
);

CREATE TABLE project (
    id uuid DEFAULT uuidv7() NOT NULL,
    name text NOT NULL,
    key text,
    organization_id uuid NOT NULL,
    owner_id uuid,
    phase text DEFAULT 'initiative'::text NOT NULL,
    closed_reason text,
    description text,
    started_at date,
    target_end_date date,
    ended_at date,
    last_activity_at timestamptz,
    visibility text DEFAULT 'workspace'::text NOT NULL,
    source text NOT NULL,
    captured_by text NOT NULL,
    raw jsonb,
    search_tsv tsvector GENERATED ALWAYS AS ((setweight(to_tsvector('simple'::regconfig, f_unaccent(COALESCE(name, ''::text))), 'A'::"char") || setweight(to_tsvector('simple'::regconfig, f_unaccent(COALESCE(key, ''::text))), 'A'::"char"))) STORED,
    version bigint DEFAULT 1 NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    archived_at timestamptz,
    CONSTRAINT project_closed_reason CHECK (((phase <> 'closed'::text) OR (closed_reason IS NOT NULL))),
    CONSTRAINT project_dates CHECK (((ended_at IS NULL) OR (started_at IS NULL) OR (ended_at >= started_at))),
    CONSTRAINT project_key_shape CHECK (((key IS NULL) OR (key ~ '^[A-Za-z][A-Za-z0-9_-]{1,23}$'::text))),
    CONSTRAINT project_phase_check CHECK (phase IN ('initiative', 'pursuing', 'delivering', 'closed')),
    CONSTRAINT project_visibility_check CHECK ((visibility = 'workspace'::text))
);

CREATE TABLE project_phase_history (
    id uuid DEFAULT uuidv7() NOT NULL,
    project_id uuid NOT NULL,
    from_phase text,
    to_phase text NOT NULL,
    reason text,
    changed_by text NOT NULL,
    occurred_at timestamptz DEFAULT now() NOT NULL
);

CREATE TABLE stage (
    id uuid DEFAULT uuidv7() NOT NULL,
    pipeline_id uuid NOT NULL,
    name text NOT NULL,
    "position" integer NOT NULL,
    semantic text DEFAULT 'open'::text NOT NULL,
    win_probability smallint DEFAULT 0 NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    archived_at timestamptz,
    CONSTRAINT stage_semantic_check CHECK (semantic IN ('open', 'won', 'lost')),
    CONSTRAINT stage_terminal_prob CHECK ((((semantic = 'won'::text) AND (win_probability = 100)) OR ((semantic = 'lost'::text) AND (win_probability = 0)) OR (semantic = 'open'::text))),
    CONSTRAINT stage_win_probability_check CHECK (((win_probability >= 0) AND (win_probability <= 100)))
);

CREATE TABLE finance_connection (
    id uuid DEFAULT uuidv7() NOT NULL,
    provider text NOT NULL,
    status text DEFAULT 'connecting'::text NOT NULL,
    capabilities jsonb DEFAULT '{}'::jsonb NOT NULL,
    credential_ref text NOT NULL,
    sync_cursor text,
    last_attempt_at timestamptz,
    last_success_at timestamptz,
    last_error_code text,
    source text NOT NULL,
    captured_by text NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    archived_at timestamptz,
    CONSTRAINT finance_connection_status_check CHECK (status IN ('connecting', 'active', 'error', 'disconnected'))
);

CREATE TABLE finance_customer_link (
    id uuid DEFAULT uuidv7() NOT NULL,
    connection_id uuid NOT NULL,
    organization_id uuid NOT NULL,
    external_customer_id text NOT NULL,
    source_updated_at timestamptz,
    sync_hash text NOT NULL,
    source text NOT NULL,
    captured_by text NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    archived_at timestamptz
);

CREATE TABLE finance_external_customer (
    id uuid DEFAULT uuidv7() NOT NULL,
    connection_id uuid NOT NULL,
    external_customer_id text NOT NULL,
    display_name text NOT NULL,
    source_updated_at timestamptz,
    sync_hash text NOT NULL,
    source text NOT NULL,
    captured_by text NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    archived_at timestamptz
);

CREATE TABLE finance_invoice (
    id uuid DEFAULT uuidv7() NOT NULL,
    connection_id uuid NOT NULL,
    organization_id uuid NOT NULL,
    external_id text NOT NULL,
    number text,
    issued_at date NOT NULL,
    due_at date,
    status text NOT NULL,
    raw_status text,
    currency char(3) NOT NULL,
    net_minor bigint NOT NULL,
    tax_minor bigint DEFAULT 0 NOT NULL,
    gross_minor bigint NOT NULL,
    open_minor bigint DEFAULT 0 NOT NULL,
    credited_minor bigint DEFAULT 0 NOT NULL,
    fx_rate_to_base numeric(20,10),
    fx_rate_date date,
    fully_paid_at timestamptz,
    disputed_at timestamptz,
    void_at timestamptz,
    credits_invoice_id uuid,
    source_updated_at timestamptz,
    sync_hash text NOT NULL,
    source text NOT NULL,
    captured_by text NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    archived_at timestamptz,
    CONSTRAINT finance_invoice_credits_not_self CHECK (((credits_invoice_id IS NULL) OR (credits_invoice_id <> id))),
    CONSTRAINT finance_invoice_currency_check CHECK ((currency ~ '^[A-Z]{3}$'::text)),
    CONSTRAINT finance_invoice_fx_pair CHECK (((fx_rate_to_base IS NULL) = (fx_rate_date IS NULL))),
    CONSTRAINT finance_invoice_paid_status CHECK (fully_paid_at IS NULL OR status IN ('paid', 'credited', 'void')),
    CONSTRAINT finance_invoice_status_check CHECK (status IN ('draft', 'open', 'partially_paid', 'paid', 'overdue', 'disputed', 'credited', 'void')),
    CONSTRAINT finance_invoice_void_agrees CHECK (((void_at IS NULL) = (status <> 'void'::text)))
);

CREATE TABLE finance_payment (
    id uuid DEFAULT uuidv7() NOT NULL,
    connection_id uuid NOT NULL,
    organization_id uuid NOT NULL,
    external_id text NOT NULL,
    invoice_id uuid,
    paid_at timestamptz NOT NULL,
    currency char(3) NOT NULL,
    amount_minor bigint NOT NULL,
    source_updated_at timestamptz,
    sync_hash text NOT NULL,
    source text NOT NULL,
    captured_by text NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    archived_at timestamptz,
    CONSTRAINT finance_payment_currency_check CHECK ((currency ~ '^[A-Z]{3}$'::text))
);

CREATE TABLE app_user (
    id uuid DEFAULT uuidv7() NOT NULL,
    email text NOT NULL,
    password_hash text,
    display_name text NOT NULL,
    timezone text DEFAULT 'UTC'::text NOT NULL,
    status text DEFAULT 'active'::text NOT NULL,
    is_agent boolean DEFAULT false NOT NULL,
    seat_type text DEFAULT 'full'::text NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    archived_at timestamptz,
    failed_login_count integer DEFAULT 0 NOT NULL,
    locked_until timestamptz,
    must_change_password boolean DEFAULT false NOT NULL,
    locale text,
    CONSTRAINT app_user_agent_is_full CHECK (((NOT is_agent) OR (seat_type = 'full'::text))),
    CONSTRAINT app_user_agent_never_forced CHECK ((NOT (is_agent AND must_change_password))),
    CONSTRAINT app_user_failed_login_count_check CHECK ((failed_login_count >= 0)),
    CONSTRAINT app_user_forced_rotation_needs_a_password CHECK ((NOT (must_change_password AND (password_hash IS NULL)))),
    CONSTRAINT app_user_locale_shipped CHECK (locale IN ('en', 'de', 'vi')),
    CONSTRAINT app_user_seat_type_check CHECK (seat_type IN ('read', 'full')),
    CONSTRAINT app_user_status_check CHECK (status IN ('invited', 'active', 'suspended', 'deactivated'))
);

CREATE TABLE auth_token (
    id uuid DEFAULT uuidv7() NOT NULL,
    user_id uuid NOT NULL,
    purpose text NOT NULL,
    token_hash text NOT NULL,
    expires_at timestamptz NOT NULL,
    used_at timestamptz,
    created_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT auth_token_purpose_check CHECK (purpose IN ('password_reset', 'email_verify', 'invite'))
);

CREATE TABLE field_mask (
    id uuid DEFAULT uuidv7() NOT NULL,
    role_key text NOT NULL,
    object text NOT NULL,
    field text NOT NULL,
    condition text NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT field_mask_condition_check CHECK (condition IN ('always', 'outside_write_authority'))
);

CREATE TABLE oauth_authorization_code (
    id uuid DEFAULT uuidv7() NOT NULL,
    code_hash text NOT NULL,
    client_id text NOT NULL,
    user_id uuid NOT NULL,
    scopes text[] NOT NULL,
    code_challenge text NOT NULL,
    redirect_uri text NOT NULL,
    resource text,
    expires_at timestamptz NOT NULL,
    consumed_at timestamptz,
    created_at timestamptz DEFAULT now() NOT NULL,
    lent_passport_id uuid
);

CREATE TABLE oauth_client (
    id uuid DEFAULT uuidv7() NOT NULL,
    client_id text NOT NULL,
    client_name text NOT NULL,
    redirect_uris text[] NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    disabled_at timestamptz,
    deleted_at timestamptz,
    created_via text DEFAULT 'dcr'::text NOT NULL,
    last_used_at timestamptz,
    metadata_expires_at timestamptz,
    CONSTRAINT oauth_client_created_via_check CHECK (created_via IN ('dcr', 'admin', 'cimd'))
);

CREATE TABLE oauth_grant (
    id uuid DEFAULT uuidv7() NOT NULL,
    client_id text NOT NULL,
    user_id uuid NOT NULL,
    scopes text[] NOT NULL,
    refresh_allowed boolean DEFAULT false NOT NULL,
    resource text,
    created_at timestamptz DEFAULT now() NOT NULL,
    revoked_at timestamptz,
    lent_passport_id uuid
);

CREATE TABLE oauth_refresh_token (
    id uuid DEFAULT uuidv7() NOT NULL,
    grant_id uuid NOT NULL,
    token_hash text NOT NULL,
    expires_at timestamptz NOT NULL,
    consumed_at timestamptz,
    replaced_by uuid,
    created_at timestamptz DEFAULT now() NOT NULL
);

CREATE TABLE onboarding_wizard_state (
    id uuid DEFAULT uuidv7() NOT NULL,
    user_id uuid NOT NULL,
    path text NOT NULL,
    step text NOT NULL,
    source_mode text,
    website_url text,
    site_read_id uuid,
    company_draft jsonb DEFAULT '{}'::jsonb NOT NULL,
    selected_fact_keys text[] DEFAULT '{}'::text[] NOT NULL,
    voice_skipped boolean DEFAULT false NOT NULL,
    connect_skipped boolean DEFAULT false NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    completed_at timestamptz,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT onboarding_wizard_state_company_draft_check CHECK ((jsonb_typeof(company_draft) = 'object'::text)),
    CONSTRAINT onboarding_wizard_state_path_check CHECK (path IN ('creator', 'member')),
    CONSTRAINT onboarding_wizard_state_source_mode_check CHECK (source_mode IN ('website', 'manual')),
    CONSTRAINT onboarding_wizard_state_step_check CHECK (step IN ('read', 'confirm', 'voice', 'results', 'connect', 'complete')),
    CONSTRAINT onboarding_wizard_state_version_check CHECK ((version >= 1))
);

CREATE TABLE passport (
    id uuid DEFAULT uuidv7() NOT NULL,
    on_behalf_of uuid NOT NULL,
    granted_by uuid NOT NULL,
    label text,
    scopes text[] NOT NULL,
    token_hash text NOT NULL,
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz,
    created_at timestamptz DEFAULT now() NOT NULL,
    oauth_grant_id uuid,
    last_used_at timestamptz
);

CREATE TABLE record_grant (
    id uuid DEFAULT uuidv7() NOT NULL,
    record_type text NOT NULL,
    record_id uuid NOT NULL,
    subject_type text NOT NULL,
    subject_id uuid NOT NULL,
    access text NOT NULL,
    granted_by uuid NOT NULL,
    reason text,
    expires_at timestamptz,
    created_at timestamptz DEFAULT now() NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    CONSTRAINT record_grant_access_check CHECK (access IN ('read', 'write')),
    CONSTRAINT record_grant_record_type_check CHECK (record_type IN ('person', 'organization', 'deal', 'lead', 'project')),
    CONSTRAINT record_grant_subject_type_check CHECK (subject_type IN ('user', 'team'))
);

CREATE TABLE role (
    id uuid DEFAULT uuidv7() NOT NULL,
    key text NOT NULL,
    name text NOT NULL,
    is_system boolean DEFAULT false NOT NULL,
    permissions jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    archived_at timestamptz,
    version bigint DEFAULT 1 NOT NULL
);

CREATE TABLE role_assignment (
    id uuid DEFAULT uuidv7() NOT NULL,
    role_id uuid NOT NULL,
    user_id uuid NOT NULL,
    team_id uuid,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL
);

CREATE TABLE session (
    id uuid DEFAULT uuidv7() NOT NULL,
    user_id uuid NOT NULL,
    token_hash text NOT NULL,
    idle_expires_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    last_seen_at timestamptz DEFAULT now() NOT NULL,
    user_agent text,
    ip inet,
    revoked_at timestamptz,
    created_at timestamptz DEFAULT now() NOT NULL
);

CREATE TABLE setup_token (
    id uuid DEFAULT uuidv7() NOT NULL,
    token_hash text NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    consumed_at timestamptz
);

CREATE TABLE team (
    id uuid DEFAULT uuidv7() NOT NULL,
    name text NOT NULL,
    parent_team_id uuid,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    archived_at timestamptz
);

CREATE TABLE team_membership (
    id uuid DEFAULT uuidv7() NOT NULL,
    team_id uuid NOT NULL,
    user_id uuid NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL
);

CREATE TABLE workspace (
    id uuid DEFAULT uuidv7() NOT NULL,
    slug text NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    archived_at timestamptz
);

CREATE TABLE provider_connection (
    id uuid DEFAULT uuidv7() NOT NULL,
    provider text NOT NULL,
    status text NOT NULL,
    mode text NOT NULL,
    preset text NOT NULL,
    automatic_individual_create boolean DEFAULT true NOT NULL,
    automatic_import boolean DEFAULT false NOT NULL,
    categories text[] NOT NULL,
    refresh_after_days integer,
    daily_run_limit integer,
    credential_ref text,
    execution_epoch bigint DEFAULT 1 NOT NULL,
    connected_by uuid,
    connected_at timestamptz,
    last_verified_at timestamptz,
    last_used_at timestamptz,
    last_safe_status_code text,
    version bigint DEFAULT 1 NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT provider_connection_categories_check CHECK ((cardinality(categories) > 0)),
    CONSTRAINT provider_connection_daily_run_limit_check CHECK ((daily_run_limit > 0)),
    CONSTRAINT provider_connection_disconnected_shape CHECK ((((status = 'disconnected'::text) AND (credential_ref IS NULL)) OR (status <> 'disconnected'::text))),
    CONSTRAINT provider_connection_mode_check CHECK (mode IN ('automatic_on_create', 'on_demand')),
    CONSTRAINT provider_connection_provider_check CHECK ((provider = 'surfe'::text)),
    CONSTRAINT provider_connection_refresh_after_days_check CHECK ((refresh_after_days > 0)),
    CONSTRAINT provider_connection_status_check CHECK (status IN ('disconnected', 'validating', 'connected', 'invalid_credentials', 'insufficient_credits', 'rate_limited', 'provider_error'))
);

CREATE TABLE provider_connection_budget (
    connection_id uuid NOT NULL,
    pool text NOT NULL,
    monthly_ceiling integer,
    pause_below_balance integer,
    last_known_balance integer,
    balance_read_at timestamptz,
    CONSTRAINT provider_connection_budget_balance_shape CHECK (((last_known_balance IS NULL) = (balance_read_at IS NULL))),
    CONSTRAINT provider_connection_budget_last_known_balance_check CHECK ((last_known_balance >= 0)),
    CONSTRAINT provider_connection_budget_monthly_ceiling_check CHECK ((monthly_ceiling >= 0)),
    CONSTRAINT provider_connection_budget_pause_below_balance_check CHECK ((pause_below_balance >= 0))
);

CREATE TABLE provider_run (
    id uuid DEFAULT uuidv7() NOT NULL,
    subject_kind text NOT NULL,
    person_id uuid,
    provider text NOT NULL,
    trigger text NOT NULL,
    state text NOT NULL,
    skip_reason text,
    claims_unwritten boolean DEFAULT false NOT NULL,
    input_fingerprint text NOT NULL,
    external_correlation_id uuid NOT NULL,
    provider_job_id text,
    connection_version bigint NOT NULL,
    connection_epoch bigint NOT NULL,
    configuration_snapshot jsonb NOT NULL,
    requested_categories text[] NOT NULL,
    requested_by uuid,
    attempt_count integer DEFAULT 0 NOT NULL,
    next_attempt_at timestamptz,
    inflight_at timestamptz,
    last_safe_status_code text,
    submitted_at timestamptz,
    completed_at timestamptz,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT provider_run_attempt_count_check CHECK ((attempt_count >= 0)),
    CONSTRAINT provider_run_claims_unwritten_shape CHECK (((NOT claims_unwritten) OR (state = 'completed'::text))),
    CONSTRAINT provider_run_provider_check CHECK ((provider = 'surfe'::text)),
    CONSTRAINT provider_run_skip_reason_check CHECK (skip_reason IS NULL OR skip_reason IN ('budget_exhausted', 'low_balance', 'suppressed', 'not_eligible', 'duplicate_subject_candidate', 'rate_limited', 'already_fresh')),
    CONSTRAINT provider_run_skip_reason_shape CHECK (((state = 'skipped'::text) = (skip_reason IS NOT NULL))),
    CONSTRAINT provider_run_state_check CHECK (state IN ('queued', 'submitting', 'in_progress', 'completed', 'no_match', 'skipped', 'submission_unknown', 'failed', 'cancelled')),
    CONSTRAINT provider_run_subject_kind_check CHECK (subject_kind IN ('person', 'scrubbed')),
    CONSTRAINT provider_run_subject_shape CHECK ((((subject_kind = 'person'::text) AND (person_id IS NOT NULL)) OR ((subject_kind = 'scrubbed'::text) AND (person_id IS NULL)))),
    CONSTRAINT provider_run_trigger_check CHECK (trigger IN ('automatic_create', 'automatic_import', 'scheduled_refresh', 'manual'))
);

CREATE TABLE provider_run_reservation (
    run_id uuid NOT NULL,
    pool text NOT NULL,
    reserved_credits integer NOT NULL,
    actual_credits integer,
    reserved_at timestamptz DEFAULT now() NOT NULL,
    reconciled_at timestamptz,
    CONSTRAINT provider_run_reservation_actual_credits_check CHECK ((actual_credits >= 0)),
    CONSTRAINT provider_run_reservation_reserved_credits_check CHECK ((reserved_credits >= 0))
);

CREATE TABLE conversation_claim (
    id uuid DEFAULT uuidv7() NOT NULL,
    person_id uuid NOT NULL,
    kind text NOT NULL,
    body text NOT NULL,
    source_activity_id uuid NOT NULL,
    source_quote text NOT NULL,
    status text DEFAULT 'open'::text NOT NULL,
    due_at timestamptz,
    corrected_by_user_id uuid,
    corrected_at timestamptz,
    evidence_fingerprint text NOT NULL,
    needs_review boolean DEFAULT false NOT NULL,
    task_activity_id uuid,
    source text NOT NULL,
    captured_by text NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    archived_at timestamptz,
    CONSTRAINT conversation_claim_kind_check CHECK (kind IN ('commitment_ours', 'commitment_theirs', 'open_question', 'decision', 'priority', 'objection', 'success_criterion', 'decision_process')),
    CONSTRAINT conversation_claim_status_check CHECK (status IN ('open', 'done', 'dismissed'))
);

CREATE TABLE dedupe_candidate (
    id uuid DEFAULT uuidv7() NOT NULL,
    entity_type text NOT NULL,
    left_person_id uuid,
    right_person_id uuid,
    left_org_id uuid,
    right_org_id uuid,
    confidence numeric(4,3) NOT NULL,
    evidence jsonb NOT NULL,
    disposition text DEFAULT 'open'::text NOT NULL,
    disposed_by uuid,
    disposed_at timestamptz,
    source text NOT NULL,
    captured_by text NOT NULL,
    raw jsonb,
    version bigint DEFAULT 1 NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    archived_at timestamptz,
    left_lead_id uuid,
    right_lead_id uuid,
    CONSTRAINT dedupe_candidate_confidence_check CHECK (((confidence >= (0)::numeric) AND (confidence <= (1)::numeric))),
    CONSTRAINT dedupe_candidate_disposed_shape CHECK (((disposition = 'open'::text) = (disposed_at IS NULL))),
    CONSTRAINT dedupe_candidate_disposition_check CHECK (disposition IN ('open', 'merged', 'not_a_duplicate')),
    CONSTRAINT dedupe_candidate_entity_type_check CHECK (entity_type IN ('person', 'organization', 'lead')),
    CONSTRAINT dedupe_candidate_ordered CHECK ((COALESCE(left_person_id, left_org_id, left_lead_id) < COALESCE(right_person_id, right_org_id, right_lead_id))),
    CONSTRAINT dedupe_candidate_shape CHECK ((((entity_type = 'person'::text) AND (left_person_id IS NOT NULL) AND (right_person_id IS NOT NULL) AND (left_org_id IS NULL) AND (right_org_id IS NULL) AND (left_lead_id IS NULL) AND (right_lead_id IS NULL)) OR ((entity_type = 'organization'::text) AND (left_org_id IS NOT NULL) AND (right_org_id IS NOT NULL) AND (left_person_id IS NULL) AND (right_person_id IS NULL) AND (left_lead_id IS NULL) AND (right_lead_id IS NULL)) OR ((entity_type = 'lead'::text) AND (left_lead_id IS NOT NULL) AND (right_lead_id IS NOT NULL) AND (left_person_id IS NULL) AND (right_person_id IS NULL) AND (left_org_id IS NULL) AND (right_org_id IS NULL))))
);

CREATE TABLE email_signature (
    id uuid DEFAULT uuidv7() NOT NULL,
    owner_id uuid NOT NULL,
    body text NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    archived_at timestamptz
);

CREATE TABLE geocode_cache (
    query text NOT NULL,
    lat double precision NOT NULL,
    lon double precision NOT NULL,
    provider text NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL
);

CREATE TABLE lead (
    id uuid DEFAULT uuidv7() NOT NULL,
    full_name text,
    email text,
    title text,
    company_name text,
    candidate_org_key text,
    status text DEFAULT 'new'::text NOT NULL,
    score integer DEFAULT 0 NOT NULL,
    owner_id uuid,
    source_system text,
    source_id text,
    promoted_person_id uuid,
    promoted_at timestamptz,
    source text NOT NULL,
    captured_by text NOT NULL,
    raw jsonb,
    version bigint DEFAULT 1 NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    archived_at timestamptz,
    legal_hold boolean DEFAULT false NOT NULL,
    score_override_reason text,
    score_computed integer,
    linkedin_url text,
    search_tsv tsvector GENERATED ALWAYS AS ((((((setweight(to_tsvector('simple'::regconfig, f_unaccent(COALESCE(full_name, ''::text))), 'A'::"char") || setweight(to_tsvector('simple'::regconfig, f_fold_apostrophes(COALESCE(full_name, ''::text))), 'A'::"char")) || setweight(to_tsvector('simple'::regconfig, f_unaccent(COALESCE(company_name, ''::text))), 'B'::"char")) || setweight(to_tsvector('simple'::regconfig, f_fold_apostrophes(COALESCE(company_name, ''::text))), 'B'::"char")) || setweight(to_tsvector('simple'::regconfig, f_unaccent(COALESCE(title, ''::text))), 'B'::"char")) || setweight(to_tsvector('simple'::regconfig, f_fold_apostrophes(COALESCE(title, ''::text))), 'B'::"char"))) STORED,
    project_id uuid,
    merged_into_id uuid,
    routed_at timestamptz,
    first_response_at timestamptz,
    sla_breached_at timestamptz,
    disqualify_reason_id uuid,
    disqualify_note text,
    status_set_by text,
    qualified_deal_id uuid,
    CONSTRAINT lead_email_norm CHECK (((email IS NULL) OR (email = lower(email)))),
    CONSTRAINT lead_score_override_reason_check CHECK (((score_override_reason IS NULL) OR (length(btrim(score_override_reason)) > 0))),
    CONSTRAINT lead_status_check CHECK (status IN ('new', 'contacted', 'engaged', 'promoted', 'disqualified')),
    CONSTRAINT lead_status_set_by_check CHECK (status_set_by IN ('human', 'system'))
);

CREATE TABLE lead_disqualify_reason (
    id uuid DEFAULT uuidv7() NOT NULL,
    label text NOT NULL,
    sort_order integer DEFAULT 0 NOT NULL,
    active boolean DEFAULT true NOT NULL,
    system boolean DEFAULT false NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT lead_disqualify_reason_label_present CHECK ((length(btrim(label)) > 0))
);

CREATE TABLE lead_manual_signal (
    id uuid DEFAULT uuidv7() NOT NULL,
    lead_id uuid NOT NULL,
    factor text NOT NULL,
    band text NOT NULL,
    points smallint NOT NULL,
    signal_kind text NOT NULL,
    confidence numeric(4,3),
    reason text NOT NULL,
    set_by uuid NOT NULL,
    set_at timestamptz DEFAULT now() NOT NULL,
    superseded_at timestamptz,
    superseded_by text,
    CONSTRAINT lead_manual_signal_band_check CHECK ((length(btrim(band)) > 0)),
    CONSTRAINT lead_manual_signal_confidence_check CHECK (((confidence IS NULL) OR ((confidence >= (0)::numeric) AND (confidence <= (1)::numeric)))),
    CONSTRAINT lead_manual_signal_factor_check CHECK (factor IN ('web_traffic', 'employees', 'budget_hint')),
    CONSTRAINT lead_manual_signal_reason_check CHECK ((length(btrim(reason)) > 0)),
    CONSTRAINT lead_manual_signal_signal_kind_check CHECK (signal_kind IN ('fact', 'assumption', 'judgement')),
    CONSTRAINT lead_manual_signal_superseded_by_check CHECK (((superseded_by IS NULL) OR (length(btrim(superseded_by)) > 0))),
    CONSTRAINT lead_manual_signal_superseded_shape CHECK (((superseded_at IS NULL) = (superseded_by IS NULL)))
);

CREATE TABLE lead_score_history (
    id uuid DEFAULT uuidv7() NOT NULL,
    lead_id uuid NOT NULL,
    score smallint NOT NULL,
    score_computed smallint NOT NULL,
    override_reason text,
    factors jsonb NOT NULL,
    raw_sum numeric(8,3) NOT NULL,
    rounded_sum smallint NOT NULL,
    computed_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT lead_score_history_override_reason_check CHECK (((override_reason IS NULL) OR (length(btrim(override_reason)) > 0))),
    CONSTRAINT lead_score_history_override_shape CHECK (((override_reason IS NOT NULL) OR (score = score_computed))),
    CONSTRAINT lead_score_history_score_check CHECK (((score >= 0) AND (score <= 100))),
    CONSTRAINT lead_score_history_score_computed_check CHECK (((score_computed >= 0) AND (score_computed <= 100)))
);

CREATE TABLE lead_source (
    id uuid DEFAULT uuidv7() NOT NULL,
    key text NOT NULL,
    label text NOT NULL,
    intent text DEFAULT 'neutral'::text NOT NULL,
    sort_order integer DEFAULT 0 NOT NULL,
    active boolean DEFAULT true NOT NULL,
    system boolean DEFAULT false NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT lead_source_intent_check CHECK (intent IN ('high', 'neutral', 'low')),
    CONSTRAINT lead_source_key_shape CHECK (((key = lower(key)) AND (length(btrim(key)) > 0))),
    CONSTRAINT lead_source_label_present CHECK ((length(btrim(label)) > 0))
);

CREATE TABLE linkedin_account (
    user_id uuid NOT NULL,
    profile_url text,
    connected_at timestamptz,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL
);

CREATE TABLE linkedin_connection (
    id uuid DEFAULT uuidv7() NOT NULL,
    owner_user_id uuid NOT NULL,
    full_name text NOT NULL,
    normalized_name text NOT NULL,
    "position" text,
    company_name text,
    normalized_company text,
    connected_on date,
    provider_member_ref text,
    email text,
    matched_person_id uuid,
    matched_org_id uuid,
    match_status text DEFAULT 'unmatched'::text NOT NULL,
    source text NOT NULL,
    synced_at timestamptz DEFAULT now() NOT NULL,
    tombstoned_at timestamptz,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    profile_url text,
    CONSTRAINT linkedin_connection_match_shape CHECK (((match_status <> 'confirmed'::text) OR (matched_person_id IS NOT NULL))),
    CONSTRAINT linkedin_connection_match_status_check CHECK (match_status IN ('unmatched', 'suggested', 'confirmed', 'rejected')),
    CONSTRAINT linkedin_connection_source_check CHECK (source IN ('portability_api', 'csv_export'))
);

CREATE TABLE organization (
    id uuid DEFAULT uuidv7() NOT NULL,
    display_name text NOT NULL,
    legal_name text,
    industry text,
    size_band text,
    owner_id uuid,
    classification text DEFAULT 'prospect'::text NOT NULL,
    relevance smallint,
    logo_object_key text,
    logo_origin text,
    parent_org_id uuid,
    merged_into_id uuid,
    source text NOT NULL,
    captured_by text NOT NULL,
    raw jsonb,
    version bigint DEFAULT 1 NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    archived_at timestamptz,
    legal_hold boolean DEFAULT false NOT NULL,
    address_line1 text,
    address_line2 text,
    address_city text,
    address_region text,
    address_postal_code text,
    address_country text,
    search_tsv tsvector GENERATED ALWAYS AS ((((setweight(to_tsvector('simple'::regconfig, f_unaccent(COALESCE(display_name, ''::text))), 'A'::"char") || setweight(to_tsvector('simple'::regconfig, f_fold_apostrophes(COALESCE(display_name, ''::text))), 'A'::"char")) || setweight(to_tsvector('simple'::regconfig, f_unaccent(((COALESCE(legal_name, ''::text) || ' '::text) || COALESCE(industry, ''::text)))), 'B'::"char")) || setweight(to_tsvector('simple'::regconfig, f_fold_apostrophes(((COALESCE(legal_name, ''::text) || ' '::text) || COALESCE(industry, ''::text)))), 'B'::"char"))) STORED,
    is_anchor boolean DEFAULT false NOT NULL,
    visibility text DEFAULT 'workspace'::text NOT NULL,
    quarantined_at timestamptz,
    name_source text DEFAULT 'human'::text NOT NULL,
    lifecycle text DEFAULT 'unknown'::text NOT NULL,
    linkedin_url text,
    description text,
    last_activity_at timestamptz,
    geocode_lat double precision,
    geocode_lon double precision,
    geocoded_at timestamptz,
    geocode_provider text,
    geocode_status text,
    geocode_input_hash text,
    CONSTRAINT organization_anchor_is_permanent CHECK (((NOT is_anchor) OR ((archived_at IS NULL) AND (merged_into_id IS NULL)))),
    CONSTRAINT organization_classification_check CHECK (classification IN ('prospect', 'customer', 'agency', 'reseller', 'tech_vendor', 'platform', 'partner', 'competitor', 'other')),
    CONSTRAINT organization_description_length CHECK (((description IS NULL) OR (length(description) <= 500))),
    CONSTRAINT organization_geocode_resolved_has_a_point CHECK (((geocode_status IS DISTINCT FROM 'ok'::text) OR ((geocode_lat IS NOT NULL) AND (geocode_lon IS NOT NULL) AND ((geocode_lat >= ('-90'::integer)::double precision) AND (geocode_lat <= (90)::double precision)) AND ((geocode_lon >= ('-180'::integer)::double precision) AND (geocode_lon <= (180)::double precision))))),
    CONSTRAINT organization_geocode_status_check CHECK (geocode_status IS NULL OR geocode_status IN ('ok', 'failed', 'no_match', 'stale')),
    CONSTRAINT organization_lifecycle_check CHECK (lifecycle IN ('unknown', 'target', 'prospect', 'opportunity', 'customer', 'former_customer', 'disqualified')),
    CONSTRAINT organization_linkedin_url_shape CHECK (((linkedin_url IS NULL) OR (linkedin_url ~ '^https://([a-z]{2,3}\.)?linkedin\.com/company/[^/?#]+$'::text))),
    CONSTRAINT organization_name_source_check CHECK (name_source IN ('human', 'dossier', 'signature', 'domain')),
    CONSTRAINT organization_not_own_parent CHECK (((parent_org_id IS NULL) OR (parent_org_id <> id))),
    CONSTRAINT organization_relevance_check CHECK (((relevance IS NULL) OR ((relevance >= 0) AND (relevance <= 100)))),
    CONSTRAINT organization_size_band_check CHECK (size_band IS NULL OR size_band IN ('1-10', '11-50', '51-200', '201-500', '501-1000', '1001-5000', '5000+')),
    CONSTRAINT organization_visibility_check CHECK (visibility IN ('workspace', 'owner'))
);

CREATE TABLE organization_domain (
    id uuid DEFAULT uuidv7() NOT NULL,
    organization_id uuid NOT NULL,
    domain text NOT NULL,
    is_primary boolean DEFAULT false NOT NULL,
    source text NOT NULL,
    captured_by text NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    archived_at timestamptz,
    CONSTRAINT org_domain_norm CHECK ((domain = lower(domain)))
);

CREATE TABLE organization_domain_disposition (
    id uuid DEFAULT uuidv7() NOT NULL,
    domain text NOT NULL,
    status text DEFAULT 'pending'::text NOT NULL,
    source text,
    evidence text,
    owner_id uuid,
    site_read_id uuid,
    organization_id uuid,
    attempts integer DEFAULT 0 NOT NULL,
    last_attempt_at timestamptz,
    next_attempt_at timestamptz DEFAULT now(),
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    pending_reason text,
    admission text,
    admission_reason text,
    admission_source text,
    admission_at timestamptz,
    CONSTRAINT organization_domain_disposition_admission_check CHECK (admission IS NULL OR admission IN ('suppressed', 'admitted')),
    CONSTRAINT organization_domain_disposition_admission_shape CHECK ((((admission IS NULL) AND (admission_reason IS NULL) AND (admission_source IS NULL) AND (admission_at IS NULL)) OR ((admission IS NOT NULL) AND (admission_reason IS NOT NULL) AND (admission_source IS NOT NULL) AND (admission_at IS NOT NULL)))),
    CONSTRAINT organization_domain_disposition_admission_source_check CHECK (admission_source IS NULL OR admission_source IN ('verdict', 'heuristic', 'human')),
    CONSTRAINT organization_domain_disposition_domain_check CHECK (((domain = lower(domain)) AND (domain <> ''::text))),
    CONSTRAINT organization_domain_disposition_pending_reason_check CHECK (((pending_reason IS NULL) OR (pending_reason = 'unevidenced'::text))),
    CONSTRAINT organization_domain_disposition_pending_reason_shape CHECK (((pending_reason IS NULL) OR ((status = 'pending'::text) AND (admission IS DISTINCT FROM 'suppressed'::text)))),
    CONSTRAINT organization_domain_disposition_settled_shape CHECK ((((status = 'pending'::text) AND (source IS NULL)) OR ((status = 'company'::text) AND (source IS NOT NULL) AND (organization_id IS NOT NULL)) OR ((status IN ('personal', 'provider', 'no_site')) AND (source IS NOT NULL)))),
    CONSTRAINT organization_domain_disposition_source_check CHECK (source IS NULL OR source IN ('site_read', 'heuristic', 'human')),
    CONSTRAINT organization_domain_disposition_status_check CHECK (status IN ('pending', 'company', 'personal', 'provider', 'no_site'))
);

CREATE TABLE organization_fact (
    id uuid DEFAULT uuidv7() NOT NULL,
    organization_id uuid NOT NULL,
    category text NOT NULL,
    field text NOT NULL,
    value text NOT NULL,
    value_key text DEFAULT ''::text NOT NULL,
    evidence_snippet text,
    source_url text,
    confidence real,
    source text DEFAULT 'site_read'::text NOT NULL,
    captured_by text NOT NULL,
    site_read_id uuid,
    captured_at timestamptz DEFAULT now() NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    retrieved_at timestamptz,
    verified_at timestamptz,
    verified_by uuid,
    CONSTRAINT org_fact_field_vocab CHECK ((((category = 'company'::text) AND (field IN ('founded_year', 'employee_range', 'phone', 'contact_email', 'location'))) OR ((category = 'offering'::text) AND (field IN ('service', 'product', 'capability'))) OR ((category = 'market'::text) AND (field IN ('served_industry', 'company_size', 'geography', 'language'))) OR ((category = 'signal'::text) AND (field IN ('certification', 'partner', 'named_customer', 'technology', 'quantified_outcome'))))),
    CONSTRAINT org_fact_site_evidence CHECK (((source <> 'site_read'::text) OR ((evidence_snippet IS NOT NULL) AND (evidence_snippet <> ''::text) AND (source_url IS NOT NULL) AND (source_url <> ''::text) AND (confidence IS NOT NULL)))),
    CONSTRAINT org_fact_value_key_cardinality CHECK ((((category = 'company'::text) AND (field <> 'location'::text) AND (value_key = ''::text)) OR ((category = 'company'::text) AND (field = 'location'::text) AND (value_key <> ''::text)) OR ((category IN ('offering', 'market', 'signal')) AND (value_key <> ''::text)))),
    CONSTRAINT org_fact_verified_pair CHECK (((verified_at IS NULL) = (verified_by IS NULL))),
    CONSTRAINT organization_fact_category_check CHECK (category IN ('company', 'offering', 'market', 'signal')),
    CONSTRAINT organization_fact_confidence_check CHECK (((confidence IS NULL) OR ((confidence >= (0)::double precision) AND (confidence <= (1)::double precision)))),
    CONSTRAINT organization_fact_source_check CHECK (source IN ('human', 'site_read', 'connector', 'migration'))
);

CREATE TABLE organization_geocode_state (
    organization_id uuid NOT NULL,
    attempts integer DEFAULT 0 NOT NULL,
    last_outcome text,
    next_attempt_at timestamptz,
    updated_at timestamptz DEFAULT now() NOT NULL
);

CREATE TABLE organization_profile_field (
    id uuid DEFAULT uuidv7() NOT NULL,
    organization_id uuid NOT NULL,
    field text NOT NULL,
    value text NOT NULL,
    evidence_snippet text,
    source_url text,
    confidence real,
    source text DEFAULT 'site_read'::text NOT NULL,
    captured_by text NOT NULL,
    captured_at timestamptz DEFAULT now() NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    retrieved_at timestamptz,
    verified_at timestamptz,
    verified_by uuid,
    CONSTRAINT org_profile_field_verified_pair CHECK (((verified_at IS NULL) = (verified_by IS NULL))),
    CONSTRAINT org_profile_site_evidence CHECK (((source <> 'site_read'::text) OR ((evidence_snippet IS NOT NULL) AND (evidence_snippet <> ''::text) AND (source_url IS NOT NULL) AND (source_url <> ''::text) AND (confidence IS NOT NULL)))),
    CONSTRAINT organization_profile_field_confidence_check CHECK (((confidence IS NULL) OR ((confidence >= (0)::double precision) AND (confidence <= (1)::double precision)))),
    CONSTRAINT organization_profile_field_field_check CHECK (field IN ('display_name', 'offer_summary', 'icp', 'value_proposition', 'usp', 'customer_pains', 'desired_outcomes', 'buying_center', 'buying_intents', 'common_objections', 'sales_motion', 'legal_name', 'registered_address', 'register_vat', 'industry', 'history')),
    CONSTRAINT organization_profile_field_source_check CHECK (source IN ('human', 'site_read', 'connector', 'migration'))
);

CREATE TABLE organization_relationship_type (
    id uuid DEFAULT uuidv7() NOT NULL,
    organization_id uuid NOT NULL,
    relationship_type text NOT NULL,
    source text NOT NULL,
    captured_by text NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    archived_at timestamptz,
    CONSTRAINT organization_relationship_type_relationship_type_check CHECK (relationship_type IN ('customer', 'partner', 'supplier', 'investor', 'portfolio_company', 'competitor', 'other'))
);

CREATE TABLE partner (
    id uuid DEFAULT uuidv7() NOT NULL,
    organization_id uuid NOT NULL,
    cert_status text DEFAULT 'applied'::text NOT NULL,
    partner_role text,
    margin_tier text,
    certified_staff smallint DEFAULT 0 NOT NULL,
    retention_rate smallint,
    joined_at date,
    renews_at date,
    relationship_stage text DEFAULT 'research'::text NOT NULL,
    partner_fit_score smallint,
    relationship_health numeric(3,2),
    last_contact_at timestamptz,
    next_step text,
    next_step_due_at date,
    served_segments text[],
    source text NOT NULL,
    captured_by text NOT NULL,
    raw jsonb,
    version bigint DEFAULT 1 NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    archived_at timestamptz,
    partner_fit_score_computed smallint,
    partner_fit_override_reason text,
    CONSTRAINT partner_cert_status_check CHECK (cert_status IN ('applied', 'certified', 'suspended')),
    CONSTRAINT partner_margin_tier_check CHECK (margin_tier IS NULL OR margin_tier IN ('tier1_15', 'tier2_20', 'tier3_25')),
    CONSTRAINT partner_partner_fit_override_reason_check CHECK (((partner_fit_override_reason IS NULL) OR (length(btrim(partner_fit_override_reason)) > 0))),
    CONSTRAINT partner_partner_fit_score_check CHECK (((partner_fit_score IS NULL) OR ((partner_fit_score >= 0) AND (partner_fit_score <= 100)))),
    CONSTRAINT partner_partner_fit_score_computed_check CHECK (((partner_fit_score_computed IS NULL) OR ((partner_fit_score_computed >= 0) AND (partner_fit_score_computed <= 100)))),
    CONSTRAINT partner_partner_role_check CHECK (partner_role IS NULL OR partner_role IN ('hosting', 'consulting', 'strategic')),
    CONSTRAINT partner_relationship_health_check CHECK (((relationship_health IS NULL) OR ((relationship_health >= (0)::numeric) AND (relationship_health <= (1)::numeric)))),
    CONSTRAINT partner_relationship_stage_check CHECK (relationship_stage IN ('research', 'identified', 'contacted', 'in_conversation', 'fit_confirmed', 'agreement_pending', 'active', 'active_referring', 'dormant', 'no_fit')),
    CONSTRAINT partner_retention_rate_check CHECK (((retention_rate IS NULL) OR ((retention_rate >= 0) AND (retention_rate <= 100))))
);

CREATE TABLE person (
    id uuid DEFAULT uuidv7() NOT NULL,
    first_name text,
    last_name text,
    full_name text NOT NULL,
    title text,
    owner_id uuid,
    merged_into_id uuid,
    source text NOT NULL,
    captured_by text NOT NULL,
    raw jsonb,
    version bigint DEFAULT 1 NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    archived_at timestamptz,
    converted_from_lead_id uuid,
    legal_hold boolean DEFAULT false NOT NULL,
    address_line1 text,
    address_line2 text,
    address_city text,
    address_region text,
    address_postal_code text,
    address_country text,
    search_tsv tsvector GENERATED ALWAYS AS ((((setweight(to_tsvector('simple'::regconfig, f_unaccent(COALESCE(full_name, ''::text))), 'A'::"char") || setweight(to_tsvector('simple'::regconfig, f_fold_apostrophes(COALESCE(full_name, ''::text))), 'A'::"char")) || setweight(to_tsvector('simple'::regconfig, f_unaccent(COALESCE(title, ''::text))), 'B'::"char")) || setweight(to_tsvector('simple'::regconfig, f_fold_apostrophes(COALESCE(title, ''::text))), 'B'::"char"))) STORED,
    visibility text DEFAULT 'workspace'::text NOT NULL,
    quarantined_at timestamptz,
    photo_object_key text,
    photo_origin text,
    last_activity_at timestamptz,
    CONSTRAINT person_visibility_check CHECK (visibility IN ('workspace', 'owner'))
);

CREATE TABLE person_channel_identity (
    id uuid DEFAULT uuidv7() NOT NULL,
    person_id uuid NOT NULL,
    provider text NOT NULL,
    channel_user_id text NOT NULL,
    username text,
    blocked_at timestamptz,
    membership_bot_id text,
    membership_update_id bigint,
    source text NOT NULL,
    captured_by text NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    archived_at timestamptz
);

CREATE TABLE person_email (
    id uuid DEFAULT uuidv7() NOT NULL,
    person_id uuid NOT NULL,
    email text NOT NULL,
    email_type text DEFAULT 'work'::text NOT NULL,
    is_primary boolean DEFAULT false NOT NULL,
    "position" integer DEFAULT 0 NOT NULL,
    source text NOT NULL,
    captured_by text NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    archived_at timestamptz,
    from_correspondence boolean DEFAULT true NOT NULL,
    CONSTRAINT person_email_email_type_check CHECK (email_type IN ('work', 'personal', 'other')),
    CONSTRAINT person_email_norm CHECK ((email = lower(email)))
);

CREATE TABLE person_phone (
    id uuid DEFAULT uuidv7() NOT NULL,
    person_id uuid NOT NULL,
    phone text NOT NULL,
    phone_type text DEFAULT 'work'::text NOT NULL,
    is_primary boolean DEFAULT false NOT NULL,
    "position" integer DEFAULT 0 NOT NULL,
    source text NOT NULL,
    captured_by text NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    archived_at timestamptz,
    CONSTRAINT person_phone_phone_type_check CHECK (phone_type IN ('work', 'mobile', 'home', 'other'))
);

CREATE TABLE person_profile_field (
    id uuid DEFAULT uuidv7() NOT NULL,
    person_id uuid NOT NULL,
    field text NOT NULL,
    value text NOT NULL,
    evidence_snippet text NOT NULL,
    source_ref text NOT NULL,
    confidence numeric(4,3),
    source text NOT NULL,
    captured_by text NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT person_profile_field_confidence_check CHECK (((confidence IS NULL) OR ((confidence >= (0)::numeric) AND (confidence <= (1)::numeric)))),
    CONSTRAINT person_profile_field_field_check CHECK (field IN ('title', 'phone', 'role', 'linkedin', 'org_name'))
);

CREATE TABLE person_provider_claim (
    id uuid DEFAULT uuidv7() NOT NULL,
    person_id uuid NOT NULL,
    run_id uuid NOT NULL,
    provider text NOT NULL,
    claim_key text NOT NULL,
    value_json jsonb NOT NULL,
    confidence numeric(5,4),
    validation_status text,
    source text DEFAULT 'surfe'::text NOT NULL,
    captured_by text DEFAULT 'connector:surfe'::text NOT NULL,
    retrieved_at timestamptz NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT person_provider_claim_claim_key_check CHECK (claim_key IN ('professional_emails', 'personal_emails', 'mobile_phones', 'linkedin_profile', 'current_employment', 'job_history', 'location', 'departments', 'seniorities')),
    CONSTRAINT person_provider_claim_confidence_check CHECK (((confidence >= (0)::numeric) AND (confidence <= (1)::numeric))),
    CONSTRAINT person_provider_claim_provider_check CHECK ((provider = 'surfe'::text))
);

CREATE TABLE person_signature_enrich_state (
    person_id uuid NOT NULL,
    activity_id uuid NOT NULL,
    last_activity_at timestamptz NOT NULL,
    attempted_at timestamptz DEFAULT now() NOT NULL
);

CREATE TABLE person_social (
    id uuid DEFAULT uuidv7() NOT NULL,
    person_id uuid NOT NULL,
    platform text NOT NULL,
    handle text NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL
);

CREATE TABLE relationship (
    id uuid DEFAULT uuidv7() NOT NULL,
    kind text NOT NULL,
    person_id uuid,
    organization_id uuid,
    counterparty_org_id uuid,
    deal_id uuid,
    role text,
    is_current_primary boolean DEFAULT false NOT NULL,
    started_at date,
    ended_at date,
    source text NOT NULL,
    captured_by text NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    archived_at timestamptz,
    project_id uuid,
    CONSTRAINT rel_dates CHECK (((ended_at IS NULL) OR (started_at IS NULL) OR (ended_at >= started_at))),
    CONSTRAINT rel_employment_shape CHECK (((kind <> 'employment'::text) OR ((person_id IS NOT NULL) AND (organization_id IS NOT NULL) AND (deal_id IS NULL) AND (project_id IS NULL)))),
    CONSTRAINT rel_partner_shape CHECK (((kind NOT IN ('partner_of', 'referred_by', 'co_sell_with')) OR ((organization_id IS NOT NULL) AND (counterparty_org_id IS NOT NULL) AND (organization_id <> counterparty_org_id) AND (person_id IS NULL) AND (deal_id IS NULL) AND (project_id IS NULL)))),
    CONSTRAINT rel_project_stakeholder_shape CHECK (((kind <> 'project_stakeholder'::text) OR ((project_id IS NOT NULL) AND (person_id IS NOT NULL) AND (organization_id IS NULL) AND (counterparty_org_id IS NULL) AND (deal_id IS NULL)))),
    CONSTRAINT rel_stakeholder_shape CHECK (((kind <> 'deal_stakeholder'::text) OR ((deal_id IS NOT NULL) AND (person_id IS NOT NULL) AND (organization_id IS NULL) AND (project_id IS NULL)))),
    CONSTRAINT relationship_kind_check CHECK (kind IN ('employment', 'deal_stakeholder', 'partner_of', 'referred_by', 'co_sell_with', 'project_stakeholder'))
);

CREATE TABLE site_read (
    id uuid DEFAULT uuidv7() NOT NULL,
    organization_id uuid,
    seed_url text NOT NULL,
    status text DEFAULT 'queued'::text NOT NULL,
    pages jsonb DEFAULT '[]'::jsonb NOT NULL,
    skipped jsonb DEFAULT '[]'::jsonb NOT NULL,
    stopped_reason text,
    fact_count integer DEFAULT 0 NOT NULL,
    proposal_ids uuid[] DEFAULT '{}'::uuid[] NOT NULL,
    requested_by text NOT NULL,
    started_at timestamptz,
    finished_at timestamptz,
    created_at timestamptz DEFAULT now() NOT NULL,
    phase text,
    pages_read integer DEFAULT 0 NOT NULL,
    target_kind text DEFAULT 'organization'::text NOT NULL,
    profile_fields jsonb DEFAULT '[]'::jsonb NOT NULL,
    facts jsonb DEFAULT '[]'::jsonb NOT NULL,
    people jsonb DEFAULT '[]'::jsonb NOT NULL,
    warnings jsonb DEFAULT '[]'::jsonb NOT NULL,
    draft_version integer DEFAULT 1 NOT NULL,
    proposal_hash text DEFAULT ''::text NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    confirmed_at timestamptz,
    status_code text,
    status_detail text,
    next_attempt_at timestamptz,
    first_grounded_at timestamptz,
    legal_entities jsonb DEFAULT '[]'::jsonb NOT NULL,
    logo_object_key text,
    logo_origin text,
    CONSTRAINT site_read_draft_version_check CHECK ((draft_version > 0)),
    CONSTRAINT site_read_outcome_shape CHECK ((((status = 'deferred'::text) AND (status_code = 'budget_deferred'::text) AND (status_detail IS NOT NULL) AND (next_attempt_at IS NOT NULL)) OR ((status = 'failed'::text) AND (status_code IS NOT NULL) AND (status_code IN ('bot_blocked', 'http_client_error', 'http_server_error', 'timeout', 'dns', 'tls', 'robots_disallowed', 'unreadable', 'internal', 'stale_reclaim')) AND (status_detail IS NOT NULL)) OR ((status IN ('queued', 'running', 'done', 'partial', 'cancelled')) AND (status_code IS NULL) AND (status_detail IS NULL) AND (next_attempt_at IS NULL)))),
    CONSTRAINT site_read_phase_check CHECK (phase IN ('crawling', 'extracting')),
    CONSTRAINT site_read_status_check CHECK (status IN ('queued', 'deferred', 'running', 'done', 'partial', 'failed', 'cancelled')),
    CONSTRAINT site_read_stopped_reason_check CHECK (stopped_reason IS NULL OR stopped_reason IN ('budget', 'page_cap', 'byte_cap', 'deadline')),
    CONSTRAINT site_read_target_kind_check CHECK (target_kind IN ('onboarding', 'organization', 'domain_triage')),
    CONSTRAINT site_read_target_shape CHECK ((((target_kind = 'onboarding'::text) AND ((organization_id IS NULL) OR ((organization_id IS NOT NULL) AND (confirmed_at IS NOT NULL)))) OR ((target_kind = 'organization'::text) AND (organization_id IS NOT NULL)) OR ((target_kind = 'domain_triage'::text) AND ((organization_id IS NULL) OR ((organization_id IS NOT NULL) AND (confirmed_at IS NOT NULL))))))
);

CREATE TABLE activity_kind (
    kind text NOT NULL
);

CREATE TABLE activity_participant_replay (
    activity_id uuid NOT NULL,
    replayed_at timestamptz DEFAULT now() NOT NULL,
    outcome text NOT NULL,
    CONSTRAINT activity_participant_replay_outcome_check CHECK (outcome IN ('participants', 'none', 'unreadable', 'no_owner'))
);

CREATE TABLE agent_task (
    id uuid DEFAULT uuidv7() NOT NULL,
    approval_id uuid NOT NULL,
    passport_id uuid NOT NULL,
    tool text NOT NULL,
    status text DEFAULT 'working'::text NOT NULL,
    status_message text,
    result jsonb,
    error jsonb,
    served_records integer,
    claimed_at timestamptz,
    expires_at timestamptz NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT agent_task_served_records CHECK (((served_records IS NULL) OR (result IS NOT NULL))),
    CONSTRAINT agent_task_status_check CHECK (status IN ('working', 'completed', 'failed', 'cancelled')),
    CONSTRAINT agent_task_terminal_payload CHECK ((((status = 'completed'::text) AND (result IS NOT NULL) AND (error IS NULL)) OR ((status = 'failed'::text) AND (error IS NOT NULL) AND (result IS NULL)) OR ((status IN ('working', 'cancelled')) AND (result IS NULL) AND (error IS NULL))))
);

CREATE TABLE audit_log (
    id uuid DEFAULT uuidv7() NOT NULL,
    workspace_id uuid NOT NULL,
    actor_type text NOT NULL,
    actor_id text NOT NULL,
    passport_id uuid,
    on_behalf_of uuid,
    action text NOT NULL,
    entity_type text NOT NULL,
    entity_id uuid NOT NULL,
    before jsonb,
    after jsonb,
    authorization_rule text,
    evidence jsonb,
    occurred_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT audit_log_action_check CHECK (action IN ('create', 'update', 'archive', 'merge', 'promote', 'restore', 'export', 'erase', 'assign', 'advance_stage', 'advance_phase', 'approve', 'reject', 'consent_grant', 'consent_withdraw', 'activity_relink', 'record_share', 'record_unshare', 'resolve', 'demote', 'import', 'import_undo', 'disqualify', 'anonymize', 'send_email', 'reset_data', 'password_link_issued', 'connect', 'disconnect', 'schedule', 'reschedule', 'cancel', 'release', 'hold', 'expire', 'restrict', 'pin', 'accrue', 'pay')),
    CONSTRAINT audit_log_actor_type_check CHECK (actor_type IN ('human', 'agent', 'connector', 'system'))
);

CREATE TABLE brief_item (
    id uuid DEFAULT uuidv7() NOT NULL,
    brief_run_id uuid NOT NULL,
    deal_id uuid NOT NULL,
    rank integer NOT NULL,
    composite double precision NOT NULL,
    feature_vector jsonb NOT NULL,
    evidence_ids uuid[] NOT NULL,
    state text DEFAULT 'new'::text NOT NULL,
    state_at timestamptz,
    snoozed_until timestamptz,
    CONSTRAINT brief_item_composite_check CHECK (((composite >= (0)::double precision) AND (composite <= (1)::double precision))),
    CONSTRAINT brief_item_rank_check CHECK ((rank >= 1)),
    CONSTRAINT brief_item_snooze_shape CHECK (((snoozed_until IS NOT NULL) = (state = 'snoozed'::text))),
    CONSTRAINT brief_item_state_check CHECK (state IN ('new', 'acted', 'dismissed', 'snoozed')),
    CONSTRAINT brief_item_state_stamped CHECK (((state = 'new'::text) = (state_at IS NULL)))
);

CREATE TABLE brief_run (
    id uuid DEFAULT uuidv7() NOT NULL,
    user_id uuid NOT NULL,
    generated_at timestamptz DEFAULT now() NOT NULL,
    as_of timestamptz NOT NULL,
    candidate_count integer NOT NULL,
    revenue_norm_minor bigint NOT NULL,
    CONSTRAINT brief_run_candidate_count_check CHECK ((candidate_count >= 0)),
    CONSTRAINT brief_run_revenue_norm_minor_check CHECK ((revenue_norm_minor > 0))
);

CREATE TABLE channel_provider (
    provider text NOT NULL,
    transport text NOT NULL,
    label text DEFAULT ''::text NOT NULL,
    credential_model text DEFAULT 'workspace_bot'::text NOT NULL,
    supplies_transport boolean DEFAULT true NOT NULL,
    CONSTRAINT channel_provider_credential_model_check CHECK (credential_model IN ('workspace_bot', 'per_member')),
    CONSTRAINT channel_provider_provider_grammar CHECK (((provider ~ '^[a-z][a-z0-9_]*$'::text) AND (char_length(provider) <= 32))),
    CONSTRAINT channel_provider_transport_check CHECK (transport IN ('core', 'unit'))
);

CREATE TABLE event_outbox (
    id uuid DEFAULT uuidv7() NOT NULL,
    stream text NOT NULL,
    envelope jsonb NOT NULL,
    published_at timestamptz,
    created_at timestamptz DEFAULT now() NOT NULL,
    seq bigint NOT NULL
);

ALTER TABLE event_outbox ALTER COLUMN seq ADD GENERATED ALWAYS AS IDENTITY (
    SEQUENCE NAME event_outbox_seq_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1
);

CREATE TABLE extension_secret (
    id uuid DEFAULT uuidv7() NOT NULL,
    extension_name text NOT NULL,
    user_id uuid,
    key text NOT NULL,
    vault_ref text NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL
);

CREATE TABLE field_provenance (
    id uuid DEFAULT uuidv7() NOT NULL,
    object_type text NOT NULL,
    object_id uuid NOT NULL,
    field_name text NOT NULL,
    source text NOT NULL,
    captured_by text NOT NULL,
    captured_at timestamptz DEFAULT now() NOT NULL,
    confidence real,
    evidence_ref text,
    CONSTRAINT field_provenance_confidence_check CHECK (((confidence IS NULL) OR ((confidence >= (0)::double precision) AND (confidence <= (1)::double precision)))),
    CONSTRAINT field_provenance_object_type_check CHECK (object_type IN ('person', 'organization', 'deal', 'lead', 'activity', 'project', 'relationship'))
);

CREATE TABLE idempotency_key (
    principal_id text NOT NULL,
    key text NOT NULL,
    endpoint text NOT NULL,
    request_digest text NOT NULL,
    response_status integer,
    response_body text,
    created_at timestamptz DEFAULT now() NOT NULL,
    response_content_type text DEFAULT 'application/json'::text NOT NULL,
    response_records integer DEFAULT 0 NOT NULL
);

CREATE TABLE org_brief (
    id uuid DEFAULT uuidv7() NOT NULL,
    user_id uuid NOT NULL,
    organization_id uuid NOT NULL,
    fingerprint text NOT NULL,
    generated_at timestamptz NOT NULL,
    generated_by text NOT NULL,
    payload jsonb NOT NULL,
    CONSTRAINT org_brief_generated_by_check CHECK (generated_by IN ('model', 'deterministic'))
);

CREATE TABLE org_dossier (
    user_id uuid NOT NULL,
    organization_id uuid NOT NULL,
    fingerprint text NOT NULL,
    payload jsonb NOT NULL,
    generated_by text NOT NULL,
    generated_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT org_dossier_generated_by_check CHECK (generated_by IN ('model', 'deterministic'))
);

CREATE TABLE org_growth_fit (
    user_id uuid NOT NULL,
    organization_id uuid NOT NULL,
    fingerprint text NOT NULL,
    payload jsonb NOT NULL,
    generated_by text NOT NULL,
    generated_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT org_growth_fit_generated_by_check CHECK (generated_by IN ('model', 'deterministic'))
);

CREATE TABLE person_brief (
    user_id uuid NOT NULL,
    person_id uuid NOT NULL,
    fingerprint text NOT NULL,
    payload jsonb NOT NULL,
    generated_by text NOT NULL,
    generated_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT person_brief_generated_by_check CHECK (generated_by IN ('model', 'deterministic'))
);

CREATE TABLE person_moment_dismissal (
    user_id uuid NOT NULL,
    person_id uuid NOT NULL,
    claim_key text NOT NULL,
    evidence_fingerprint text NOT NULL,
    dismissed_at timestamptz DEFAULT now() NOT NULL
);

CREATE TABLE setting (
    key text NOT NULL,
    value jsonb NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT setting_key_check CHECK ((key ~ '^[a-z][a-z0-9_]*\.[a-z][a-z0-9_]*$'::text))
);

CREATE TABLE signal_thread_scan (
    thread_key text NOT NULL,
    last_activity_at timestamptz NOT NULL,
    scanned_at timestamptz DEFAULT now() NOT NULL,
    message_count integer DEFAULT 0 NOT NULL,
    refusals integer DEFAULT 0 NOT NULL,
    refused_activity_at timestamptz,
    refused_message_count integer,
    resolved_org_id uuid
);

CREATE TABLE suggestion_dismissal (
    id uuid DEFAULT uuidv7() NOT NULL,
    user_id uuid NOT NULL,
    organization_id uuid NOT NULL,
    fingerprint text NOT NULL,
    CONSTRAINT suggestion_dismissal_fingerprint_shape CHECK ((fingerprint ~ '^[0-9a-f]{64}$'::text))
);

CREATE TABLE system_log (
    id uuid DEFAULT uuidv7() NOT NULL,
    workspace_id uuid NOT NULL,
    actor_type text NOT NULL,
    actor_id text NOT NULL,
    passport_id uuid,
    on_behalf_of uuid,
    action text NOT NULL,
    detail jsonb,
    occurred_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT system_log_actor_type_check CHECK (actor_type IN ('human', 'agent', 'connector', 'system'))
);

CREATE TABLE user_record_view (
    id uuid DEFAULT uuidv7() NOT NULL,
    user_id uuid NOT NULL,
    entity_type text NOT NULL,
    entity_id uuid NOT NULL,
    last_viewed_at timestamptz NOT NULL,
    CONSTRAINT user_record_view_entity_type_check CHECK (entity_type IN ('organization', 'person'))
);

CREATE TABLE vault_secret (
    ref text NOT NULL,
    ciphertext bytea NOT NULL,
    key_version integer NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL
);

CREATE TABLE erasure_suppression (
    kind text NOT NULL,
    value_hash text NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT erasure_suppression_kind_check CHECK (kind IN ('email', 'channel_identity'))
);

CREATE TABLE retention_policy (
    id uuid DEFAULT uuidv7() NOT NULL,
    object_type text NOT NULL,
    category text,
    retain_days integer NOT NULL,
    action text NOT NULL,
    lawful_basis text,
    enabled boolean DEFAULT true NOT NULL,
    CONSTRAINT retention_policy_action_check CHECK (action IN ('archive', 'anonymize', 'erase'))
);

CREATE TABLE quota (
    id uuid DEFAULT uuidv7() NOT NULL,
    owner_id uuid,
    team_id uuid,
    period_start date NOT NULL,
    period_end date NOT NULL,
    target_minor bigint NOT NULL,
    currency char(3) NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    archived_at timestamptz,
    CONSTRAINT quota_currency_check CHECK ((currency ~ '^[A-Z]{3}$'::text)),
    CONSTRAINT quota_owner_xor_team CHECK (((owner_id IS NOT NULL) <> (team_id IS NOT NULL))),
    CONSTRAINT quota_period_valid CHECK ((period_end >= period_start)),
    CONSTRAINT quota_target_nonneg CHECK ((target_minor >= 0))
);

CREATE TABLE embed_store_binding (
    singleton boolean DEFAULT true NOT NULL,
    populated_identity text NOT NULL,
    status text DEFAULT 'idle'::text NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    reembedding_run uuid,
    reembedding_identity text,
    CONSTRAINT embed_store_binding_run_shape CHECK ((((status = 'reembedding'::text) = (reembedding_run IS NOT NULL)) AND ((reembedding_run IS NULL) = (reembedding_identity IS NULL)))),
    CONSTRAINT embed_store_binding_singleton_check CHECK (singleton),
    CONSTRAINT embed_store_binding_status_check CHECK (status IN ('idle', 'reembedding'))
);

CREATE TABLE embedding (
    entity_type text NOT NULL,
    entity_id uuid NOT NULL,
    chunk_ix integer DEFAULT 0 NOT NULL,
    chunk_hash text NOT NULL,
    model text NOT NULL,
    embedding vector NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT embedding_entity_type_check CHECK (entity_type IN ('person', 'organization', 'deal', 'lead', 'activity', 'project', 'relationship'))
);

CREATE TABLE graph_interaction_edge (
    user_id uuid NOT NULL,
    person_id uuid NOT NULL,
    last_at timestamptz NOT NULL,
    last_inbound_at timestamptz,
    last_outbound_at timestamptz,
    count_90d integer DEFAULT 0 NOT NULL,
    in_count_90d integer DEFAULT 0 NOT NULL,
    out_count_90d integer DEFAULT 0 NOT NULL,
    count_total integer DEFAULT 0 NOT NULL,
    computed_at timestamptz DEFAULT now() NOT NULL
);

CREATE TABLE signal (
    id uuid DEFAULT uuidv7() NOT NULL,
    kind text NOT NULL,
    source_channel text DEFAULT 'derived'::text NOT NULL,
    raw_ref text,
    entity_type text,
    entity_id uuid,
    resolution_state text DEFAULT 'resolved'::text NOT NULL,
    resolution_confidence numeric,
    resolved_org_id uuid,
    resolved_person_id uuid,
    severity text DEFAULT 'info'::text NOT NULL,
    summary text NOT NULL,
    evidence jsonb DEFAULT '[]'::jsonb NOT NULL,
    status text DEFAULT 'open'::text NOT NULL,
    detected_at timestamptz DEFAULT now() NOT NULL,
    source text NOT NULL,
    captured_by text NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    archived_at timestamptz,
    fingerprint text,
    visibility text DEFAULT 'workspace'::text NOT NULL,
    owner_id uuid,
    CONSTRAINT signal_entity_pair CHECK (((entity_type IS NULL) = (entity_id IS NULL))),
    CONSTRAINT signal_entity_type_check CHECK (entity_type IS NULL OR entity_type IN ('deal', 'organization', 'person')),
    CONSTRAINT signal_kind_check CHECK (kind IN ('stalled_deal', 'champion_left', 'reengagement', 'buying_intent', 'risk', 'other', 'contract_ended', 'new_opportunity', 'commitment_made', 'ghosted_thread')),
    CONSTRAINT signal_owner_private_names_its_owner CHECK (((visibility <> 'owner'::text) OR (owner_id IS NOT NULL))),
    CONSTRAINT signal_resolution_confidence_check CHECK (((resolution_confidence IS NULL) OR ((resolution_confidence >= (0)::numeric) AND (resolution_confidence <= (1)::numeric)))),
    CONSTRAINT signal_resolution_state_check CHECK (resolution_state IN ('resolved', 'low_confidence', 'unresolved', 'dropped')),
    CONSTRAINT signal_resolved_has_entity CHECK (((resolution_state <> 'resolved'::text) OR (entity_type IS NOT NULL))),
    CONSTRAINT signal_severity_check CHECK (severity IN ('info', 'warn', 'urgent')),
    CONSTRAINT signal_source_channel_check CHECK (source_channel IN ('derived', 'inbound', 'web', 'social', 'deal_room_engagement')),
    CONSTRAINT signal_status_check CHECK (status IN ('open', 'acknowledged', 'resolved', 'dismissed')),
    CONSTRAINT signal_visibility_check CHECK (visibility IN ('workspace', 'owner'))
);

CREATE TABLE signal_resolution (
    id uuid DEFAULT uuidv7() NOT NULL,
    signal_id uuid NOT NULL,
    matched_on text,
    matched_org_id uuid,
    match_confidence numeric,
    match_detail jsonb,
    outcome text,
    note text,
    resolved_by uuid,
    source text NOT NULL,
    captured_by text NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT signal_resolution_match_confidence_check CHECK (((match_confidence IS NULL) OR ((match_confidence >= (0)::numeric) AND (match_confidence <= (1)::numeric)))),
    CONSTRAINT signal_resolution_matched_on_check CHECK (matched_on IS NULL OR matched_on IN ('domain', 'name', 'prior_interaction', 'manual', 'none')),
    CONSTRAINT signal_resolution_outcome_check CHECK (outcome IS NULL OR outcome IN ('acknowledged', 'resolved', 'dismissed'))
);

CREATE TABLE webhook_delivery (
    id uuid DEFAULT uuidv7() NOT NULL,
    subscription_id uuid NOT NULL,
    event_id uuid NOT NULL,
    event_type text NOT NULL,
    payload text NOT NULL,
    status text NOT NULL,
    attempts integer DEFAULT 0 NOT NULL,
    last_status_code integer,
    last_error text,
    next_retry_at timestamptz,
    delivered_at timestamptz,
    dead_lettered_at timestamptz,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT webhook_delivery_status_check CHECK (status IN ('pending', 'delivered', 'retrying', 'dead_lettered'))
);

CREATE TABLE webhook_subscription (
    id uuid DEFAULT uuidv7() NOT NULL,
    owner_id uuid NOT NULL,
    target_url text NOT NULL,
    event_types text[] NOT NULL,
    signing_secret_ref text NOT NULL,
    state text DEFAULT 'active'::text NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    archived_at timestamptz,
    CONSTRAINT webhook_subscription_event_types_check CHECK ((cardinality(event_types) > 0)),
    CONSTRAINT webhook_subscription_state_check CHECK (state IN ('active', 'paused')),
    CONSTRAINT webhook_subscription_target_url_check CHECK ((target_url ~ '^https://'::text))
);

CREATE FUNCTION activity_refuse_restricted_mutation() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
  IF TG_OP = 'DELETE' THEN
    IF OLD.restricted_at IS NOT NULL THEN
      RAISE EXCEPTION 'activity % is restricted under a statutory retention obligation until %', OLD.id, OLD.restricted_until
        USING ERRCODE = 'check_violation',
              CONSTRAINT = 'activity_restricted_immutable';
    END IF;
    RETURN OLD;
  END IF;

  -- The stamp never changes once written — the class OR the timestamp saying
  -- when it was earned. Leaving the timestamp mutable would let a writer keep
  -- the class and move the date: the same rewriting of history with an extra
  -- step, on the field a supervisory authority would be shown.
  IF OLD.retention_class IS NOT NULL
     AND (NEW.retention_class IS DISTINCT FROM OLD.retention_class
          OR NEW.retention_class_at IS DISTINCT FROM OLD.retention_class_at) THEN
    RAISE EXCEPTION 'activity % carries retention class % earned at %, which is stamped once and never re-derived', OLD.id, OLD.retention_class, OLD.retention_class_at
      USING ERRCODE = 'check_violation',
            CONSTRAINT = 'activity_retention_class_monotonic';
  END IF;

  -- A restriction is substantiated at the moment it is written. The table
  -- CHECK sees only this row, so it can require a class and no more; the
  -- evidence is in another table and this is the only place that can look.
  IF OLD.restricted_at IS NULL AND NEW.restricted_at IS NOT NULL
     AND NOT EXISTS (SELECT 1 FROM activity_retention_evidence e
                      WHERE e.activity_id = NEW.id) THEN
    RAISE EXCEPTION 'activity % cannot be restricted with no retention evidence recording what qualified it', NEW.id
      USING ERRCODE = 'check_violation',
            CONSTRAINT = 'activity_restriction_needs_evidence';
  END IF;

  -- A deadline already recorded never moves nearer. A pin or a re-restriction
  -- of a row that still carries its class may only extend it.
  IF OLD.restricted_until IS NOT NULL AND NEW.restricted_at IS NOT NULL
     AND NEW.restricted_until < OLD.restricted_until THEN
    RAISE EXCEPTION 'activity % is held until % and a statutory deadline never shortens', OLD.id, OLD.restricted_until
      USING ERRCODE = 'check_violation',
            CONSTRAINT = 'activity_restriction_never_shortens';
  END IF;

  IF OLD.restricted_at IS NOT NULL THEN
    -- Still restricted after the write: refused outright.
    IF NEW.restricted_at IS NOT NULL THEN
      RAISE EXCEPTION 'activity % is restricted under a statutory retention obligation until %', OLD.id, OLD.restricted_until
        USING ERRCODE = 'check_violation',
              CONSTRAINT = 'activity_restricted_immutable';
    END IF;

    -- Lifting: the content goes with it, in this statement. Both legitimate
    -- callers erase as they lift, and a lift that leaves the body readable is
    -- not a completion of the erasure — it is a way to undo the restriction
    -- and keep the data.
    IF NEW.body IS NOT NULL OR NEW.raw IS NOT NULL
       OR NEW.counterparty_email IS NOT NULL
       -- The subject must not survive AS IT WAS. It may go null or be replaced
       -- by the erasure's tombstone name — erasuretimeline.go writes the
       -- placeholder rather than a null, so demanding null here would refuse
       -- the very statement this guard exists to admit. The test is that it
       -- CHANGED, which the placeholder satisfies and keeping the original
       -- does not. Spelling the placeholder's literal value here would be a
       -- second copy of a Go constant, drifting the first time somebody edits
       -- one of them.
       OR (OLD.subject IS NOT NULL AND NEW.subject IS NOT DISTINCT FROM OLD.subject) THEN
      RAISE EXCEPTION 'activity % may only leave restriction by being erased: clear body, raw and counterparty_email, and replace the subject, in the same statement', OLD.id
        USING ERRCODE = 'check_violation',
              CONSTRAINT = 'activity_restriction_lift_erases';
    END IF;
  END IF;

  RETURN NEW;
END;
$$;

CREATE FUNCTION activity_retention_evidence_is_frozen() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
  -- A row goes only with the activity it substantiates, through the CASCADE.
  -- A direct delete is refused. The two are distinguishable because the
  -- CASCADE has already removed the parent by the time this fires, so the
  -- activity is gone exactly when the delete is legitimate.
  IF TG_OP = 'DELETE' THEN
    IF EXISTS (SELECT 1 FROM activity a WHERE a.id = OLD.activity_id) THEN
      RAISE EXCEPTION 'retention evidence % is frozen and is removed only with the activity it substantiates', OLD.id
        USING ERRCODE = 'check_violation',
              CONSTRAINT = 'activity_retention_evidence_frozen';
    END IF;
    RETURN OLD;
  END IF;

  IF NEW.activity_id     IS DISTINCT FROM OLD.activity_id
     OR NEW.basis        IS DISTINCT FROM OLD.basis
     OR NEW.qualified_at IS DISTINCT FROM OLD.qualified_at
     OR NEW.deal_name    IS DISTINCT FROM OLD.deal_name
     OR NEW.decided_by_name IS DISTINCT FROM OLD.decided_by_name
     OR NEW.reason       IS DISTINCT FROM OLD.reason
     OR NEW.created_at   IS DISTINCT FROM OLD.created_at
     -- The reference may be CLEARED by its FK, never repointed.
     OR (NEW.deal_id IS NOT NULL AND NEW.deal_id IS DISTINCT FROM OLD.deal_id)
     OR (NEW.decided_by IS NOT NULL AND NEW.decided_by IS DISTINCT FROM OLD.decided_by) THEN
    RAISE EXCEPTION 'retention evidence % is frozen at the moment it qualified and may not be rewritten', OLD.id
      USING ERRCODE = 'check_violation',
            CONSTRAINT = 'activity_retention_evidence_frozen';
  END IF;

  RETURN NEW;
END;
$$;

CREATE FUNCTION assert_deal_project_same_org() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
  IF NEW.project_id IS NULL THEN
    RETURN NULL;
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM project p
    WHERE p.id = NEW.project_id
      AND p.organization_id IS NOT DISTINCT FROM NEW.organization_id
  ) THEN
    RAISE EXCEPTION 'deal and project belong to different companies'
      USING ERRCODE = 'check_violation', CONSTRAINT = 'deal_project_same_org';
  END IF;
  RETURN NULL;
END;
$$;

CREATE FUNCTION audit_log_immutable() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
  RAISE EXCEPTION 'audit_log is append-only (attempted % on row %)', TG_OP, OLD.id
    USING ERRCODE = 'check_violation';
END; $$;

CREATE FUNCTION deal_clear_partner_attribution_on_org_delete() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
  UPDATE deal
     SET partner_org_id = NULL, partner_attribution = NULL
   WHERE partner_org_id = OLD.id;
  RETURN OLD;
END;
$$;

CREATE FUNCTION last_activity_of_deal(did uuid) RETURNS timestamptz
    LANGUAGE sql STABLE
    AS $$
  SELECT max(a.occurred_at)
    FROM activity_link l
    JOIN activity a ON a.id = l.activity_id AND a.archived_at IS NULL
   WHERE l.deal_id = did
$$;

CREATE FUNCTION last_activity_of_organization(oid uuid) RETURNS timestamptz
    LANGUAGE sql STABLE
    AS $$
  SELECT max(v) FROM (
    -- Filed against the account itself.
    SELECT max(a.occurred_at) AS v
      FROM activity_link l
      JOIN activity a ON a.id = l.activity_id AND a.archived_at IS NULL
     WHERE l.organization_id = oid
    UNION ALL
    -- Filed against one of its deals.
    SELECT max(a.occurred_at)
      FROM deal d
      JOIN activity_link l ON l.deal_id = d.id
      JOIN activity a ON a.id = l.activity_id AND a.archived_at IS NULL
     WHERE d.organization_id = oid
    UNION ALL
    -- Filed against a contact it currently employs.
    SELECT max(a.occurred_at)
      FROM relationship r
      JOIN activity_link l ON l.person_id = r.person_id
      JOIN activity a ON a.id = l.activity_id AND a.archived_at IS NULL
     WHERE r.organization_id = oid AND r.kind = 'employment'
       AND r.ended_at IS NULL AND r.archived_at IS NULL
  ) arms
$$;

CREATE FUNCTION last_activity_of_person(pid uuid) RETURNS timestamptz
    LANGUAGE sql STABLE
    AS $$
  SELECT max(a.occurred_at)
    FROM activity_link l
    JOIN activity a ON a.id = l.activity_id AND a.archived_at IS NULL
   WHERE l.person_id = pid
$$;

CREATE FUNCTION last_activity_of_project(pid uuid) RETURNS timestamptz
    LANGUAGE sql STABLE
    AS $$
  SELECT max(a.occurred_at)
    FROM activity_link l
    JOIN activity a ON a.id = l.activity_id AND a.archived_at IS NULL
   WHERE l.project_id = pid
$$;

CREATE FUNCTION move_last_activity(tbl regclass, rid uuid) RETURNS void
    LANGUAGE plpgsql
    AS $$
DECLARE
  v timestamptz;
BEGIN
  IF rid IS NULL THEN RETURN; END IF;
  CASE tbl
    WHEN 'person'::regclass THEN
      PERFORM 1 FROM person WHERE id = rid FOR UPDATE;
      v := last_activity_of_person(rid);
      PERFORM set_config('margince.last_activity_move', 'on', true);
      UPDATE person SET last_activity_at = v WHERE id = rid;
    WHEN 'deal'::regclass THEN
      PERFORM 1 FROM deal WHERE id = rid FOR UPDATE;
      v := last_activity_of_deal(rid);
      PERFORM set_config('margince.last_activity_move', 'on', true);
      UPDATE deal SET last_activity_at = v WHERE id = rid;
    WHEN 'organization'::regclass THEN
      PERFORM 1 FROM organization WHERE id = rid FOR UPDATE;
      v := last_activity_of_organization(rid);
      PERFORM set_config('margince.last_activity_move', 'on', true);
      UPDATE organization SET last_activity_at = v WHERE id = rid;
  END CASE;
  PERFORM set_config('margince.last_activity_move', 'off', true);
END;
$$;

CREATE FUNCTION move_project_last_activity(pid uuid) RETURNS void
    LANGUAGE plpgsql
    AS $$
DECLARE
  v timestamptz;
BEGIN
  IF pid IS NULL THEN RETURN; END IF;
  PERFORM 1 FROM project WHERE id = pid FOR UPDATE;
  v := last_activity_of_project(pid);
  PERFORM set_config('margince.last_activity_move', 'on', true);
  UPDATE project SET last_activity_at = v WHERE id = pid;
  PERFORM set_config('margince.last_activity_move', 'off', true);
END;
$$;

CREATE FUNCTION organization_no_ancestor_cycle() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
  ancestor uuid := NEW.parent_org_id;
BEGIN
  WHILE ancestor IS NOT NULL LOOP
    IF ancestor = NEW.id THEN
      RAISE EXCEPTION 'organization % would become its own ancestor', NEW.id
        USING ERRCODE = 'check_violation';
    END IF;
    SELECT parent_org_id INTO ancestor FROM organization WHERE id = ancestor;
  END LOOP;
  RETURN NEW;
END;
$$;

CREATE FUNCTION organization_refuse_anchor_retirement() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
  IF NEW.merged_into_id IS NOT NULL
     AND EXISTS (SELECT 1 FROM organization o
                  WHERE o.id = NEW.merged_into_id AND o.is_anchor) THEN
    RAISE EXCEPTION 'organization % may not be merged into the anchor organization', NEW.id
      USING ERRCODE = 'check_violation',
            CONSTRAINT = 'organization_anchor_is_permanent';
  END IF;
  IF TG_OP = 'UPDATE' AND OLD.is_anchor AND NOT NEW.is_anchor
     AND OLD.archived_at IS NULL AND OLD.merged_into_id IS NULL THEN
    RAISE EXCEPTION 'organization % is the anchor organization and may not be demoted', NEW.id
      USING ERRCODE = 'check_violation',
            CONSTRAINT = 'organization_anchor_is_permanent';
  END IF;
  RETURN NEW;
END;
$$;

CREATE FUNCTION refresh_last_activity_for_link(pid uuid, did uuid, oid uuid) RETURNS void
    LANGUAGE plpgsql
    AS $$
DECLARE
  reached uuid;
BEGIN
  PERFORM move_last_activity('person', pid);
  PERFORM move_last_activity('deal', did);
  -- Ordered by id: two writers reaching the same accounts lock them in the
  -- same order, so they queue rather than deadlock.
  FOR reached IN
     SELECT x FROM (
       SELECT oid AS x WHERE oid IS NOT NULL
       UNION SELECT d.organization_id FROM deal d WHERE d.id = did AND d.organization_id IS NOT NULL
       UNION SELECT r.organization_id FROM relationship r
              WHERE r.person_id = pid AND r.kind = 'employment' AND r.ended_at IS NULL AND r.archived_at IS NULL
     ) reach ORDER BY x
  LOOP
    PERFORM move_last_activity('organization', reached);
  END LOOP;
END;
$$;

CREATE FUNCTION system_log_immutable() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
  RAISE EXCEPTION 'system_log is append-only (attempted % on row %)', TG_OP, OLD.id
    USING ERRCODE = 'check_violation';
END; $$;

CREATE FUNCTION trg_activity_last_activity() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
  PERFORM refresh_last_activity_for_link(l.person_id, l.deal_id, l.organization_id)
     FROM activity_link l WHERE l.activity_id = NEW.id;
  RETURN NULL;
END;
$$;

CREATE FUNCTION trg_activity_project_last_activity() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
  PERFORM move_project_last_activity(l.project_id)
     FROM activity_link l WHERE l.activity_id = NEW.id AND l.project_id IS NOT NULL;
  RETURN NULL;
END;
$$;

CREATE FUNCTION trg_deal_last_activity() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
  PERFORM move_last_activity('organization', OLD.organization_id);
  PERFORM move_last_activity('organization', NEW.organization_id);
  RETURN NULL;
END;
$$;

CREATE FUNCTION trg_relationship_last_activity() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
  IF TG_OP IN ('DELETE', 'UPDATE') AND OLD.kind = 'employment' THEN
    PERFORM move_last_activity('organization', OLD.organization_id);
  END IF;
  IF TG_OP IN ('INSERT', 'UPDATE') AND NEW.kind = 'employment' THEN
    PERFORM move_last_activity('organization', NEW.organization_id);
  END IF;
  RETURN NULL;
END;
$$;

COMMENT ON SCHEMA ext IS 'Extension tables (ADR-0069): ext_<name>_<table>, applied by the migrate role from each enabled unit''s own migrations. Tenant isolation is FORCE RLS plus a workspace-bound policy per table, NOT ownership — a per-unit ext_<name> owner role exists only in the pre-merge migration gate (issue #628). The core owns public; nothing here is core data.';

COMMENT ON COLUMN attachment.doc_state IS 'Asserted, never inferred (DOC-DDL-1): a human or the producing source sets it.';

COMMENT ON COLUMN attachment.organization_id IS 'Account roll-up read path, NOT a second parent — visibility stays the primary parent''s.';

COMMENT ON COLUMN attachment.contract_id IS 'The agreement this document is about (ADR-0109). A roll-up read path, not a second parent: visibility stays the primary parent''s.';

COMMENT ON COLUMN capture_pending_counterparty.kind IS 'What kind of sender this address turned out to be. Orthogonal to status, which is the row lifecycle.';

COMMENT ON TABLE commission_entry IS 'What one partner earned on one won deal. Append-forward: a clawback is a reversal row plus a void, never an edit.';

COMMENT ON COLUMN comms_outbound.attachments IS 'Immutable snapshot of what was attached (ADR-0086 §4): filename, type, size, checksum. Never rewritten by a later change to the document.';

COMMENT ON COLUMN consent_event.issuance_trigger IS 'What caused the confirmation mail to be sent (ADR-0098 D5), so the whole chain — ask → mail → click — is provable.';

COMMENT ON TABLE consent_existing_customer_flag IS 'UWG §7(3) existing-customer flag with its four cumulative conditions as columns (ADR-0098 D4).';

COMMENT ON COLUMN consent_purpose.class IS 'Which gate this purpose answers to (ADR-0098 D1). business_correspondence and transactional are never consent-gated; marketing needs DOI proof or the §7(3) flag.';

COMMENT ON TABLE consent_qualifying_event IS 'The recorded event that flipped business correspondence to allowed (ADR-0098 D2). Recording it is what makes the Art 6(1)(f) balancing accountable.';

COMMENT ON COLUMN contract.captured_by IS 'The principal that recorded this agreement, prefixed by kind ("human:<id>" / "agent:<id>") exactly as every other table spells it. Never a bare user id: an agent under a passport is not a row in app_user.';

COMMENT ON TABLE conversation_claim IS 'What was promised, asked and decided in captured conversations (ADR-0097 D1). A business entity, written through the audited write shape — not a cache.';

COMMENT ON COLUMN conversation_claim.evidence_fingerprint IS 'Pins the evidence a correction was made against. Unchanged fingerprint → the correction holds; changed → it re-arms.';

COMMENT ON COLUMN deal.won_without_contract_reason IS 'Why this deal was won with no contract record behind it (ADR-0109 §6). NULL on a deal that has one — the two are distinguishable, which is the point.';

COMMENT ON COLUMN deal.partner_attribution IS 'What the partner named by partner_org_id did for this deal: sourced (they brought it) or influenced (they helped one we already had). Commission accrues on sourced only.';

COMMENT ON COLUMN extension_secret.vault_ref IS 'An opaque keyvault handle (ADR-0069): safe to log, never the secret itself.';

COMMENT ON TABLE org_dossier IS 'Read-model cache (DOSS-DDL-1). Keyed per READER: no assembly crosses readers, whatever their masks.';

COMMENT ON COLUMN org_dossier.user_id IS 'The reader this assembly was generated for. Written into every read explicitly — RLS binds the workspace, not the reader.';

COMMENT ON TABLE org_growth_fit IS 'Read-model cache (DOSS-DDL-2). Per reader: it folds seat-dependent workspace context and makes recommendations.';

COMMENT ON COLUMN organization.classification IS 'RETIRED (ADR-0079/A124) — superseded by organization.lifecycle + organization_relationship_type. Written by nothing; dropped in a follow-up migration.';

COMMENT ON COLUMN organization.linkedin_url IS 'Canonical LinkedIn company URL (PO-DDL-N-2). Unique among live rows per workspace.';

COMMENT ON COLUMN organization_fact.retrieved_at IS 'When the source was last actually read (PO-DDL-N-2); distinct from captured_at.';

COMMENT ON COLUMN organization_fact.verified_at IS 'When a human last confirmed this claim (PO-DDL-N-2). Paired with verified_by.';

CREATE VIEW organization_open_pipeline_rollup WITH (security_invoker='true') AS
 SELECT organization_id,
    sum(amount_minor_base) AS open_pipeline_minor_base,
    count(*) AS open_deal_count
   FROM deal d
  WHERE ((status = 'open'::text) AND (organization_id IS NOT NULL) AND (archived_at IS NULL))
  GROUP BY organization_id;

COMMENT ON COLUMN organization_profile_field.retrieved_at IS 'When the source was last actually read (PO-DDL-N-2); distinct from captured_at.';

COMMENT ON COLUMN organization_profile_field.verified_at IS 'When a human last confirmed this claim (PO-DDL-N-2). Paired with verified_by.';

COMMENT ON COLUMN person.photo_origin IS 'How the photo got here (ADR-0096 D5). Human upload is the only writer — never a connector, never Gravatar.';

COMMENT ON TABLE person_brief IS 'Read-model cache (ADR-0097 D4), keyed per READER: no brief crosses readers, whatever their masks.';

COMMENT ON TABLE person_moment_dismissal IS 'Per-viewer view state (ADR-0096 D3). Held against an evidence fingerprint so a dismissal re-arms when the evidence moves.';

COMMENT ON TABLE setting IS 'Installation settings, one row per setting (ADR-0090/A135). Non-tenant by design. The catalog is platform/settings; an unregistered key here is a fitness-test failure.';

ALTER TABLE activity_audience_member
    ADD CONSTRAINT activity_audience_member_pkey PRIMARY KEY (activity_id, subject_type, subject_id);

ALTER TABLE activity_kind
    ADD CONSTRAINT activity_kind_pkey PRIMARY KEY (kind);

ALTER TABLE activity_link
    ADD CONSTRAINT activity_link_pkey PRIMARY KEY (id);

ALTER TABLE activity
    ADD CONSTRAINT activity_meeting_no_overlap EXCLUDE USING gist (host_user_id WITH =, tsrange(timezone('UTC'::text, occurred_at), (timezone('UTC'::text, occurred_at) + '01:00:00'::interval)) WITH &&) WHERE (((kind = 'meeting'::text) AND (host_user_id IS NOT NULL) AND (archived_at IS NULL)));

ALTER TABLE activity_participant
    ADD CONSTRAINT activity_participant_pkey PRIMARY KEY (id);

ALTER TABLE activity_participant_replay
    ADD CONSTRAINT activity_participant_replay_pkey PRIMARY KEY (activity_id);

ALTER TABLE activity
    ADD CONSTRAINT activity_pkey PRIMARY KEY (id);

ALTER TABLE activity_retention_evidence
    ADD CONSTRAINT activity_retention_evidence_pkey PRIMARY KEY (id);

ALTER TABLE agent_run
    ADD CONSTRAINT agent_run_pkey PRIMARY KEY (id);

ALTER TABLE agent_run
    ADD CONSTRAINT agent_run_trigger_unique UNIQUE (trigger_ref);

ALTER TABLE agent_task
    ADD CONSTRAINT agent_task_pkey PRIMARY KEY (id);

ALTER TABLE ai_call_config
    ADD CONSTRAINT ai_call_config_pkey PRIMARY KEY (hash);

ALTER TABLE ai_call_payload
    ADD CONSTRAINT ai_call_payload_pkey PRIMARY KEY (id);

ALTER TABLE ai_call
    ADD CONSTRAINT ai_call_pkey PRIMARY KEY (id);

ALTER TABLE ai_feedback
    ADD CONSTRAINT ai_feedback_pkey PRIMARY KEY (id);

ALTER TABLE ai_feedback
    ADD CONSTRAINT ai_feedback_subject_type_subject_id_claim_kind_key UNIQUE (subject_type, subject_id, claim_kind, claim_key);

ALTER TABLE ai_model_rate
    ADD CONSTRAINT ai_model_rate_key UNIQUE (provider, model_id, effective_date);

ALTER TABLE ai_model_rate
    ADD CONSTRAINT ai_model_rate_pkey PRIMARY KEY (id);

ALTER TABLE ai_usage
    ADD CONSTRAINT ai_usage_pkey PRIMARY KEY (day, task, tier);

ALTER TABLE app_user
    ADD CONSTRAINT app_user_pkey PRIMARY KEY (id);

ALTER TABLE approval
    ADD CONSTRAINT approval_pkey PRIMARY KEY (id);

ALTER TABLE attachment_extraction
    ADD CONSTRAINT attachment_extraction_pkey PRIMARY KEY (id);

ALTER TABLE attachment
    ADD CONSTRAINT attachment_pkey PRIMARY KEY (id);

ALTER TABLE audit_log
    ADD CONSTRAINT audit_log_pkey PRIMARY KEY (id);

ALTER TABLE auth_token
    ADD CONSTRAINT auth_token_pkey PRIMARY KEY (id);

ALTER TABLE automation
    ADD CONSTRAINT automation_pkey PRIMARY KEY (id);

ALTER TABLE booking_page
    ADD CONSTRAINT booking_page_pkey PRIMARY KEY (id);

ALTER TABLE booking_page
    ADD CONSTRAINT booking_page_slug_key UNIQUE (slug);

ALTER TABLE brief_item
    ADD CONSTRAINT brief_item_pkey PRIMARY KEY (id);

ALTER TABLE brief_run
    ADD CONSTRAINT brief_run_pkey PRIMARY KEY (id);

ALTER TABLE capture_auto_enrich_budget
    ADD CONSTRAINT capture_auto_enrich_budget_pkey PRIMARY KEY (budget_date);

ALTER TABLE capture_auto_enrich_state
    ADD CONSTRAINT capture_auto_enrich_state_pkey PRIMARY KEY (organization_id);

ALTER TABLE capture_backfill
    ADD CONSTRAINT capture_backfill_pkey PRIMARY KEY (id);

ALTER TABLE capture_connection
    ADD CONSTRAINT capture_connection_unique UNIQUE (user_id, provider);

ALTER TABLE capture_digest
    ADD CONSTRAINT capture_digest_pkey PRIMARY KEY (id);

ALTER TABLE capture_digest
    ADD CONSTRAINT capture_digest_user_id_digest_date_key UNIQUE (user_id, digest_date);

ALTER TABLE capture_exclusion
    ADD CONSTRAINT capture_exclusion_pkey PRIMARY KEY (id);

ALTER TABLE capture_freemail_domain
    ADD CONSTRAINT capture_freemail_domain_pkey PRIMARY KEY (id);

ALTER TABLE capture_pending_counterparty
    ADD CONSTRAINT capture_pending_counterparty_pkey PRIMARY KEY (id);

ALTER TABLE capture_sync_state
    ADD CONSTRAINT capture_sync_state_pkey PRIMARY KEY (connection_id);

ALTER TABLE capture_trace
    ADD CONSTRAINT capture_trace_pkey PRIMARY KEY (id);

ALTER TABLE channel_connection
    ADD CONSTRAINT channel_connection_pkey PRIMARY KEY (id);

ALTER TABLE channel_provider
    ADD CONSTRAINT channel_provider_pkey PRIMARY KEY (provider);

ALTER TABLE commission_entry
    ADD CONSTRAINT commission_entry_pkey PRIMARY KEY (id);

ALTER TABLE comms_outbound
    ADD CONSTRAINT comms_outbound_message_unique UNIQUE (message_id);

ALTER TABLE comms_outbound
    ADD CONSTRAINT comms_outbound_pkey PRIMARY KEY (id);

ALTER TABLE capture_connection
    ADD CONSTRAINT connector_connection_pkey PRIMARY KEY (id);

ALTER TABLE consent_doi_token
    ADD CONSTRAINT consent_doi_token_hash_unique UNIQUE (token_hash);

ALTER TABLE consent_doi_token
    ADD CONSTRAINT consent_doi_token_pkey PRIMARY KEY (id);

ALTER TABLE consent_event
    ADD CONSTRAINT consent_event_pkey PRIMARY KEY (id);

ALTER TABLE consent_existing_customer_flag
    ADD CONSTRAINT consent_existing_customer_flag_pkey PRIMARY KEY (person_id);

ALTER TABLE consent_purpose
    ADD CONSTRAINT consent_purpose_key_unique UNIQUE (key);

ALTER TABLE consent_purpose
    ADD CONSTRAINT consent_purpose_pkey PRIMARY KEY (id);

ALTER TABLE consent_qualifying_event
    ADD CONSTRAINT consent_qualifying_event_pkey PRIMARY KEY (id);

ALTER TABLE contract
    ADD CONSTRAINT contract_pkey PRIMARY KEY (id);

ALTER TABLE conversation_claim
    ADD CONSTRAINT conversation_claim_pkey PRIMARY KEY (id);

ALTER TABLE custom_field
    ADD CONSTRAINT custom_field_pkey PRIMARY KEY (id);

ALTER TABLE data_subject_request
    ADD CONSTRAINT data_subject_request_pkey PRIMARY KEY (id);

ALTER TABLE deal_forecast_history
    ADD CONSTRAINT deal_forecast_history_pkey PRIMARY KEY (id);

ALTER TABLE deal
    ADD CONSTRAINT deal_pkey PRIMARY KEY (id);

ALTER TABLE deal_stage_history
    ADD CONSTRAINT deal_stage_history_pkey PRIMARY KEY (id);

ALTER TABLE dedupe_candidate
    ADD CONSTRAINT dedupe_candidate_pkey PRIMARY KEY (id);

ALTER TABLE email_signature
    ADD CONSTRAINT email_signature_owner_id_key UNIQUE (owner_id);

ALTER TABLE email_signature
    ADD CONSTRAINT email_signature_pkey PRIMARY KEY (id);

ALTER TABLE embed_store_binding
    ADD CONSTRAINT embed_store_binding_pkey PRIMARY KEY (singleton);

ALTER TABLE embedding
    ADD CONSTRAINT embedding_pkey PRIMARY KEY (entity_type, entity_id, chunk_ix);

ALTER TABLE erasure_suppression
    ADD CONSTRAINT erasure_suppression_pkey PRIMARY KEY (kind, value_hash);

ALTER TABLE event_outbox
    ADD CONSTRAINT event_outbox_pkey PRIMARY KEY (id);

ALTER TABLE extension_secret
    ADD CONSTRAINT extension_secret_pkey PRIMARY KEY (id);

ALTER TABLE field_mask
    ADD CONSTRAINT field_mask_pkey PRIMARY KEY (id);

ALTER TABLE field_mask
    ADD CONSTRAINT field_mask_role_key_object_field_key UNIQUE (role_key, object, field);

ALTER TABLE field_provenance
    ADD CONSTRAINT field_provenance_pkey PRIMARY KEY (id);

ALTER TABLE finance_connection
    ADD CONSTRAINT finance_connection_pkey PRIMARY KEY (id);

ALTER TABLE finance_customer_link
    ADD CONSTRAINT finance_customer_link_pkey PRIMARY KEY (id);

ALTER TABLE finance_external_customer
    ADD CONSTRAINT finance_external_customer_connection_id_extern_key UNIQUE (connection_id, external_customer_id);

ALTER TABLE finance_external_customer
    ADD CONSTRAINT finance_external_customer_pkey PRIMARY KEY (id);

ALTER TABLE finance_invoice
    ADD CONSTRAINT finance_invoice_connection_id_external_id_key UNIQUE (connection_id, external_id);

ALTER TABLE finance_invoice
    ADD CONSTRAINT finance_invoice_pkey PRIMARY KEY (id);

ALTER TABLE finance_payment
    ADD CONSTRAINT finance_payment_connection_id_external_id_key UNIQUE (connection_id, external_id);

ALTER TABLE finance_payment
    ADD CONSTRAINT finance_payment_pkey PRIMARY KEY (id);

ALTER TABLE fx_rate
    ADD CONSTRAINT fx_rate_pair_day UNIQUE (from_currency, to_currency, rate_date);

ALTER TABLE fx_rate
    ADD CONSTRAINT fx_rate_pkey PRIMARY KEY (id);

ALTER TABLE geocode_cache
    ADD CONSTRAINT geocode_cache_pkey PRIMARY KEY (query);

ALTER TABLE graph_interaction_edge
    ADD CONSTRAINT graph_interaction_edge_pkey PRIMARY KEY (user_id, person_id);

ALTER TABLE idempotency_key
    ADD CONSTRAINT idempotency_key_pkey PRIMARY KEY (principal_id, key, endpoint);

ALTER TABLE lead_disqualify_reason
    ADD CONSTRAINT lead_disqualify_reason_pkey PRIMARY KEY (id);

ALTER TABLE lead_manual_signal
    ADD CONSTRAINT lead_manual_signal_pkey PRIMARY KEY (id);

ALTER TABLE lead
    ADD CONSTRAINT lead_pkey PRIMARY KEY (id);

ALTER TABLE lead_score_history
    ADD CONSTRAINT lead_score_history_pkey PRIMARY KEY (id);

ALTER TABLE lead_source
    ADD CONSTRAINT lead_source_pkey PRIMARY KEY (id);

ALTER TABLE linkedin_account
    ADD CONSTRAINT linkedin_account_pkey PRIMARY KEY (user_id);

ALTER TABLE linkedin_connection
    ADD CONSTRAINT linkedin_connection_pkey PRIMARY KEY (id);

ALTER TABLE list_member
    ADD CONSTRAINT list_member_pkey PRIMARY KEY (id);

ALTER TABLE list_member
    ADD CONSTRAINT list_member_unique UNIQUE (list_id, entity_type, entity_id);

ALTER TABLE list
    ADD CONSTRAINT list_pkey PRIMARY KEY (id);

ALTER TABLE oauth_authorization_code
    ADD CONSTRAINT oauth_authorization_code_pkey PRIMARY KEY (id);

ALTER TABLE oauth_client
    ADD CONSTRAINT oauth_client_client_id_key UNIQUE (client_id);

ALTER TABLE oauth_client
    ADD CONSTRAINT oauth_client_pkey PRIMARY KEY (id);

ALTER TABLE oauth_client
    ADD CONSTRAINT oauth_client_unique UNIQUE (client_id);

ALTER TABLE oauth_authorization_code
    ADD CONSTRAINT oauth_code_unique UNIQUE (code_hash);

ALTER TABLE oauth_grant
    ADD CONSTRAINT oauth_grant_pkey PRIMARY KEY (id);

ALTER TABLE oauth_grant
    ADD CONSTRAINT oauth_grant_ws_id_key UNIQUE (id);

ALTER TABLE oauth_refresh_token
    ADD CONSTRAINT oauth_refresh_token_pkey PRIMARY KEY (id);

ALTER TABLE oauth_refresh_token
    ADD CONSTRAINT oauth_refresh_unique UNIQUE (token_hash);

ALTER TABLE offer_line_item
    ADD CONSTRAINT offer_line_item_pkey PRIMARY KEY (id);

ALTER TABLE offer
    ADD CONSTRAINT offer_number_rev_unique UNIQUE (offer_number, revision);

ALTER TABLE offer
    ADD CONSTRAINT offer_pkey PRIMARY KEY (id);

ALTER TABLE offer_template
    ADD CONSTRAINT offer_template_name_unique UNIQUE (name);

ALTER TABLE offer_template
    ADD CONSTRAINT offer_template_pkey PRIMARY KEY (id);

ALTER TABLE onboarding_wizard_state
    ADD CONSTRAINT onboarding_wizard_state_pkey PRIMARY KEY (id);

ALTER TABLE onboarding_wizard_state
    ADD CONSTRAINT onboarding_wizard_state_user_id_key UNIQUE (user_id);

ALTER TABLE org_brief
    ADD CONSTRAINT org_brief_pkey PRIMARY KEY (id);

ALTER TABLE org_brief
    ADD CONSTRAINT org_brief_user_id_organization_id_key UNIQUE (user_id, organization_id);

ALTER TABLE org_dossier
    ADD CONSTRAINT org_dossier_pkey PRIMARY KEY (user_id, organization_id);

ALTER TABLE org_growth_fit
    ADD CONSTRAINT org_growth_fit_pkey PRIMARY KEY (user_id, organization_id);

ALTER TABLE organization_domain_disposition
    ADD CONSTRAINT organization_domain_disposition_pkey PRIMARY KEY (id);

ALTER TABLE organization_domain
    ADD CONSTRAINT organization_domain_pkey PRIMARY KEY (id);

ALTER TABLE organization_fact
    ADD CONSTRAINT organization_fact_pkey PRIMARY KEY (id);

ALTER TABLE organization_geocode_state
    ADD CONSTRAINT organization_geocode_state_pkey PRIMARY KEY (organization_id);

ALTER TABLE organization
    ADD CONSTRAINT organization_pkey PRIMARY KEY (id);

ALTER TABLE organization_profile_field
    ADD CONSTRAINT organization_profile_field_pkey PRIMARY KEY (id);

ALTER TABLE organization_relationship_type
    ADD CONSTRAINT organization_relationship_type_pkey PRIMARY KEY (id);

ALTER TABLE partner
    ADD CONSTRAINT partner_organization_id_key UNIQUE (organization_id);

ALTER TABLE partner
    ADD CONSTRAINT partner_pkey PRIMARY KEY (id);

ALTER TABLE passport
    ADD CONSTRAINT passport_pkey PRIMARY KEY (id);

ALTER TABLE passport
    ADD CONSTRAINT passport_token_hash_key UNIQUE (token_hash);

ALTER TABLE person_brief
    ADD CONSTRAINT person_brief_pkey PRIMARY KEY (user_id, person_id);

ALTER TABLE person_channel_identity
    ADD CONSTRAINT person_channel_identity_pkey PRIMARY KEY (id);

ALTER TABLE person_consent
    ADD CONSTRAINT person_consent_lead_unique UNIQUE (lead_id, purpose_id);

ALTER TABLE person_consent
    ADD CONSTRAINT person_consent_pkey PRIMARY KEY (id);

ALTER TABLE person_consent
    ADD CONSTRAINT person_consent_unique UNIQUE (person_id, purpose_id);

ALTER TABLE person_email
    ADD CONSTRAINT person_email_pkey PRIMARY KEY (id);

ALTER TABLE person_moment_dismissal
    ADD CONSTRAINT person_moment_dismissal_pkey PRIMARY KEY (user_id, person_id, claim_key);

ALTER TABLE person_phone
    ADD CONSTRAINT person_phone_pkey PRIMARY KEY (id);

ALTER TABLE person
    ADD CONSTRAINT person_pkey PRIMARY KEY (id);

ALTER TABLE person_profile_field
    ADD CONSTRAINT person_profile_field_pkey PRIMARY KEY (id);

ALTER TABLE person_provider_claim
    ADD CONSTRAINT person_provider_claim_pkey PRIMARY KEY (id);

ALTER TABLE person_provider_claim
    ADD CONSTRAINT person_provider_claim_run_id_claim_key_key UNIQUE (run_id, claim_key);

ALTER TABLE person_signature_enrich_state
    ADD CONSTRAINT person_signature_enrich_state_pkey PRIMARY KEY (person_id);

ALTER TABLE person_social
    ADD CONSTRAINT person_social_person_id_platform_key UNIQUE (person_id, platform);

ALTER TABLE person_social
    ADD CONSTRAINT person_social_pkey PRIMARY KEY (id);

ALTER TABLE pipeline
    ADD CONSTRAINT pipeline_name_unique UNIQUE (name);

ALTER TABLE pipeline
    ADD CONSTRAINT pipeline_pkey PRIMARY KEY (id);

ALTER TABLE preference_token
    ADD CONSTRAINT preference_token_pkey PRIMARY KEY (id);

ALTER TABLE preference_token
    ADD CONSTRAINT preference_token_token_key UNIQUE (token);

ALTER TABLE product
    ADD CONSTRAINT product_pkey PRIMARY KEY (id);

ALTER TABLE project_phase_history
    ADD CONSTRAINT project_phase_history_pkey PRIMARY KEY (id);

ALTER TABLE project
    ADD CONSTRAINT project_pkey PRIMARY KEY (id);

ALTER TABLE provider_connection_budget
    ADD CONSTRAINT provider_connection_budget_pkey PRIMARY KEY (connection_id, pool);

ALTER TABLE provider_connection
    ADD CONSTRAINT provider_connection_credential_ref_key UNIQUE (credential_ref);

ALTER TABLE provider_connection
    ADD CONSTRAINT provider_connection_pkey PRIMARY KEY (id);

ALTER TABLE provider_connection
    ADD CONSTRAINT provider_connection_provider_key UNIQUE (provider);

ALTER TABLE provider_run
    ADD CONSTRAINT provider_run_external_correlation_id_key UNIQUE (external_correlation_id);

ALTER TABLE provider_run
    ADD CONSTRAINT provider_run_pkey PRIMARY KEY (id);

ALTER TABLE provider_run_reservation
    ADD CONSTRAINT provider_run_reservation_pkey PRIMARY KEY (run_id, pool);

ALTER TABLE quota
    ADD CONSTRAINT quota_pkey PRIMARY KEY (id);

ALTER TABLE raw_capture
    ADD CONSTRAINT raw_capture_pkey PRIMARY KEY (id);

ALTER TABLE raw_capture
    ADD CONSTRAINT raw_capture_source_unique UNIQUE (source_system, source_id);

ALTER TABLE record_grant
    ADD CONSTRAINT record_grant_pkey PRIMARY KEY (id);

ALTER TABLE record_grant
    ADD CONSTRAINT record_grant_unique UNIQUE (record_type, record_id, subject_type, subject_id);

ALTER TABLE relationship
    ADD CONSTRAINT relationship_pkey PRIMARY KEY (id);

ALTER TABLE retention_policy
    ADD CONSTRAINT retention_policy_pkey PRIMARY KEY (id);

ALTER TABLE retention_policy
    ADD CONSTRAINT retention_policy_unique UNIQUE NULLS NOT DISTINCT (object_type, category);

ALTER TABLE role_assignment
    ADD CONSTRAINT role_assignment_pkey PRIMARY KEY (id);

ALTER TABLE role
    ADD CONSTRAINT role_key_unique UNIQUE (key);

ALTER TABLE role
    ADD CONSTRAINT role_pkey PRIMARY KEY (id);

ALTER TABLE runner_job
    ADD CONSTRAINT runner_job_pkey PRIMARY KEY (id);

ALTER TABLE runner_job
    ADD CONSTRAINT runner_job_trigger_unique UNIQUE (agent_spec, trigger_ref);

ALTER TABLE saved_view
    ADD CONSTRAINT saved_view_pkey PRIMARY KEY (id);

ALTER TABLE scheduled_send
    ADD CONSTRAINT scheduled_send_pkey PRIMARY KEY (id);

ALTER TABLE session
    ADD CONSTRAINT session_pkey PRIMARY KEY (id);

ALTER TABLE session
    ADD CONSTRAINT session_token_hash_key UNIQUE (token_hash);

ALTER TABLE setting
    ADD CONSTRAINT setting_pkey PRIMARY KEY (key);

ALTER TABLE setup_token
    ADD CONSTRAINT setup_token_pkey PRIMARY KEY (id);

ALTER TABLE setup_token
    ADD CONSTRAINT setup_token_token_hash_key UNIQUE (token_hash);

ALTER TABLE signal
    ADD CONSTRAINT signal_pkey PRIMARY KEY (id);

ALTER TABLE signal_resolution
    ADD CONSTRAINT signal_resolution_pkey PRIMARY KEY (id);

ALTER TABLE signal_thread_scan
    ADD CONSTRAINT signal_thread_scan_pkey PRIMARY KEY (thread_key);

ALTER TABLE signing_key
    ADD CONSTRAINT signing_key_pkey PRIMARY KEY (kid);

ALTER TABLE site_read
    ADD CONSTRAINT site_read_pkey PRIMARY KEY (id);

ALTER TABLE stage
    ADD CONSTRAINT stage_id_pipeline_unique UNIQUE (id, pipeline_id);

ALTER TABLE stage
    ADD CONSTRAINT stage_pkey PRIMARY KEY (id);

ALTER TABLE suggestion_dismissal
    ADD CONSTRAINT suggestion_dismissal_pkey PRIMARY KEY (id);

ALTER TABLE suggestion_dismissal
    ADD CONSTRAINT suggestion_dismissal_user_id_organization_id_f_key UNIQUE (user_id, organization_id, fingerprint);

ALTER TABLE system_log
    ADD CONSTRAINT system_log_pkey PRIMARY KEY (id);

ALTER TABLE tag
    ADD CONSTRAINT tag_pkey PRIMARY KEY (id);

ALTER TABLE taggable
    ADD CONSTRAINT taggable_pkey PRIMARY KEY (id);

ALTER TABLE taggable
    ADD CONSTRAINT taggable_unique UNIQUE (tag_id, entity_type, entity_id);

ALTER TABLE team_membership
    ADD CONSTRAINT team_membership_pkey PRIMARY KEY (id);

ALTER TABLE team_membership
    ADD CONSTRAINT team_membership_unique UNIQUE (team_id, user_id);

ALTER TABLE team
    ADD CONSTRAINT team_name_unique UNIQUE (name);

ALTER TABLE team
    ADD CONSTRAINT team_pkey PRIMARY KEY (id);

ALTER TABLE transcript_read
    ADD CONSTRAINT transcript_read_pkey PRIMARY KEY (id);

ALTER TABLE ai_call
    ADD CONSTRAINT uq_ai_call_ws_id UNIQUE (id);

ALTER TABLE brief_item
    ADD CONSTRAINT uq_brief_item_run_deal UNIQUE (brief_run_id, deal_id);

ALTER TABLE brief_item
    ADD CONSTRAINT uq_brief_item_run_rank UNIQUE (brief_run_id, rank);

ALTER TABLE capture_connection
    ADD CONSTRAINT uq_capture_connection_ws_id UNIQUE (id);

ALTER TABLE lead
    ADD CONSTRAINT uq_lead_ws_id UNIQUE (id);

ALTER TABLE offer_template
    ADD CONSTRAINT uq_offer_template_ws_id UNIQUE (id);

ALTER TABLE offer
    ADD CONSTRAINT uq_offer_ws_id UNIQUE (id);

ALTER TABLE offer_line_item
    ADD CONSTRAINT uq_oli_position UNIQUE (offer_id, "position");

ALTER TABLE organization_fact
    ADD CONSTRAINT uq_org_fact UNIQUE (organization_id, category, field, value_key);

ALTER TABLE organization_profile_field
    ADD CONSTRAINT uq_org_profile_field UNIQUE (organization_id, field);

ALTER TABLE passport
    ADD CONSTRAINT uq_passport_ws_id UNIQUE (id);

ALTER TABLE person_profile_field
    ADD CONSTRAINT uq_person_profile_field UNIQUE (person_id, field);

ALTER TABLE product
    ADD CONSTRAINT uq_product_ws_id UNIQUE (id);

ALTER TABLE project
    ADD CONSTRAINT uq_project_ws_id UNIQUE (id);

ALTER TABLE site_read
    ADD CONSTRAINT uq_site_read_ws_id UNIQUE (id);

ALTER TABLE voice_corpus_source
    ADD CONSTRAINT uq_voice_corpus_source_ref UNIQUE (voice_profile_id, source_ref);

ALTER TABLE voice_learning_signal
    ADD CONSTRAINT uq_voice_learning_signal_draft UNIQUE (draft_ref_hash);

ALTER TABLE voice_profile_delta
    ADD CONSTRAINT uq_voice_profile_delta_version UNIQUE (voice_profile_id, to_version);

ALTER TABLE voice_profile_version
    ADD CONSTRAINT uq_voice_profile_version_number UNIQUE (voice_profile_id, profile_version);

ALTER TABLE voice_profile_version
    ADD CONSTRAINT uq_voice_profile_version_profile_number UNIQUE (voice_profile_id, profile_version);

ALTER TABLE user_record_view
    ADD CONSTRAINT user_record_view_pkey PRIMARY KEY (id);

ALTER TABLE user_record_view
    ADD CONSTRAINT user_record_view_user_id_entity_type_entity_id_key UNIQUE (user_id, entity_type, entity_id);

ALTER TABLE vault_secret
    ADD CONSTRAINT vault_secret_pkey PRIMARY KEY (ref);

ALTER TABLE voice_build
    ADD CONSTRAINT voice_build_pkey PRIMARY KEY (id);

ALTER TABLE voice_corpus_source
    ADD CONSTRAINT voice_corpus_source_pkey PRIMARY KEY (id);

ALTER TABLE voice_learning_signal
    ADD CONSTRAINT voice_learning_signal_pkey PRIMARY KEY (id);

ALTER TABLE voice_profile_delta
    ADD CONSTRAINT voice_profile_delta_pkey PRIMARY KEY (id);

ALTER TABLE voice_profile
    ADD CONSTRAINT voice_profile_pkey PRIMARY KEY (id);

ALTER TABLE voice_profile_version
    ADD CONSTRAINT voice_profile_version_pkey PRIMARY KEY (id);

ALTER TABLE webhook_delivery
    ADD CONSTRAINT webhook_delivery_dedupe_key UNIQUE (subscription_id, event_id);

ALTER TABLE webhook_delivery
    ADD CONSTRAINT webhook_delivery_pkey PRIMARY KEY (id);

ALTER TABLE webhook_subscription
    ADD CONSTRAINT webhook_subscription_pkey PRIMARY KEY (id);

ALTER TABLE workflow_run
    ADD CONSTRAINT workflow_run_pkey PRIMARY KEY (id);

ALTER TABLE workflow_run
    ADD CONSTRAINT workflow_run_unique UNIQUE (handler, idempotency_key);

ALTER TABLE workspace_email_domain
    ADD CONSTRAINT workspace_email_domain_pkey PRIMARY KEY (domain);

ALTER TABLE workspace
    ADD CONSTRAINT workspace_pkey PRIMARY KEY (id);

ALTER TABLE workspace
    ADD CONSTRAINT workspace_slug_unique UNIQUE (slug);

CREATE INDEX ai_call_logical_idx ON ai_call USING btree (logical_call_id);

CREATE INDEX ai_call_payload_call ON ai_call_payload USING btree (ai_call_id);

CREATE INDEX ai_call_payload_ws_time ON ai_call_payload USING btree (occurred_at);

CREATE INDEX ai_call_terminal_trace_idx ON ai_call USING btree (occurred_at DESC, id DESC) WHERE is_terminal;

CREATE INDEX ai_call_ws_corr ON ai_call USING btree (correlation_id);

CREATE INDEX ai_call_ws_run ON ai_call USING btree (agent_run_id);

CREATE INDEX ai_call_ws_time ON ai_call USING btree (occurred_at DESC);

CREATE INDEX attachment_account_ix ON attachment USING btree (organization_id, pinned DESC, created_at DESC) WHERE (archived_at IS NULL);

CREATE INDEX attachment_contract_ix ON attachment USING btree (contract_id, created_at DESC) WHERE ((contract_id IS NOT NULL) AND (archived_at IS NULL));

CREATE UNIQUE INDEX attachment_external_part_key ON attachment USING btree (external_source_id, external_part_id) WHERE (external_source_id IS NOT NULL);

CREATE INDEX capture_trace_counterparty ON capture_trace USING btree (counterparty) WHERE (counterparty IS NOT NULL);

CREATE INDEX capture_trace_message ON capture_trace USING btree (source_system, source_id);

CREATE UNIQUE INDEX capture_trace_natural_key ON capture_trace USING btree (COALESCE(user_id, '00000000-0000-0000-0000-000000000000'::uuid), source_system, source_id, stage, outcome);

CREATE INDEX capture_trace_user_window ON capture_trace USING btree (user_id, occurred_at DESC);

CREATE INDEX capture_trace_window ON capture_trace USING btree (occurred_at DESC);

CREATE INDEX comms_outbound_workspace_activity_ix ON comms_outbound USING btree (activity_id);

CREATE INDEX consent_qualifying_event_person_ix ON consent_qualifying_event USING btree (person_id, occurred_at DESC);

CREATE UNIQUE INDEX consent_qualifying_event_source_unique ON consent_qualifying_event USING btree (person_id, source_entity_type, source_entity_id) WHERE (source_entity_id IS NOT NULL);

CREATE INDEX contract_account_ix ON contract USING btree (organization_id, created_at DESC, id DESC) WHERE (archived_at IS NULL);

CREATE INDEX contract_deal_ix ON contract USING btree (deal_id) WHERE ((deal_id IS NOT NULL) AND (archived_at IS NULL));

CREATE INDEX contract_renewal_due_ix ON contract USING btree (renewal_on) WHERE ((renewal_on IS NOT NULL) AND (archived_at IS NULL));

CREATE INDEX conversation_claim_activity_ix ON conversation_claim USING btree (source_activity_id) WHERE (archived_at IS NULL);

CREATE INDEX conversation_claim_person_ix ON conversation_claim USING btree (person_id, kind) WHERE (archived_at IS NULL);

CREATE INDEX deal_won_without_contract_ix ON deal USING btree (won_without_contract_reason) WHERE (won_without_contract_reason IS NOT NULL);

CREATE UNIQUE INDEX extension_secret_user_key ON extension_secret USING btree (extension_name, user_id, key) WHERE (user_id IS NOT NULL);

CREATE UNIQUE INDEX extension_secret_workspace_key ON extension_secret USING btree (extension_name, key) WHERE (user_id IS NULL);

CREATE INDEX extension_secret_workspace_user ON extension_secret USING btree (user_id);

CREATE UNIQUE INDEX finance_customer_link_external_ux ON finance_customer_link USING btree (connection_id, external_customer_id) WHERE (archived_at IS NULL);

CREATE UNIQUE INDEX finance_customer_link_organization_ux ON finance_customer_link USING btree (connection_id, organization_id) WHERE (archived_at IS NULL);

CREATE INDEX finance_invoice_account_ix ON finance_invoice USING btree (organization_id, issued_at DESC);

CREATE INDEX finance_invoice_credits_ix ON finance_invoice USING btree (credits_invoice_id) WHERE (credits_invoice_id IS NOT NULL);

CREATE INDEX finance_invoice_open_ix ON finance_invoice USING btree (organization_id) WHERE ((open_minor > 0) AND (void_at IS NULL));

CREATE INDEX finance_payment_account_ix ON finance_payment USING btree (organization_id, paid_at DESC);

CREATE INDEX finance_payment_invoice_ix ON finance_payment USING btree (invoice_id) WHERE (invoice_id IS NOT NULL);

CREATE INDEX idx_aam_subject ON activity_audience_member USING btree (subject_type, subject_id);

CREATE INDEX idx_activity_channel_thread ON activity USING btree (channel_provider, thread_key) WHERE (channel_provider IS NOT NULL);

CREATE INDEX idx_activity_counterparty_email ON activity USING btree (counterparty_email) WHERE (counterparty_email IS NOT NULL);

CREATE INDEX idx_activity_counterparty_outbound_attested ON activity USING btree (counterparty_email) WHERE ((counterparty_email IS NOT NULL) AND counterparty_outbound_attested);

CREATE INDEX idx_activity_direction ON activity USING btree (direction, occurred_at DESC) WHERE ((direction IS NOT NULL) AND (archived_at IS NULL));

CREATE INDEX idx_activity_kind ON activity USING btree (kind, occurred_at DESC) WHERE (archived_at IS NULL);

CREATE INDEX idx_activity_labeled ON activity USING btree (capture_labeled_at) WHERE (capture_labeled_at IS NOT NULL);

CREATE INDEX idx_activity_meeting_host ON activity USING btree (host_user_id, occurred_at) WHERE ((kind = 'meeting'::text) AND (archived_at IS NULL));

CREATE INDEX idx_activity_reminders ON activity USING btree (remind_at) WHERE ((kind = 'task'::text) AND (remind_at IS NOT NULL) AND (is_done = false) AND (archived_at IS NULL));

CREATE INDEX idx_activity_restricted_until ON activity USING btree (restricted_until) WHERE (restricted_at IS NOT NULL);

CREATE INDEX idx_activity_search ON activity USING gin (search_tsv);

CREATE INDEX idx_activity_tasks ON activity USING btree (assignee_id, due_at) WHERE ((kind = 'task'::text) AND (is_done = false) AND (archived_at IS NULL));

CREATE INDEX idx_activity_thread ON activity USING btree (thread_key) WHERE (thread_key IS NOT NULL);

CREATE INDEX idx_activity_unlabeled ON activity USING btree (occurred_at) WHERE ((capture_label IS NULL) AND (captured_by ~~ 'connector:%'::text) AND (kind = 'email'::text));

CREATE INDEX idx_activity_ws_time ON activity USING btree (occurred_at DESC) WHERE (archived_at IS NULL);

CREATE INDEX idx_agent_run_awaiting ON agent_run USING btree (approval_id) WHERE (status = 'awaiting_approval'::text);

CREATE UNIQUE INDEX idx_agent_task_approval ON agent_task USING btree (approval_id);

CREATE INDEX idx_agent_task_expiry ON agent_task USING btree (expires_at);

CREATE INDEX idx_agent_task_passport ON agent_task USING btree (passport_id);

CREATE INDEX idx_ai_feedback_subject ON ai_feedback USING btree (subject_type, subject_id);

CREATE INDEX idx_alink_deal ON activity_link USING btree (deal_id) WHERE (deal_id IS NOT NULL);

CREATE INDEX idx_alink_lead ON activity_link USING btree (lead_id) WHERE (lead_id IS NOT NULL);

CREATE INDEX idx_alink_org ON activity_link USING btree (organization_id) WHERE (organization_id IS NOT NULL);

CREATE INDEX idx_alink_person ON activity_link USING btree (person_id) WHERE (person_id IS NOT NULL);

CREATE INDEX idx_alink_project ON activity_link USING btree (project_id) WHERE (project_id IS NOT NULL);

CREATE INDEX idx_aparticipant_address ON activity_participant USING btree (lower(address)) WHERE (address IS NOT NULL);

CREATE INDEX idx_aparticipant_person ON activity_participant USING btree (person_id, activity_id) WHERE (person_id IS NOT NULL);

CREATE INDEX idx_aparticipant_user ON activity_participant USING btree (user_id, activity_id) WHERE (user_id IS NOT NULL);

CREATE INDEX idx_app_user_live ON app_user USING btree (created_at, id) WHERE (archived_at IS NULL);

CREATE INDEX idx_approval_bundle ON approval USING btree (bundle_id, created_at) WHERE (bundle_id IS NOT NULL);

CREATE INDEX idx_approval_expiry_due ON approval USING btree (expires_at) WHERE (status = 'pending'::text);

CREATE INDEX idx_approval_inbox ON approval USING btree (created_at) WHERE (status = 'pending'::text);

CREATE INDEX idx_approval_target ON approval USING btree (target_entity_id) WHERE (target_entity_id IS NOT NULL);

CREATE INDEX idx_are_activity ON activity_retention_evidence USING btree (activity_id);

CREATE INDEX idx_are_deal ON activity_retention_evidence USING btree (deal_id) WHERE (deal_id IS NOT NULL);

CREATE INDEX idx_are_decided_by ON activity_retention_evidence USING btree (decided_by) WHERE (decided_by IS NOT NULL);

CREATE INDEX idx_attachment_entity ON attachment USING btree (entity_type, entity_id) WHERE (archived_at IS NULL);

CREATE INDEX idx_attachment_extraction_latest ON attachment_extraction USING btree (attachment_id, created_at DESC);

CREATE INDEX idx_audit_actor ON audit_log USING btree (workspace_id, actor_id, occurred_at DESC);

CREATE INDEX idx_audit_entity ON audit_log USING btree (workspace_id, entity_type, entity_id, occurred_at DESC);

CREATE INDEX idx_audit_time ON audit_log USING btree (workspace_id, occurred_at DESC);

CREATE UNIQUE INDEX idx_auth_token_hash ON auth_token USING btree (token_hash);

CREATE INDEX idx_auth_token_user ON auth_token USING btree (user_id, purpose) WHERE (used_at IS NULL);

CREATE INDEX idx_automation_key_live ON automation USING btree (key) WHERE (enabled AND (archived_at IS NULL));

CREATE INDEX idx_booking_page_host ON booking_page USING btree (host_user_id) WHERE (revoked_at IS NULL);

CREATE INDEX idx_brief_item_deal ON brief_item USING btree (deal_id) WHERE (state <> 'new'::text);

CREATE INDEX idx_brief_item_run ON brief_item USING btree (brief_run_id, rank);

CREATE INDEX idx_brief_item_state ON brief_item USING btree (brief_run_id, state, state_at);

CREATE INDEX idx_brief_run_user ON brief_run USING btree (user_id, generated_at DESC);

CREATE INDEX idx_capture_auto_enrich_due ON capture_auto_enrich_state USING btree (next_attempt_at) WHERE (next_attempt_at IS NOT NULL);

CREATE INDEX idx_capture_backfill_conn ON capture_backfill USING btree (connection_id, created_at DESC);

CREATE INDEX idx_capture_connection ON capture_connection USING btree (provider, status) WHERE (archived_at IS NULL);

CREATE INDEX idx_capture_exclusion_value ON capture_exclusion USING btree (kind, value);

CREATE INDEX idx_capture_pending_counterparty_due ON capture_pending_counterparty USING btree (next_attempt_at) WHERE (next_attempt_at IS NOT NULL);

CREATE UNIQUE INDEX idx_capture_pending_counterparty_live ON capture_pending_counterparty USING btree (email) WHERE (status IN ('pending', 'unsure'));

CREATE INDEX idx_capture_pending_counterparty_noise ON capture_pending_counterparty USING btree (email) WHERE (status = 'noise'::text);

CREATE UNIQUE INDEX idx_capture_pending_counterparty_suppressed ON capture_pending_counterparty USING btree (email) WHERE (status = 'suppressed'::text);

CREATE INDEX idx_capture_sync_due ON capture_sync_state USING btree (next_sync_at);

CREATE INDEX idx_capture_watch_renew ON capture_connection USING btree (watch_expires_at) WHERE ((watch_expires_at IS NOT NULL) AND (status = 'connected'::text));

CREATE INDEX idx_commission_deal ON commission_entry USING btree (deal_id);

CREATE INDEX idx_commission_partner ON commission_entry USING btree (partner_org_id, status);

CREATE INDEX idx_consent_doi_token_person ON consent_doi_token USING btree (person_id, purpose_id);

CREATE INDEX idx_consent_event_lead ON consent_event USING btree (lead_id, captured_at DESC) WHERE (lead_id IS NOT NULL);

CREATE INDEX idx_consent_event_person ON consent_event USING btree (person_id, captured_at DESC);

CREATE INDEX idx_custom_field_object ON custom_field USING btree (object, status);

CREATE INDEX idx_deal_close ON deal USING btree (expected_close_date) WHERE ((status = 'open'::text) AND (archived_at IS NULL));

CREATE INDEX idx_deal_forecast_history_deal ON deal_forecast_history USING btree (deal_id, changed_at);

CREATE INDEX idx_deal_name_trgm ON deal USING gin (f_fold_apostrophes(lower(name)) gin_trgm_ops);

CREATE INDEX idx_deal_org ON deal USING btree (organization_id) WHERE ((organization_id IS NOT NULL) AND (archived_at IS NULL));

CREATE INDEX idx_deal_owner ON deal USING btree (owner_id) WHERE (archived_at IS NULL);

CREATE INDEX idx_deal_partner ON deal USING btree (partner_org_id) WHERE ((partner_org_id IS NOT NULL) AND (archived_at IS NULL));

CREATE INDEX idx_deal_partner_attribution ON deal USING btree (partner_attribution) WHERE ((partner_attribution IS NOT NULL) AND (archived_at IS NULL));

CREATE INDEX idx_deal_pipeline ON deal USING btree (pipeline_id, stage_id) WHERE (archived_at IS NULL);

CREATE INDEX idx_deal_project ON deal USING btree (project_id) WHERE ((project_id IS NOT NULL) AND (archived_at IS NULL));

CREATE INDEX idx_deal_search ON deal USING gin (search_tsv);

CREATE INDEX idx_deal_stage ON deal USING btree (stage_id) WHERE (archived_at IS NULL);

CREATE INDEX idx_deal_stalled ON deal USING btree (last_activity_at) WHERE ((status = 'open'::text) AND (archived_at IS NULL));

CREATE INDEX idx_dedupe_candidate_open ON dedupe_candidate USING btree (confidence DESC) WHERE ((disposition = 'open'::text) AND (archived_at IS NULL));

CREATE INDEX idx_domain_disposition_admission ON organization_domain_disposition USING btree (admission_at DESC) WHERE (admission IS NOT NULL);

CREATE INDEX idx_domain_disposition_unevidenced ON organization_domain_disposition USING btree (updated_at DESC) WHERE (pending_reason = 'unevidenced'::text);

CREATE INDEX idx_dsh_deal ON deal_stage_history USING btree (deal_id, changed_at);

CREATE INDEX idx_dsh_ws_time ON deal_stage_history USING btree (changed_at);

CREATE INDEX idx_dsr_open ON data_subject_request USING btree (due_at) WHERE (status IN ('open', 'in_progress'));

CREATE INDEX idx_event_outbox_unpublished ON event_outbox USING btree (seq) WHERE (published_at IS NULL);

CREATE INDEX idx_field_provenance_object ON field_provenance USING btree (object_type, object_id, field_name, captured_at DESC);

CREATE INDEX idx_fx_rate_lookup ON fx_rate USING btree (from_currency, to_currency, rate_date);

CREATE INDEX idx_graph_edge_person ON graph_interaction_edge USING btree (person_id, last_at DESC);

CREATE INDEX idx_graph_edge_user ON graph_interaction_edge USING btree (user_id, last_at DESC);

CREATE INDEX idx_idempotency_key_created ON idempotency_key USING btree (created_at);

CREATE INDEX idx_lead_cand_org ON lead USING btree (candidate_org_key) WHERE ((candidate_org_key IS NOT NULL) AND (archived_at IS NULL));

CREATE INDEX idx_lead_disqualify_reason ON lead USING btree (disqualify_reason_id) WHERE (disqualify_reason_id IS NOT NULL);

CREATE INDEX idx_lead_linkedin ON lead USING btree (linkedin_url) WHERE (linkedin_url IS NOT NULL);

CREATE INDEX idx_lead_manual_signal_lead ON lead_manual_signal USING btree (lead_id, set_at DESC);

CREATE INDEX idx_lead_merged_into ON lead USING btree (merged_into_id) WHERE (merged_into_id IS NOT NULL);

CREATE INDEX idx_lead_name_trgm ON lead USING gin (f_fold_apostrophes(lower(((COALESCE(full_name, ''::text) || ' '::text) || COALESCE(company_name, ''::text)))) gin_trgm_ops);

CREATE INDEX idx_lead_owner ON lead USING btree (owner_id) WHERE (archived_at IS NULL);

CREATE INDEX idx_lead_project ON lead USING btree (project_id) WHERE ((project_id IS NOT NULL) AND (archived_at IS NULL));

CREATE INDEX idx_lead_qualified_deal ON lead USING btree (qualified_deal_id) WHERE (qualified_deal_id IS NOT NULL);

CREATE INDEX idx_lead_score ON lead USING btree (score DESC) WHERE ((archived_at IS NULL) AND (status IN ('new', 'contacted', 'engaged')));

CREATE INDEX idx_lead_score_history_series ON lead_score_history USING btree (lead_id, computed_at DESC, id DESC);

CREATE INDEX idx_lead_search ON lead USING gin (search_tsv);

CREATE INDEX idx_lead_sla_open ON lead USING btree (created_at) WHERE ((archived_at IS NULL) AND (first_response_at IS NULL) AND (sla_breached_at IS NULL));

CREATE INDEX idx_lead_ws_live ON lead USING btree (status) WHERE (archived_at IS NULL);

CREATE INDEX idx_linkedin_connection_email ON linkedin_connection USING btree (lower(email)) WHERE ((email IS NOT NULL) AND (tombstoned_at IS NULL));

CREATE INDEX idx_linkedin_connection_match ON linkedin_connection USING btree (normalized_name, normalized_company) WHERE (tombstoned_at IS NULL);

CREATE INDEX idx_linkedin_connection_org ON linkedin_connection USING btree (matched_org_id) WHERE ((matched_org_id IS NOT NULL) AND (tombstoned_at IS NULL));

CREATE INDEX idx_list_member_entity ON list_member USING btree (entity_type, entity_id);

CREATE INDEX idx_list_member_list ON list_member USING btree (list_id);

CREATE INDEX idx_offer_deal ON offer USING btree (deal_id, revision DESC) WHERE (archived_at IS NULL);

CREATE INDEX idx_offer_status ON offer USING btree (status) WHERE (archived_at IS NULL);

CREATE INDEX idx_offer_template_fk ON offer USING btree (template_id) WHERE (template_id IS NOT NULL);

CREATE INDEX idx_oli_offer ON offer_line_item USING btree (offer_id, "position");

CREATE INDEX idx_org_class ON organization USING btree (classification) WHERE (archived_at IS NULL);

CREATE INDEX idx_org_created_keyset ON organization USING btree (created_at DESC, id DESC) WHERE (archived_at IS NULL);

CREATE INDEX idx_org_domain_org ON organization_domain USING btree (organization_id) WHERE (archived_at IS NULL);

CREATE INDEX idx_org_last_activity_keyset ON organization USING btree (last_activity_at DESC NULLS LAST, created_at DESC, id DESC) WHERE (archived_at IS NULL);

CREATE INDEX idx_org_legal_name_trgm ON organization USING gin (f_fold_apostrophes(lower(legal_name)) gin_trgm_ops);

CREATE INDEX idx_org_lifecycle ON organization USING btree (lifecycle) WHERE (archived_at IS NULL);

CREATE INDEX idx_org_name_keyset ON organization USING btree (display_name, created_at DESC, id DESC) WHERE (archived_at IS NULL);

CREATE INDEX idx_org_name_keyset_desc ON organization USING btree (display_name DESC NULLS LAST, created_at DESC, id DESC) WHERE (archived_at IS NULL);

CREATE INDEX idx_org_name_trgm ON organization USING gin (f_fold_apostrophes(lower(display_name)) gin_trgm_ops);

CREATE INDEX idx_org_owner ON organization USING btree (owner_id) WHERE (archived_at IS NULL);

CREATE INDEX idx_org_parent ON organization USING btree (parent_org_id) WHERE (parent_org_id IS NOT NULL);

CREATE INDEX idx_org_rel_type_cascade ON organization_relationship_type USING btree (organization_id);

CREATE INDEX idx_org_rel_type_org ON organization_relationship_type USING btree (organization_id) WHERE (archived_at IS NULL);

CREATE INDEX idx_org_search ON organization USING gin (search_tsv);

CREATE INDEX idx_org_updated_keyset ON organization USING btree (updated_at DESC NULLS LAST, created_at DESC, id DESC) WHERE (archived_at IS NULL);

CREATE INDEX idx_organization_domain_disposition_due ON organization_domain_disposition USING btree (next_attempt_at) WHERE (next_attempt_at IS NOT NULL);

CREATE INDEX idx_organization_geocoded ON organization USING btree (geocode_lat, geocode_lon) WHERE ((geocode_status = 'ok'::text) AND (archived_at IS NULL));

CREATE INDEX idx_partner_stage ON partner USING btree (relationship_stage) WHERE (archived_at IS NULL);

CREATE INDEX idx_partner_tier ON partner USING btree (margin_tier) WHERE (archived_at IS NULL);

CREATE INDEX idx_passport_obo ON passport USING btree (on_behalf_of) WHERE (revoked_at IS NULL);

CREATE INDEX idx_person_channel_identity_person ON person_channel_identity USING btree (person_id);

CREATE INDEX idx_person_created_keyset ON person USING btree (created_at DESC, id DESC) WHERE (archived_at IS NULL);

CREATE INDEX idx_person_email_correspondence ON person_email USING btree (email) WHERE (from_correspondence AND (archived_at IS NULL));

CREATE INDEX idx_person_email_person ON person_email USING btree (person_id) WHERE (archived_at IS NULL);

CREATE INDEX idx_person_from_lead ON person USING btree (converted_from_lead_id) WHERE (converted_from_lead_id IS NOT NULL);

CREATE INDEX idx_person_last_activity_keyset ON person USING btree (last_activity_at DESC NULLS LAST, created_at DESC, id DESC) WHERE (archived_at IS NULL);

CREATE INDEX idx_person_merged_into ON person USING btree (merged_into_id) WHERE (merged_into_id IS NOT NULL);

CREATE INDEX idx_person_name_keyset ON person USING btree (full_name, created_at DESC, id DESC) WHERE (archived_at IS NULL);

CREATE INDEX idx_person_name_keyset_desc ON person USING btree (full_name DESC NULLS LAST, created_at DESC, id DESC) WHERE (archived_at IS NULL);

CREATE INDEX idx_person_name_trgm ON person USING gin (f_fold_apostrophes(lower(full_name)) gin_trgm_ops);

CREATE INDEX idx_person_owner ON person USING btree (owner_id) WHERE (archived_at IS NULL);

CREATE INDEX idx_person_phone_person ON person_phone USING btree (person_id) WHERE (archived_at IS NULL);

CREATE INDEX idx_person_profile_field ON person_profile_field USING btree (person_id);

CREATE INDEX idx_person_search ON person USING gin (search_tsv);

CREATE INDEX idx_person_social_person ON person_social USING btree (person_id);

CREATE INDEX idx_person_updated_keyset ON person USING btree (updated_at DESC NULLS LAST, created_at DESC, id DESC) WHERE (archived_at IS NULL);

CREATE INDEX idx_pph_project ON project_phase_history USING btree (project_id, occurred_at DESC);

CREATE INDEX idx_product_active ON product USING btree (active) WHERE (archived_at IS NULL);

CREATE INDEX idx_project_last_activity_keyset ON project USING btree (last_activity_at DESC NULLS LAST, created_at DESC, id DESC) WHERE (archived_at IS NULL);

CREATE INDEX idx_project_name_trgm ON project USING gin (f_unaccent(lower(name)) gin_trgm_ops);

CREATE INDEX idx_project_org ON project USING btree (organization_id) WHERE (archived_at IS NULL);

CREATE INDEX idx_project_org_open ON project USING btree (organization_id) WHERE ((phase <> 'closed'::text) AND (archived_at IS NULL));

CREATE INDEX idx_project_owner ON project USING btree (owner_id) WHERE (archived_at IS NULL);

CREATE INDEX idx_project_search ON project USING gin (search_tsv);

CREATE INDEX idx_quota_owner ON quota USING btree (owner_id) WHERE (owner_id IS NOT NULL);

CREATE INDEX idx_quota_team ON quota USING btree (team_id) WHERE (team_id IS NOT NULL);

CREATE INDEX idx_record_grant_record ON record_grant USING btree (record_type, record_id);

CREATE INDEX idx_record_grant_subject ON record_grant USING btree (subject_type, subject_id);

CREATE INDEX idx_rel_deal_stakeholders ON relationship USING btree (deal_id) WHERE ((kind = 'deal_stakeholder'::text) AND (archived_at IS NULL));

CREATE INDEX idx_rel_employer_people ON relationship USING btree (organization_id, person_id) WHERE ((kind = 'employment'::text) AND (ended_at IS NULL) AND (archived_at IS NULL));

CREATE INDEX idx_rel_org_people ON relationship USING btree (organization_id) WHERE ((kind = 'employment'::text) AND (archived_at IS NULL));

CREATE INDEX idx_rel_partner_counterparty ON relationship USING btree (counterparty_org_id) WHERE ((kind IN ('partner_of', 'referred_by', 'co_sell_with')) AND (archived_at IS NULL));

CREATE INDEX idx_rel_partner_org ON relationship USING btree (organization_id) WHERE ((kind IN ('partner_of', 'referred_by', 'co_sell_with')) AND (archived_at IS NULL));

CREATE INDEX idx_rel_person_orgs ON relationship USING btree (person_id) WHERE ((kind = 'employment'::text) AND (archived_at IS NULL));

CREATE INDEX idx_rel_person_projects ON relationship USING btree (person_id) WHERE ((kind = 'project_stakeholder'::text) AND (archived_at IS NULL));

CREATE INDEX idx_rel_project_stakeholders ON relationship USING btree (project_id) WHERE ((kind = 'project_stakeholder'::text) AND (archived_at IS NULL));

CREATE INDEX idx_rel_stakeholder_deals ON relationship USING btree (person_id) WHERE ((kind = 'deal_stakeholder'::text) AND (archived_at IS NULL));

CREATE INDEX idx_rel_traverse_deal ON relationship USING btree (deal_id) WHERE (archived_at IS NULL);

CREATE INDEX idx_rel_traverse_organization ON relationship USING btree (organization_id) WHERE (archived_at IS NULL);

CREATE INDEX idx_rel_traverse_person ON relationship USING btree (person_id) WHERE (archived_at IS NULL);

CREATE INDEX idx_rel_traverse_project ON relationship USING btree (project_id) WHERE (archived_at IS NULL);

CREATE INDEX idx_role_assignment_user ON role_assignment USING btree (user_id);

CREATE INDEX idx_runner_job_due ON runner_job USING btree (status, due_at);

CREATE INDEX idx_saved_view_owner ON saved_view USING btree (owner_id, resource) WHERE (archived_at IS NULL);

CREATE INDEX idx_scheduled_send_anchor ON scheduled_send USING btree (anchor_activity_id) WHERE (anchor_activity_id IS NOT NULL);

CREATE INDEX idx_scheduled_send_due ON scheduled_send USING btree (scheduled_at) WHERE (status = 'scheduled'::text);

CREATE INDEX idx_scheduled_send_owner ON scheduled_send USING btree (scheduled_by, status, scheduled_at DESC);

CREATE INDEX idx_session_user ON session USING btree (user_id) WHERE (revoked_at IS NULL);

CREATE INDEX idx_signal_open ON signal USING btree (status, severity, detected_at DESC);

CREATE INDEX idx_signal_owner_private ON signal USING btree (owner_id) WHERE ((visibility = 'owner'::text) AND (archived_at IS NULL));

CREATE INDEX idx_signal_unresolved ON signal USING btree (resolution_state, detected_at DESC);

CREATE INDEX idx_sigres_signal ON signal_resolution USING btree (signal_id, created_at DESC);

CREATE INDEX idx_site_read_org ON site_read USING btree (organization_id, created_at DESC);

CREATE INDEX idx_site_read_retry_due ON site_read USING btree (next_attempt_at, id) WHERE ((status IN ('deferred', 'failed')) AND (next_attempt_at IS NOT NULL));

CREATE INDEX idx_stage_pipeline ON stage USING btree (pipeline_id) WHERE (archived_at IS NULL);

CREATE INDEX idx_system_log_action ON system_log USING btree (workspace_id, action, occurred_at DESC);

CREATE INDEX idx_system_log_actor ON system_log USING btree (workspace_id, actor_id, occurred_at DESC);

CREATE INDEX idx_system_log_time ON system_log USING btree (workspace_id, occurred_at DESC);

CREATE INDEX idx_taggable_entity ON taggable USING btree (entity_type, entity_id);

CREATE INDEX idx_taggable_tag ON taggable USING btree (tag_id);

CREATE INDEX idx_team_membership_team ON team_membership USING btree (team_id);

CREATE INDEX idx_team_membership_user ON team_membership USING btree (user_id);

CREATE INDEX idx_transcript_read_latest ON transcript_read USING btree (activity_id, created_at DESC);

CREATE INDEX idx_voice_corpus_profile ON voice_corpus_source USING btree (voice_profile_id, created_at DESC);

CREATE INDEX idx_webhook_delivery_by_subscription ON webhook_delivery USING btree (subscription_id, created_at DESC);

CREATE INDEX idx_webhook_delivery_due ON webhook_delivery USING btree (next_retry_at) WHERE (status = 'retrying'::text);

CREATE INDEX idx_webhook_subscription_live ON webhook_subscription USING btree (state) WHERE (archived_at IS NULL);

CREATE INDEX oauth_code_lent_passport_ix ON oauth_authorization_code USING btree (lent_passport_id);

CREATE INDEX oauth_grant_lent_passport_ix ON oauth_grant USING btree (lent_passport_id);

CREATE INDEX oauth_grant_user_live_ix ON oauth_grant USING btree (user_id, id) WHERE (revoked_at IS NULL);

CREATE INDEX oauth_refresh_token_grant_ix ON oauth_refresh_token USING btree (grant_id);

CREATE INDEX org_brief_organization_ix ON org_brief USING btree (organization_id);

CREATE INDEX org_dossier_organization_ix ON org_dossier USING btree (organization_id);

CREATE INDEX org_growth_fit_organization_ix ON org_growth_fit USING btree (organization_id);

CREATE UNIQUE INDEX organization_linkedin_url_key ON organization USING btree (lower(linkedin_url)) WHERE ((linkedin_url IS NOT NULL) AND (archived_at IS NULL));

CREATE INDEX passport_oauth_grant_ix ON passport USING btree (oauth_grant_id);

CREATE INDEX person_brief_person_ix ON person_brief USING btree (person_id);

CREATE INDEX person_moment_dismissal_person_ix ON person_moment_dismissal USING btree (person_id);

CREATE INDEX person_provider_claim_latest ON person_provider_claim USING btree (person_id, provider, retrieved_at DESC);

CREATE INDEX provider_run_due ON provider_run USING btree (state, next_attempt_at) WHERE (state IN ('queued', 'submitting', 'in_progress', 'completed'));

CREATE UNIQUE INDEX provider_run_one_live_person_fingerprint ON provider_run USING btree (person_id, provider, input_fingerprint) WHERE ((subject_kind = 'person'::text) AND (state IN ('queued', 'submitting', 'in_progress', 'submission_unknown')));

CREATE INDEX provider_run_person_history ON provider_run USING btree (person_id, provider, created_at DESC) WHERE (subject_kind = 'person'::text);

CREATE UNIQUE INDEX setup_token_one_outstanding ON setup_token USING btree (((consumed_at IS NULL))) WHERE (consumed_at IS NULL);

CREATE INDEX signal_entity_ix ON signal USING btree (entity_type, entity_id) WHERE (entity_id IS NOT NULL);

CREATE INDEX signal_resolved_org_ix ON signal USING btree (resolved_org_id) WHERE (resolved_org_id IS NOT NULL);

CREATE INDEX suggestion_dismissal_organization_ix ON suggestion_dismissal USING btree (organization_id);

CREATE UNIQUE INDEX uq_activity_link ON activity_link USING btree (activity_id, entity_type, COALESCE(person_id, organization_id, deal_id, lead_id, project_id));

CREATE UNIQUE INDEX uq_activity_link_project ON activity_link USING btree (activity_id) WHERE (entity_type = 'project'::text);

CREATE UNIQUE INDEX uq_activity_participant ON activity_participant USING btree (activity_id, role, COALESCE(user_id, '00000000-0000-0000-0000-000000000000'::uuid), COALESCE(person_id, '00000000-0000-0000-0000-000000000000'::uuid), COALESCE(address, ''::text));

CREATE UNIQUE INDEX uq_activity_retention_evidence ON activity_retention_evidence USING btree (activity_id, deal_id, deal_name, basis) NULLS NOT DISTINCT WHERE (basis <> 'controller_pin'::text);

CREATE UNIQUE INDEX uq_activity_source ON activity USING btree (source_system, source_id) WHERE ((source_system IS NOT NULL) AND (source_id IS NOT NULL));

CREATE UNIQUE INDEX uq_app_user_email ON app_user USING btree (lower(email));

CREATE UNIQUE INDEX uq_attachment_extraction_inflight ON attachment_extraction USING btree (attachment_id) WHERE (status IN ('queued', 'running'));

CREATE UNIQUE INDEX uq_capture_backfill_live ON capture_backfill USING btree (connection_id) WHERE (status IN ('queued', 'running'));

CREATE UNIQUE INDEX uq_capture_exclusion ON capture_exclusion USING btree (scope, COALESCE(user_id, '00000000-0000-0000-0000-000000000000'::uuid), kind, value);

CREATE UNIQUE INDEX uq_capture_freemail_domain ON capture_freemail_domain USING btree (domain);

CREATE UNIQUE INDEX uq_channel_connection_ws ON channel_connection USING btree (provider) WHERE (archived_at IS NULL);

CREATE UNIQUE INDEX uq_commission_live_per_deal ON commission_entry USING btree (deal_id) WHERE ((status <> 'void'::text) AND (reversal_of IS NULL));

CREATE UNIQUE INDEX uq_commission_trigger_event ON commission_entry USING btree (trigger_event_id) WHERE (trigger_event_id IS NOT NULL);

CREATE UNIQUE INDEX uq_custom_field_column ON custom_field USING btree (object, column_name);

CREATE UNIQUE INDEX uq_custom_field_slug ON custom_field USING btree (object, slug);

CREATE UNIQUE INDEX uq_dedupe_candidate_pair ON dedupe_candidate USING btree (entity_type, COALESCE(left_person_id, left_org_id, left_lead_id), COALESCE(right_person_id, right_org_id, right_lead_id));

CREATE UNIQUE INDEX uq_lead_email_dedupe ON lead USING btree (email) WHERE ((email IS NOT NULL) AND (archived_at IS NULL));

CREATE UNIQUE INDEX uq_lead_manual_signal_live ON lead_manual_signal USING btree (lead_id, factor) WHERE (superseded_at IS NULL);

CREATE UNIQUE INDEX uq_lead_source ON lead USING btree (source_system, source_id) WHERE ((source_system IS NOT NULL) AND (source_id IS NOT NULL));

CREATE UNIQUE INDEX uq_lead_source_key ON lead_source USING btree (key);

CREATE UNIQUE INDEX uq_linkedin_connection_natural ON linkedin_connection USING btree (owner_user_id, normalized_name, COALESCE(normalized_company, ''::text), COALESCE(connected_on, '1970-01-01'::date)) WHERE (provider_member_ref IS NULL);

CREATE UNIQUE INDEX uq_linkedin_connection_provider ON linkedin_connection USING btree (owner_user_id, provider_member_ref) WHERE (provider_member_ref IS NOT NULL);

CREATE UNIQUE INDEX uq_offer_template_default ON offer_template USING btree (locale) WHERE (is_default AND (archived_at IS NULL));

CREATE UNIQUE INDEX uq_org_domain ON organization_domain USING btree (domain) WHERE (archived_at IS NULL);

CREATE UNIQUE INDEX uq_org_domain_primary ON organization_domain USING btree (organization_id) WHERE (is_primary AND (archived_at IS NULL));

CREATE UNIQUE INDEX uq_org_rel_type ON organization_relationship_type USING btree (organization_id, relationship_type) WHERE (archived_at IS NULL);

CREATE UNIQUE INDEX uq_organization_anchor ON organization USING btree ((true)) WHERE (is_anchor AND (archived_at IS NULL));

CREATE UNIQUE INDEX uq_organization_domain_disposition ON organization_domain_disposition USING btree (domain);

CREATE UNIQUE INDEX uq_person_channel_identity ON person_channel_identity USING btree (provider, channel_user_id) WHERE (archived_at IS NULL);

CREATE UNIQUE INDEX uq_person_email_dedupe ON person_email USING btree (email) WHERE (archived_at IS NULL);

CREATE UNIQUE INDEX uq_person_email_primary ON person_email USING btree (person_id, email_type) WHERE (is_primary AND (archived_at IS NULL));

CREATE UNIQUE INDEX uq_person_phone_primary ON person_phone USING btree (person_id, phone_type) WHERE (is_primary AND (archived_at IS NULL));

CREATE UNIQUE INDEX uq_pipeline_default ON pipeline USING btree ((true)) WHERE (is_default AND (archived_at IS NULL));

CREATE UNIQUE INDEX uq_preference_token_person ON preference_token USING btree (person_id) WHERE (revoked_at IS NULL);

CREATE UNIQUE INDEX uq_product_sku ON product USING btree (sku) WHERE ((sku IS NOT NULL) AND (archived_at IS NULL));

CREATE UNIQUE INDEX uq_project_key ON project USING btree (lower(key)) WHERE ((key IS NOT NULL) AND (archived_at IS NULL));

CREATE UNIQUE INDEX uq_rel_current_primary_employer ON relationship USING btree (person_id) WHERE ((kind = 'employment'::text) AND is_current_primary AND (archived_at IS NULL));

CREATE UNIQUE INDEX uq_rel_deal_person_role ON relationship USING btree (deal_id, person_id, role) WHERE ((kind = 'deal_stakeholder'::text) AND (archived_at IS NULL));

CREATE UNIQUE INDEX uq_rel_employment ON relationship USING btree (person_id, organization_id) WHERE ((kind = 'employment'::text) AND (ended_at IS NULL) AND (archived_at IS NULL));

CREATE UNIQUE INDEX uq_rel_project_stakeholder ON relationship USING btree (project_id, person_id) WHERE ((kind = 'project_stakeholder'::text) AND (archived_at IS NULL));

CREATE UNIQUE INDEX uq_role_assignment ON role_assignment USING btree (role_id, user_id, COALESCE(team_id, '00000000-0000-0000-0000-000000000000'::uuid));

CREATE UNIQUE INDEX uq_signal_fingerprint ON signal USING btree (fingerprint) WHERE ((fingerprint IS NOT NULL) AND (archived_at IS NULL));

CREATE UNIQUE INDEX uq_site_read_onboarding_inflight ON site_read USING btree (seed_url) WHERE ((target_kind = 'onboarding'::text) AND (status IN ('queued', 'deferred', 'running')));

CREATE UNIQUE INDEX uq_site_read_org_inflight ON site_read USING btree (organization_id, seed_url) WHERE ((target_kind = 'organization'::text) AND (status IN ('queued', 'deferred', 'running')));

CREATE UNIQUE INDEX uq_site_read_triage_inflight ON site_read USING btree (seed_url) WHERE ((target_kind = 'domain_triage'::text) AND (status IN ('queued', 'deferred', 'running')));

CREATE UNIQUE INDEX uq_stage_position ON stage USING btree (pipeline_id, "position") WHERE (archived_at IS NULL);

CREATE UNIQUE INDEX uq_tag_name ON tag USING btree (lower(name));

CREATE UNIQUE INDEX uq_transcript_read_inflight ON transcript_read USING btree (activity_id) WHERE (status IN ('queued', 'running'));

CREATE UNIQUE INDEX uq_voice_profile_user_live ON voice_profile USING btree (owner_id) WHERE ((scope = 'user'::text) AND (archived_at IS NULL));

CREATE INDEX voice_build_deferred_due ON voice_build USING btree (next_attempt_at, id) WHERE ((status = 'deferred'::text) AND (archived_at IS NULL));

CREATE UNIQUE INDEX voice_build_one_active ON voice_build USING btree (voice_profile_id) WHERE (status IN ('queued', 'deferred', 'running'));

CREATE INDEX voice_build_poll ON voice_build USING btree (voice_profile_id, created_at DESC, id DESC) WHERE (archived_at IS NULL);

CREATE INDEX voice_build_profile_fk ON voice_build USING btree (voice_profile_id);

CREATE INDEX voice_build_requester_fk ON voice_build USING btree (requested_by) WHERE (requested_by IS NOT NULL);

CREATE INDEX voice_corpus_source_manifest ON voice_corpus_source USING btree (voice_profile_id, created_at DESC, id DESC) WHERE (archived_at IS NULL);

CREATE INDEX voice_corpus_source_profile_fk ON voice_corpus_source USING btree (voice_profile_id);

CREATE INDEX voice_learning_signal_profile_fk ON voice_learning_signal USING btree (voice_profile_id);

CREATE INDEX voice_learning_signal_retention ON voice_learning_signal USING btree (retention_until) WHERE (content_erased_at IS NULL);

CREATE INDEX voice_profile_delta_history ON voice_profile_delta USING btree (voice_profile_id, created_at DESC, id DESC) WHERE (archived_at IS NULL);

CREATE INDEX voice_profile_delta_profile_fk ON voice_profile_delta USING btree (voice_profile_id);

CREATE INDEX voice_profile_owner_fk ON voice_profile USING btree (owner_id) WHERE (owner_id IS NOT NULL);

CREATE INDEX voice_profile_team_fk ON voice_profile USING btree (team_id) WHERE (team_id IS NOT NULL);

CREATE INDEX voice_profile_version_history ON voice_profile_version USING btree (voice_profile_id, created_at DESC, id DESC) WHERE (archived_at IS NULL);

CREATE UNIQUE INDEX voice_profile_version_one_active ON voice_profile_version USING btree (voice_profile_id) WHERE (status = 'active'::text);

CREATE INDEX voice_profile_version_profile_fk ON voice_profile_version USING btree (voice_profile_id);

CREATE TRIGGER activity_last_activity AFTER UPDATE OF occurred_at, archived_at ON activity FOR EACH ROW WHEN (((old.occurred_at IS DISTINCT FROM new.occurred_at) OR (old.archived_at IS DISTINCT FROM new.archived_at))) EXECUTE FUNCTION trg_activity_last_activity();

CREATE TRIGGER activity_link_last_activity AFTER INSERT OR DELETE OR UPDATE ON activity_link FOR EACH ROW EXECUTE FUNCTION trg_activity_link_last_activity();

CREATE TRIGGER activity_link_project_last_activity AFTER INSERT OR DELETE OR UPDATE ON activity_link FOR EACH ROW EXECUTE FUNCTION trg_activity_link_project_last_activity();

CREATE TRIGGER activity_project_last_activity AFTER UPDATE OF occurred_at, archived_at ON activity FOR EACH ROW WHEN (((old.occurred_at IS DISTINCT FROM new.occurred_at) OR (old.archived_at IS DISTINCT FROM new.archived_at))) EXECUTE FUNCTION trg_activity_project_last_activity();

CREATE TRIGGER activity_refuse_restricted_mutation BEFORE DELETE OR UPDATE ON activity FOR EACH ROW EXECUTE FUNCTION activity_refuse_restricted_mutation();

CREATE TRIGGER activity_retention_evidence_is_frozen BEFORE DELETE OR UPDATE ON activity_retention_evidence FOR EACH ROW EXECUTE FUNCTION activity_retention_evidence_is_frozen();

CREATE TRIGGER commission_entry_touch BEFORE UPDATE ON commission_entry FOR EACH ROW EXECUTE FUNCTION set_updated_at_bump_version();

CREATE TRIGGER contract_set_updated_at BEFORE UPDATE ON contract FOR EACH ROW EXECUTE FUNCTION set_updated_at_bump_version();

CREATE TRIGGER deal_last_activity AFTER UPDATE OF organization_id ON deal FOR EACH ROW WHEN ((old.organization_id IS DISTINCT FROM new.organization_id)) EXECUTE FUNCTION trg_deal_last_activity();

CREATE TRIGGER organization_delete_clears_deal_partner BEFORE DELETE ON organization FOR EACH ROW EXECUTE FUNCTION deal_clear_partner_attribution_on_org_delete();

CREATE TRIGGER organization_refuse_anchor_retirement BEFORE INSERT OR UPDATE OF merged_into_id, is_anchor ON organization FOR EACH ROW EXECUTE FUNCTION organization_refuse_anchor_retirement();

CREATE TRIGGER relationship_last_activity AFTER INSERT OR DELETE OR UPDATE ON relationship FOR EACH ROW EXECUTE FUNCTION trg_relationship_last_activity();

CREATE TRIGGER trg_activity_updated BEFORE UPDATE ON activity FOR EACH ROW EXECUTE FUNCTION set_updated_at_bump_version();

CREATE TRIGGER trg_app_user_updated BEFORE UPDATE ON app_user FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TRIGGER trg_approval_updated BEFORE UPDATE ON approval FOR EACH ROW EXECUTE FUNCTION set_updated_at_bump_version();

CREATE TRIGGER trg_attachment_updated BEFORE UPDATE ON attachment FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TRIGGER trg_audit_no_mutate BEFORE DELETE OR UPDATE ON audit_log FOR EACH ROW EXECUTE FUNCTION audit_log_immutable();

CREATE TRIGGER trg_capture_backfill_updated BEFORE UPDATE ON capture_backfill FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TRIGGER trg_capture_sync_state_updated BEFORE UPDATE ON capture_sync_state FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TRIGGER trg_channel_connection_updated BEFORE UPDATE ON channel_connection FOR EACH ROW WHEN (((to_jsonb(old.*) - 'poll_offset'::text) IS DISTINCT FROM (to_jsonb(new.*) - 'poll_offset'::text))) EXECUTE FUNCTION set_updated_at_bump_version();

CREATE TRIGGER trg_conversation_claim_updated BEFORE UPDATE ON conversation_claim FOR EACH ROW EXECUTE FUNCTION set_updated_at_bump_version();

CREATE TRIGGER trg_custom_field_updated BEFORE UPDATE ON custom_field FOR EACH ROW EXECUTE FUNCTION set_updated_at_bump_version();

CREATE CONSTRAINT TRIGGER trg_deal_project_same_org AFTER INSERT OR UPDATE OF organization_id, project_id ON deal DEFERRABLE INITIALLY IMMEDIATE FOR EACH ROW EXECUTE FUNCTION assert_deal_project_same_org();

CREATE TRIGGER trg_deal_updated BEFORE UPDATE ON deal FOR EACH ROW EXECUTE FUNCTION set_updated_at_bump_version();

CREATE TRIGGER trg_dedupe_candidate_updated BEFORE UPDATE ON dedupe_candidate FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TRIGGER trg_dsr_updated BEFORE UPDATE ON data_subject_request FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TRIGGER trg_email_signature_updated BEFORE UPDATE ON email_signature FOR EACH ROW EXECUTE FUNCTION set_updated_at_bump_version();

CREATE TRIGGER trg_finance_connection_updated BEFORE UPDATE ON finance_connection FOR EACH ROW EXECUTE FUNCTION set_updated_at_bump_version();

CREATE TRIGGER trg_finance_customer_link_updated BEFORE UPDATE ON finance_customer_link FOR EACH ROW EXECUTE FUNCTION set_updated_at_bump_version();

CREATE TRIGGER trg_finance_external_customer_updated BEFORE UPDATE ON finance_external_customer FOR EACH ROW EXECUTE FUNCTION set_updated_at_bump_version();

CREATE TRIGGER trg_finance_invoice_updated BEFORE UPDATE ON finance_invoice FOR EACH ROW EXECUTE FUNCTION set_updated_at_bump_version();

CREATE TRIGGER trg_finance_payment_updated BEFORE UPDATE ON finance_payment FOR EACH ROW EXECUTE FUNCTION set_updated_at_bump_version();

CREATE TRIGGER trg_lead_disqualify_reason_updated BEFORE UPDATE ON lead_disqualify_reason FOR EACH ROW EXECUTE FUNCTION set_updated_at_bump_version();

CREATE TRIGGER trg_lead_source_updated BEFORE UPDATE ON lead_source FOR EACH ROW EXECUTE FUNCTION set_updated_at_bump_version();

CREATE TRIGGER trg_lead_updated BEFORE UPDATE ON lead FOR EACH ROW EXECUTE FUNCTION set_updated_at_bump_version();

CREATE TRIGGER trg_list_updated BEFORE UPDATE ON list FOR EACH ROW EXECUTE FUNCTION set_updated_at_bump_version();

CREATE TRIGGER trg_offer_template_updated BEFORE UPDATE ON offer_template FOR EACH ROW EXECUTE FUNCTION set_updated_at_bump_version();

CREATE TRIGGER trg_offer_updated BEFORE UPDATE ON offer FOR EACH ROW EXECUTE FUNCTION set_updated_at_bump_version();

CREATE TRIGGER trg_oli_updated BEFORE UPDATE ON offer_line_item FOR EACH ROW EXECUTE FUNCTION set_updated_at_bump_version();

CREATE TRIGGER trg_organization_domain_updated BEFORE UPDATE ON organization_domain FOR EACH ROW EXECUTE FUNCTION set_updated_at_bump_version();

CREATE TRIGGER trg_organization_fact_updated BEFORE UPDATE ON organization_fact FOR EACH ROW EXECUTE FUNCTION set_updated_at_bump_version();

CREATE TRIGGER trg_organization_geocode_stale BEFORE UPDATE OF address_line1, address_line2, address_city, address_region, address_postal_code, address_country ON organization FOR EACH ROW WHEN (((old.address_line1 IS DISTINCT FROM new.address_line1) OR (old.address_line2 IS DISTINCT FROM new.address_line2) OR (old.address_city IS DISTINCT FROM new.address_city) OR (old.address_region IS DISTINCT FROM new.address_region) OR (old.address_postal_code IS DISTINCT FROM new.address_postal_code) OR (old.address_country IS DISTINCT FROM new.address_country))) EXECUTE FUNCTION organization_geocode_goes_stale();

CREATE TRIGGER trg_organization_no_cycle BEFORE INSERT OR UPDATE OF parent_org_id ON organization FOR EACH ROW WHEN ((new.parent_org_id IS NOT NULL)) EXECUTE FUNCTION organization_no_ancestor_cycle();

CREATE TRIGGER trg_organization_profile_field_updated BEFORE UPDATE ON organization_profile_field FOR EACH ROW EXECUTE FUNCTION set_updated_at_bump_version();

CREATE TRIGGER trg_organization_relationship_type_updated BEFORE UPDATE ON organization_relationship_type FOR EACH ROW EXECUTE FUNCTION set_updated_at_bump_version();

CREATE TRIGGER trg_organization_updated BEFORE UPDATE ON organization FOR EACH ROW EXECUTE FUNCTION set_updated_at_bump_version();

CREATE TRIGGER trg_partner_updated BEFORE UPDATE ON partner FOR EACH ROW EXECUTE FUNCTION set_updated_at_bump_version();

CREATE TRIGGER trg_person_channel_identity_updated BEFORE UPDATE ON person_channel_identity FOR EACH ROW EXECUTE FUNCTION set_updated_at_bump_version();

CREATE TRIGGER trg_person_email_updated BEFORE UPDATE ON person_email FOR EACH ROW EXECUTE FUNCTION set_updated_at_bump_version();

CREATE TRIGGER trg_person_phone_updated BEFORE UPDATE ON person_phone FOR EACH ROW EXECUTE FUNCTION set_updated_at_bump_version();

CREATE TRIGGER trg_person_profile_field_updated BEFORE UPDATE ON person_profile_field FOR EACH ROW EXECUTE FUNCTION set_updated_at_bump_version();

CREATE TRIGGER trg_person_updated BEFORE UPDATE ON person FOR EACH ROW EXECUTE FUNCTION set_updated_at_bump_version();

CREATE TRIGGER trg_pipeline_updated BEFORE UPDATE ON pipeline FOR EACH ROW EXECUTE FUNCTION set_updated_at_bump_version();

CREATE TRIGGER trg_product_updated BEFORE UPDATE ON product FOR EACH ROW EXECUTE FUNCTION set_updated_at_bump_version();

CREATE TRIGGER trg_project_updated BEFORE UPDATE ON project FOR EACH ROW EXECUTE FUNCTION set_updated_at_bump_version();

CREATE TRIGGER trg_provider_connection_updated BEFORE UPDATE ON provider_connection FOR EACH ROW EXECUTE FUNCTION set_updated_at_bump_version();

CREATE TRIGGER trg_provider_run_updated BEFORE UPDATE ON provider_run FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TRIGGER trg_quota_updated BEFORE UPDATE ON quota FOR EACH ROW EXECUTE FUNCTION set_updated_at_bump_version();

CREATE TRIGGER trg_relationship_updated BEFORE UPDATE ON relationship FOR EACH ROW EXECUTE FUNCTION set_updated_at_bump_version();

CREATE TRIGGER trg_role_assignment_updated BEFORE UPDATE ON role_assignment FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TRIGGER trg_role_updated BEFORE UPDATE ON role FOR EACH ROW EXECUTE FUNCTION set_updated_at_bump_version();

CREATE TRIGGER trg_saved_view_updated BEFORE UPDATE ON saved_view FOR EACH ROW EXECUTE FUNCTION set_updated_at_bump_version();

CREATE TRIGGER trg_signal_updated BEFORE UPDATE ON signal FOR EACH ROW EXECUTE FUNCTION set_updated_at_bump_version();

CREATE TRIGGER trg_stage_updated BEFORE UPDATE ON stage FOR EACH ROW EXECUTE FUNCTION set_updated_at_bump_version();

CREATE TRIGGER trg_system_log_no_mutate BEFORE DELETE OR UPDATE ON system_log FOR EACH ROW EXECUTE FUNCTION system_log_immutable();

CREATE TRIGGER trg_tag_updated BEFORE UPDATE ON tag FOR EACH ROW EXECUTE FUNCTION set_updated_at_bump_version();

CREATE TRIGGER trg_team_membership_updated BEFORE UPDATE ON team_membership FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TRIGGER trg_team_updated BEFORE UPDATE ON team FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TRIGGER trg_webhook_delivery_updated BEFORE UPDATE ON webhook_delivery FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TRIGGER trg_webhook_subscription_updated BEFORE UPDATE ON webhook_subscription FOR EACH ROW EXECUTE FUNCTION set_updated_at_bump_version();

CREATE TRIGGER trg_workspace_updated BEFORE UPDATE ON workspace FOR EACH ROW EXECUTE FUNCTION set_updated_at();

ALTER TABLE activity
    ADD CONSTRAINT activity_assignee_id_fkey FOREIGN KEY (assignee_id) REFERENCES app_user(id) ON DELETE SET NULL (assignee_id);

ALTER TABLE activity_audience_member
    ADD CONSTRAINT activity_audience_member_activity_id_fkey FOREIGN KEY (activity_id) REFERENCES activity(id) ON DELETE CASCADE;

ALTER TABLE activity
    ADD CONSTRAINT activity_channel_provider_fkey FOREIGN KEY (channel_provider) REFERENCES channel_provider(provider);

ALTER TABLE activity
    ADD CONSTRAINT activity_host_user_id_fkey FOREIGN KEY (host_user_id) REFERENCES app_user(id) ON DELETE SET NULL (host_user_id);

ALTER TABLE activity
    ADD CONSTRAINT activity_kind_fkey FOREIGN KEY (kind) REFERENCES activity_kind(kind);

ALTER TABLE activity_link
    ADD CONSTRAINT activity_link_activity_id_fkey FOREIGN KEY (activity_id) REFERENCES activity(id) ON DELETE CASCADE;

ALTER TABLE activity_link
    ADD CONSTRAINT activity_link_deal_id_fkey FOREIGN KEY (deal_id) REFERENCES deal(id) ON DELETE CASCADE;

ALTER TABLE activity_link
    ADD CONSTRAINT activity_link_lead_id_fkey FOREIGN KEY (lead_id) REFERENCES lead(id) ON DELETE CASCADE;

ALTER TABLE activity_link
    ADD CONSTRAINT activity_link_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES organization(id) ON DELETE CASCADE;

ALTER TABLE activity_link
    ADD CONSTRAINT activity_link_person_id_fkey FOREIGN KEY (person_id) REFERENCES person(id) ON DELETE CASCADE;

ALTER TABLE activity_link
    ADD CONSTRAINT activity_link_project_id_fkey FOREIGN KEY (project_id) REFERENCES project(id) ON DELETE CASCADE;

ALTER TABLE activity_participant
    ADD CONSTRAINT activity_participant_activity_fkey FOREIGN KEY (activity_id) REFERENCES activity(id) ON DELETE CASCADE;

ALTER TABLE activity_participant
    ADD CONSTRAINT activity_participant_person_fkey FOREIGN KEY (person_id) REFERENCES person(id) ON DELETE CASCADE;

ALTER TABLE activity_participant_replay
    ADD CONSTRAINT activity_participant_replay_activity_fkey FOREIGN KEY (activity_id) REFERENCES activity(id) ON DELETE CASCADE;

ALTER TABLE activity_participant
    ADD CONSTRAINT activity_participant_user_fkey FOREIGN KEY (user_id) REFERENCES app_user(id) ON DELETE CASCADE;

ALTER TABLE activity_retention_evidence
    ADD CONSTRAINT activity_retention_evidence_activity_id_fkey FOREIGN KEY (activity_id) REFERENCES activity(id) ON DELETE CASCADE;

ALTER TABLE activity_retention_evidence
    ADD CONSTRAINT activity_retention_evidence_deal_id_fkey FOREIGN KEY (deal_id) REFERENCES deal(id) ON DELETE SET NULL;

ALTER TABLE activity_retention_evidence
    ADD CONSTRAINT activity_retention_evidence_decided_by_fkey FOREIGN KEY (decided_by) REFERENCES app_user(id) ON DELETE SET NULL;

ALTER TABLE agent_run
    ADD CONSTRAINT agent_run_approval_fkey FOREIGN KEY (approval_id) REFERENCES approval(id) ON DELETE SET NULL (approval_id);

ALTER TABLE agent_run
    ADD CONSTRAINT agent_run_passport_fkey FOREIGN KEY (passport_id) REFERENCES passport(id) ON DELETE SET NULL (passport_id);

ALTER TABLE agent_task
    ADD CONSTRAINT agent_task_approval_fk FOREIGN KEY (approval_id) REFERENCES approval(id) ON DELETE RESTRICT;

ALTER TABLE agent_task
    ADD CONSTRAINT agent_task_passport_fk FOREIGN KEY (passport_id) REFERENCES passport(id) ON DELETE RESTRICT;

ALTER TABLE ai_call
    ADD CONSTRAINT ai_call_config_fk FOREIGN KEY (config_hash) REFERENCES ai_call_config(hash);

ALTER TABLE ai_call_payload
    ADD CONSTRAINT ai_call_payload_ai_call_fkey FOREIGN KEY (ai_call_id) REFERENCES ai_call(id) ON DELETE CASCADE;

ALTER TABLE approval
    ADD CONSTRAINT approval_decided_by_fkey FOREIGN KEY (decided_by) REFERENCES app_user(id) ON DELETE SET NULL (decided_by);

ALTER TABLE approval
    ADD CONSTRAINT approval_on_behalf_of_fkey FOREIGN KEY (on_behalf_of) REFERENCES app_user(id) ON DELETE SET NULL (on_behalf_of);

ALTER TABLE approval
    ADD CONSTRAINT approval_passport_id_fkey FOREIGN KEY (passport_id) REFERENCES passport(id) ON DELETE SET NULL (passport_id);

ALTER TABLE attachment
    ADD CONSTRAINT attachment_contract_id_fkey FOREIGN KEY (contract_id) REFERENCES contract(id) ON DELETE SET NULL;

ALTER TABLE attachment_extraction
    ADD CONSTRAINT attachment_extraction_attachment_id_fkey FOREIGN KEY (attachment_id) REFERENCES attachment(id) ON DELETE CASCADE;

ALTER TABLE attachment
    ADD CONSTRAINT attachment_supersedes_fkey FOREIGN KEY (supersedes_id) REFERENCES attachment(id) ON DELETE SET NULL (supersedes_id);

ALTER TABLE audit_log
    ADD CONSTRAINT audit_log_on_behalf_of_fkey FOREIGN KEY (on_behalf_of) REFERENCES app_user(id) ON DELETE SET NULL (on_behalf_of);

ALTER TABLE audit_log
    ADD CONSTRAINT audit_log_workspace_id_fkey FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;

ALTER TABLE auth_token
    ADD CONSTRAINT auth_token_user_fkey FOREIGN KEY (user_id) REFERENCES app_user(id) ON DELETE CASCADE;

ALTER TABLE automation
    ADD CONSTRAINT automation_owner_fkey FOREIGN KEY (owner_id) REFERENCES app_user(id) ON DELETE SET NULL (owner_id);

ALTER TABLE booking_page
    ADD CONSTRAINT booking_page_host_fkey FOREIGN KEY (host_user_id) REFERENCES app_user(id) ON DELETE CASCADE;

ALTER TABLE brief_item
    ADD CONSTRAINT brief_item_deal_fkey FOREIGN KEY (deal_id) REFERENCES deal(id) ON DELETE CASCADE;

ALTER TABLE brief_item
    ADD CONSTRAINT brief_item_run_fkey FOREIGN KEY (brief_run_id) REFERENCES brief_run(id) ON DELETE CASCADE;

ALTER TABLE brief_run
    ADD CONSTRAINT brief_run_user_fkey FOREIGN KEY (user_id) REFERENCES app_user(id) ON DELETE CASCADE;

ALTER TABLE capture_auto_enrich_state
    ADD CONSTRAINT capture_auto_enrich_state_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES organization(id) ON DELETE CASCADE;

ALTER TABLE capture_backfill
    ADD CONSTRAINT capture_backfill_connection_fkey FOREIGN KEY (connection_id) REFERENCES capture_connection(id) ON DELETE CASCADE;

ALTER TABLE capture_connection
    ADD CONSTRAINT capture_connection_user_id_fkey FOREIGN KEY (user_id) REFERENCES app_user(id) ON DELETE CASCADE;

ALTER TABLE capture_digest
    ADD CONSTRAINT capture_digest_user_id_fkey FOREIGN KEY (user_id) REFERENCES app_user(id) ON DELETE CASCADE;

ALTER TABLE capture_exclusion
    ADD CONSTRAINT capture_exclusion_user_id_fkey FOREIGN KEY (user_id) REFERENCES app_user(id) ON DELETE CASCADE;

ALTER TABLE capture_freemail_domain
    ADD CONSTRAINT capture_freemail_domain_created_by_fkey FOREIGN KEY (created_by) REFERENCES app_user(id) ON DELETE SET NULL (created_by);

ALTER TABLE capture_pending_counterparty
    ADD CONSTRAINT capture_pending_counterparty_activity_id_fkey FOREIGN KEY (activity_id) REFERENCES activity(id) ON DELETE CASCADE;

ALTER TABLE capture_pending_counterparty
    ADD CONSTRAINT capture_pending_counterparty_owner_id_fkey FOREIGN KEY (owner_id) REFERENCES app_user(id) ON DELETE CASCADE;

ALTER TABLE capture_pending_counterparty
    ADD CONSTRAINT capture_pending_counterparty_proposal_id_fkey FOREIGN KEY (proposal_id) REFERENCES approval(id) ON DELETE SET NULL;

ALTER TABLE capture_sync_state
    ADD CONSTRAINT capture_sync_state_connection_id_fkey FOREIGN KEY (connection_id) REFERENCES capture_connection(id) ON DELETE CASCADE;

ALTER TABLE channel_connection
    ADD CONSTRAINT channel_connection_connected_by_fkey FOREIGN KEY (connected_by) REFERENCES app_user(id) ON DELETE RESTRICT;

ALTER TABLE commission_entry
    ADD CONSTRAINT commission_entry_deal_id_fkey FOREIGN KEY (deal_id) REFERENCES deal(id) ON DELETE CASCADE;

ALTER TABLE commission_entry
    ADD CONSTRAINT commission_entry_partner_org_id_fkey FOREIGN KEY (partner_org_id) REFERENCES organization(id) ON DELETE CASCADE;

ALTER TABLE commission_entry
    ADD CONSTRAINT commission_entry_reversal_of_fkey FOREIGN KEY (reversal_of) REFERENCES commission_entry(id) ON DELETE RESTRICT;

ALTER TABLE comms_outbound
    ADD CONSTRAINT comms_outbound_activity_id_fkey FOREIGN KEY (activity_id) REFERENCES activity(id) ON DELETE CASCADE;

ALTER TABLE comms_outbound
    ADD CONSTRAINT comms_outbound_user_id_fkey FOREIGN KEY (user_id) REFERENCES app_user(id) ON DELETE RESTRICT;

ALTER TABLE consent_doi_token
    ADD CONSTRAINT consent_doi_token_person_id_fkey FOREIGN KEY (person_id) REFERENCES person(id) ON DELETE CASCADE;

ALTER TABLE consent_doi_token
    ADD CONSTRAINT consent_doi_token_purpose_id_fkey FOREIGN KEY (purpose_id) REFERENCES consent_purpose(id) ON DELETE RESTRICT;

ALTER TABLE consent_event
    ADD CONSTRAINT consent_event_lead_id_fkey FOREIGN KEY (lead_id) REFERENCES lead(id);

ALTER TABLE consent_event
    ADD CONSTRAINT consent_event_person_id_fkey FOREIGN KEY (person_id) REFERENCES person(id) ON DELETE CASCADE;

ALTER TABLE consent_event
    ADD CONSTRAINT consent_event_purpose_id_fkey FOREIGN KEY (purpose_id) REFERENCES consent_purpose(id) ON DELETE RESTRICT;

ALTER TABLE consent_existing_customer_flag
    ADD CONSTRAINT consent_existing_customer_person_fkey FOREIGN KEY (person_id) REFERENCES person(id) ON DELETE CASCADE;

ALTER TABLE consent_existing_customer_flag
    ADD CONSTRAINT consent_existing_customer_setter_fkey FOREIGN KEY (set_by_user_id) REFERENCES app_user(id) ON DELETE SET NULL;

ALTER TABLE consent_qualifying_event
    ADD CONSTRAINT consent_qualifying_event_person_fkey FOREIGN KEY (person_id) REFERENCES person(id) ON DELETE CASCADE;

ALTER TABLE contract
    ADD CONSTRAINT contract_deal_id_fkey FOREIGN KEY (deal_id) REFERENCES deal(id) ON DELETE SET NULL;

ALTER TABLE contract
    ADD CONSTRAINT contract_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES organization(id) ON DELETE RESTRICT;

ALTER TABLE contract
    ADD CONSTRAINT contract_project_id_fkey FOREIGN KEY (project_id) REFERENCES project(id) ON DELETE SET NULL;

ALTER TABLE contract
    ADD CONSTRAINT contract_superseded_by_id_fkey FOREIGN KEY (superseded_by_id) REFERENCES contract(id) ON DELETE SET NULL;

ALTER TABLE conversation_claim
    ADD CONSTRAINT conversation_claim_activity_fkey FOREIGN KEY (source_activity_id) REFERENCES activity(id) ON DELETE CASCADE;

ALTER TABLE conversation_claim
    ADD CONSTRAINT conversation_claim_corrector_fkey FOREIGN KEY (corrected_by_user_id) REFERENCES app_user(id) ON DELETE SET NULL;

ALTER TABLE conversation_claim
    ADD CONSTRAINT conversation_claim_person_fkey FOREIGN KEY (person_id) REFERENCES person(id) ON DELETE CASCADE;

ALTER TABLE conversation_claim
    ADD CONSTRAINT conversation_claim_task_fkey FOREIGN KEY (task_activity_id) REFERENCES activity(id) ON DELETE SET NULL;

ALTER TABLE custom_field
    ADD CONSTRAINT custom_field_created_by_fkey FOREIGN KEY (created_by) REFERENCES app_user(id) ON DELETE RESTRICT;

ALTER TABLE deal_forecast_history
    ADD CONSTRAINT deal_forecast_history_deal_id_fkey FOREIGN KEY (deal_id) REFERENCES deal(id) ON DELETE CASCADE;

ALTER TABLE deal
    ADD CONSTRAINT deal_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES organization(id) ON DELETE SET NULL (organization_id);

ALTER TABLE deal
    ADD CONSTRAINT deal_owner_id_fkey FOREIGN KEY (owner_id) REFERENCES app_user(id) ON DELETE SET NULL (owner_id);

ALTER TABLE deal
    ADD CONSTRAINT deal_partner_org_id_fkey FOREIGN KEY (partner_org_id) REFERENCES organization(id) ON DELETE SET NULL (partner_org_id);

ALTER TABLE deal
    ADD CONSTRAINT deal_pipeline_id_fkey FOREIGN KEY (pipeline_id) REFERENCES pipeline(id) ON DELETE RESTRICT;

ALTER TABLE deal
    ADD CONSTRAINT deal_project_id_fkey FOREIGN KEY (project_id) REFERENCES project(id) ON DELETE SET NULL (project_id);

ALTER TABLE deal_stage_history
    ADD CONSTRAINT deal_stage_history_deal_id_fkey FOREIGN KEY (deal_id) REFERENCES deal(id) ON DELETE CASCADE;

ALTER TABLE deal_stage_history
    ADD CONSTRAINT deal_stage_history_from_stage_id_fkey FOREIGN KEY (from_stage_id) REFERENCES stage(id) ON DELETE SET NULL (from_stage_id);

ALTER TABLE deal_stage_history
    ADD CONSTRAINT deal_stage_history_to_stage_id_fkey FOREIGN KEY (to_stage_id) REFERENCES stage(id) ON DELETE RESTRICT;

ALTER TABLE deal
    ADD CONSTRAINT deal_stage_id_fkey FOREIGN KEY (stage_id) REFERENCES stage(id) ON DELETE RESTRICT;

ALTER TABLE deal
    ADD CONSTRAINT deal_stage_in_pipeline FOREIGN KEY (stage_id, pipeline_id) REFERENCES stage(id, pipeline_id);

ALTER TABLE dedupe_candidate
    ADD CONSTRAINT dedupe_candidate_disposed_by_fkey FOREIGN KEY (disposed_by) REFERENCES app_user(id) ON DELETE SET NULL (disposed_by);

ALTER TABLE dedupe_candidate
    ADD CONSTRAINT dedupe_candidate_left_lead_id_fkey FOREIGN KEY (left_lead_id) REFERENCES lead(id) ON DELETE CASCADE;

ALTER TABLE dedupe_candidate
    ADD CONSTRAINT dedupe_candidate_left_org_id_fkey FOREIGN KEY (left_org_id) REFERENCES organization(id) ON DELETE CASCADE;

ALTER TABLE dedupe_candidate
    ADD CONSTRAINT dedupe_candidate_left_person_id_fkey FOREIGN KEY (left_person_id) REFERENCES person(id) ON DELETE CASCADE;

ALTER TABLE dedupe_candidate
    ADD CONSTRAINT dedupe_candidate_right_lead_id_fkey FOREIGN KEY (right_lead_id) REFERENCES lead(id) ON DELETE CASCADE;

ALTER TABLE dedupe_candidate
    ADD CONSTRAINT dedupe_candidate_right_org_id_fkey FOREIGN KEY (right_org_id) REFERENCES organization(id) ON DELETE CASCADE;

ALTER TABLE dedupe_candidate
    ADD CONSTRAINT dedupe_candidate_right_person_id_fkey FOREIGN KEY (right_person_id) REFERENCES person(id) ON DELETE CASCADE;

ALTER TABLE data_subject_request
    ADD CONSTRAINT dsr_assignee_fkey FOREIGN KEY (assignee_id) REFERENCES app_user(id) ON DELETE SET NULL (assignee_id);

ALTER TABLE email_signature
    ADD CONSTRAINT email_signature_owner_id_fkey FOREIGN KEY (owner_id) REFERENCES app_user(id) ON DELETE CASCADE;

ALTER TABLE extension_secret
    ADD CONSTRAINT extension_secret_workspace_id_user_id_fkey FOREIGN KEY (user_id) REFERENCES app_user(id) ON DELETE CASCADE;

ALTER TABLE finance_customer_link
    ADD CONSTRAINT finance_customer_link_connection_fk FOREIGN KEY (connection_id) REFERENCES finance_connection(id) ON DELETE RESTRICT;

ALTER TABLE finance_customer_link
    ADD CONSTRAINT finance_customer_link_organization_fk FOREIGN KEY (organization_id) REFERENCES organization(id) ON DELETE RESTRICT;

ALTER TABLE finance_external_customer
    ADD CONSTRAINT finance_external_customer_connection_fk FOREIGN KEY (connection_id) REFERENCES finance_connection(id) ON DELETE RESTRICT;

ALTER TABLE finance_invoice
    ADD CONSTRAINT finance_invoice_connection_fk FOREIGN KEY (connection_id) REFERENCES finance_connection(id) ON DELETE RESTRICT;

ALTER TABLE finance_invoice
    ADD CONSTRAINT finance_invoice_credits_fk FOREIGN KEY (credits_invoice_id) REFERENCES finance_invoice(id) ON DELETE RESTRICT;

ALTER TABLE finance_invoice
    ADD CONSTRAINT finance_invoice_organization_fk FOREIGN KEY (organization_id) REFERENCES organization(id) ON DELETE RESTRICT;

ALTER TABLE finance_payment
    ADD CONSTRAINT finance_payment_connection_fk FOREIGN KEY (connection_id) REFERENCES finance_connection(id) ON DELETE RESTRICT;

ALTER TABLE finance_payment
    ADD CONSTRAINT finance_payment_invoice_fk FOREIGN KEY (invoice_id) REFERENCES finance_invoice(id) ON DELETE RESTRICT;

ALTER TABLE finance_payment
    ADD CONSTRAINT finance_payment_organization_fk FOREIGN KEY (organization_id) REFERENCES organization(id) ON DELETE RESTRICT;

ALTER TABLE graph_interaction_edge
    ADD CONSTRAINT graph_interaction_edge_workspace_id_person_id_fkey FOREIGN KEY (person_id) REFERENCES person(id) ON DELETE CASCADE;

ALTER TABLE graph_interaction_edge
    ADD CONSTRAINT graph_interaction_edge_workspace_id_user_id_fkey FOREIGN KEY (user_id) REFERENCES app_user(id) ON DELETE CASCADE;

ALTER TABLE lead
    ADD CONSTRAINT lead_disqualify_reason_id_fkey FOREIGN KEY (disqualify_reason_id) REFERENCES lead_disqualify_reason(id) ON DELETE RESTRICT;

ALTER TABLE lead_manual_signal
    ADD CONSTRAINT lead_manual_signal_lead_id_fkey FOREIGN KEY (lead_id) REFERENCES lead(id) ON DELETE CASCADE;

ALTER TABLE lead_manual_signal
    ADD CONSTRAINT lead_manual_signal_set_by_fkey FOREIGN KEY (set_by) REFERENCES app_user(id);

ALTER TABLE lead
    ADD CONSTRAINT lead_merged_into_id_fkey FOREIGN KEY (merged_into_id) REFERENCES lead(id) ON DELETE SET NULL (merged_into_id);

ALTER TABLE lead
    ADD CONSTRAINT lead_owner_id_fkey FOREIGN KEY (owner_id) REFERENCES app_user(id) ON DELETE SET NULL (owner_id);

ALTER TABLE lead
    ADD CONSTRAINT lead_project_id_fkey FOREIGN KEY (project_id) REFERENCES project(id) ON DELETE SET NULL (project_id);

ALTER TABLE lead
    ADD CONSTRAINT lead_promoted_person_id_fkey FOREIGN KEY (promoted_person_id) REFERENCES person(id) ON DELETE SET NULL (promoted_person_id);

ALTER TABLE lead
    ADD CONSTRAINT lead_qualified_deal_id_fkey FOREIGN KEY (qualified_deal_id) REFERENCES deal(id) ON DELETE SET NULL;

ALTER TABLE lead_score_history
    ADD CONSTRAINT lead_score_history_lead_id_fkey FOREIGN KEY (lead_id) REFERENCES lead(id) ON DELETE CASCADE;

ALTER TABLE linkedin_account
    ADD CONSTRAINT linkedin_account_workspace_id_user_id_fkey FOREIGN KEY (user_id) REFERENCES app_user(id) ON DELETE CASCADE;

ALTER TABLE linkedin_connection
    ADD CONSTRAINT linkedin_connection_workspace_id_matched_org_id_fkey FOREIGN KEY (matched_org_id) REFERENCES organization(id) ON DELETE SET NULL (matched_org_id);

ALTER TABLE linkedin_connection
    ADD CONSTRAINT linkedin_connection_workspace_id_matched_person_id_fkey FOREIGN KEY (matched_person_id) REFERENCES person(id) ON DELETE CASCADE;

ALTER TABLE linkedin_connection
    ADD CONSTRAINT linkedin_connection_workspace_id_owner_user_id_fkey FOREIGN KEY (owner_user_id) REFERENCES app_user(id) ON DELETE CASCADE;

ALTER TABLE list_member
    ADD CONSTRAINT list_member_list_id_fkey FOREIGN KEY (list_id) REFERENCES list(id) ON DELETE CASCADE;

ALTER TABLE list
    ADD CONSTRAINT list_owner_id_fkey FOREIGN KEY (owner_id) REFERENCES app_user(id) ON DELETE SET NULL (owner_id);

ALTER TABLE list
    ADD CONSTRAINT list_team_id_fkey FOREIGN KEY (team_id) REFERENCES team(id) ON DELETE SET NULL (team_id);

ALTER TABLE oauth_authorization_code
    ADD CONSTRAINT oauth_code_lent_passport_fkey FOREIGN KEY (lent_passport_id) REFERENCES passport(id) ON DELETE SET NULL (lent_passport_id);

ALTER TABLE oauth_authorization_code
    ADD CONSTRAINT oauth_code_user_fkey FOREIGN KEY (user_id) REFERENCES app_user(id) ON DELETE CASCADE;

ALTER TABLE oauth_grant
    ADD CONSTRAINT oauth_grant_client_fkey FOREIGN KEY (client_id) REFERENCES oauth_client(client_id) ON DELETE RESTRICT;

ALTER TABLE oauth_grant
    ADD CONSTRAINT oauth_grant_lent_passport_fkey FOREIGN KEY (lent_passport_id) REFERENCES passport(id) ON DELETE SET NULL (lent_passport_id);

ALTER TABLE oauth_grant
    ADD CONSTRAINT oauth_grant_user_fkey FOREIGN KEY (user_id) REFERENCES app_user(id) ON DELETE CASCADE;

ALTER TABLE oauth_refresh_token
    ADD CONSTRAINT oauth_refresh_grant_fkey FOREIGN KEY (grant_id) REFERENCES oauth_grant(id) ON DELETE CASCADE;

ALTER TABLE offer
    ADD CONSTRAINT offer_buyer_org_fkey FOREIGN KEY (buyer_org_id) REFERENCES organization(id) ON DELETE SET NULL (buyer_org_id);

ALTER TABLE offer
    ADD CONSTRAINT offer_deal_fkey FOREIGN KEY (deal_id) REFERENCES deal(id) ON DELETE RESTRICT;

ALTER TABLE offer
    ADD CONSTRAINT offer_template_id_fkey FOREIGN KEY (template_id) REFERENCES offer_template(id) ON DELETE SET NULL (template_id);

ALTER TABLE offer_line_item
    ADD CONSTRAINT oli_offer_fkey FOREIGN KEY (offer_id) REFERENCES offer(id) ON DELETE CASCADE;

ALTER TABLE offer_line_item
    ADD CONSTRAINT oli_product_fkey FOREIGN KEY (product_id) REFERENCES product(id) ON DELETE SET NULL (product_id);

ALTER TABLE onboarding_wizard_state
    ADD CONSTRAINT onboarding_wizard_state_read_fkey FOREIGN KEY (site_read_id) REFERENCES site_read(id) ON DELETE SET NULL (site_read_id);

ALTER TABLE onboarding_wizard_state
    ADD CONSTRAINT onboarding_wizard_state_user_fkey FOREIGN KEY (user_id) REFERENCES app_user(id) ON DELETE CASCADE;

ALTER TABLE org_brief
    ADD CONSTRAINT org_brief_org_fkey FOREIGN KEY (organization_id) REFERENCES organization(id) ON DELETE CASCADE;

ALTER TABLE org_brief
    ADD CONSTRAINT org_brief_user_id_fkey FOREIGN KEY (user_id) REFERENCES app_user(id) ON DELETE CASCADE;

ALTER TABLE org_dossier
    ADD CONSTRAINT org_dossier_org_fkey FOREIGN KEY (organization_id) REFERENCES organization(id) ON DELETE CASCADE;

ALTER TABLE org_dossier
    ADD CONSTRAINT org_dossier_user_fkey FOREIGN KEY (user_id) REFERENCES app_user(id) ON DELETE CASCADE;

ALTER TABLE organization_fact
    ADD CONSTRAINT org_fact_org_fkey FOREIGN KEY (organization_id) REFERENCES organization(id) ON DELETE CASCADE;

ALTER TABLE organization_fact
    ADD CONSTRAINT org_fact_site_read_fkey FOREIGN KEY (site_read_id) REFERENCES site_read(id) ON DELETE SET NULL (site_read_id);

ALTER TABLE org_growth_fit
    ADD CONSTRAINT org_growth_fit_org_fkey FOREIGN KEY (organization_id) REFERENCES organization(id) ON DELETE CASCADE;

ALTER TABLE org_growth_fit
    ADD CONSTRAINT org_growth_fit_user_fkey FOREIGN KEY (user_id) REFERENCES app_user(id) ON DELETE CASCADE;

ALTER TABLE organization_profile_field
    ADD CONSTRAINT org_profile_field_org_fkey FOREIGN KEY (organization_id) REFERENCES organization(id) ON DELETE CASCADE;

ALTER TABLE organization_domain_disposition
    ADD CONSTRAINT organization_domain_disposition_org_fkey FOREIGN KEY (organization_id) REFERENCES organization(id) ON DELETE CASCADE;

ALTER TABLE organization_domain_disposition
    ADD CONSTRAINT organization_domain_disposition_owner_fkey FOREIGN KEY (owner_id) REFERENCES app_user(id) ON DELETE SET NULL (owner_id);

ALTER TABLE organization_domain_disposition
    ADD CONSTRAINT organization_domain_disposition_site_read_fkey FOREIGN KEY (site_read_id) REFERENCES site_read(id) ON DELETE SET NULL (site_read_id);

ALTER TABLE organization_domain
    ADD CONSTRAINT organization_domain_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES organization(id) ON DELETE CASCADE;

ALTER TABLE organization_geocode_state
    ADD CONSTRAINT organization_geocode_state_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES organization(id) ON DELETE CASCADE;

ALTER TABLE organization
    ADD CONSTRAINT organization_merged_into_id_fkey FOREIGN KEY (merged_into_id) REFERENCES organization(id) ON DELETE SET NULL (merged_into_id);

ALTER TABLE organization
    ADD CONSTRAINT organization_owner_id_fkey FOREIGN KEY (owner_id) REFERENCES app_user(id) ON DELETE SET NULL (owner_id);

ALTER TABLE organization
    ADD CONSTRAINT organization_parent_org_id_fkey FOREIGN KEY (parent_org_id) REFERENCES organization(id) ON DELETE SET NULL (parent_org_id);

ALTER TABLE organization_relationship_type
    ADD CONSTRAINT organization_relationship_typ_workspace_id_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES organization(id) ON DELETE CASCADE;

ALTER TABLE partner
    ADD CONSTRAINT partner_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES organization(id) ON DELETE CASCADE;

ALTER TABLE passport
    ADD CONSTRAINT passport_grant_fkey FOREIGN KEY (oauth_grant_id) REFERENCES oauth_grant(id) ON DELETE RESTRICT;

ALTER TABLE passport
    ADD CONSTRAINT passport_granted_by_fkey FOREIGN KEY (granted_by) REFERENCES app_user(id) ON DELETE RESTRICT;

ALTER TABLE passport
    ADD CONSTRAINT passport_on_behalf_of_fkey FOREIGN KEY (on_behalf_of) REFERENCES app_user(id) ON DELETE CASCADE;

ALTER TABLE person_brief
    ADD CONSTRAINT person_brief_person_fkey FOREIGN KEY (person_id) REFERENCES person(id) ON DELETE CASCADE;

ALTER TABLE person_brief
    ADD CONSTRAINT person_brief_user_fkey FOREIGN KEY (user_id) REFERENCES app_user(id) ON DELETE CASCADE;

ALTER TABLE person_channel_identity
    ADD CONSTRAINT person_channel_identity_person_id_fkey FOREIGN KEY (person_id) REFERENCES person(id) ON DELETE CASCADE;

ALTER TABLE person_channel_identity
    ADD CONSTRAINT person_channel_identity_provider_fkey FOREIGN KEY (provider) REFERENCES channel_provider(provider);

ALTER TABLE person_consent
    ADD CONSTRAINT person_consent_lead_id_fkey FOREIGN KEY (lead_id) REFERENCES lead(id) ON DELETE CASCADE;

ALTER TABLE person_consent
    ADD CONSTRAINT person_consent_person_id_fkey FOREIGN KEY (person_id) REFERENCES person(id) ON DELETE CASCADE;

ALTER TABLE person_consent
    ADD CONSTRAINT person_consent_purpose_id_fkey FOREIGN KEY (purpose_id) REFERENCES consent_purpose(id) ON DELETE RESTRICT;

ALTER TABLE person
    ADD CONSTRAINT person_converted_from_lead_id_fkey FOREIGN KEY (converted_from_lead_id) REFERENCES lead(id) ON DELETE SET NULL (converted_from_lead_id);

ALTER TABLE person_email
    ADD CONSTRAINT person_email_person_id_fkey FOREIGN KEY (person_id) REFERENCES person(id) ON DELETE CASCADE;

ALTER TABLE person
    ADD CONSTRAINT person_merged_into_id_fkey FOREIGN KEY (merged_into_id) REFERENCES person(id) ON DELETE SET NULL (merged_into_id);

ALTER TABLE person_moment_dismissal
    ADD CONSTRAINT person_moment_dismissal_person_fkey FOREIGN KEY (person_id) REFERENCES person(id) ON DELETE CASCADE;

ALTER TABLE person_moment_dismissal
    ADD CONSTRAINT person_moment_dismissal_user_fkey FOREIGN KEY (user_id) REFERENCES app_user(id) ON DELETE CASCADE;

ALTER TABLE person
    ADD CONSTRAINT person_owner_id_fkey FOREIGN KEY (owner_id) REFERENCES app_user(id) ON DELETE SET NULL (owner_id);

ALTER TABLE person_phone
    ADD CONSTRAINT person_phone_person_id_fkey FOREIGN KEY (person_id) REFERENCES person(id) ON DELETE CASCADE;

ALTER TABLE person_profile_field
    ADD CONSTRAINT person_profile_field_person_fk FOREIGN KEY (person_id) REFERENCES person(id) ON DELETE CASCADE;

ALTER TABLE person_provider_claim
    ADD CONSTRAINT person_provider_claim_person_id_fkey FOREIGN KEY (person_id) REFERENCES person(id) ON DELETE CASCADE;

ALTER TABLE person_provider_claim
    ADD CONSTRAINT person_provider_claim_run_id_fkey FOREIGN KEY (run_id) REFERENCES provider_run(id) ON DELETE CASCADE;

ALTER TABLE person_signature_enrich_state
    ADD CONSTRAINT person_signature_enrich_state_activity_id_fkey FOREIGN KEY (activity_id) REFERENCES activity(id) ON DELETE CASCADE;

ALTER TABLE person_signature_enrich_state
    ADD CONSTRAINT person_signature_enrich_state_person_id_fkey FOREIGN KEY (person_id) REFERENCES person(id) ON DELETE CASCADE;

ALTER TABLE person_social
    ADD CONSTRAINT person_social_person_id_fkey FOREIGN KEY (person_id) REFERENCES person(id) ON DELETE CASCADE;

ALTER TABLE preference_token
    ADD CONSTRAINT preference_token_person_fkey FOREIGN KEY (person_id) REFERENCES person(id) ON DELETE CASCADE;

ALTER TABLE project
    ADD CONSTRAINT project_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES organization(id) ON DELETE RESTRICT;

ALTER TABLE project
    ADD CONSTRAINT project_owner_id_fkey FOREIGN KEY (owner_id) REFERENCES app_user(id) ON DELETE SET NULL (owner_id);

ALTER TABLE project_phase_history
    ADD CONSTRAINT project_phase_history_project_id_fkey FOREIGN KEY (project_id) REFERENCES project(id) ON DELETE CASCADE;

ALTER TABLE provider_connection_budget
    ADD CONSTRAINT provider_connection_budget_connection_id_fkey FOREIGN KEY (connection_id) REFERENCES provider_connection(id) ON DELETE CASCADE;

ALTER TABLE provider_connection
    ADD CONSTRAINT provider_connection_connected_by_fkey FOREIGN KEY (connected_by) REFERENCES app_user(id);

ALTER TABLE provider_run
    ADD CONSTRAINT provider_run_person_id_fkey FOREIGN KEY (person_id) REFERENCES person(id) ON DELETE CASCADE;

ALTER TABLE provider_run
    ADD CONSTRAINT provider_run_requested_by_fkey FOREIGN KEY (requested_by) REFERENCES app_user(id);

ALTER TABLE provider_run_reservation
    ADD CONSTRAINT provider_run_reservation_run_id_fkey FOREIGN KEY (run_id) REFERENCES provider_run(id) ON DELETE CASCADE;

ALTER TABLE quota
    ADD CONSTRAINT quota_owner_id_fkey FOREIGN KEY (owner_id) REFERENCES app_user(id) ON DELETE SET NULL (owner_id);

ALTER TABLE quota
    ADD CONSTRAINT quota_team_id_fkey FOREIGN KEY (team_id) REFERENCES team(id) ON DELETE SET NULL (team_id);

ALTER TABLE record_grant
    ADD CONSTRAINT record_grant_granted_by_fkey FOREIGN KEY (granted_by) REFERENCES app_user(id) ON DELETE RESTRICT;

ALTER TABLE relationship
    ADD CONSTRAINT relationship_counterparty_org_id_fkey FOREIGN KEY (counterparty_org_id) REFERENCES organization(id) ON DELETE CASCADE;

ALTER TABLE relationship
    ADD CONSTRAINT relationship_deal_id_fkey FOREIGN KEY (deal_id) REFERENCES deal(id) ON DELETE CASCADE;

ALTER TABLE relationship
    ADD CONSTRAINT relationship_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES organization(id) ON DELETE CASCADE;

ALTER TABLE relationship
    ADD CONSTRAINT relationship_person_id_fkey FOREIGN KEY (person_id) REFERENCES person(id) ON DELETE CASCADE;

ALTER TABLE relationship
    ADD CONSTRAINT relationship_project_id_fkey FOREIGN KEY (project_id) REFERENCES project(id) ON DELETE CASCADE;

ALTER TABLE role_assignment
    ADD CONSTRAINT role_assignment_role_id_fkey FOREIGN KEY (role_id) REFERENCES role(id) ON DELETE CASCADE;

ALTER TABLE role_assignment
    ADD CONSTRAINT role_assignment_team_id_fkey FOREIGN KEY (team_id) REFERENCES team(id) ON DELETE CASCADE;

ALTER TABLE role_assignment
    ADD CONSTRAINT role_assignment_user_id_fkey FOREIGN KEY (user_id) REFERENCES app_user(id) ON DELETE CASCADE;

ALTER TABLE runner_job
    ADD CONSTRAINT runner_job_passport_fkey FOREIGN KEY (passport_id) REFERENCES passport(id) ON DELETE SET NULL (passport_id);

ALTER TABLE runner_job
    ADD CONSTRAINT runner_job_run_fkey FOREIGN KEY (agent_run_id) REFERENCES agent_run(id) ON DELETE SET NULL (agent_run_id);

ALTER TABLE saved_view
    ADD CONSTRAINT saved_view_owner_fkey FOREIGN KEY (owner_id) REFERENCES app_user(id) ON DELETE CASCADE;

ALTER TABLE scheduled_send
    ADD CONSTRAINT scheduled_send_activity_id_fkey FOREIGN KEY (activity_id) REFERENCES activity(id) ON DELETE RESTRICT;

ALTER TABLE scheduled_send
    ADD CONSTRAINT scheduled_send_agent_on_behalf_of_fkey FOREIGN KEY (agent_on_behalf_of) REFERENCES app_user(id) ON DELETE SET NULL;

ALTER TABLE scheduled_send
    ADD CONSTRAINT scheduled_send_anchor_activity_id_fkey FOREIGN KEY (anchor_activity_id) REFERENCES activity(id) ON DELETE CASCADE;

ALTER TABLE scheduled_send
    ADD CONSTRAINT scheduled_send_delivery_id_fkey FOREIGN KEY (delivery_id) REFERENCES comms_outbound(id) ON DELETE RESTRICT;

ALTER TABLE scheduled_send
    ADD CONSTRAINT scheduled_send_scheduled_by_fkey FOREIGN KEY (scheduled_by) REFERENCES app_user(id) ON DELETE RESTRICT;

ALTER TABLE session
    ADD CONSTRAINT session_user_id_fkey FOREIGN KEY (user_id) REFERENCES app_user(id) ON DELETE CASCADE;

ALTER TABLE signal
    ADD CONSTRAINT signal_owner_fkey FOREIGN KEY (owner_id) REFERENCES app_user(id) ON DELETE SET NULL (owner_id);

ALTER TABLE signal
    ADD CONSTRAINT signal_resolved_org_fkey FOREIGN KEY (resolved_org_id) REFERENCES organization(id) ON DELETE SET NULL (resolved_org_id);

ALTER TABLE signal
    ADD CONSTRAINT signal_resolved_person_fkey FOREIGN KEY (resolved_person_id) REFERENCES person(id) ON DELETE SET NULL (resolved_person_id);

ALTER TABLE signal_thread_scan
    ADD CONSTRAINT signal_thread_scan_resolved_org_fkey FOREIGN KEY (resolved_org_id) REFERENCES organization(id) ON DELETE SET NULL (resolved_org_id);

ALTER TABLE signal_resolution
    ADD CONSTRAINT sigres_org_fkey FOREIGN KEY (matched_org_id) REFERENCES organization(id) ON DELETE SET NULL (matched_org_id);

ALTER TABLE signal_resolution
    ADD CONSTRAINT sigres_resolved_by_fkey FOREIGN KEY (resolved_by) REFERENCES app_user(id) ON DELETE SET NULL (resolved_by);

ALTER TABLE signal_resolution
    ADD CONSTRAINT sigres_signal_fkey FOREIGN KEY (signal_id) REFERENCES signal(id) ON DELETE CASCADE;

ALTER TABLE site_read
    ADD CONSTRAINT site_read_org_fkey FOREIGN KEY (organization_id) REFERENCES organization(id) ON DELETE CASCADE;

ALTER TABLE stage
    ADD CONSTRAINT stage_pipeline_id_fkey FOREIGN KEY (pipeline_id) REFERENCES pipeline(id) ON DELETE CASCADE;

ALTER TABLE suggestion_dismissal
    ADD CONSTRAINT suggestion_dismissal_org_fkey FOREIGN KEY (organization_id) REFERENCES organization(id) ON DELETE CASCADE;

ALTER TABLE suggestion_dismissal
    ADD CONSTRAINT suggestion_dismissal_user_id_fkey FOREIGN KEY (user_id) REFERENCES app_user(id) ON DELETE CASCADE;

ALTER TABLE system_log
    ADD CONSTRAINT system_log_on_behalf_of_fkey FOREIGN KEY (on_behalf_of) REFERENCES app_user(id) ON DELETE SET NULL (on_behalf_of);

ALTER TABLE system_log
    ADD CONSTRAINT system_log_workspace_id_fkey FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;

ALTER TABLE taggable
    ADD CONSTRAINT taggable_tag_id_fkey FOREIGN KEY (tag_id) REFERENCES tag(id) ON DELETE CASCADE;

ALTER TABLE team_membership
    ADD CONSTRAINT team_membership_team_id_fkey FOREIGN KEY (team_id) REFERENCES team(id) ON DELETE CASCADE;

ALTER TABLE team_membership
    ADD CONSTRAINT team_membership_user_id_fkey FOREIGN KEY (user_id) REFERENCES app_user(id) ON DELETE CASCADE;

ALTER TABLE team
    ADD CONSTRAINT team_parent_team_id_fkey FOREIGN KEY (parent_team_id) REFERENCES team(id) ON DELETE SET NULL (parent_team_id);

ALTER TABLE transcript_read
    ADD CONSTRAINT transcript_read_activity_id_fkey FOREIGN KEY (activity_id) REFERENCES activity(id) ON DELETE CASCADE;

ALTER TABLE user_record_view
    ADD CONSTRAINT user_record_view_user_id_fkey FOREIGN KEY (user_id) REFERENCES app_user(id) ON DELETE CASCADE;

ALTER TABLE voice_build
    ADD CONSTRAINT voice_build_profile_fkey FOREIGN KEY (voice_profile_id) REFERENCES voice_profile(id) ON DELETE CASCADE;

ALTER TABLE voice_build
    ADD CONSTRAINT voice_build_requester_fkey FOREIGN KEY (requested_by) REFERENCES app_user(id) ON DELETE SET NULL (requested_by);

ALTER TABLE voice_build
    ADD CONSTRAINT voice_build_result_version_fkey FOREIGN KEY (voice_profile_id, result_version) REFERENCES voice_profile_version(voice_profile_id, profile_version);

ALTER TABLE voice_corpus_source
    ADD CONSTRAINT voice_corpus_source_profile_fkey FOREIGN KEY (voice_profile_id) REFERENCES voice_profile(id) ON DELETE CASCADE;

ALTER TABLE voice_learning_signal
    ADD CONSTRAINT voice_learning_signal_profile_fkey FOREIGN KEY (voice_profile_id) REFERENCES voice_profile(id) ON DELETE CASCADE;

ALTER TABLE voice_learning_signal
    ADD CONSTRAINT voice_learning_signal_version_fkey FOREIGN KEY (voice_profile_id, profile_version) REFERENCES voice_profile_version(voice_profile_id, profile_version);

ALTER TABLE voice_profile_delta
    ADD CONSTRAINT voice_profile_delta_from_fkey FOREIGN KEY (voice_profile_id, from_version) REFERENCES voice_profile_version(voice_profile_id, profile_version);

ALTER TABLE voice_profile_delta
    ADD CONSTRAINT voice_profile_delta_profile_fkey FOREIGN KEY (voice_profile_id) REFERENCES voice_profile(id) ON DELETE CASCADE;

ALTER TABLE voice_profile_delta
    ADD CONSTRAINT voice_profile_delta_to_fkey FOREIGN KEY (voice_profile_id, to_version) REFERENCES voice_profile_version(voice_profile_id, profile_version);

ALTER TABLE voice_profile
    ADD CONSTRAINT voice_profile_owner_fkey FOREIGN KEY (owner_id) REFERENCES app_user(id) ON DELETE RESTRICT;

ALTER TABLE voice_profile
    ADD CONSTRAINT voice_profile_team_fkey FOREIGN KEY (team_id) REFERENCES team(id) ON DELETE RESTRICT;

ALTER TABLE voice_profile_version
    ADD CONSTRAINT voice_profile_version_predecessor_fkey FOREIGN KEY (voice_profile_id, predecessor_version) REFERENCES voice_profile_version(voice_profile_id, profile_version);

ALTER TABLE voice_profile_version
    ADD CONSTRAINT voice_profile_version_profile_fkey FOREIGN KEY (voice_profile_id) REFERENCES voice_profile(id) ON DELETE CASCADE;

ALTER TABLE webhook_delivery
    ADD CONSTRAINT webhook_delivery_subscription_fkey FOREIGN KEY (subscription_id) REFERENCES webhook_subscription(id) ON DELETE CASCADE;

ALTER TABLE webhook_subscription
    ADD CONSTRAINT webhook_subscription_owner_fkey FOREIGN KEY (owner_id) REFERENCES app_user(id) ON DELETE CASCADE;

DO $$
BEGIN
  IF EXISTS (SELECT FROM pg_roles WHERE rolname = 'margince_app') THEN
    GRANT USAGE ON SCHEMA ext TO margince_app;
    GRANT USAGE ON SCHEMA public TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE activity TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE activity_audience_member TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE activity_kind TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE activity_link TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE activity_participant TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE activity_participant_replay TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE activity_retention_evidence TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE agent_run TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE agent_task TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE ai_call TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE ai_call_config TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE ai_call_payload TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE ai_feedback TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE ai_model_rate TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE ai_usage TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE app_user TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE approval TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE attachment TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE attachment_extraction TO margince_app;
    GRANT SELECT,INSERT ON TABLE audit_log TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE auth_token TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE automation TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE booking_page TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE brief_item TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE brief_run TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE capture_auto_enrich_budget TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE capture_auto_enrich_state TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE capture_backfill TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE capture_connection TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE capture_digest TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE capture_exclusion TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE capture_freemail_domain TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE capture_pending_counterparty TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE capture_sync_state TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE capture_trace TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE channel_connection TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE channel_provider TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE commission_entry TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE comms_outbound TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE consent_doi_token TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE consent_event TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE consent_existing_customer_flag TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE consent_purpose TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE consent_qualifying_event TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE contract TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE conversation_claim TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE custom_field TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE data_subject_request TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE deal TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE deal_forecast_history TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE deal_stage_history TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE dedupe_candidate TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE email_signature TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE embed_store_binding TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE embedding TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE erasure_suppression TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE event_outbox TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE extension_secret TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE field_mask TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE field_provenance TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE finance_connection TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE finance_customer_link TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE finance_external_customer TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE finance_invoice TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE finance_payment TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE fx_rate TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE geocode_cache TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE graph_interaction_edge TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE idempotency_key TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE lead TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE lead_disqualify_reason TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE lead_manual_signal TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE lead_score_history TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE lead_source TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE linkedin_account TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE linkedin_connection TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE list TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE list_member TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE oauth_authorization_code TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE oauth_client TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE oauth_grant TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE oauth_refresh_token TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE offer TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE offer_line_item TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE offer_template TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE onboarding_wizard_state TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE org_brief TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE org_dossier TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE org_growth_fit TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE organization TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE organization_domain TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE organization_domain_disposition TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE organization_fact TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE organization_geocode_state TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE organization_open_pipeline_rollup TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE organization_profile_field TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE organization_relationship_type TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE partner TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE passport TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE person TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE person_brief TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE person_channel_identity TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE person_consent TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE person_email TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE person_moment_dismissal TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE person_phone TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE person_profile_field TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE person_provider_claim TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE person_signature_enrich_state TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE person_social TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE pipeline TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE preference_token TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE product TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE project TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE project_phase_history TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE provider_connection TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE provider_connection_budget TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE provider_run TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE provider_run_reservation TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE quota TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE raw_capture TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE record_grant TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE relationship TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE retention_policy TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE role TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE role_assignment TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE runner_job TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE saved_view TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE scheduled_send TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE session TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE setting TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE setup_token TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE signal TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE signal_resolution TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE signal_thread_scan TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE signing_key TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE site_read TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE stage TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE suggestion_dismissal TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE system_log TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE tag TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE taggable TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE team TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE team_membership TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE transcript_read TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE user_record_view TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE vault_secret TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE voice_build TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE voice_corpus_source TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE voice_learning_signal TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE voice_profile TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE voice_profile_delta TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE voice_profile_version TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE webhook_delivery TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE webhook_subscription TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE workflow_run TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE workspace TO margince_app;
    GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE workspace_email_domain TO margince_app;
    ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT,INSERT,DELETE,UPDATE ON TABLES TO margince_app;


    --
    -- PostgreSQL database dump complete
    --
  END IF;
END $$;

INSERT INTO activity_kind (kind) VALUES ('email');

INSERT INTO activity_kind (kind) VALUES ('call');

INSERT INTO activity_kind (kind) VALUES ('meeting');

INSERT INTO activity_kind (kind) VALUES ('note');

INSERT INTO activity_kind (kind) VALUES ('task');

INSERT INTO activity_kind (kind) VALUES ('message');

INSERT INTO channel_provider (provider, transport, label, credential_model, supplies_transport) VALUES ('telegram', 'core', 'Telegram', 'workspace_bot', true);

INSERT INTO channel_provider (provider, transport, label, credential_model, supplies_transport) VALUES ('whatsapp', 'core', 'WhatsApp', 'workspace_bot', false);

INSERT INTO lead_disqualify_reason (label, sort_order, active, system, version) VALUES ('Not a good fit', 10, true, true, 1);

INSERT INTO lead_disqualify_reason (label, sort_order, active, system, version) VALUES ('Bad timing', 20, true, true, 1);

INSERT INTO lead_disqualify_reason (label, sort_order, active, system, version) VALUES ('No budget', 30, true, true, 1);

INSERT INTO lead_disqualify_reason (label, sort_order, active, system, version) VALUES ('No decision power', 40, true, true, 1);

INSERT INTO lead_disqualify_reason (label, sort_order, active, system, version) VALUES ('Chose a competitor', 50, true, true, 1);

INSERT INTO lead_disqualify_reason (label, sort_order, active, system, version) VALUES ('No interest', 60, true, true, 1);

INSERT INTO lead_disqualify_reason (label, sort_order, active, system, version) VALUES ('Not reachable', 70, true, true, 1);

INSERT INTO lead_disqualify_reason (label, sort_order, active, system, version) VALUES ('Duplicate or spam', 80, true, true, 1);

INSERT INTO field_mask (role_key, object, field, condition) VALUES ('rep', 'deal', 'amount_minor', 'outside_write_authority');

INSERT INTO lead_source (key, label, intent, sort_order, active, system, version) VALUES ('manual', 'Created manually', 'neutral', 10, true, true, 1);

INSERT INTO lead_source (key, label, intent, sort_order, active, system, version) VALUES ('inbound', 'Inbound', 'high', 20, true, true, 1);

INSERT INTO lead_source (key, label, intent, sort_order, active, system, version) VALUES ('webform', 'Web form', 'high', 30, true, true, 1);

INSERT INTO lead_source (key, label, intent, sort_order, active, system, version) VALUES ('referral', 'Referral', 'high', 40, true, true, 1);

INSERT INTO lead_source (key, label, intent, sort_order, active, system, version) VALUES ('import', 'Import', 'low', 50, true, true, 1);

INSERT INTO lead_source (key, label, intent, sort_order, active, system, version) VALUES ('crawl', 'Crawl', 'low', 60, true, true, 1);

SELECT pg_catalog.setval('public.event_outbox_seq_seq', 1, false);

