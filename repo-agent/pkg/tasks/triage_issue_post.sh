#!/bin/bash
# Copyright 2026 The Kubernetes Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# you may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

set -e

export PROMPT_FILE="{{ .PromptFile }}"

OUTPUT_DIR="$(dirname "${PROMPT_FILE}")"

cat "${OUTPUT_DIR}/raw-agent-output.txt"
# Extract prow /kind command and everything following it (including metadata) from agent response
sed -n '/^\/kind /,$p' "${OUTPUT_DIR}/raw-agent-output.txt" > "${OUTPUT_DIR}/agent-output.txt" || true
