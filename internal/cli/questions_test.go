package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// asked finds the questions keelbox asks, by the call that asks them: the
// method, and the start of the question as the source writes it.
var asked = regexp.MustCompile(`\.(Choose|Confirm|AskValid|AskOptional|AskOr|AskTyped|Ask)\(((?:"[^"]*"|[^,"()]|\([^()]*\))+)`)

// literal is a string literal of a question's first argument.
var literal = regexp.MustCompile(`"[^"]*"`)

// question is the question an argument builds, with NAME where it puts a
// value in: the format of a Sprintf, or the literals of a concatenation.
func question(arg string) string {
	parts := literal.FindAllString(arg, -1)
	for i, p := range parts {
		parts[i] = strings.Trim(p, `"`)
	}
	if len(parts) == 0 {
		return ""
	}
	if strings.HasPrefix(strings.TrimSpace(arg), "fmt.Sprintf(") {
		return parts[0]
	}
	return strings.Join(parts, "NAME")
}

// Questions end the same way. A menu or a yes or no is a question and ends
// with a question mark; what asks for a typed value is a noun phrase, to
// which the prompt adds the colon. No question carries a config key: that
// is for the hint above it.
func TestQuestionsEndTheSameWay(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	key := regexp.MustCompile(`\b[a-z_]+\.[a-z_]+(\.[a-z_]+)*:? `)
	seen := 0
	for _, f := range files {
		// park and resize word their own questions.
		if strings.HasSuffix(f, "_test.go") || strings.Contains(f, "park") || f == "resize.go" {
			continue
		}
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range asked.FindAllStringSubmatch(string(data), -1) {
			method := m[1]
			q := question(m[2])
			if q == "" {
				continue // a question built elsewhere and passed in
			}
			seen++
			switch method {
			case "Choose", "Confirm":
				if !strings.HasSuffix(q, "?") {
					t.Errorf("%s: %s(%q): a menu or a yes or no ends with a question mark", f, method, q)
				}
			default:
				if strings.HasSuffix(q, ":") || strings.HasSuffix(q, "?") || strings.HasSuffix(q, ")") {
					t.Errorf("%s: %s(%q): a typed value is asked for with a noun phrase, without a colon or brackets", f, method, q)
				}
			}
			if key.MatchString(q + " ") {
				t.Errorf("%s: %s(%q): a config key belongs in the hint, not the question", f, method, q)
			}
		}
	}
	if seen < 25 {
		t.Errorf("found %d questions; the pattern no longer matches how they are asked", seen)
	}
}
