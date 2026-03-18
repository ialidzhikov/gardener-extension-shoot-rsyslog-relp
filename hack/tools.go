// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and Gardener contributors
//
// SPDX-License-Identifier: Apache-2.0

//go:build tools
// +build tools

// This package imports things required by build scripts, to force `go mod` to see them as dependencies
package tools

import (
	_ "github.com/gardener/gardener/.github"
	_ "github.com/gardener/gardener/.github/ISSUE_TEMPLATE"
	_ "github.com/gardener/repo-tools/hack/tools/logcheck/plugin"
	_ "github.com/gardener/repo-tools/hack/tools/report-collector"

	_ "github.com/ahmetb/gen-crd-api-reference-docs"
	_ "golang.org/x/tools/cmd/goimports"
	_ "k8s.io/code-generator"
	_ "sigs.k8s.io/controller-tools/cmd/controller-gen"
)
