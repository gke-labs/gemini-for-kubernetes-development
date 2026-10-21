package rules

import (
	"sort"
	"strings"
	"sync"
	"time"
)

// Summary is a watch's tally of what the rules decided since it started:
// each rule's runs and give-ups, as the scanners recorded carrying them
// out; the tasks of each rule dropped at start (Dropped, from the
// dispatcher's decision at start), with the last reason; and a
// consistency check - the engine deciding again on each record agrees with
// what the scanner did, so a record that does not was decided on facts
// other than those recorded. Safe for concurrent use.
type Summary struct {
	set Set

	mu           sync.Mutex
	since        time.Time
	records      int
	gated        int
	inconsistent int
	byRule       map[string]*RuleTally
	recent       []Inconsistency
}

// RuleTally is one rule's decisions since the watch started.
type RuleTally struct {
	Run    int `json:"run,omitempty"`
	GiveUp int `json:"giveUp,omitempty"`
	// Dropped are its tasks dropped before they started, LastDrop why the
	// last one was.
	Dropped  int    `json:"dropped,omitempty"`
	LastDrop string `json:"lastDrop,omitempty"`
}

// Inconsistency is a record on which the engine, deciding again, decides
// otherwise than the scanner did.
type Inconsistency struct {
	At      time.Time `json:"at"`
	Kind    string    `json:"kind"`
	Number  int       `json:"number"`
	Rules   []string  `json:"rules,omitempty"`
	Scanner []string  `json:"scanner,omitempty"`
}

// SummaryView is the Summary so far, as the watch's queue API serves it.
type SummaryView struct {
	Since time.Time `json:"since"`
	// Records are the decisions recorded and checked; Consistent those the
	// engine decides the same on again; Gated those a gate outside the
	// rules held back, which are counted, not checked.
	Records    int                  `json:"records"`
	Consistent int                  `json:"consistent"`
	Gated      int                  `json:"gated"`
	ByRule     map[string]RuleTally `json:"byRule"`
	// Recent are the last inconsistencies, newest first.
	Recent []Inconsistency `json:"recent,omitempty"`
}

// maxInconsistencies bounds the inconsistencies a Summary remembers.
const maxInconsistencies = 50

// NewSummary tallies the decisions the scanners record, checked against
// set's.
func NewSummary(set Set) *Summary {
	return &Summary{set: set, since: time.Now().UTC(), byRule: map[string]*RuleTally{}}
}

// Observe tallies one recorded decision and checks it against the rules'.
// A record a gate outside the rules held back is counted, not checked.
func (s *Summary) Observe(rec Record) {
	if s == nil {
		return
	}
	if rec.Gate != "" {
		s.mu.Lock()
		s.gated++
		s.mu.Unlock()
		return
	}
	engine, scanner, same := compare(s.set, rec)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records++
	for _, d := range rec.Decided {
		rule, outcome, _ := strings.Cut(d, " ")
		switch outcome {
		case OutcomeRun:
			s.rule(rule).Run++
		case OutcomeGiveUp:
			s.rule(rule).GiveUp++
		}
	}
	if same {
		return
	}
	s.inconsistent++
	s.recent = append(s.recent, Inconsistency{At: rec.At, Kind: rec.Item.Kind, Number: rec.Item.Number, Rules: engine, Scanner: scanner})
	if len(s.recent) > maxInconsistencies {
		s.recent = s.recent[len(s.recent)-maxInconsistencies:]
	}
}

// Dropped tallies a task of rule dropped before it started, and why.
func (s *Summary) Dropped(rule, reason string) {
	if s == nil || rule == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.rule(rule)
	r.Dropped++
	r.LastDrop = reason
}

func (s *Summary) rule(name string) *RuleTally {
	r := s.byRule[name]
	if r == nil {
		r = &RuleTally{}
		s.byRule[name] = r
	}
	return r
}

// View is the tally so far.
func (s *Summary) View() SummaryView {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := SummaryView{Since: s.since, Records: s.records, Consistent: s.records - s.inconsistent, Gated: s.gated, ByRule: map[string]RuleTally{}}
	for name, r := range s.byRule {
		out.ByRule[name] = *r
	}
	for i := len(s.recent) - 1; i >= 0; i-- {
		out.Recent = append(out.Recent, s.recent[i])
	}
	return out
}

// compare is the rules' decisions on a record and the scanner's, sorted,
// and whether they are the same.
func compare(set Set, rec Record) (engine, scanner []string, same bool) {
	env := rec.Env
	if env.Now.IsZero() {
		env.Now = rec.At
	}
	for _, d := range set.Decide(rec.Item, env) {
		engine = append(engine, d.Rule+" "+d.Outcome)
	}
	scanner = append([]string(nil), rec.Decided...)
	sort.Strings(engine)
	sort.Strings(scanner)
	return engine, scanner, strings.Join(engine, ",") == strings.Join(scanner, ",")
}
