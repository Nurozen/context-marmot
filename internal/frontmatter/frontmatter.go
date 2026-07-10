// Package frontmatter splits line-anchored YAML frontmatter from a markdown
// body. Both delimiters must be "---" alone on their own line (trailing
// spaces, tabs, and \r are tolerated — editors produce them), so a "---"
// inside a YAML value or in the body can never be mistaken for the closing
// delimiter.
package frontmatter

import (
	"errors"
	"strings"
)

var (
	// ErrMissing is returned when the content does not open with a "---" line.
	ErrMissing = errors.New("missing YAML frontmatter")
	// ErrUnterminated is returned when no closing "---" line exists.
	ErrUnterminated = errors.New("unterminated YAML frontmatter")
)

// Split separates anchored YAML frontmatter from the body. The opening
// delimiter must be "---" alone on the first line; the closing delimiter
// must be "---" alone on its own line. The body is everything after the
// closing delimiter line (the delimiter's own line terminator is consumed,
// matching what writers emit: "---\n<yaml>---\n<body>").
func Split(data []byte) (yamlBlock []byte, body string, err error) {
	content := string(data)
	firstEnd := strings.IndexByte(content, '\n')
	var rest string
	if firstEnd < 0 {
		if !isDelimiterLine(content) {
			return nil, "", ErrMissing
		}
		rest = ""
	} else {
		if !isDelimiterLine(content[:firstEnd]) {
			return nil, "", ErrMissing
		}
		rest = content[firstEnd+1:]
	}

	for idx := 0; idx <= len(rest); {
		lineEnd := strings.IndexByte(rest[idx:], '\n')
		if lineEnd < 0 {
			if isDelimiterLine(rest[idx:]) {
				return []byte(rest[:idx]), "", nil
			}
			break
		}
		if isDelimiterLine(rest[idx : idx+lineEnd]) {
			return []byte(rest[:idx]), rest[idx+lineEnd+1:], nil
		}
		idx += lineEnd + 1
	}
	return nil, "", ErrUnterminated
}

// isDelimiterLine reports whether line (without its \n) is exactly "---",
// optionally followed by spaces, tabs, or a trailing \r. "----" and
// "--- foo" are NOT delimiters.
func isDelimiterLine(line string) bool {
	if !strings.HasPrefix(line, "---") {
		return false
	}
	return strings.TrimRight(line[3:], " \t\r") == ""
}
