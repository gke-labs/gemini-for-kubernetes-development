#!/usr/bin/env python3

# Copyright 2025 The Kubernetes Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

import os
import glob

def find_repo_root():
    """Finds the repository root from the script's location."""
    return os.path.abspath(os.path.join(os.path.dirname(__file__), '..', '..', '..'))

def find_go_mod_files(mod_name_patterns=['go.mod']):
    """Finds all go.mod files in the repository."""
    repo_root = find_repo_root()
    mod_files = []
    for pattern in mod_name_patterns:
        search_pattern = os.path.join(repo_root, '**', pattern)
        mod_files.extend(glob.glob(search_pattern, recursive=True))

    mod_files.sort()

    return mod_files

def for_each_module(func):
    """Runs a function for each module directory containing go.mod."""
    mod_files = find_go_mod_files()
    for mod_file in mod_files:
        mod_dir = os.path.dirname(mod_file)
        func(mod_dir)