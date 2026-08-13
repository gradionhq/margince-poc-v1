// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package deployconfig

import (
	"fmt"
	"os"
	"strings"
)

// LicenseTokenEnvVar overrides license.token_file when it is set. It is the
// same variable name the bundled validation module reads its own token from, so
// a container that already exports the license needs no configuration file at
// all.
const LicenseTokenEnvVar = "MARGINCE_LICENSE"

// License points at the installation's entitlement token. The token is a file
// reference, never an inline value: it is a credential, and this file is
// routinely read, copied and pasted into a support thread.
//
// An installation with no license section runs unlicensed — every development
// and CI process in this repository does.
type License struct {
	TokenFile string `yaml:"token_file"`
}

// TokenLimit bounds a token file. A license is a JWT — a few hundred bytes, a
// few thousand at the outside — and everything downstream copies it whole: into
// a process environment, into a WebAssembly module's linear memory, and into
// whatever the module quotes back on refusal. A pointed-at-the-wrong-file
// mistake (a log, an image) must fail as one rather than being carried.
const TokenLimit = 64 << 10

// Token resolves the license token: the environment variable when set,
// otherwise the file reference, otherwise empty for an unlicensed
// installation.
//
// A configured file that cannot be read, that holds nothing, or that is too
// large to be a license is an ERROR rather than an unlicensed installation.
// Those two are the same posture to every later caller, and an operator who
// typed the path wrong would otherwise get a workspace that silently believes it
// has no entitlement — which is exactly the failure the file's strict decoding
// (an unknown key is a boot error) exists to prevent everywhere else.
func (l License) Token() (string, error) {
	if token := strings.TrimSpace(os.Getenv(LicenseTokenEnvVar)); token != "" {
		return token, nil
	}
	if l.TokenFile == "" {
		return "", nil
	}
	info, err := os.Stat(l.TokenFile)
	if err != nil {
		return "", fmt.Errorf("deployconfig: reading license.token_file: %w", err)
	}
	if info.Size() > TokenLimit {
		return "", fmt.Errorf("deployconfig: license.token_file %s is %d bytes; a license token is not (limit %d) — "+
			"check the path points at the token and not at something else", l.TokenFile, info.Size(), TokenLimit)
	}
	raw, err := os.ReadFile(l.TokenFile) // #nosec G304 -- the operator's own token path; reading it is the function's purpose
	if err != nil {
		return "", fmt.Errorf("deployconfig: reading license.token_file: %w", err)
	}
	// A token is a JWT and carries no internal whitespace, so trimming both ends
	// tolerates however the operator's editor or secret store terminated the
	// file.
	token := strings.TrimSpace(string(raw))
	if token == "" {
		return "", fmt.Errorf("deployconfig: license.token_file %s is empty — remove the setting to run unlicensed, "+
			"or write the license token into the file", l.TokenFile)
	}
	return token, nil
}

// TokenOrigin names where Token would take a token from, for the boot log.
//
// Which of the two won is worth saying out loud: the environment outranks the
// file, so an installation can be pointed at a different license — or, with the
// file absent, at none — by whoever can set a variable in the deploy pipeline
// without touching the deployment file the operator reviews.
func (l License) TokenOrigin() string {
	if strings.TrimSpace(os.Getenv(LicenseTokenEnvVar)) != "" {
		return LicenseTokenEnvVar
	}
	if l.TokenFile != "" {
		return "license.token_file"
	}
	return "none"
}
