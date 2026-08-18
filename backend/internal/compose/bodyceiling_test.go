// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gradionhq/margince/backend/internal/platform/deployconfig"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
)

// The security property, stated once: a body's ceiling is decided by the route
// it is addressed to, never by what the sender says the body is.
//
// The version of this that shipped chose on Content-Type alone, which handed
// the file bound to every route in the product. Several decode `r.Body` with
// no bound of their own — `/oauth/register` and `/v1/auth/login` among them,
// both unauthenticated — so a one-header lie was a 25x memory amplification on
// an anonymous endpoint. `TestALyingContentTypeBuysNothing` is that attack.
//
// The declaration gates — that the ceiling table matches the routes which
// actually carry files — live in bodyceilingcensus_test.go.

// testLimits are deliberately three DIFFERENT numbers, none of them a shipped
// default. A test that gives every route the same ceiling passes just as
// happily against a table that hands every route the same one, and a table
// wired to the wrong field is the likelier mistake than one wired to nothing.
var testLimits = deployconfig.UploadLimits{
	Attachment:     31_000_000,
	CSVImport:      12_000_000,
	LinkedInImport: 7_000_000,
}

func ceilingFor(t *testing.T, method, path, contentType string) int64 {
	t.Helper()
	req := httptest.NewRequest(method, path, http.NoBody)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	return bodyCeilingFor(uploadCeilings(testLimits))(req)
}

func TestEachUploadRouteRidesItsOwnConfiguredCeiling(t *testing.T) {
	for path, want := range map[string]int64{
		"/v1/attachments":             testLimits.Attachment,
		"/v1/imports/sources":         testLimits.CSVImport,
		"/v1/me/linkedin-connections": testLimits.LinkedInImport,
	} {
		got := ceilingFor(t, http.MethodPost, path,
			"multipart/form-data; boundary=----WebKitFormBoundary7MA4YWxkTrZu0gW")
		if got != want {
			t.Errorf("%s rode ceiling %d, want its own configured %d — a route "+
				"reading under a ceiling that is not the one it was granted "+
				"refuses files the operator meant it to accept", path, got, want)
		}
	}
}

func TestALyingContentTypeBuysNothing(t *testing.T) {
	// The routes the redteam reached: unauthenticated, and each decodes r.Body
	// with no bound of its own. None of them may be widened by asking.
	for _, path := range []string{
		"/oauth/register",
		"/v1/auth/login",
		"/v1/deals",
		"/v1/organizations",
		"/mcp",
	} {
		got := ceilingFor(t, http.MethodPost, path,
			"multipart/form-data; boundary=x")
		if got != httperr.MaxBodyBytes {
			t.Errorf("%s was widened to %d by a Content-Type header — a route "+
				"that carries no file must not be able to ask for the file bound",
				path, got)
		}
	}
}

func TestAnUploadRouteCarryingJSONStaysTight(t *testing.T) {
	// The route is declared, the body is not a file. Both conditions are
	// required, so this rides the tight bound.
	for _, contentType := range []string{
		"application/json",
		"",                                 // absent
		"multipart/form-datax; boundary=x", // the prefix-match hole
		"multipart/mixed; boundary=x",
		"multipart/form-data; boundary", // malformed parameters
	} {
		got := ceilingFor(t, http.MethodPost, "/v1/attachments", contentType)
		if got != httperr.MaxBodyBytes {
			t.Errorf("/v1/attachments with Content-Type %q rode %d, want the "+
				"JSON ceiling %d", contentType, got, httperr.MaxBodyBytes)
		}
	}
}

func TestOnlyPOSTCarriesAFile(t *testing.T) {
	for _, method := range []string{
		http.MethodGet, http.MethodPatch, http.MethodPut, http.MethodDelete,
	} {
		got := ceilingFor(t, method, "/v1/attachments",
			"multipart/form-data; boundary=x")
		if got != httperr.MaxBodyBytes {
			t.Errorf("%s /v1/attachments rode %d, want the JSON ceiling %d",
				method, got, httperr.MaxBodyBytes)
		}
	}
}

func TestAPathTheRouterDoesNotServeIsNotAnUploadRoute(t *testing.T) {
	// Normalizing here would be guessing at how the router resolves a path it
	// does not mount, and a guess that lands wide is a grant nobody wrote.
	for _, path := range []string{
		"/v1/attachments/",
		"/v1/attachments//",
		"//v1/attachments",
		"/v1/ATTACHMENTS",
	} {
		got := ceilingFor(t, http.MethodPost, path, "multipart/form-data; boundary=x")
		if got != httperr.MaxBodyBytes {
			t.Errorf("%q rode the file ceiling %d — only the exact path the "+
				"router mounts carries a file", path, got)
		}
	}
}
