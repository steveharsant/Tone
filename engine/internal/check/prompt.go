package check

import (
	"strings"

	"github.com/steveharsant/tone/engine/internal/provider"
)

// categoryBriefs are the editing lenses the model may use. Which ones are
// active is driven by config — Phase 1 ships correctness+clarity, Phase 3
// turns on engagement+delivery with no pipeline changes.
var categoryBriefs = map[string]string{
	CategoryCorrectness: `"correctness": objective errors — spelling, grammar, punctuation, verb agreement, wrong or missing articles, incorrect word forms.`,
	CategoryClarity:     `"clarity": wordiness, redundancy, convoluted phrasing, passive voice where active reads better. The replacement must preserve meaning while being shorter or plainer.`,
	CategoryEngagement:  `"engagement": dull, vague, or repetitive word choice. Suggest a stronger or more precise word only when the improvement is obvious.`,
	CategoryDelivery:    `"delivery": tone problems — hedging, unintended bluntness, over-apologizing, mismatched formality.`,
}

const systemPreamble = `You are a precise copy editor inside a writing assistant. You receive one passage of text. Find problems and return them as JSON.

Respond with ONLY a JSON object, no prose, in exactly this shape:
{"suggestions":[{"original":"...","replacement":"...","category":"...","rule":"...","explanation":"..."}]}

Hard rules:
- "original" MUST be copied character-for-character from the input text (same case, same punctuation, same spacing). Never paraphrase it.
- Keep "original" as short as possible while still unambiguous — a word or short phrase, not the whole sentence, unless the whole sentence must change.
- "replacement" is the corrected text that should replace "original" verbatim.
- Never return a suggestion where "replacement" equals "original".
- Suggestions must not overlap each other.
- "rule" is a 1-3 word kebab-case label (e.g. "spelling", "subject-verb-agreement", "wordiness").
- "explanation" is at most 15 words, phrased for the writer.
- Do not invent problems. Correct text gets {"suggestions":[]}.
- Do not comment on stylistic choices that are already acceptable.

Allowed categories:`

// buildMessages assembles the chat for one segment.
func buildMessages(segText string, categories []string) []provider.Message {
	var sb strings.Builder
	sb.WriteString(systemPreamble)
	for _, cat := range categories {
		if brief, ok := categoryBriefs[cat]; ok {
			sb.WriteString("\n- ")
			sb.WriteString(brief)
		}
	}
	return []provider.Message{
		{Role: "system", Content: sb.String()},
		{Role: "user", Content: segText},
	}
}
