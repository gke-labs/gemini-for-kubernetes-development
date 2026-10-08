package fanout

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
)

// ProgressMarker marks the comment the fan-out keeps its progress in.
const ProgressMarker = "<!-- factory:fanout-progress -->"

var stateRe = regexp.MustCompile(`<!--\s*factory:fanout-state\s+(\{.*?\})\s*-->`)

// State is what a fan-out remembers between passes, kept in a hidden block
// of the progress comment so that GitHub holds all of it.
type State struct {
	// Window is how many children may be labelled and open at once.
	Window int `json:"window"`
	// Counted are the children closed and the PRs closed unmerged that the
	// window has already moved for, so a pass never counts one twice.
	Counted []int `json:"counted,omitempty"`
	// Checkpoints are the checkpoints already stopped at.
	Checkpoints []int `json:"checkpoints,omitempty"`
	// Children are the item children created, by item key. GitHub's search
	// and timeline can lag a creation; this cannot.
	Children map[string]int `json:"children,omitempty"`
	// Final is the final child, once created.
	Final int `json:"final,omitempty"`
	// Started are the children the fan-out labelled. One is never labelled
	// again, so a person who unlabels a child has the last word.
	Started []int `json:"started,omitempty"`
}

// ParseState reads the state block from a progress comment.
func ParseState(body string) (*State, bool) {
	m := stateRe.FindStringSubmatch(body)
	if m == nil {
		return nil, false
	}
	var st State
	if err := json.Unmarshal([]byte(m[1]), &st); err != nil || st.Window < 1 {
		return nil, false
	}
	return &st, true
}

// block renders the state block.
func (st State) block() string {
	data, _ := json.Marshal(st)
	return fmt.Sprintf("<!-- factory:fanout-state %s -->", data)
}

// Created records a child the caller created for c, as number.
func (st *State) Created(c NewChild, number int) {
	if c.Final {
		st.Final = number
	} else {
		if st.Children == nil {
			st.Children = map[string]int{}
		}
		st.Children[c.Key] = number
	}
	if len(c.Labels) > 0 {
		st.started(number)
	}
}

func (st *State) counted(n int) bool { return slices.Contains(st.Counted, n) }

func (st *State) count(n int) {
	if !st.counted(n) {
		st.Counted = append(st.Counted, n)
	}
}

func (st *State) started(n int) {
	if !slices.Contains(st.Started, n) {
		st.Started = append(st.Started, n)
	}
}

func (st *State) clone() State {
	c := *st
	c.Counted = slices.Clone(st.Counted)
	c.Checkpoints = slices.Clone(st.Checkpoints)
	c.Started = slices.Clone(st.Started)
	c.Children = make(map[string]int, len(st.Children))
	for k, v := range st.Children {
		c.Children[k] = v
	}
	return c
}
