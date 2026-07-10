// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package blobstore

import (
	"context"
	"os"
)

// FromEnv builds a Store from the MARGINCE_BLOBSTORE_* environment. Secrets
// come from the environment, never CLI flags (which leak into the process
// table). It reports configured=false with a nil Store when no endpoint is
// set, so a deployment without object storage boots normally — the
// attachment endpoints answer 501 rather than the process failing to start.
//
//nolint:ireturn // the seam has two providers behind one Store; returning the interface is the design.
func FromEnv(ctx context.Context) (store Store, configured bool, err error) {
	endpoint := os.Getenv("MARGINCE_BLOBSTORE_ENDPOINT")
	if endpoint == "" {
		return nil, false, nil
	}
	s, err := New(ctx, Config{
		Endpoint:  endpoint,
		AccessKey: os.Getenv("MARGINCE_BLOBSTORE_ACCESS_KEY"),
		SecretKey: os.Getenv("MARGINCE_BLOBSTORE_SECRET_KEY"),
		Bucket:    os.Getenv("MARGINCE_BLOBSTORE_BUCKET"),
		Region:    os.Getenv("MARGINCE_BLOBSTORE_REGION"),
		UseSSL:    os.Getenv("MARGINCE_BLOBSTORE_USE_SSL") == "true",
	})
	if err != nil {
		return nil, false, err
	}
	return s, true, nil
}
