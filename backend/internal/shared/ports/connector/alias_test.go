// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package connector_test

import (
	"reflect"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
	"github.com/gradionhq/margince/backend/pkg/extension"
)

// The core's file types ARE the published ones, not copies of them. A fork would
// let a unit's file and a core connector's file drift apart field by field while
// every call site still compiled, which is the failure the alias exists to make
// impossible.
func TestCoreFileTypesAreThePublishedTypes(t *testing.T) {
	for _, c := range []struct {
		name      string
		core, pub reflect.Type
	}{
		{"Part", reflect.TypeOf(connector.Part{}), reflect.TypeOf(extension.InboundFile{})},
		{"PartDrop", reflect.TypeOf(connector.PartDrop{}), reflect.TypeOf(extension.FileDrop{})},
		{"OutboundFile", reflect.TypeOf(connector.OutboundFile{}), reflect.TypeOf(extension.OutboundFile{})},
	} {
		if c.core != c.pub {
			t.Errorf("connector.%s is %v, not the published %v: an alias was replaced by a copy",
				c.name, c.core, c.pub)
		}
	}
}
