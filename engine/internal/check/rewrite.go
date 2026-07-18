package check

import (
	"encoding/json"
	"math"
	"strings"

	"github.com/steveharsant/tone/engine/internal/provider"
)

func jsonUnmarshal(s string, v any) error { return json.Unmarshal([]byte(s), v) }

// BuildRewriteMessages assembles the prompt for a full-text rewrite — the
// "make this more formal" flow. Unlike checks, the model returns prose, not
// JSON, and the whole selection is rewritten as one piece.
func BuildRewriteMessages(text, tone, instruction string, styleRules []string) []provider.Message {
	var sb strings.Builder
	sb.WriteString("You rewrite text inside a writing assistant.\n")
	switch {
	case instruction != "":
		sb.WriteString("Rewrite the user's text following this instruction: ")
		sb.WriteString(instruction)
		sb.WriteString(".")
	case tone != "":
		sb.WriteString("Rewrite the user's text to sound more ")
		sb.WriteString(tone)
		sb.WriteString(", while fixing any spelling or grammar errors.")
	default:
		sb.WriteString("Rewrite the user's text to be clearer and error-free.")
	}
	// JSON output is demanded (and constrained via JSONMode) deliberately:
	// free-prose prompts tempt hybrid reasoning models into thinking out
	// loud in the content itself; a JSON grammar forces a direct answer.
	sb.WriteString(`

Rules:
- Preserve the meaning, facts, names, numbers, and language of the original.
- Preserve the original's line breaks and general structure.
- Keep roughly the original length unless the instruction says otherwise.
- Respond with ONLY this JSON object, nothing else: {"rewritten":"<the rewritten text>"}
- No explanations, no analysis, no steps — just the JSON.`)

	if len(styleRules) > 0 {
		sb.WriteString("\n\nThe writer enforces these personal style rules — never violate them:")
		for _, r := range styleRules {
			if r = strings.TrimSpace(r); r != "" {
				sb.WriteString("\n- " + r)
			}
		}
	}
	return []provider.Message{
		{Role: "system", Content: sb.String()},
		{Role: "user", Content: text},
	}
}

// ParseRewriteOutput extracts the rewritten text from the model's JSON
// reply, tolerating fences and falling back to treating the whole reply as
// prose if the JSON never materialized.
func ParseRewriteOutput(s string) string {
	cleaned := CleanRewriteOutput(s)
	start, end := strings.Index(cleaned, "{"), strings.LastIndex(cleaned, "}")
	if start >= 0 && end > start {
		var out struct {
			Rewritten string `json:"rewritten"`
		}
		if err := jsonUnmarshal(cleaned[start:end+1], &out); err == nil && strings.TrimSpace(out.Rewritten) != "" {
			return strings.TrimSpace(out.Rewritten)
		}
	}
	return cleaned
}

// CleanRewriteOutput strips the wrappers models love to add around prose:
// code fences and a single pair of enclosing quotes.
func CleanRewriteOutput(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		s = strings.TrimPrefix(s, "```")
		if i := strings.Index(s, "\n"); i >= 0 {
			s = s[i+1:]
		}
		if i := strings.LastIndex(s, "```"); i >= 0 {
			s = s[:i]
		}
		s = strings.TrimSpace(s)
	}
	for _, q := range [][2]string{{`"`, `"`}, {"“", "”"}, {"'", "'"}} {
		if len(s) > 1 && strings.HasPrefix(s, q[0]) && strings.HasSuffix(s, q[1]) &&
			!strings.Contains(s[len(q[0]):len(s)-len(q[1])], q[0]) {
			s = strings.TrimSpace(s[len(q[0]) : len(s)-len(q[1])])
		}
	}
	return s
}

// scoreWeights: how much each category hurts the writing score.
var scoreWeights = map[string]float64{
	CategoryCorrectness: 3,
	CategoryClarity:     1.5,
	CategoryEngagement:  1,
	CategoryDelivery:    1,
}

// Score summarizes text quality as 0–100 from weighted suggestion density —
// deterministic and free (no extra model call). 100 = nothing to flag.
func Score(text string, sugs []Suggestion) int {
	words := len(strings.Fields(text))
	if words == 0 {
		return 100
	}
	var weighted float64
	for _, s := range sugs {
		if w, ok := scoreWeights[s.Category]; ok {
			weighted += w
		} else {
			weighted++
		}
	}
	// Calibration: one typo in ~15 words ≈ 80; error-dense text bottoms out.
	score := 100 - int(math.Round(100*weighted/float64(words)))
	if score < 0 {
		return 0
	}
	if score > 100 {
		return 100
	}
	return score
}
