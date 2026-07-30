// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package webread

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

func TestHeadAssetExtraction(t *testing.T) {
	base, err := url.Parse("https://acme.example/de/start")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name      string
		html      string
		wantOG    string
		wantIcons []IconRef
	}{
		{
			name:   "og:image and icons resolve against the page URL",
			html:   `<meta property="og:image" content="/img/share.png"><link rel="icon" href="favicon.png" sizes="32x32">`,
			wantOG: "https://acme.example/img/share.png",
			wantIcons: []IconRef{
				{URL: "https://acme.example/de/favicon.png", Rel: RelIcon, Sizes: "32x32"},
			},
		},
		{
			name:   "og:image spelled in name= is still og:image",
			html:   `<meta name="og:image" content="https://cdn.example/mark.png">`,
			wantOG: "https://cdn.example/mark.png",
		},
		{
			name:   "the first og:image wins over later per-locale repeats",
			html:   `<meta property="og:image" content="/a.png"><meta property="og:image" content="/b.png">`,
			wantOG: "https://acme.example/a.png",
		},
		{
			name: "a rel token list is read token by token",
			html: `<link rel="shortcut icon" href="/f.ico"><link rel="apple-touch-icon-precomposed" href="/t.png" sizes="180x180">`,
			wantIcons: []IconRef{
				{URL: "https://acme.example/f.ico", Rel: RelIcon},
				{URL: "https://acme.example/t.png", Rel: RelAppleTouchIcon, Sizes: "180x180"},
			},
		},
		{
			name: "a mask-icon is not the company's mark",
			html: `<link rel="mask-icon" href="/pinned.svg" color="#000"><link rel="icon" href="/real.png">`,
			wantIcons: []IconRef{
				{URL: "https://acme.example/real.png", Rel: RelIcon},
			},
		},
		{
			name: "an empty or unresolvable reference declares no asset",
			html: `<link rel="icon" href=""><meta property="og:image" content="  ">` +
				`<link rel="icon" href="data:image/png;base64,AAAA"><link rel="icon" href="/kept.png">`,
			wantIcons: []IconRef{
				{URL: "https://acme.example/kept.png", Rel: RelIcon},
			},
		},
		{
			name: "the same icon declared twice collapses",
			html: `<link rel="icon" href="/f.png"><link rel="icon" href="/f.png" sizes="64x64">`,
			wantIcons: []IconRef{
				{URL: "https://acme.example/f.png", Rel: RelIcon},
			},
		},
		{
			name: "a page declaring nothing yields nothing",
			html: `<html><body><a href="/x">x</a><img src="/logo.png"></body></html>`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotOG, gotIcons := extractHeadAssets(tc.html, base)
			if gotOG != tc.wantOG {
				t.Fatalf("og:image = %q, want %q", gotOG, tc.wantOG)
			}
			if len(gotIcons) == 0 && len(tc.wantIcons) == 0 {
				return
			}
			if !reflect.DeepEqual(gotIcons, tc.wantIcons) {
				t.Fatalf("icons = %+v, want %+v", gotIcons, tc.wantIcons)
			}
		})
	}
}

func TestFetchPageCarriesTheDeclaredVisualIdentity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<head><meta property="og:image" content="/share.png">` +
			`<link rel="apple-touch-icon" href="/touch.png" sizes="180x180"></head><body>Acme</body>`))
	}))
	defer server.Close()

	page, err := newFetcher(server.Client().Transport).FetchPage(context.Background(), server.URL+"/")
	if err != nil {
		t.Fatalf("FetchPage: %v", err)
	}
	if page.OGImage != server.URL+"/share.png" {
		t.Fatalf("OGImage = %q", page.OGImage)
	}
	want := []IconRef{{URL: server.URL + "/touch.png", Rel: RelAppleTouchIcon, Sizes: "180x180"}}
	if !reflect.DeepEqual(page.Icons, want) {
		t.Fatalf("Icons = %+v, want %+v", page.Icons, want)
	}
}

func TestFetchAssetReturnsTheRawBytesAndTheDeclaredType(t *testing.T) {
	body := []byte{0x89, 'P', 'N', 'G', 1, 2, 3}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if accept := r.Header.Get("Accept"); accept != acceptImage {
			t.Errorf("Accept = %q, want %q", accept, acceptImage)
		}
		if agent := r.Header.Get("User-Agent"); agent != UserAgent {
			t.Errorf("User-Agent = %q, want the named bot", agent)
		}
		w.Header().Set("Content-Type", "image/png; charset=binary")
		_, _ = w.Write(body)
	}))
	defer server.Close()

	got, mediaType, err := newFetcher(server.Client().Transport).FetchAsset(context.Background(), server.URL+"/logo.png")
	if err != nil {
		t.Fatalf("FetchAsset: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("body = %v, want %v", got, body)
	}
	if mediaType != "image/png" {
		t.Fatalf("media type = %q, want image/png", mediaType)
	}
}

func TestFetchAssetRefusesAnAssetOverTheCapRatherThanTruncatingIt(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(bytes.Repeat([]byte{0x41}, maxAssetBytes+64))
	}))
	defer server.Close()

	_, _, err := newFetcher(server.Client().Transport).FetchAsset(context.Background(), server.URL+"/huge.png")
	if err == nil {
		t.Fatal("an asset over the cap must be refused, not silently truncated")
	}
	if !strings.Contains(err.Error(), "cap") {
		t.Fatalf("the error must name the cap it exceeded, got %v", err)
	}
}

func TestFetchAssetHonoursTheStatusAndTheRobotsGate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/robots.txt":
			_, _ = w.Write([]byte("User-agent: *\nDisallow: /private/\n"))
		case "/missing.png":
			w.WriteHeader(http.StatusNotFound)
		default:
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write([]byte{0x89, 'P', 'N', 'G'})
		}
	}))
	defer server.Close()
	fetcher := newFetcher(server.Client().Transport)

	if _, _, err := fetcher.FetchAsset(context.Background(), server.URL+"/missing.png"); err == nil {
		t.Fatal("an asset a page named but the server does not have must be an error")
	}
	_, _, err := fetcher.FetchAsset(context.Background(), server.URL+"/private/logo.png")
	if !errors.Is(err, ErrRobotsDisallowed) {
		t.Fatalf("a disallowed asset path must report the site's answer, got %v", err)
	}
}
