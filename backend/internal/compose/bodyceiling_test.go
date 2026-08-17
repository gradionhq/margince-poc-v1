// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/platform/httperr"
)

// The security property, stated once: a body's ceiling is decided by the route
// it is addressed to, never by what the sender says the body is.
//
// The version of this that shipped chose on Content-Type alone, which handed
// the 25 MiB bound to every route in the product. Several decode `r.Body` with
// no bound of their own — `/oauth/register` and `/v1/auth/login` among them,
// both unauthenticated — so a one-header lie was a 25x memory amplification on
// an anonymous endpoint. `TestALyingContentTypeBuysNothing` is that attack.

func ceilingFor(t *testing.T, method, path, contentType string) int64 {
	t.Helper()
	req := httptest.NewRequest(method, path, http.NoBody)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	return bodyCeiling(req)
}

func TestAnUploadRouteGetsTheWideCeiling(t *testing.T) {
	for path := range fileUploadRoutes {
		got := ceilingFor(t, http.MethodPost, path,
			"multipart/form-data; boundary=----WebKitFormBoundary7MA4YWxkTrZu0gW")
		if got != httperr.MaxMultipartBodyBytes {
			t.Errorf("%s rode ceiling %d, want the multipart ceiling %d — its "+
				"declared cap cannot run", path, got, httperr.MaxMultipartBodyBytes)
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

// multipartParse finds every handler that parses a multipart body.
var multipartParse = regexp.MustCompile(`ParseMultipartForm\(`)

// TestEveryMultipartRouteIsDeclared derives the obligation from the tree rather
// than restating it: a handler that parses a multipart body needs its route in
// `fileUploadRoutes`, or the parse runs under the 1 MiB bound and whatever cap
// it declares is dead. A hand-kept list would pass while the next upload route
// silently ran at the wrong ceiling — which is the bug this all came from.
func TestEveryMultipartRouteIsDeclared(t *testing.T) {
	root := filepath.Join("..", "..")
	var parsers []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") ||
			strings.HasSuffix(path, "_test.go") {
			return nil
		}
		source, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if multipartParse.Match(source) {
			parsers = append(parsers, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking the backend tree: %v", err)
	}
	if len(parsers) == 0 {
		t.Fatal("found no multipart handlers at all — the walk is broken, and a " +
			"walk that finds nothing passes this test for the wrong reason")
	}
	if len(parsers) != len(fileUploadRoutes) {
		t.Errorf("%d handler(s) parse a multipart body but %d route(s) are "+
			"declared in fileUploadRoutes: %v\n"+
			"An undeclared upload route parses under the %d-byte JSON bound, so "+
			"the cap it declares never runs.",
			len(parsers), len(fileUploadRoutes), parsers, httperr.MaxBodyBytes)
	}
}
