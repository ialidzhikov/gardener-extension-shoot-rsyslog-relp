// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and Gardener contributors
//
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"github.com/gardener/repo-tools/hack/tools/logcheck/pkg/logcheck"
	"golang.org/x/tools/go/analysis/singlechecker"
)

func main() {
	// make a callable binary with a single check out of Analyzer
	singlechecker.Main(logcheck.Analyzer)
}
