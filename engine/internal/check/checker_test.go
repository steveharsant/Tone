package check

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/steveharsant/tone/engine/internal/provider"
)

// fakeProvider returns canned content keyed by a substring of the user
// message, and counts calls so cache behavior can be asserted.
type fakeProvider struct {
	responses map[string]string // substring of segment → raw model output
	calls     atomic.Int64
}

func (f *fakeProvider) Name() string { return "fake" }

func (f *fakeProvider) Complete(_ context.Context, req provider.Request) (string, error) {
	f.calls.Add(1)
	user := req.Messages[len(req.Messages)-1].Content
	for k, v := range f.responses {
		if strings.Contains(user, k) {
			return v, nil
		}
	}
	return `{"suggestions":[]}`, nil
}

func TestCheckerEndToEnd(t *testing.T) {
	fake := &fakeProvider{responses: map[string]string{
		"definately": `{"suggestions":[{"original":"definately","replacement":"definitely","category":"correctness","rule":"spelling","explanation":"Misspelling."}]}`,
		"in order to": "```json\n{\"suggestions\":[{\"original\":\"in order to\",\"replacement\":\"to\",\"category\":\"clarity\",\"rule\":\"wordiness\",\"explanation\":\"Wordy.\"}]}\n```",
	}}
	c := New(fake, "test-model", NewCache(16))

	text := "I will definately come. We met in order to talk."
	sugs, stats, err := c.Check(context.Background(), text, Options{Spelling: true, Grammar: true, Clarity: true})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Segments != 2 || stats.CacheHits != 0 {
		t.Errorf("stats = %+v", stats)
	}
	if len(sugs) != 2 {
		t.Fatalf("got %d suggestions: %+v", len(sugs), sugs)
	}
	// Spans are UTF-16 (== byte offsets here, all ASCII) over the whole doc.
	if got := text[sugs[0].Span.Start:sugs[0].Span.End]; got != "definately" {
		t.Errorf("span 0 covers %q", got)
	}
	if got := text[sugs[1].Span.Start:sugs[1].Span.End]; got != "in order to" {
		t.Errorf("span 1 covers %q", got)
	}
	if sugs[0].Category != "correctness" || sugs[1].Category != "clarity" {
		t.Errorf("categories: %s, %s", sugs[0].Category, sugs[1].Category)
	}
	if sugs[0].ID == "" || sugs[0].ID == sugs[1].ID {
		t.Error("suggestion IDs must be unique and non-empty")
	}

	// Second pass: everything unchanged → all from cache, no provider calls.
	before := fake.calls.Load()
	_, stats2, err := c.Check(context.Background(), text, Options{Spelling: true, Grammar: true, Clarity: true})
	if err != nil {
		t.Fatal(err)
	}
	if stats2.CacheHits != 2 {
		t.Errorf("cache hits = %d, want 2", stats2.CacheHits)
	}
	if fake.calls.Load() != before {
		t.Error("provider was called despite full cache hit")
	}
}

func TestCheckerFiltersDisabledCategories(t *testing.T) {
	fake := &fakeProvider{responses: map[string]string{
		"quick": `{"suggestions":[{"original":"quick","replacement":"rapid","category":"engagement","rule":"word-choice","explanation":"Stronger."}]}`,
	}}
	c := New(fake, "m", NewCache(16))
	sugs, _, err := c.Check(context.Background(), "The quick fix.", Options{Spelling: true, Grammar: true, Clarity: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(sugs) != 0 {
		t.Fatalf("disabled category leaked through: %+v", sugs)
	}
}

func TestCheckerUnicodeSpans(t *testing.T) {
	fake := &fakeProvider{responses: map[string]string{
		"tehm": `{"suggestions":[{"original":"tehm","replacement":"them","category":"correctness","rule":"spelling","explanation":"Typo."}]}`,
	}}
	c := New(fake, "m", NewCache(16))
	text := "😀😀 I saw tehm yesterday."
	sugs, _, err := c.Check(context.Background(), text, Options{Spelling: true, Grammar: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(sugs) != 1 {
		t.Fatalf("got %d suggestions", len(sugs))
	}
	// In UTF-16: two emoji = 4 units, space = 1, "I saw " = 6 → start 11.
	if sugs[0].Span.Start != 11 || sugs[0].Span.End != 15 {
		t.Errorf("span = %+v, want {11 15}", sugs[0].Span)
	}
}

func TestParseRawsTolerance(t *testing.T) {
	cases := []string{
		`{"suggestions":[{"original":"a","replacement":"b","category":"correctness"}]}`,
		"```json\n{\"suggestions\":[{\"original\":\"a\",\"replacement\":\"b\",\"category\":\"correctness\"}]}\n```",
		"Here you go:\n{\"suggestions\":[{\"original\":\"a\",\"replacement\":\"b\",\"category\":\"correctness\"}]}",
		`[{"original":"a","replacement":"b","category":"correctness"}]`,
	}
	for i, in := range cases {
		if got := parseRaws(in); len(got) != 1 || got[0].Original != "a" {
			t.Errorf("case %d: parseRaws = %+v", i, got)
		}
	}
	if got := parseRaws("total garbage, no json"); got != nil {
		t.Errorf("garbage should parse to nil, got %+v", got)
	}
}

func TestCacheEviction(t *testing.T) {
	c := NewCache(2)
	c.Put("a", nil)
	c.Put("b", []RawSuggestion{{Original: "x"}})
	if _, ok := c.Get("a"); !ok {
		t.Fatal("'a' should still be cached (empty results are cacheable)")
	}
	c.Put("c", nil) // evicts LRU = "b" (a was touched by Get)
	if _, ok := c.Get("b"); ok {
		t.Error("'b' should have been evicted")
	}
	if _, ok := c.Get("a"); !ok {
		t.Error("'a' should survive eviction")
	}
}

func TestCheckerDisabledRules(t *testing.T) {
	fake := &fakeProvider{responses: map[string]string{
		"in order to": `{"suggestions":[{"original":"in order to","replacement":"to","category":"clarity","rule":"Wordiness","explanation":"Wordy."}]}`,
	}}
	c := New(fake, "m", NewCache(16))
	sugs, _, err := c.Check(context.Background(), "We met in order to talk.",
		Options{Clarity: true, DisabledRules: []string{"wordiness"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(sugs) != 0 {
		t.Fatalf("disabled rule leaked through: %+v", sugs)
	}
}

func TestStyleRulesEnableDeliveryAndReachPrompt(t *testing.T) {
	opts := Options{Spelling: true, StyleRules: []string{"Do not use contractions"}}
	if !opts.AllowedCategories()[CategoryDelivery] {
		t.Fatal("style rules must enable the delivery category")
	}
	msgs := buildMessages("Don't worry.", opts)
	if !strings.Contains(msgs[0].Content, "Do not use contractions") {
		t.Fatal("style rule missing from system prompt")
	}
}

func TestOptionsKeyChangesInvalidateCache(t *testing.T) {
	a := Options{Spelling: true}
	b := Options{Spelling: true, ToneTarget: "formal"}
	if a.key() == b.key() {
		t.Fatal("differing options must produce differing cache keys")
	}
}

func TestCheckTieredEmitsAllTiersConcurrently(t *testing.T) {
	fake := &fakeProvider{responses: map[string]string{
		"definately": `{"suggestions":[{"original":"definately","replacement":"definitely","category":"correctness","rule":"spelling","explanation":"Typo."}]}`,
	}}
	c := New(fake, "m", NewCache(64))
	got := map[string]int{}
	var mu sync.Mutex
	err := c.CheckTiered(context.Background(), "I will definately come.",
		Options{Spelling: true, Grammar: true, Clarity: true},
		func(tier string, sugs []Suggestion, _ Stats) {
			mu.Lock()
			got[tier] = len(sugs)
			mu.Unlock()
		})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("tiers emitted = %v, want spelling/grammar/clarity", got)
	}
	// The fake answers every tier's prompt the same way; correctness-category
	// suggestions only survive the spelling and grammar passes.
	if got["spelling"] != 1 || got["grammar"] != 1 || got["clarity"] != 0 {
		t.Errorf("per-tier counts = %v", got)
	}
}

func TestScore(t *testing.T) {
	text := "This is a twenty word sentence used for scoring the writing quality of a document with some words padded."
	if got := Score(text, nil); got != 100 {
		t.Errorf("clean text score = %d, want 100", got)
	}
	one := []Suggestion{{Category: CategoryCorrectness}}
	got := Score(text, one)
	if got >= 100 || got < 60 {
		t.Errorf("one error in ~19 words = %d, want moderate penalty", got)
	}
	many := []Suggestion{
		{Category: CategoryCorrectness}, {Category: CategoryCorrectness},
		{Category: CategoryCorrectness}, {Category: CategoryCorrectness},
		{Category: CategoryCorrectness}, {Category: CategoryCorrectness},
	}
	if got := Score("short bad text here", many); got != 0 {
		t.Errorf("error-dense text = %d, want 0", got)
	}
	if got := Score("", nil); got != 100 {
		t.Errorf("empty text = %d, want 100", got)
	}
}

func TestCleanRewriteOutput(t *testing.T) {
	cases := map[string]string{
		"\"Hello there.\"":              "Hello there.",
		"```\nHello there.\n```":        "Hello there.",
		"```text\nHello there.\n```":    "Hello there.",
		"  Hello there.  ":              "Hello there.",
		`He said "hi" and "bye" today.`: `He said "hi" and "bye" today.`, // interior quotes stay
	}
	for in, want := range cases {
		if got := CleanRewriteOutput(in); got != want {
			t.Errorf("CleanRewriteOutput(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseRewriteOutput(t *testing.T) {
	cases := map[string]string{
		`{"rewritten":"Hello there."}`:                      "Hello there.",
		"```json\n{\"rewritten\":\"Hello there.\"}\n```":    "Hello there.",
		`plain prose fallback with no json braces`:          "plain prose fallback with no json braces",
		`{"rewritten":""}`:                                  `{"rewritten":""}`, // empty JSON → fall back to raw
	}
	for in, want := range cases {
		if got := ParseRewriteOutput(in); got != want {
			t.Errorf("ParseRewriteOutput(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAlternativesFlowThrough(t *testing.T) {
	fake := &fakeProvider{responses: map[string]string{
		"hers": `{"suggestions":[{"original":"hers","replacement":"here","alternatives":["her's","hers","here"],"category":"correctness","rule":"spelling","explanation":"Likely meant 'here'."}]}`,
	}}
	c := New(fake, "m", NewCache(16))
	sugs, _, err := c.Check(context.Background(), "Come over hers tomorrow.", Options{Spelling: true, Grammar: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(sugs) != 1 {
		t.Fatalf("got %d suggestions", len(sugs))
	}
	// "here" duplicates the replacement, "hers" duplicates the original —
	// only "her's" survives.
	if len(sugs[0].Alternatives) != 1 || sugs[0].Alternatives[0] != "her's" {
		t.Errorf("alternatives = %v", sugs[0].Alternatives)
	}
}

func TestCapitalizationRule(t *testing.T) {
	fake := &fakeProvider{responses: map[string]string{}}
	c := New(fake, "m", NewCache(16))
	text := "this sentence starts lowercase. This one is fine. iPhone sales rose."
	sugs, _, err := c.Check(context.Background(), text, Options{Grammar: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(sugs) != 1 {
		t.Fatalf("got %d suggestions: %+v", len(sugs), sugs)
	}
	s := sugs[0]
	if s.Rule != "capitalization" || s.Original != "t" || s.Replacement != "T" || s.Span.Start != 0 {
		t.Errorf("suggestion = %+v", s)
	}
	// Muting the rule silences it.
	sugs2, _, _ := c.Check(context.Background(), text, Options{Grammar: true, DisabledRules: []string{"capitalization"}})
	if len(sugs2) != 0 {
		t.Errorf("muted capitalization still flagged: %+v", sugs2)
	}
	// Spelling-only pass must not emit it (grammar owns the rule).
	sugs3, _, _ := c.Check(context.Background(), text, Options{Spelling: true})
	if len(sugs3) != 0 {
		t.Errorf("spelling pass emitted capitalization: %+v", sugs3)
	}
}

func TestLanguageAffectsPromptAndCacheKey(t *testing.T) {
	gb := Options{Spelling: true, Language: "en-GB"}
	us := Options{Spelling: true, Language: "en-US"}
	if gb.key() == us.key() {
		t.Fatal("language must be part of the cache key")
	}
	msgs := buildMessages("The colour is nice.", gb)
	if !strings.Contains(msgs[0].Content, "BRITISH") {
		t.Error("British instruction missing from prompt")
	}
	tiers := TiersFor(gb)
	if len(tiers) == 0 || tiers[0].Opts.Language != "en-GB" {
		t.Error("language must propagate into tier options")
	}
}
