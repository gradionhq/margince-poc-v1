// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package blobstore

import (
	"context"

	"github.com/gradionhq/margince/backend/internal/platform/config"
)

// The variable names this package answers to. Declared next to the code that
// reads them so the surface can be enumerated from the package rather than
// maintained as a list somewhere else — which is what let six production
// variables go undocumented across the tree.
// #nosec G101 -- these are the NAMES of variables, not their values; the whole point of naming them here is that the credential itself never appears in Go
const (
	EnvEndpoint  = "MARGINCE_BLOBSTORE_ENDPOINT"
	EnvAccessKey = "MARGINCE_BLOBSTORE_ACCESS_KEY"
	EnvSecretKey = "MARGINCE_BLOBSTORE_SECRET_KEY"
	EnvBucket    = "MARGINCE_BLOBSTORE_BUCKET"
	EnvRegion    = "MARGINCE_BLOBSTORE_REGION"
	EnvUseSSL    = "MARGINCE_BLOBSTORE_USE_SSL"
)

// FromEnv builds a Store from the MARGINCE_BLOBSTORE_* configuration. Secrets
// come from the environment, never CLI flags (which leak into the process
// table). It reports configured=false with a nil Store when no endpoint is
// set, so a deployment without object storage boots normally — the
// attachment endpoints answer 501 rather than the process failing to start.
//
// The lookup is a parameter (OPS-CFG-2): the composition root supplies the
// environment, and a test supplies a map without mutating process state.
//
//nolint:ireturn // the seam has two providers behind one Store; returning the interface is the design.
func FromEnv(ctx context.Context, env config.Lookup) (store Store, configured bool, err error) {
	endpoint := env(EnvEndpoint)
	if endpoint == "" {
		return nil, false, nil
	}
	s, err := New(ctx, Config{
		Endpoint:  endpoint,
		AccessKey: env(EnvAccessKey),
		SecretKey: env(EnvSecretKey),
		Bucket:    env(EnvBucket),
		Region:    env(EnvRegion),
		UseSSL:    env(EnvUseSSL) == "true",
	})
	if err != nil {
		return nil, false, err
	}
	return s, true, nil
}
