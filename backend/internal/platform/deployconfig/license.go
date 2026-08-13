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

// Token resolves the license token: the environment variable when set,
// otherwise the file reference, otherwise empty for an unlicensed
// installation.
//
// A configured file that cannot be read, or that holds nothing, is an ERROR
// rather than an unlicensed installation. Those two are the same posture to
// every later caller, and an operator who typed the path wrong would otherwise
// get a workspace that silently believes it has no entitlement — which is
// exactly the failure the strict decoder above exists to prevent.
func (l License) Token() (string, error) {
	if token := strings.TrimSpace(os.Getenv(LicenseTokenEnvVar)); token != "" {
		return token, nil
	}
	if l.TokenFile == "" {
		return "", nil
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
