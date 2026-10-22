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

package factorycli

// ResearchSandboxName is a research session's conversation sandbox
// (RepoSandboxName with research.yaml's prefix).
//
// Knowing the name without asking factory is what lets a caller find,
// watch or delete the sandbox for a session it has only the id of.
func ResearchSandboxName(repo, sessionID string) string {
	return RepoSandboxName(ResearchPrefix, repo, sessionID)
}

// IsResearchSandbox reports whether labels are a research sandbox's.
func IsResearchSandbox(labels map[string]string) bool {
	return labels[LabelSandboxType] == ResearchPrefix
}

// ResearchRunAnnotation is where factory records a research conversation's
// start (factory sandbox.RunAnnotation("research")): its task, whose agent
// session the conversation is.
const ResearchRunAnnotation = "sandbox.gemini.google.com/research-run"

// ResearchTask is the task whose session a research sandbox's conversation
// is, empty for a sandbox the recipe did not start.
func ResearchTask(annotations map[string]string) string {
	return RecordedRunSession(annotations, ResearchRunAnnotation)
}

// ResearchRunName is the run a session's start is recorded under: one per
// session, so a relaunch after a controller restart follows the start
// instead of asking the question twice. Identifiers only.
func ResearchRunName(sessionID string) string {
	return "research/" + sessionID
}
