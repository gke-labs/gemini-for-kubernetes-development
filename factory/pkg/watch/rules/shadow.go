package rules

import (
	"sort"
	"strings"
	"sync"
	"time"
)

// Shadow runs the rules beside today's scanners: every decision a scanner
// records, the rules decide on the same facts, and the Shadow counts where
// they agree and where they differ, by rule (design: Moving without
// regressing, shadow mode). It acts on nothing. Safe for concurrent use.
type Shadow struct {
	set Set

	mu       sync.Mutex
	since    time.Time
	compared int
	differ   int
	gated    int
	byRule   map[string]*ShadowRule
	recent   []ShadowDiff
}

// ShadowRule is one rule's tally: the records where the scanner or the
// rules decided it, and those where only one side did; and, once the
// engine decides it for the scanner, the records it did so on and those
// where the scanner's own condition said otherwise (Handed).
type ShadowRule struct {
	Decided int          `json:"decided"`
	Differ  int          `json:"differ"`
	Handed  *HandedTally `json:"handed,omitempty"`
}

// HandedTally counts a rule the engine decided for the scanner: on how
// many records, and on how many the scanner's own condition disagreed.
type HandedTally struct {
	Decided int `json:"decided"`
	Differ  int `json:"differ"`
}

// ShadowDiff is one decision where the rules and the scanner differ.
type ShadowDiff struct {
	At      time.Time `json:"at"`
	Kind    string    `json:"kind"`
	Number  int       `json:"number"`
	Rules   []string  `json:"rules,omitempty"`
	Scanner []string  `json:"scanner,omitempty"`
}

// ShadowSummary is the Shadow's tally since it started.
type ShadowSummary struct {
	Since    time.Time             `json:"since"`
	Compared int                   `json:"compared"`
	Agree    int                   `json:"agree"`
	Gated    int                   `json:"gated"`
	ByRule   map[string]ShadowRule `json:"byRule"`
	// Recent are the last differences, newest first.
	Recent []ShadowDiff `json:"recent,omitempty"`
}

// maxShadowDiffs bounds the differences a Shadow remembers.
const maxShadowDiffs = 50

// NewShadow compares the scanners' decisions with set's.
func NewShadow(set Set) *Shadow {
	return &Shadow{set: set, since: time.Now().UTC(), byRule: map[string]*ShadowRule{}}
}

// Observe compares one recorded decision with the rules'. A record a gate
// outside the rules held back is counted, not compared.
func (s *Shadow) Observe(rec Record) {
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
	handed := handedTally(s.set, rec)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.compared++
	for rule, t := range handed {
		r := s.rule(rule)
		if r.Handed == nil {
			r.Handed = &HandedTally{}
		}
		r.Handed.Decided += t.Decided
		r.Handed.Differ += t.Differ
	}
	decided := map[string]bool{}
	for _, d := range append(append([]string(nil), engine...), scanner...) {
		rule, _, _ := strings.Cut(d, " ")
		decided[rule] = true
	}
	for rule := range decided {
		s.rule(rule).Decided++
	}
	if same {
		return
	}
	s.differ++
	for _, rule := range symmetricRules(engine, scanner) {
		s.rule(rule).Differ++
	}
	s.recent = append(s.recent, ShadowDiff{At: rec.At, Kind: rec.Item.Kind, Number: rec.Item.Number, Rules: engine, Scanner: scanner})
	if len(s.recent) > maxShadowDiffs {
		s.recent = s.recent[len(s.recent)-maxShadowDiffs:]
	}
}

func (s *Shadow) rule(name string) *ShadowRule {
	r := s.byRule[name]
	if r == nil {
		r = &ShadowRule{}
		s.byRule[name] = r
	}
	return r
}

// Summary is the tally so far.
func (s *Shadow) Summary() ShadowSummary {
	s.mu.Lock()
	defer s.mu.Unlock()
	// A record differs once, however many rules it differs on.
	out := ShadowSummary{Since: s.since, Compared: s.compared, Agree: s.compared - s.differ, Gated: s.gated, ByRule: map[string]ShadowRule{}}
	for name, r := range s.byRule {
		c := *r
		if r.Handed != nil {
			h := *r.Handed
			c.Handed = &h
		}
		out.ByRule[name] = c
	}
	for i := len(s.recent) - 1; i >= 0; i-- {
		out.Recent = append(out.Recent, s.recent[i])
	}
	return out
}

// compare is the rules' decisions on a record and the scanner's, sorted,
// and whether they are the same.
// handedTally compares, for each rule the engine decided for the scanner
// on the record, the engine's decision (any outcome) with the scanner's
// own condition.
func handedTally(set Set, rec Record) map[string]HandedTally {
	if len(rec.Handed) == 0 {
		return nil
	}
	env := rec.Env
	if env.Now.IsZero() {
		env.Now = rec.At
	}
	ds := set.Decide(rec.Item, env)
	out := map[string]HandedTally{}
	for _, h := range rec.Handed {
		t := out[h.Rule]
		t.Decided++
		if Decided(ds, h.Rule) != h.Legacy {
			t.Differ++
		}
		out[h.Rule] = t
	}
	return out
}

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
