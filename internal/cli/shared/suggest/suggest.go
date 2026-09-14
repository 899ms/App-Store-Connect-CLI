package suggest

import (
	"sort"
	"strings"
)

type candidate struct {
	name  string
	score int
	dist  int
}

// Commands returns up to three likely command-name suggestions for the provided
// input, ranked by prefix relationship, then substring containment, then edit
// distance. It is intentionally conservative: if we aren't reasonably
// confident, it returns nil.
func Commands(input string, candidates []string) []string {
	in := strings.ToLower(strings.TrimSpace(input))
	if in == "" {
		return nil
	}

	collected := make([]candidate, 0, len(candidates))
	for _, raw := range candidates {
		name := strings.ToLower(strings.TrimSpace(raw))
		if name == "" || name == in {
			continue
		}

		d := editDistance(in, name)

		// Strongest signal: prefix relationship.
		if strings.HasPrefix(name, in) || strings.HasPrefix(in, name) {
			collected = append(collected, candidate{name: name, score: 0, dist: d})
			continue
		}
		// A remembered fragment of a hyphenated name (`phased` for
		// `phased-release`) or a name buried in a longer guess.
		if isSubstringMatch(in, name) {
			collected = append(collected, candidate{name: name, score: 1, dist: d})
			continue
		}
		if !withinThreshold(in, d) {
			continue
		}
		collected = append(collected, candidate{name: name, score: 2, dist: d})
	}

	if len(collected) == 0 {
		return nil
	}

	sort.Slice(collected, func(i, j int) bool {
		if collected[i].score != collected[j].score {
			return collected[i].score < collected[j].score
		}
		if collected[i].dist != collected[j].dist {
			return collected[i].dist < collected[j].dist
		}
		return collected[i].name < collected[j].name
	})

	const max = 3
	out := make([]string, 0, max)
	seen := make(map[string]struct{}, max)
	for _, c := range collected {
		if _, ok := seen[c.name]; ok {
			continue
		}
		seen[c.name] = struct{}{}
		out = append(out, c.name)
		if len(out) >= max {
			break
		}
	}
	return out
}

// Flags returns likely flag-name suggestions, including command-specific
// identifier forms such as id/version-id and subscription-id/id.
func Flags(input string, candidates []string) []string {
	suggestions := Commands(input, candidates)
	if len(suggestions) >= 3 {
		return suggestions
	}

	in := strings.ToLower(strings.TrimSpace(input))
	if in == "" {
		return suggestions
	}

	seen := make(map[string]struct{}, len(suggestions))
	for _, suggestion := range suggestions {
		seen[suggestion] = struct{}{}
	}

	suffixMatches := make([]string, 0)
	for _, raw := range candidates {
		name := strings.ToLower(strings.TrimSpace(raw))
		if name == "" || name == in {
			continue
		}
		identifierTypo := strings.HasSuffix(name, "-id") && withinThreshold(in, editDistance(in, "id"))
		if strings.HasSuffix(name, "-"+in) || strings.HasSuffix(in, "-"+name) || identifierTypo {
			if _, ok := seen[name]; !ok {
				suffixMatches = append(suffixMatches, name)
			}
		}
	}
	sort.Strings(suffixMatches)

	for _, match := range suffixMatches {
		suggestions = append(suggestions, match)
		if len(suggestions) == 3 {
			break
		}
	}
	return suggestions
}

// minSubstringMatchLength keeps two-letter fragments from matching half the
// command tree: a substring needs at least this many characters to count.
const minSubstringMatchLength = 3

func isSubstringMatch(input, name string) bool {
	if min(len(input), len(name)) < minSubstringMatchLength {
		return false
	}
	return strings.Contains(name, input) || strings.Contains(input, name)
}

func withinThreshold(input string, dist int) bool {
	n := len(input)
	// Conservative default thresholds that work well for short command names.
	switch {
	case n <= 4:
		return dist <= 1
	case n <= 7:
		return dist <= 2
	default:
		return dist <= 3
	}
}

// editDistance computes the optimal string alignment distance (Levenshtein
// plus adjacent transpositions counted as one edit), so `lsit` sits one edit
// from `list`. Command names are short ASCII, so the full matrix is cheap.
func editDistance(a, b string) int {
	if a == b {
		return 0
	}
	if a == "" {
		return len(b)
	}
	if b == "" {
		return len(a)
	}

	rows := make([][]int, len(a)+1)
	for i := range rows {
		rows[i] = make([]int, len(b)+1)
		rows[i][0] = i
	}
	for j := 0; j <= len(b); j++ {
		rows[0][j] = j
	}

	for i := 1; i <= len(a); i++ {
		for j := 1; j <= len(b); j++ {
			cost := 0
			if a[i-1] != b[j-1] {
				cost = 1
			}
			best := min(rows[i-1][j]+1, rows[i][j-1]+1, rows[i-1][j-1]+cost)
			if i > 1 && j > 1 && a[i-1] == b[j-2] && a[i-2] == b[j-1] {
				best = min(best, rows[i-2][j-2]+1)
			}
			rows[i][j] = best
		}
	}

	return rows[len(a)][len(b)]
}
