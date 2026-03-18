// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and Gardener contributors
//
// SPDX-License-Identifier: Apache-2.0

package logcheck_test

import (
	"testing"

	"github.com/gardener/repo-tools/hack/tools/logcheck/pkg/logcheck"
	"golang.org/x/tools/go/analysis/analysistest"
)

func TestLogcheck(t *testing.T) {
	for _, test := range []string{
		"use-logr",
		"no-logr",
	} {
		t.Run(test, func(t *testing.T) {
			analysistest.Run(t, analysistest.TestData(), logcheck.Analyzer, test)
		})
	}
}
