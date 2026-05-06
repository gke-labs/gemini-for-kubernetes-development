// Copyright 2026 The Kubernetes Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// This file just exists as a place to put //go:generate directives that should apply to the entire project

package controllers

// Generate RBAC rules
//go:generate go tool sigs.k8s.io/controller-tools/cmd/controller-gen paths=./configdir/... output:rbac:dir=../../k8s rbac:roleName=configdir-controller,fileName=configdir-rbac.generated.yaml
//go:generate go tool sigs.k8s.io/controller-tools/cmd/controller-gen paths=./syncer/... output:rbac:dir=../../k8s rbac:roleName=syncer-role,fileName=syncer-rbac.generated.yaml
//go:generate go tool sigs.k8s.io/controller-tools/cmd/controller-gen paths=./repowatch/... output:rbac:dir=../../k8s rbac:roleName=repo-agent-controller,fileName=rbac.generated.yaml
