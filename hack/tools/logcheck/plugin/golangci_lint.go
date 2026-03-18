// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and Gardener contributors
//
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"github.com/gardener/repo-tools/hack/tools/logcheck/pkg/logcheck"
	"golang.org/x/tools/go/analysis"
)

// New returns the logcheck analyzer.
func New(_ any) ([]*analysis.Analyzer, error) {
	return []*analysis.Analyzer{logcheck.Analyzer}, nil
}
