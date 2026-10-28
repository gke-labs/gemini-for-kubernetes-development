package graph

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// StatusKey is the comment key of a graph's status comment: the watcher
// keeps one per parent, edited in place.
const StatusKey = "graph-status"

var stateRE = regexp.MustCompile(`<!--\s*factory:graph-state\s+(\{.*?\})\s*-->`)

// State is what a graph remembers between passes, kept in a hidden block
// of its status comment so that GitHub holds all of it.
type State struct {
	// Completed is that the graph finished and closed its parent: done
	// once, never again.
	Completed bool `json:"completed,omitempty"`
	// Checkpoints are the steps whose checkpoint already fired.
	Checkpoints []string `json:"checkpoints,omitempty"`
	// Params is the hash of the params the first child was made with
	// (HashParams): the parent's params are frozen from then on.
	Params string `json:"params,omitempty"`
	// Children are the children created, by step id: GitHub's listings
	// can lag a creation; this cannot.
	Children map[string]int `json:"children,omitempty"`
	// Inherited are the parent's human assignees already handed down to
	// its open children: one added to the parent later is added to them
	// once, and one taken off a child by hand is not put back.
	Inherited []string `json:"inherited,omitempty"`
}

// ParseState reads the state block from a status comment; the zero State
// when there is none.
func ParseState(body string) (State, bool) {
	m := stateRE.FindStringSubmatch(body)
	if m == nil {
		return State{}, false
	}
	var st State
	if err := json.Unmarshal([]byte(m[1]), &st); err != nil {
		return State{}, false
	}
	return st, true
}

// Block renders the state block, for the status comment.
func (st State) Block() string {
	st.Checkpoints = slices.Clone(st.Checkpoints)
	slices.Sort(st.Checkpoints)
	st.Inherited = slices.Clone(st.Inherited)
	slices.Sort(st.Inherited)
	data, _ := json.Marshal(st)
	return fmt.Sprintf("<!-- factory:graph-state %s -->", data)
}

func (st State) fired(step string) bool { return slices.Contains(st.Checkpoints, step) }

func (st State) clone() State {
	c := st
	c.Checkpoints = slices.Clone(st.Checkpoints)
	c.Children = maps.Clone(st.Children)
	c.Inherited = slices.Clone(st.Inherited)
	return c
}

// HashParams is a short, stable hash of params: what a child records, and
// the state freezes, so an edited parent is noticed.
func HashParams(params map[string]string) string {
	keys := slices.Sorted(maps.Keys(params))
	h := sha256.New()
	for _, k := range keys {
		fmt.Fprintf(h, "%s=%s\n", k, params[k])
	}
	return hex.EncodeToString(h.Sum(nil))[:12]
}

// ChildInfo is what a child's hidden marker says: its parent, the source
// of its steps (a template's name, or "plan"), its step, its depth (its
// parent's + 1) and the params hash it was made with.
type ChildInfo struct {
	Parent int
	Source string
	Step   string
	Depth  int
	Params string
}

var childMarkerRE = regexp.MustCompile(`<!--\s*factory:graph-child\s+([^>]*?)\s*-->`)

// Marker is the child's hidden marker, for the end of its body.
func (c ChildInfo) Marker() string {
	return fmt.Sprintf("<!-- factory:graph-child parent=%d source=%s step=%s depth=%d params=%s -->", c.Parent, c.Source, c.Step, c.Depth, c.Params)
}

// ParseChildMarker reads a child's marker from its body.
func ParseChildMarker(body string) (ChildInfo, bool) {
	m := childMarkerRE.FindStringSubmatch(body)
	if m == nil {
		return ChildInfo{}, false
	}
	var c ChildInfo
	for _, kv := range strings.Fields(m[1]) {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		switch k {
		case "parent":
			c.Parent, _ = strconv.Atoi(v)
		case "source":
			c.Source = v
		case "step":
			c.Step = v
		case "depth":
			c.Depth, _ = strconv.Atoi(v)
		case "params":
			c.Params = v
		}
	}
	if c.Parent <= 0 || c.Step == "" {
		return ChildInfo{}, false
	}
	return c, true
}
