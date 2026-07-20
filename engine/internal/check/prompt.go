package check

import (
	"strings"

	"github.com/steveharsant/tone/engine/internal/provider"
)

// Options carries everything that shapes a check: which lenses are active,
// the target voice, and the user's own style rules. All fields participate
// in the cache key — changing any of them invalidates cached results.
type Options struct {
	Spelling   bool
	Grammar    bool
	Clarity    bool
	Vocabulary bool
	Tone       bool
	// ToneTarget: "", "formal", "casual", "confident", "friendly", "academic".
	ToneTarget string
	// StyleRules are user-authored constraints, e.g. "Do not use contractions".
	StyleRules []string
	// DisabledRules suppresses suggestions whose rule slug matches
	// (case-insensitive, dashes/spaces interchangeable).
	DisabledRules []string
}

// AllowedCategories is the set of wire categories the enabled checks can emit.
func (o Options) AllowedCategories() map[string]bool {
	m := map[string]bool{}
	if o.Spelling || o.Grammar {
		m[CategoryCorrectness] = true
	}
	if o.Clarity {
		m[CategoryClarity] = true
	}
	if o.Vocabulary {
		m[CategoryEngagement] = true
	}
	if o.Tone || len(o.StyleRules) > 0 {
		// Style-rule violations report as "delivery" even with the tone
		// check off — the user asked for those rules explicitly.
		m[CategoryDelivery] = true
	}
	return m
}

// promptVersion MUST be bumped whenever any prompt template above changes:
// cached results are keyed on options + segment text only, so without a
// version, stale results from the old prompts would be served forever.
const promptVersion = "pv5"

// key serializes the prompt-affecting state for cache keying.
func (o Options) key() string {
	var sb strings.Builder
	sb.WriteString(promptVersion)
	sb.WriteByte('|')
	for _, b := range []bool{o.Spelling, o.Grammar, o.Clarity, o.Vocabulary, o.Tone} {
		if b {
			sb.WriteByte('1')
		} else {
			sb.WriteByte('0')
		}
	}
	sb.WriteByte('|')
	sb.WriteString(o.ToneTarget)
	sb.WriteByte('|')
	sb.WriteString(strings.Join(o.StyleRules, "\x1f"))
	return sb.String()
}

const systemPreamble = `You are a precise copy editor inside a writing assistant. You receive one passage of text. Find problems and return them as JSON.

Respond with ONLY a JSON object, no prose, in exactly this shape:
{"suggestions":[{"original":"...","replacement":"...","alternatives":["..."],"category":"...","rule":"...","explanation":"..."}]}

Hard rules:
- "original" MUST be copied character-for-character from the input text (same case, same punctuation, same spacing). Never paraphrase it.
- Keep "original" as short as possible while covering the COMPLETE fix. When adjacent words are part of one error, correct them together in a single suggestion: "sum thing" → "something" as ONE suggestion, never "sum" → "some" followed by a leftover. Never split one fix into pieces, and never cover a whole sentence unless the whole sentence must change.
- "replacement" is the corrected text that should replace "original" verbatim. It must consist of real, correctly spelled words — never invent words.
- Choose the replacement the writer most likely INTENDED given the whole sentence, not the closest-looking edit: a typo like "hers" in "come over hers" means "here", not "her's".
- "alternatives" (optional, up to 2) lists other plausible corrections when the intent is ambiguous. Omit or leave empty when there is only one sensible fix.
- Never return a suggestion where "replacement" equals "original".
- Suggestions must not overlap each other.
- "rule" is a 1-3 word kebab-case label (e.g. "spelling", "subject-verb-agreement", "wordiness").
- "explanation" is at most 15 words, phrased for the writer.
- Do not invent problems. Correct text gets {"suggestions":[]}.
- Do not comment on stylistic choices that are already acceptable.

Only look for the problem types listed below, and use exactly the category slug shown for each:`

func buildMessages(segText string, opts Options) []provider.Message {
	var sb strings.Builder
	sb.WriteString(systemPreamble)

	if opts.Spelling {
		sb.WriteString("\n- category \"correctness\": spelling mistakes and typos, including in informal text (\"whatts\" → \"what's\"). Also flag slang spellings of real words (\"dawg\" → \"dog\", \"doin\" → \"doing\") — the writer can dismiss them if intentional.")
	}
	if opts.Grammar {
		sb.WriteString("\n- category \"correctness\": grammar and punctuation errors — verb agreement, tense, articles, commas, apostrophes, missing words, and capitalization (sentences start with a capital letter). Flag dropped verbs and non-standard forms even in casual writing (\"How you doin?\" → \"How are you doing?\").")
	}
	if opts.Clarity {
		sb.WriteString("\n- category \"clarity\": wordiness, redundancy, convoluted phrasing, passive voice where active reads better. The replacement must preserve meaning while being shorter or plainer.")
	}
	if opts.Vocabulary {
		sb.WriteString("\n- category \"engagement\": dull, vague, or repetitive word choice. Suggest a stronger or more precise word only when the improvement is obvious.")
	}
	if opts.Tone {
		sb.WriteString("\n- category \"delivery\": tone problems — hedging, unintended bluntness, over-apologizing, mismatched formality.")
		if opts.ToneTarget != "" {
			sb.WriteString(" The writer wants a " + opts.ToneTarget + " tone; flag wording that clearly works against it and suggest a replacement that fits.")
		}
	}

	if len(opts.StyleRules) > 0 {
		sb.WriteString("\n\nThe writer also enforces these personal style rules. Flag violations (category \"delivery\", rule \"style-rule\") and never suggest anything that would break them:")
		for _, r := range opts.StyleRules {
			r = strings.TrimSpace(r)
			if r != "" {
				sb.WriteString("\n- " + r)
			}
		}
	}

	return []provider.Message{
		{Role: "system", Content: sb.String()},
		{Role: "user", Content: segText},
	}
}
