// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package migrations

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/gradionhq/margince/backend/internal/platform/dbmigrate"
)

func TestVoiceProfileLifecycleUpgradePreservesBuiltProfile(t *testing.T) {
	ownerDSN, _ := dsns(t)
	conn := connect(t, ownerDSN)
	resetSchema(t, conn)
	ctx := context.Background()

	core := migrateToBeforeVoiceLifecycle(t, conn)
	workspaceID := seedWorkspace(t, conn, "pre-voice-lifecycle")
	var ownerID string
	if err := conn.QueryRow(ctx, `
		INSERT INTO app_user (workspace_id, email, display_name)
		VALUES ($1, 'voice-owner@pre-lifecycle.test', 'Voice Owner')
		RETURNING id`, workspaceID).Scan(&ownerID); err != nil {
		t.Fatalf("seeding voice owner: %v", err)
	}

	profileBytes := []byte("# Voice DNA\n\nPrecise punctuation — and café-grade UTF-8.\n")
	var profileID string
	if err := conn.QueryRow(ctx, `
		INSERT INTO voice_profile
		  (workspace_id, owner_id, scope, model_ref, status, voice_profile_md,
		   profile_version, personality_md)
		VALUES ($1, $2, 'user', 'anthropic:legacy-builder', 'ready', $3, 7,
		        'Human-authored personality remains separate.')
		RETURNING id`, workspaceID, ownerID, string(profileBytes)).Scan(&profileID); err != nil {
		t.Fatalf("seeding built voice profile: %v", err)
	}

	corpusContent := "A legacy post with an em dash — and Grüße from the owner."
	if _, err := conn.Exec(ctx, `
		INSERT INTO voice_corpus_source
		  (workspace_id, voice_profile_id, kind, register, weight, source_label,
		   source_ref, content, word_count, excluded)
		VALUES ($1, $2, 'post', 'written', 1.5, 'Legacy LinkedIn post',
		        'linkedin:legacy-42', $3, 12, false)`, workspaceID, profileID, corpusContent); err != nil {
		t.Fatalf("seeding legacy corpus source: %v", err)
	}

	if _, err := dbmigrate.Up(ctx, conn, core); err != nil {
		t.Fatalf("applying 0107 over legacy Voice data: %v", err)
	}

	contentDigest := sha256.Sum256([]byte(corpusContent))
	wantContentHash := "sha256:" + fmt.Sprintf("%x", contentDigest)
	sourceDigest := md5.Sum([]byte(wantContentHash))
	wantSourceHash := fmt.Sprintf("%x", sourceDigest)

	assertMigratedVoiceCorpus(t, conn, profileID, wantContentHash)
	assertMigratedVoiceProfile(t, conn, profileID, ownerID, wantSourceHash)
	assertMigratedVoiceVersion(t, conn, profileID, profileBytes, wantSourceHash)
	assertVoiceOwnerDeleteRestricted(t, conn, ownerID)
}

func migrateToBeforeVoiceLifecycle(t *testing.T, conn *pgx.Conn) dbmigrate.Namespace {
	t.Helper()
	core, err := Core()
	if err != nil {
		t.Fatalf("loading core migrations: %v", err)
	}
	voiceLifecycleIndex := -1
	for i, migration := range core.Migrations {
		if migration.Version == "0107" {
			voiceLifecycleIndex = i
			break
		}
	}
	if voiceLifecycleIndex < 0 {
		t.Fatal("core migrations contain no 0107 — the Voice lifecycle migration is missing")
	}

	before := dbmigrate.Namespace{Name: core.Name, Migrations: core.Migrations[:voiceLifecycleIndex]}
	if _, err := dbmigrate.Up(context.Background(), conn, before); err != nil {
		t.Fatalf("migrating to pre-0107: %v", err)
	}
	return core
}

func assertMigratedVoiceCorpus(t *testing.T, conn *pgx.Conn, profileID, wantContentHash string) {
	t.Helper()
	var contentHash, kind, register, source, capturedBy string
	if err := conn.QueryRow(context.Background(), `
		SELECT content_hash, kind, register, source, captured_by
		FROM voice_corpus_source
		WHERE voice_profile_id = $1`, profileID).
		Scan(&contentHash, &kind, &register, &source, &capturedBy); err != nil {
		t.Fatalf("reading migrated corpus source: %v", err)
	}
	if contentHash != wantContentHash {
		t.Errorf("content_hash = %q, want %q", contentHash, wantContentHash)
	}
	if kind != "linkedin" || register != "social" {
		t.Errorf("translated corpus vocabulary = (%q, %q), want (linkedin, social)", kind, register)
	}
	if source != "ui" || capturedBy != "system" {
		t.Errorf("corpus provenance = (%q, %q), want (ui, system)", source, capturedBy)
	}
}

func assertMigratedVoiceProfile(t *testing.T, conn *pgx.Conn, profileID, ownerID, wantSourceHash string) {
	t.Helper()
	var gotOwnerID, status, sourceHash, source, capturedBy string
	var profileVersion int
	var builtAtPresent bool
	if err := conn.QueryRow(context.Background(), `
		SELECT owner_id, status, profile_version, active_source_hash, source,
		       captured_by, last_built_at IS NOT NULL
		FROM voice_profile
		WHERE id = $1`, profileID).
		Scan(&gotOwnerID, &status, &profileVersion, &sourceHash, &source, &capturedBy, &builtAtPresent); err != nil {
		t.Fatalf("reading migrated voice profile: %v", err)
	}
	if gotOwnerID != ownerID {
		t.Errorf("owner_id = %q, want preserved owner %q", gotOwnerID, ownerID)
	}
	if status != "ready" || profileVersion != 7 {
		t.Errorf("active profile state = (%q, %d), want (ready, 7)", status, profileVersion)
	}
	if sourceHash != wantSourceHash {
		t.Errorf("active_source_hash = %q, want %q", sourceHash, wantSourceHash)
	}
	if source != "ui" || capturedBy != "system" || !builtAtPresent {
		t.Errorf("profile backfill = source %q, captured_by %q, built_at %v; want ui, system, true", source, capturedBy, builtAtPresent)
	}
}

func assertMigratedVoiceVersion(t *testing.T, conn *pgx.Conn, profileID string, wantBytes []byte, wantSourceHash string) {
	t.Helper()
	var gotBytes []byte
	var status, sourceHash, source, capturedBy string
	var profileVersion, sourceCount int
	if err := conn.QueryRow(context.Background(), `
		SELECT convert_to(voice_profile_md, 'UTF8'), profile_version, status,
		       source_hash, source_count, source, captured_by
		FROM voice_profile_version
		WHERE voice_profile_id = $1`, profileID).
		Scan(&gotBytes, &profileVersion, &status, &sourceHash, &sourceCount, &source, &capturedBy); err != nil {
		t.Fatalf("reading migrated immutable voice version: %v", err)
	}
	if !bytes.Equal(gotBytes, wantBytes) {
		t.Errorf("immutable version bytes = %q, want exact legacy bytes %q", gotBytes, wantBytes)
	}
	if profileVersion != 7 || status != "active" {
		t.Errorf("immutable version state = (%d, %q), want (7, active)", profileVersion, status)
	}
	if sourceHash != wantSourceHash || sourceCount != 1 {
		t.Errorf("immutable version source snapshot = (%q, %d), want (%q, 1)", sourceHash, sourceCount, wantSourceHash)
	}
	if source != "ui" || capturedBy != "system" {
		t.Errorf("immutable version provenance = (%q, %q), want (ui, system)", source, capturedBy)
	}
}

func assertVoiceOwnerDeleteRestricted(t *testing.T, conn *pgx.Conn, ownerID string) {
	t.Helper()
	_, err := conn.Exec(context.Background(), `DELETE FROM app_user WHERE id = $1`, ownerID)
	if err == nil {
		t.Fatal("deleting the owner of a live Voice profile succeeded; the owner FK must restrict deletion")
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("deleting Voice owner returned %T, want a Postgres FK violation", err)
	}
	if pgErr.Code != "23503" || pgErr.ConstraintName != "voice_profile_owner_fkey" {
		t.Errorf("deleting Voice owner failed with (%s, %q), want (23503, voice_profile_owner_fkey)", pgErr.Code, pgErr.ConstraintName)
	}
}
