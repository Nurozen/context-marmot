package mcp

import (
	"sort"
	"strings"
	"unicode"

	"github.com/nurozen/context-marmot/internal/node"
)

// lexicalStopwords is a tiny stoplist used to avoid trivial-token noise in the
// lexical fallback. Kept intentionally small — the goal is just to drop the
// most obviously useless tokens, not to do real IR-grade text processing.
var lexicalStopwords = map[string]bool{
	"a":   true,
	"an":  true,
	"the": true,
	"is":  true,
	"of":  true,
	"in":  true,
	"to":  true,
	"and": true,
	"or":  true,
	"on":  true,
	"for": true,
}

// tokenize lowercases the input and splits on any non-letter/digit rune.
// Stopwords and empty tokens are dropped.
func tokenize(s string) []string {
	if s == "" {
		return nil
	}
	fields := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if f == "" || lexicalStopwords[f] {
			continue
		}
		out = append(out, f)
	}
	return out
}

// containsToken reports whether haystack (already lower-cased) contains the
// given token as a substring. Substring rather than whole-word so e.g.
// "auth" matches "authentication".
func containsToken(haystackLower, token string) bool {
	if token == "" {
		return false
	}
	return strings.Contains(haystackLower, token)
}

// scoredNode pairs a node with its lexical score for sorting.
type scoredNode struct {
	node  *node.Node
	score float64
}

// LexicalSearch performs keyword-based scoring of nodes against a query.
// Used as a fallback when embeddings are unavailable.
//
// Scoring:
//   - +3.0 if any query token appears in node.Summary
//   - +1.0 if any query token appears in node.Context
//   - +0.5 per matching tag
//
// Returns up to topK nodes with score > 0, sorted by score descending.
// An empty query or empty input slice returns nil.
func LexicalSearch(query string, nodes []*node.Node, topK int) []*node.Node {
	tokens := tokenize(query)
	if len(tokens) == 0 || len(nodes) == 0 || topK <= 0 {
		return nil
	}

	scored := make([]scoredNode, 0, len(nodes))
	for _, n := range nodes {
		if n == nil {
			continue
		}
		var score float64

		// Summary match (+3.0 if any token matches).
		summaryLower := strings.ToLower(n.Summary)
		for _, tok := range tokens {
			if containsToken(summaryLower, tok) {
				score += 3.0
				break
			}
		}

		// Context match (+1.0 if any token matches).
		contextLower := strings.ToLower(n.Context)
		for _, tok := range tokens {
			if containsToken(contextLower, tok) {
				score += 1.0
				break
			}
		}

		// Tag match (+0.5 per matching tag, counted once per tag).
		for _, tag := range n.Tags {
			tagLower := strings.ToLower(tag)
			for _, tok := range tokens {
				if containsToken(tagLower, tok) {
					score += 0.5
					break
				}
			}
		}

		if score > 0 {
			scored = append(scored, scoredNode{node: n, score: score})
		}
	}

	// Sort by score descending. Stable sort keeps relative order for ties.
	sort.SliceStable(scored, func(i, j int) bool {
		return scored[i].score > scored[j].score
	})

	if len(scored) > topK {
		scored = scored[:topK]
	}

	out := make([]*node.Node, len(scored))
	for i, s := range scored {
		out[i] = s.node
	}
	return out
}
