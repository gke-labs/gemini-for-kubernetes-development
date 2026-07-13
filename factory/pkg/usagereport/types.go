/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package usagereport pushes per-task gemini-cli token usage to the central
// token-usage collector service (the hidden "factory token-daemon" command,
// implemented in factory/pkg/tokenusage). Reporting is best-effort and
// entirely disabled unless COLLECTOR_URL is set; it must never fail a task.
package usagereport

import (
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/tokenusage"
)

// Aliases to the canonical collector types defined in factory/pkg/tokenusage.
type (
	Stats       = tokenusage.Stats
	ModelUsage  = tokenusage.ModelUsage
	APIUsage    = tokenusage.APIUsage
	TokenUsage  = tokenusage.TokenUsage
	UsageRecord = tokenusage.UsageRecord
	Rollup      = tokenusage.Rollup
)
