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

package tokenusage

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Archived tasks: a task's directory as the harvest found it in its
// sandbox (the transcript, session/stream.ndjson, its inputs, recipe,
// result, step logs), harvested with its usage. The collector only passes
// it through: a task PUT is spooled on the PVC, the flusher uploads it to
// the archive and deletes it, and the task-index log (sealed, archived and
// replayed like the records log) indexes what the archive holds. The
// collector never serves a task's files; readers go to the archive.
//
// In the archive, under the collector's prefix, one folder per task,
// named as in the sandbox, each file gzipped:
//
//	tasks/<owner>/<repo>/<sandbox>/<task>/
//	  manifest.json                 the TaskRecord, written last
//	  task.json.gz, inputs.json.gz, recipe.source.yaml.gz, …
//	  steps/<NN-step>.log.gz
//	  session/stream.ndjson.gz

// logTasks is the index log's name. It is not "tasks", where the tasks'
// folders are: startup lists the log's segments by prefix.
const logTasks = "task-index"

// manifestFile is written last in a task's folder: a folder without one
// is an upload that did not finish.
const manifestFile = "manifest.json"

var (
	// maxTaskBytes bounds one task's files; maxSpoolBytes bounds the
	// tasks waiting on the PVC, so that an archive outage fills neither
	// the PVC the usage logs share nor memory.
	maxTaskBytes  int64 = 256 << 20
	maxSpoolBytes int64 = 512 << 20

	// ErrNoArchive refuses a task when there is no archive to send it to:
	// the PVC alone would only fill up.
	ErrNoArchive = errors.New("archiving tasks needs an archive (GCS_BUCKET)")
	// ErrSpoolFull refuses a task while the spool is full.
	ErrSpoolFull = errors.New("the task spool is full")
	// ErrTaskTooLarge refuses a task over maxTaskBytes.
	ErrTaskTooLarge = errors.New("task too large")

	pathPart = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]*$`)
	repoName = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

	// secretPatterns are credential shapes masked in a task's files before
	// they touch the PVC. Tokens are kept out of the engine's environment
	// (#1695, #1696), and the task server never serves a task's env or
	// secrets; this is the backstop for what leaks anyway.
	secretPatterns = []*regexp.Regexp{
		regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{36,}`),
		regexp.MustCompile(`github_pat_[A-Za-z0-9_]{22,}`),
		regexp.MustCompile(`AIza[0-9A-Za-z_-]{35}`),
		regexp.MustCompile(`ya29\.[0-9A-Za-z_-]{20,}`),
		regexp.MustCompile(`sk-ant-[0-9A-Za-z_-]{20,}`),
	}
)

// redact masks the credential shapes in line.
func redact(line []byte) []byte {
	for _, re := range secretPatterns {
		line = re.ReplaceAll(line, []byte("[REDACTED]"))
	}
	return line
}

// validFileName reports whether name is a task file's name: a file of
// the task directory, or of a directory one down ("steps/01-clone.log").
func validFileName(name string) bool {
	parts := strings.Split(name, "/")
	if len(parts) > 2 {
		return false
	}
	for _, p := range parts {
		if !pathPart.MatchString(p) {
			return false
		}
	}
	return name != manifestFile
}

// taskFolder is where a task goes in the archive.
func taskFolder(rec TaskRecord) string {
	return fmt.Sprintf("tasks/%s/%s/%s/", rec.Repo, rec.Sandbox, rec.Task)
}

func (s *Store) taskSpoolDir() string { return filepath.Join(s.root, "tasks") }

// PutTask redacts a task's files, read from a tar stream (regular files
// named as in the task directory), and spools them for the flusher, with
// rec (repo, sandbox, task, issue, pr, taskType) to index them by. A task
// whose files the archive already holds unchanged is not spooled again.
func (s *Store) PutTask(rec TaskRecord, files io.Reader) error {
	if s.archive == nil {
		return ErrNoArchive
	}
	if !pathPart.MatchString(rec.Sandbox) || !pathPart.MatchString(rec.Task) {
		return fmt.Errorf("invalid sandbox %q or task %q", rec.Sandbox, rec.Task)
	}
	if !repoName.MatchString(rec.Repo) {
		return fmt.Errorf("invalid repo %q", rec.Repo)
	}
	rec.Key = rec.Sandbox + "/" + rec.Task
	if spooled, err := dirSize(s.taskSpoolDir()); err != nil {
		return err
	} else if spooled >= maxSpoolBytes {
		return ErrSpoolFull
	}

	// Each PUT is an entry of its own, made in a temporary directory and
	// renamed into place whole, so the flusher never sees half of one.
	// Entries are named by time: the flusher uploads them in order.
	if err := os.MkdirAll(s.taskSpoolDir()+".tmp", 0o755); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(s.taskSpoolDir()+".tmp", "put-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	var total int64
	tr := tar.NewReader(files)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("invalid task archive: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		if !validFileName(hdr.Name) {
			return fmt.Errorf("invalid file name %q", hdr.Name)
		}
		f, err := spoolFile(filepath.Join(tmp, "files", hdr.Name), tr, maxTaskBytes-total)
		if err != nil {
			return err
		}
		total += f.Bytes
		f.Name = hdr.Name
		rec.Files = append(rec.Files, f)
	}
	if len(rec.Files) == 0 {
		return nil
	}
	sort.Slice(rec.Files, func(i, j int) bool { return rec.Files[i].Name < rec.Files[j].Name })
	describeTask(&rec, filepath.Join(tmp, "files"))

	s.mu.Lock()
	existing, ok := s.tasks[rec.Key]
	s.mu.Unlock()
	if ok && sameFiles(existing.Files, rec.Files) {
		return nil // archived already, unchanged
	}

	meta, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(tmp, "meta.json"), meta, 0o644); err != nil {
		return err
	}
	if err := os.MkdirAll(s.taskSpoolDir(), 0o755); err != nil {
		return err
	}
	entry := filepath.Join(s.taskSpoolDir(), fmt.Sprintf("%020d-%s-%s", time.Now().UnixNano(), rec.Sandbox, rec.Task))
	return os.Rename(tmp, entry)
}

// spoolFile copies r to path line by line, redacting each, and returns
// the file's size and sha256; more than limit bytes is ErrTaskTooLarge.
func spoolFile(path string, r io.Reader, limit int64) (TaskFile, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return TaskFile{}, err
	}
	f, err := os.Create(path)
	if err != nil {
		return TaskFile{}, err
	}
	defer f.Close()
	h := sha256.New()
	w := io.MultiWriter(f, h)
	var out TaskFile
	br := bufio.NewReader(io.LimitReader(r, limit+1))
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			line = redact(line)
			if _, werr := w.Write(line); werr != nil {
				return TaskFile{}, werr
			}
			out.Bytes += int64(len(line))
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return TaskFile{}, fmt.Errorf("reading %s: %w", filepath.Base(path), err)
		}
	}
	if out.Bytes > limit {
		return TaskFile{}, ErrTaskTooLarge
	}
	out.SHA256 = hex.EncodeToString(h.Sum(nil))
	return out, f.Close()
}

// describeTask fills in what the task's own files say about it. Each is
// best-effort: a task without the file, or with another shape of it,
// leaves its fields empty.
func describeTask(rec *TaskRecord, dir string) {
	var task struct {
		Recipe  string `json:"recipe"`
		RunName string `json:"run_name"`
		URL     string `json:"url"`
	}
	if readJSON(filepath.Join(dir, "task.json"), &task) {
		rec.Recipe, rec.RunName, rec.URL = task.Recipe, task.RunName, task.URL
	}
	var status struct {
		State     string `json:"state"`
		ExitCode  any    `json:"exit_code"`
		StartedAt string `json:"started_at"`
		EndedAt   string `json:"ended_at"`
	}
	if readJSON(filepath.Join(dir, "status.json"), &status) {
		rec.State, rec.StartedAt, rec.EndedAt = status.State, status.StartedAt, status.EndedAt
		if status.ExitCode != nil {
			rec.ExitCode = fmt.Sprint(status.ExitCode)
		}
	}
	var push struct {
		Branch string `json:"branch"`
		Head   string `json:"head"`
	}
	if readJSON(filepath.Join(dir, "push.json"), &push) {
		rec.PushedBranch, rec.PushedHead = push.Branch, push.Head
	}
	if data, err := os.ReadFile(filepath.Join(dir, "task-output.applied")); err == nil {
		rec.Applied = strings.TrimSpace(string(data))
	}
	if inst := computeInstructions(rec, dir); inst != nil {
		rec.Instructions = inst
	}
}

func computeInstructions(rec *TaskRecord, dir string) *Instructions {
	inst := &Instructions{Repo: rec.Repo}

	var taskData map[string]any
	readJSON(filepath.Join(dir, "task.json"), &taskData)
	var inputsData map[string]any
	readJSON(filepath.Join(dir, "inputs.json"), &inputsData)

	if inst.Repo == "" {
		if r, ok := inputsData["repo"].(string); ok && r != "" {
			inst.Repo = r
		} else if owner, ok := inputsData["repo_owner"].(string); ok && owner != "" {
			if name, ok := inputsData["repo_name"].(string); ok && name != "" {
				inst.Repo = owner + "/" + name
			}
		} else if r, ok := taskData["repo"].(string); ok && r != "" {
			inst.Repo = r
		}
	}

	for _, key := range []string{"commit", "commit_sha", "sha", "base_commit", "head_commit", "default_branch_commit"} {
		if val, ok := inputsData[key].(string); ok && val != "" {
			inst.Commit = val
			break
		}
		if val, ok := taskData[key].(string); ok && val != "" {
			inst.Commit = val
			break
		}
	}

	var wfName, wfPath string
	if wfVal, ok := inputsData["workflow"]; ok {
		switch v := wfVal.(type) {
		case string:
			wfName = v
		case map[string]any:
			if n, ok := v["name"].(string); ok {
				wfName = n
			}
			if p, ok := v["path"].(string); ok {
				wfPath = p
			}
		}
	}
	if wfVal, ok := taskData["workflow"]; ok && wfName == "" && wfPath == "" {
		switch v := wfVal.(type) {
		case string:
			wfName = v
		case map[string]any:
			if n, ok := v["name"].(string); ok {
				wfName = n
			}
			if p, ok := v["path"].(string); ok {
				wfPath = p
			}
		}
	}
	if wfName == "" {
		for _, key := range []string{"agent_name", "workflow_name"} {
			if val, ok := inputsData[key].(string); ok && val != "" {
				wfName = val
				break
			}
			if val, ok := taskData[key].(string); ok && val != "" {
				wfName = val
				break
			}
		}
	}
	if wfPath == "" {
		for _, key := range []string{"agent_file", "workflow_path"} {
			if val, ok := inputsData[key].(string); ok && val != "" {
				wfPath = val
				break
			}
			if val, ok := taskData[key].(string); ok && val != "" {
				wfPath = val
				break
			}
		}
	}
	if wfName == "" && wfPath != "" {
		wfName = strings.TrimSuffix(filepath.Base(wfPath), filepath.Ext(wfPath))
	} else if wfPath == "" && strings.HasPrefix(wfName, ".agents/") {
		wfPath = wfName
		wfName = strings.TrimSuffix(filepath.Base(wfPath), filepath.Ext(wfPath))
	}
	if wfName != "" || wfPath != "" {
		inst.Workflow = &WorkflowRef{Name: wfName, Path: wfPath}
	}

	recipeName := rec.Recipe
	if recipeName == "" {
		if r, ok := taskData["recipe"].(string); ok {
			recipeName = r
		} else if r, ok := inputsData["recipe"].(string); ok {
			recipeName = r
		} else if rMap, ok := inputsData["recipe"].(map[string]any); ok {
			if n, ok := rMap["name"].(string); ok {
				recipeName = n
			}
		}
	}
	var recipeHash string
	if srcBytes, err := os.ReadFile(filepath.Join(dir, "recipe.source.yaml")); err == nil {
		h := sha256.Sum256(srcBytes)
		recipeHash = "sha256:" + hex.EncodeToString(h[:])
	} else if yamlBytes, err := os.ReadFile(filepath.Join(dir, "recipe.yaml")); err == nil {
		h := sha256.Sum256(yamlBytes)
		recipeHash = "sha256:" + hex.EncodeToString(h[:])
	} else if rMap, ok := inputsData["recipe"].(map[string]any); ok {
		if h, ok := rMap["hash"].(string); ok {
			recipeHash = h
		}
	}
	if recipeName != "" || recipeHash != "" {
		inst.Recipe = &RecipeRef{Name: recipeName, Hash: recipeHash}
	}

	if ctxVal, ok := inputsData["context"]; ok {
		inst.Context = toStringSlice(ctxVal)
	} else if ctxVal, ok := taskData["context"]; ok {
		inst.Context = toStringSlice(ctxVal)
	}

	sessionPath := filepath.Join(dir, "session", "session.json")
	if _, err := os.Stat(sessionPath); err != nil {
		sessionPath = filepath.Join(dir, "session.json")
	}
	var sessionRec struct {
		Engine string `json:"engine"`
		Model  string `json:"model"`
	}
	if readJSON(sessionPath, &sessionRec) {
		inst.Engine = sessionRec.Engine
		inst.Model = sessionRec.Model
	}
	if inst.Engine == "" {
		if eng, ok := inputsData["engine"].(string); ok {
			inst.Engine = eng
		} else if eng, ok := taskData["engine"].(string); ok {
			inst.Engine = eng
		}
	}
	if inst.Model == "" {
		if mdl, ok := inputsData["model"].(string); ok {
			inst.Model = mdl
		} else if mdl, ok := taskData["model"].(string); ok {
			inst.Model = mdl
		}
	}

	inst.Skills = parseSkillsFromStream(dir)

	if inst.isEmpty() {
		return nil
	}
	return inst
}

func (in *Instructions) isEmpty() bool {
	if in == nil {
		return true
	}
	return in.Repo == "" && in.Commit == "" && in.Workflow == nil && in.Recipe == nil &&
		in.Engine == "" && in.Model == "" && len(in.Context) == 0 && len(in.Skills) == 0
}

func toStringSlice(v any) []string {
	switch val := v.(type) {
	case []string:
		return val
	case []any:
		var res []string
		for _, item := range val {
			if s, ok := item.(string); ok && s != "" {
				res = append(res, s)
			}
		}
		return res
	case string:
		if val != "" {
			return []string{val}
		}
	}
	return nil
}

var skillTitleRe = regexp.MustCompile(`(?i)(?:activate_skill|Activate skill)[\s(:]+(?:name[=:]\s*)?["']?([A-Za-z0-9_.-]+)`)

type toolCallSub struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	RawInput  json.RawMessage `json:"rawInput"`
	Input     json.RawMessage `json:"input"`
	Args      json.RawMessage `json:"args"`
	Arguments json.RawMessage `json:"arguments"`
}

func parseSkillsFromStream(dir string) []SkillRef {
	skillsDirs := []string{".gemini/skills", ".agents/skills"}
	knownSkills := make(map[string]string)

	if data, err := os.ReadFile(filepath.Join(dir, ".agents", "skills.json")); err == nil {
		var list []string
		if json.Unmarshal(data, &list) == nil {
			for _, d := range list {
				d = filepath.ToSlash(filepath.Clean(strings.TrimSpace(d)))
				if d != "" && !slices.Contains(skillsDirs, d) {
					skillsDirs = append(skillsDirs, d)
				}
			}
		} else {
			var obj map[string]any
			if json.Unmarshal(data, &obj) == nil {
				if dirs, ok := obj["directories"].([]any); ok {
					for _, d := range dirs {
						if s, ok := d.(string); ok {
							s = filepath.ToSlash(filepath.Clean(strings.TrimSpace(s)))
							if s != "" && !slices.Contains(skillsDirs, s) {
								skillsDirs = append(skillsDirs, s)
							}
						}
					}
				}
				for k, v := range obj {
					if s, ok := v.(string); ok && k != "directories" {
						knownSkills[k] = filepath.ToSlash(filepath.Clean(s))
					}
				}
			}
		}
	}

	streamPath := filepath.Join(dir, "session", "stream.ndjson")
	if _, err := os.Stat(streamPath); err != nil {
		streamPath = filepath.Join(dir, "stream.ndjson")
	}
	f, err := os.Open(streamPath)
	if err != nil {
		return nil
	}
	defer f.Close()

	skillMap := make(map[string]*SkillRef)
	toolCallSkills := make(map[string]string)

	br := bufio.NewReader(f)
	var lineSeq int64
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			lineSeq++
			processStreamLine(line, lineSeq, skillsDirs, knownSkills, skillMap, toolCallSkills)
		}
		if err != nil {
			break
		}
	}

	if len(skillMap) == 0 {
		return nil
	}
	skills := make([]SkillRef, 0, len(skillMap))
	for _, s := range skillMap {
		skills = append(skills, *s)
	}
	sort.Slice(skills, func(i, j int) bool {
		if skills[i].FirstSeq != skills[j].FirstSeq {
			return skills[i].FirstSeq < skills[j].FirstSeq
		}
		return skills[i].Name < skills[j].Name
	})
	return skills
}

func processStreamLine(line []byte, fallbackSeq int64, skillsDirs []string, knownSkills map[string]string,
	skillMap map[string]*SkillRef, toolCallSkills map[string]string) {
	var ev struct {
		Seq  int64           `json:"seq"`
		Kind string          `json:"kind"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(line, &ev); err != nil {
		return
	}
	seq := ev.Seq
	if seq == 0 {
		seq = fallbackSeq
	}

	var data struct {
		SessionUpdate string          `json:"sessionUpdate"`
		ToolCallID    string          `json:"toolCallId"`
		Title         string          `json:"title"`
		Kind          string          `json:"kind"`
		Status        string          `json:"status"`
		Name          string          `json:"name"`
		Tool          string          `json:"tool"`
		RawInput      json.RawMessage `json:"rawInput"`
		Input         json.RawMessage `json:"input"`
		Args          json.RawMessage `json:"args"`
		Arguments     json.RawMessage `json:"arguments"`
		ToolCalls     []toolCallSub   `json:"toolCalls"`
	}
	if len(ev.Data) > 0 {
		_ = json.Unmarshal(ev.Data, &data)
	}

	if data.ToolCallID != "" {
		if sName, ok := toolCallSkills[data.ToolCallID]; ok {
			if sk, ok := skillMap[sName]; ok {
				if seq > sk.LastSeq {
					sk.LastSeq = seq
				}
			}
		}
	}

	toolName := data.Name
	if toolName == "" {
		toolName = data.Tool
	}
	handleToolInvocation(seq, data.ToolCallID, data.Kind, toolName, data.Title,
		data.RawInput, data.Input, data.Args, data.Arguments,
		skillsDirs, knownSkills, skillMap, toolCallSkills)

	for _, tc := range data.ToolCalls {
		handleToolInvocation(seq, tc.ID, "", tc.Name, "",
			tc.RawInput, tc.Input, tc.Args, tc.Arguments,
			skillsDirs, knownSkills, skillMap, toolCallSkills)
	}
}

func handleToolInvocation(seq int64, toolCallID, toolKind, toolName, title string,
	rawInput, input, args, arguments json.RawMessage,
	skillsDirs []string, knownSkills map[string]string,
	skillMap map[string]*SkillRef, toolCallSkills map[string]string) {
	argsMap := parseArgs(rawInput, input, args, arguments)

	isActivateSkill := toolName == "activate_skill" ||
		strings.Contains(toolName, "activate_skill") ||
		strings.HasPrefix(toolCallID, "activate_skill") ||
		strings.Contains(title, "activate_skill") ||
		strings.HasPrefix(title, "Activate skill")

	if isActivateSkill {
		skillName := ""
		for _, k := range []string{"name", "skill", "skill_name"} {
			if s, ok := argsMap[k].(string); ok && s != "" {
				skillName = s
				break
			}
		}
		if skillName == "" {
			if m := skillTitleRe.FindStringSubmatch(title); len(m) > 1 {
				skillName = m[1]
			}
		}
		if skillName != "" {
			skillPath := ""
			if p, ok := knownSkills[skillName]; ok {
				skillPath = p
			} else if p, ok := argsMap["path"].(string); ok && p != "" {
				skillPath = p
			} else {
				prefix := ".gemini/skills"
				if len(skillsDirs) > 0 {
					prefix = skillsDirs[0]
				}
				skillPath = prefix + "/" + skillName
			}
			recordSkill(skillName, skillPath, "activated", seq, toolCallID, skillMap, toolCallSkills)
			return
		}
	}

	candPath := ""
	for _, k := range []string{"file_path", "path", "filename", "file", "target", "uri"} {
		if s, ok := argsMap[k].(string); ok && s != "" {
			candPath = s
			break
		}
	}
	if candPath == "" && title != "" {
		candPath = title
	}
	cand := filepath.ToSlash(filepath.Clean(strings.Trim(candPath, " \"'`:")))
	cand = strings.TrimPrefix(cand, "./")
	cand = strings.TrimPrefix(cand, "/")

	if cand != "" {
		for sName, sPath := range knownSkills {
			cleanSPath := strings.TrimPrefix(filepath.ToSlash(filepath.Clean(sPath)), "./")
			cleanSPath = strings.TrimPrefix(cleanSPath, "/")
			if cand == cleanSPath || strings.HasPrefix(cand, cleanSPath+"/") {
				recordSkill(sName, sPath, "read", seq, toolCallID, skillMap, toolCallSkills)
				return
			}
		}

		for _, d := range skillsDirs {
			cleanDir := strings.TrimPrefix(filepath.ToSlash(filepath.Clean(d)), "./")
			cleanDir = strings.TrimPrefix(cleanDir, "/")
			prefix := cleanDir + "/"
			if strings.HasPrefix(cand, prefix) {
				rest := strings.TrimPrefix(cand, prefix)
				parts := strings.Split(rest, "/")
				sName := parts[0]
				if sName != "" && pathPart.MatchString(sName) {
					sPath := cleanDir + "/" + sName
					recordSkill(sName, sPath, "read", seq, toolCallID, skillMap, toolCallSkills)
					return
				}
			}
		}
	}
}

func recordSkill(name, path, how string, seq int64, toolCallID string,
	skillMap map[string]*SkillRef, toolCallSkills map[string]string) {
	if existing, ok := skillMap[name]; ok {
		if seq < existing.FirstSeq {
			existing.FirstSeq = seq
		}
		if seq > existing.LastSeq {
			existing.LastSeq = seq
		}
		if how == "activated" {
			existing.How = "activated"
		}
		if existing.Path == "" {
			existing.Path = path
		}
	} else {
		skillMap[name] = &SkillRef{
			Name:     name,
			Path:     path,
			How:      how,
			FirstSeq: seq,
			LastSeq:  seq,
		}
	}
	if toolCallID != "" {
		toolCallSkills[toolCallID] = name
	}
}

func parseArgs(blobs ...json.RawMessage) map[string]any {
	out := make(map[string]any)
	for _, b := range blobs {
		if len(b) == 0 {
			continue
		}
		var m map[string]any
		if json.Unmarshal(b, &m) == nil {
			for k, v := range m {
				out[k] = v
			}
		} else {
			var s string
			if json.Unmarshal(b, &s) == nil {
				var inner map[string]any
				if json.Unmarshal([]byte(s), &inner) == nil {
					for k, v := range inner {
						out[k] = v
					}
				}
			}
		}
	}
	return out
}

func readJSON(path string, v any) bool {
	data, err := os.ReadFile(path)
	return err == nil && json.Unmarshal(data, v) == nil
}

// sameFiles reports whether two file lists, sorted by name, are the same
// files with the same content.
func sameFiles(a, b []TaskFile) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Name != b[i].Name || a[i].SHA256 != b[i].SHA256 {
			return false
		}
	}
	return true
}

// uploadTasks uploads the spooled tasks in order: each file whose content
// the archive does not already hold, then the manifest; it indexes the
// task once the archive has it all and removes it from the PVC. The
// caller holds s.flushMu.
func (s *Store) uploadTasks(ctx context.Context) error {
	entries, err := os.ReadDir(s.taskSpoolDir())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		if err := s.uploadTask(ctx, filepath.Join(s.taskSpoolDir(), name)); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) uploadTask(ctx context.Context, dir string) error {
	meta, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil {
		return err
	}
	var rec TaskRecord
	if err := json.Unmarshal(meta, &rec); err != nil {
		return fmt.Errorf("spooled task %s: %w", filepath.Base(dir), err)
	}
	rec.Folder = taskFolder(rec)

	s.mu.Lock()
	archived := map[string]string{}
	for _, f := range s.tasks[rec.Key].Files {
		archived[f.Name] = f.SHA256
	}
	s.mu.Unlock()

	for _, f := range rec.Files {
		if archived[f.Name] == f.SHA256 {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, "files", f.Name))
		if err != nil {
			return err
		}
		gz, err := gzipBytes(data)
		if err != nil {
			return err
		}
		if err := s.archive.Write(ctx, rec.Folder+f.Name+".gz", gz); err != nil {
			return fmt.Errorf("uploading %s%s: %w", rec.Folder, f.Name, err)
		}
	}
	rec.ArchivedAt = time.Now().UTC().Format(time.RFC3339)
	manifest, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	if err := s.archive.Write(ctx, rec.Folder+manifestFile, manifest); err != nil {
		return fmt.Errorf("uploading %s%s: %w", rec.Folder, manifestFile, err)
	}
	if err := s.indexTask(rec); err != nil {
		return err
	}
	return os.RemoveAll(dir)
}

// indexTask upserts a task's index record.
func (s *Store) indexTask(rec TaskRecord) error {
	if rec.Key == "" {
		return fmt.Errorf("task key is required")
	}
	line, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.tasksLog.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("appending to the task index: %w", err)
	}
	if err := s.tasksLog.Sync(); err != nil {
		return fmt.Errorf("syncing the task index: %w", err)
	}
	s.tasks[rec.Key] = rec
	return nil
}

// ListTasks returns the matching task records, newest archived first.
func (s *Store) ListTasks(f TaskFilter) []TaskRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []TaskRecord
	for _, rec := range s.tasks {
		if (f.Repo != "" && rec.Repo != f.Repo) || (f.Issue != 0 && rec.Issue != f.Issue) ||
			(f.PR != 0 && rec.PR != f.PR) || (f.Sandbox != "" && rec.Sandbox != f.Sandbox) {
			continue
		}
		out = append(out, rec)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ArchivedAt != out[j].ArchivedAt {
			return out[i].ArchivedAt > out[j].ArchivedAt
		}
		return out[i].Key < out[j].Key
	})
	return out
}

// dirSize is the total size of the regular files under dir, 0 when it
// does not exist.
func dirSize(dir string) (int64, error) {
	var total int64
	err := filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.Type().IsRegular() {
			info, err := d.Info()
			if err != nil {
				return err
			}
			total += info.Size()
		}
		return nil
	})
	return total, err
}

// queryInt parses an optional integer query value.
func queryInt(v string) (int, error) {
	if v == "" {
		return 0, nil
	}
	return strconv.Atoi(v)
}

// TarFiles writes files (name → content) as a tar stream, in name order:
// the body of a task PUT.
func TarFiles(files map[string][]byte) ([]byte, error) {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, name := range names {
		data := files[name]
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
			return nil, err
		}
		if _, err := tw.Write(data); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ReadTaskFile reads an archived task's file from the archive, decompressing
// it if stored gzipped.
func (s *Store) ReadTaskFile(ctx context.Context, sandbox, task, name string) (io.ReadCloser, error) {
	s.mu.Lock()
	if s.archive == nil {
		s.mu.Unlock()
		return nil, ErrNoArchive
	}
	key := sandbox + "/" + task
	rec, ok := s.tasks[key]
	s.mu.Unlock()
	if !ok {
		return nil, ErrNotFound
	}
	folder := rec.Folder
	if folder == "" {
		folder = taskFolder(rec)
	}

	var data []byte
	var readErr error
	if !strings.HasSuffix(name, ".gz") {
		data, readErr = s.archive.Read(ctx, folder+name+".gz")
		if errors.Is(readErr, ErrNotFound) {
			data, readErr = s.archive.Read(ctx, folder+name)
		}
	} else {
		data, readErr = s.archive.Read(ctx, folder+name)
	}
	if errors.Is(readErr, ErrNotFound) && (name == "feedback.jsonl" || name == "feedback.jsonl.gz") && rec.PR > 0 && rec.Repo != "" {
		fbPath := fmt.Sprintf("prs/%s/%d/feedback.jsonl.gz", rec.Repo, rec.PR)
		data, readErr = s.archive.Read(ctx, fbPath)
		if errors.Is(readErr, ErrNotFound) {
			data, readErr = s.archive.Read(ctx, strings.TrimSuffix(fbPath, ".gz"))
		}
	}
	if readErr != nil {
		return nil, readErr
	}

	if len(data) >= 2 && data[0] == 0x1f && data[1] == 0x8b {
		zr, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("decompressing %s: %w", name, err)
		}
		return zr, nil
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

// validTaskFilePath reports whether name is a valid file path within an archived
// task. It refuses leading slashes, "..", empty parts, and invalid characters.
func validTaskFilePath(name string) bool {
	if name == "" || strings.HasPrefix(name, "/") || strings.HasSuffix(name, "/") {
		return false
	}
	parts := strings.Split(name, "/")
	for _, p := range parts {
		if p == "" || p == "." || p == ".." || !pathPart.MatchString(p) {
			return false
		}
	}
	return true
}
