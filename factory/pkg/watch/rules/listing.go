package rules

import (
	"slices"
	"strings"
)

// A scanner sees only the items its listings bring in: those assigned to
// the pool, those carrying the trigger label (and, on issues, those the
// watcher opened). A rule nothing narrows that way - a rule on someone
// else's pull request, say - is reached through its labels instead: the
// labels in the overseer's own namespace (Env.Prefixes: overseer/...,
// <trigger>/...) its condition requires are listed too (ListingLabels).
// A label outside that namespace widens nothing, so mislabelling cannot
// make the watch list arbitrary items.

// ListingLabels are the labels the scanner of kind lists besides the
// trigger label, for the enabled rules on kind that the pool and trigger
// listings do not reach: the namespaced labels of their labels.all and
// labels.any (any-of alternatives too), placeholders filled and */x for
// each prefix. The gate and hand-over labels (*/stop, */ready-for-human)
// and the trigger label itself are never among them. Sorted, unique.
func (s Set) ListingLabels(kind string, env Env) []string {
	var out []string
	for _, r := range s {
		if r.On != kind || !r.IsEnabled() || r.When.narrowed(env) {
			continue
		}
		for _, w := range append([]When{r.When}, r.When.AnyOf...) {
			for _, l := range w.listingLabels(env) {
				if !slices.Contains(out, l) {
					out = append(out, l)
				}
			}
		}
	}
	slices.Sort(out)
	return out
}

// Unlisted are the enabled rules on kind no listing reaches: nothing in
// their condition narrows them to the pool or the trigger label, and they
// name no namespaced label to list (or an any-of alternative of theirs
// names none). They see only items listed for other rules; the watch warns.
func (s Set) Unlisted(kind string, env Env) []string {
	var out []string
	for _, r := range s {
		if r.On != kind || !r.IsEnabled() || r.When.narrowed(env) || len(r.When.listingLabels(env)) > 0 {
			continue
		}
		reached := len(r.When.AnyOf) > 0
		for _, alt := range r.When.AnyOf {
			if !alt.narrowed(env) && len(alt.listingLabels(env)) == 0 {
				reached = false
			}
		}
		if !reached {
			out = append(out, r.Name)
		}
	}
	return out
}

// AdmitsAuthor is whether an enabled rule on the item's kind could decide
// on an item of its author: its author predicate, any-of alternatives
// included, lets the author in. A scanner stops at an item none admits -
// a pull request someone outside the pool opened, when every rule says
// author: pool - before reading anything more of it.
func (s Set) AdmitsAuthor(it Item, env Env) bool {
	return slices.ContainsFunc(s, func(r *Rule) bool {
		if r.On != it.Kind || !r.IsEnabled() || !r.When.admitsAuthor(it, env) {
			return false
		}
		return len(r.When.AnyOf) == 0 || slices.ContainsFunc(r.When.AnyOf, func(alt When) bool { return alt.admitsAuthor(it, env) })
	})
}

// narrowed is whether a listing other than by label reaches what w admits:
// the pool's (author or assignee pool), the watcher's own (opened-by), or
// the trigger label's.
func (w When) narrowed(env Env) bool {
	if w.Author.Is(AuthorPool) || w.Assignee == "pool" || w.OpenedBy == "watcher" {
		return true
	}
	if l := w.Labels; l != nil && env.Trigger != "" {
		trigger := func(p string) bool { return strings.EqualFold(fill(p, env), env.Trigger) }
		if slices.ContainsFunc(l.All, trigger) || (len(l.Any) > 0 && !slices.ContainsFunc(l.Any, func(p string) bool { return !trigger(p) })) {
			return true
		}
	}
	return len(w.AnyOf) > 0 && !slices.ContainsFunc(w.AnyOf, func(alt When) bool { return !alt.narrowed(env) })
}

// listingLabels are w's own required labels (not its any-of's) a listing
// can narrow on: in the overseer's namespace, never a gate or hand-over
// label.
func (w When) listingLabels(env Env) []string {
	l := w.Labels
	if l == nil {
		return nil
	}
	var out []string
	for _, p := range append(slices.Clone(l.All), l.Any...) {
		for _, name := range expandLabel(p, env) {
			if namespaced(name, env) && !slices.Contains(out, name) {
				out = append(out, name)
			}
		}
	}
	return out
}

// expandLabel is the label names a pattern matches: */x in each prefix,
// else the name with its placeholders filled.
func expandLabel(p string, env Env) []string {
	if suffix, ok := strings.CutPrefix(p, "*/"); ok {
		var out []string
		for _, pfx := range env.Prefixes {
			out = append(out, pfx+"/"+suffix)
		}
		return out
	}
	return []string{fill(p, env)}
}

// namespaced is whether name is a label of the overseer's own a listing may
// narrow on: <prefix>/<x> for one of its prefixes, x not stop or
// ready-for-human (gated, or handed to the humans: no rule acts on them).
func namespaced(name string, env Env) bool {
	for _, pfx := range env.Prefixes {
		if rest, ok := strings.CutPrefix(strings.ToLower(name), strings.ToLower(pfx)+"/"); ok && rest != "" {
			return rest != "stop" && rest != "ready-for-human"
		}
	}
	return false
}
