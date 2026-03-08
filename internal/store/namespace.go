package store

import (
	"fmt"
	"regexp"
	"strings"
)

// nsMaxLen is the maximum length of a namespace.
const nsMaxLen = 128

// nsSegmentRegex matches valid namespace segments: starts with a letter, digit,
// or hyphen (for negative IDs like Telegram group chats), followed by letters,
// digits, hyphens, or underscores.
var nsSegmentRegex = regexp.MustCompile(`^[a-zA-Z0-9-][a-zA-Z0-9_-]*$`)

// ValidateNS validates a namespace string. A valid namespace:
//   - Is non-empty and <= 128 chars
//   - Contains only alphanumeric, hyphens, underscores, and colons
//   - Each segment (split by `:`) is non-empty and matches [a-zA-Z0-9][a-zA-Z0-9_-]*
//   - No leading, trailing, or consecutive colons
func ValidateNS(ns string) error {
	if ns == "" {
		return fmt.Errorf("namespace cannot be empty")
	}
	if len(ns) > nsMaxLen {
		return fmt.Errorf("namespace too long (%d chars, max %d)", len(ns), nsMaxLen)
	}
	if strings.HasPrefix(ns, ":") || strings.HasSuffix(ns, ":") {
		return fmt.Errorf("namespace %q must not start or end with ':'", ns)
	}
	if strings.Contains(ns, "::") {
		return fmt.Errorf("namespace %q must not contain consecutive colons '::'", ns)
	}

	segments := strings.Split(ns, ":")
	for _, seg := range segments {
		if seg == "" {
			return fmt.Errorf("namespace %q contains an empty segment", ns)
		}
		if !nsSegmentRegex.MatchString(seg) {
			return fmt.Errorf("namespace segment %q is invalid — use letters, digits, hyphens, or underscores (must start with letter or digit)", seg)
		}
	}
	return nil
}

// NSFilter represents a parsed namespace filter.
type NSFilter struct {
	Pattern  string // The raw pattern without the trailing '*'
	IsPrefix bool   // true if the original filter ended with '*' or ':'
}

// ParseNSFilter parses a namespace filter string. Supports:
//   - "" -> match all (empty filter)
//   - "exact" -> exact match
//   - "prefix:*" -> prefix match (matches "prefix:anything")
func ParseNSFilter(filter string) NSFilter {
	if filter == "" {
		return NSFilter{}
	}
	if strings.HasSuffix(filter, ":*") {
		return NSFilter{
			Pattern:  strings.TrimSuffix(filter, "*"),
			IsPrefix: true,
		}
	}
	if strings.HasSuffix(filter, "*") {
		return NSFilter{
			Pattern:  strings.TrimSuffix(filter, "*"),
			IsPrefix: true,
		}
	}
	return NSFilter{Pattern: filter}
}

// MatchNS returns true if the given namespace matches this filter.
func (f NSFilter) MatchNS(ns string) bool {
	if f.Pattern == "" && !f.IsPrefix {
		return true // empty filter matches everything
	}
	if f.IsPrefix {
		return strings.HasPrefix(ns, f.Pattern)
	}
	return ns == f.Pattern
}

// SQL returns the WHERE clause fragment and argument for this filter.
// The column parameter is the SQL column name (e.g., "m.ns" or "ns").
// Returns ("", nil) if the filter matches all namespaces.
func (f NSFilter) SQL(column string) (string, []interface{}) {
	if f.Pattern == "" && !f.IsPrefix {
		return "", nil
	}
	if f.IsPrefix {
		return column + " LIKE ?", []interface{}{f.Pattern + "%"}
	}
	return column + " = ?", []interface{}{f.Pattern}
}

// NSSegments returns the hierarchical segments of a namespace.
// e.g., "reflect:agent-memory" -> ["reflect", "agent-memory"]
func NSSegments(ns string) []string {
	return strings.Split(ns, ":")
}

// NSParent returns the parent namespace, or "" if at the root.
// e.g., "reflect:agent-memory" -> "reflect"
func NSParent(ns string) string {
	idx := strings.LastIndex(ns, ":")
	if idx < 0 {
		return ""
	}
	return ns[:idx]
}

// NSDepth returns the nesting depth of a namespace.
// e.g., "reflect" -> 1, "reflect:agent-memory" -> 2
func NSDepth(ns string) int {
	return len(NSSegments(ns))
}
