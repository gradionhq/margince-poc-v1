// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package webread

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFetchReturnsMarkdownVerbatimWhenServed(t *testing.T) {
	const md = "# Prices\n\n| model | in | out |\n| --- | --- | --- |\n| opus | 15 | 75 |\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if !strings.Contains(r.Header.Get("Accept"), "text/markdown") {
			t.Errorf("Fetch Accept = %q, want it to include text/markdown", r.Header.Get("Accept"))
		}
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		//craft:ignore swallowed-errors httptest handler write; a failed write fails the test through the assertions below
		_, _ = io.WriteString(w, md)
	}))
	defer srv.Close()

	doc, err := testFetcher().Fetch(context.Background(), srv.URL+"/pricing")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !doc.IsMarkdown() {
		t.Errorf("IsMarkdown() = false, want true (MediaType %q)", doc.MediaType)
	}
	if doc.Text != md {
		t.Errorf("markdown was altered:\n got %q\nwant %q", doc.Text, md)
	}
	if !strings.Contains(doc.Text, "| --- |") {
		t.Error("markdown table structure did not survive verbatim return")
	}
}

func TestFetchStripsHTMLWhenNoMarkdown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		//craft:ignore swallowed-errors httptest handler write; a failed write fails the test through the assertions below
		_, _ = io.WriteString(w, "<html><body>  Hello   <b>world</b> </body></html>")
	}))
	defer srv.Close()

	doc, err := testFetcher().Fetch(context.Background(), srv.URL+"/p")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if doc.IsMarkdown() {
		t.Error("IsMarkdown() = true for a text/html response")
	}
	if doc.Text != "Hello world" {
		t.Errorf("Text = %q, want %q", doc.Text, "Hello world")
	}
}

func TestFetchToleratesAMalformedContentType(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "not a media type ///")
		//craft:ignore swallowed-errors httptest handler write; a failed write fails the test through the assertions below
		_, _ = io.WriteString(w, "<p>hi there</p>")
	}))
	defer srv.Close()

	doc, err := testFetcher().Fetch(context.Background(), srv.URL+"/x")
	if err != nil {
		t.Fatalf("Fetch errored on a malformed Content-Type: %v", err)
	}
	if doc.IsMarkdown() {
		t.Error("a malformed Content-Type was treated as markdown")
	}
	if doc.Text != "hi there" {
		t.Errorf("Text = %q, want %q", doc.Text, "hi there")
	}
}

func TestFetchPageRequestsHTMLNotMarkdownAndHarvestsLinks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if strings.Contains(r.Header.Get("Accept"), "text/markdown") {
			t.Errorf("FetchPage sent Accept %q; the crawler must request HTML only", r.Header.Get("Accept"))
		}
		w.Header().Set("Content-Type", "text/html")
		//craft:ignore swallowed-errors httptest handler write; a failed write fails the test through the assertions below
		_, _ = io.WriteString(w, `<html><body><a href="/next">n</a> hi</body></html>`)
	}))
	defer srv.Close()

	page, err := testFetcher().FetchPage(context.Background(), srv.URL+"/")
	if err != nil {
		t.Fatalf("FetchPage: %v", err)
	}
	if page.Text != "n hi" {
		t.Errorf("Text = %q, want %q", page.Text, "n hi")
	}
	if len(page.Links) != 1 || !strings.HasSuffix(page.Links[0], "/next") {
		t.Errorf("Links = %v, want one link ending in /next", page.Links)
	}
}
