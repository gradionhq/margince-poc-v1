// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package integrations

// The typed refusals this module raises. Each names the offending field so the
// surface can point at it, which is what apperrors.FieldFault exists for — a
// generic 422 tells a customer their configuration was wrong without telling
// them which part.

// MissingAPIKeyError maps to 422: connecting needs a key to verify.
type MissingAPIKeyError struct{}

func (e *MissingAPIKeyError) Error() string { return "an API key is required to connect a provider" }

func (e *MissingAPIKeyError) FieldFault() (field, code, message string) {
	return "api_key", "api_key_required", e.Error()
}

// InvalidModeError maps to 422: the trigger mode is a closed vocabulary.
type InvalidModeError struct{ Mode string }

func (e *InvalidModeError) Error() string {
	return "mode must be automatic_on_create or on_demand, not " + e.Mode
}

func (e *InvalidModeError) FieldFault() (field, code, message string) {
	return "configuration.mode", "invalid_mode", e.Error()
}

// UnsellableSelectionError maps to 422: the saved categories name something
// this provider does not offer. JSON Schema cannot catch it — the vocabulary
// belongs to the provider, so the contract admits any string map.
type UnsellableSelectionError struct{ Reason string }

func (e *UnsellableSelectionError) Error() string { return e.Reason }

func (e *UnsellableSelectionError) FieldFault() (field, code, message string) {
	return "configuration.categories", "unsellable_selection", e.Error()
}

// UnknownPoolError maps to 422: a budget names a credit pool the provider
// does not meter.
type UnknownPoolError struct {
	Provider string
	Pool     string
}

func (e *UnknownPoolError) Error() string {
	return "provider " + e.Provider + " has no credit pool named " + e.Pool
}

func (e *UnknownPoolError) FieldFault() (field, code, message string) {
	return "configuration.budgets", "unknown_pool", e.Error()
}

// VerificationFailedError maps to 422: the provider refused the credential.
// Nothing is stored when this is returned — not the key, not a row, not an
// audit image (PI-AC-1). The message names the call that failed but never the
// provider's own body, which could carry a fragment of the key we just sent.
type VerificationFailedError struct {
	Provider string
	Call     string
}

func (e *VerificationFailedError) Error() string {
	if e.Call == "" {
		return "the provider rejected that credential"
	}
	return "the provider rejected that credential (" + e.Call + ")"
}

func (e *VerificationFailedError) FieldFault() (field, code, message string) {
	return "api_key", "verification_failed", e.Error()
}
