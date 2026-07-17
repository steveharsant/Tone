// Package catalog holds the curated model list shown by the setup wizard.
// Curated deliberately: five options with honest tradeoffs beats a wall of
// tags. Modern small instruct models + constrained JSON decoding beat older
// grammar fine-tunes for this task.
package catalog

type Model struct {
	Tag      string  `json:"tag"` // ollama model tag
	Name     string  `json:"name"`
	SizeGB   float64 `json:"size_gb"`   // approximate download at Q4
	MinRAMGB int     `json:"min_ram_gb"` // rough comfortable minimum (RAM or VRAM)
	Pitch    string  `json:"pitch"`
	Default  bool    `json:"default"` // pre-selected for the 8GB+ VRAM tier
}

var Curated = []Model{
	{
		Tag:      "qwen3:8b",
		Name:     "Qwen3 8B",
		SizeGB:   5.2,
		MinRAMGB: 8,
		Pitch:    "Best quality in range: strongest JSON reliability, nuanced clarity and tone suggestions, multilingual. Wants 8GB VRAM or 16GB RAM.",
		Default:  true,
	},
	{
		Tag:      "qwen3:4b",
		Name:     "Qwen3 4B",
		SizeGB:   2.6,
		MinRAMGB: 6,
		Pitch:    "Best balance for modest hardware; still reliable structured output, slightly blunter style advice.",
	},
	{
		Tag:      "gemma3:4b",
		Name:     "Gemma 3 4B",
		SizeGB:   3.3,
		MinRAMGB: 6,
		Pitch:    "Strongest natural-prose rewrites per size; a touch less strict about JSON (the engine compensates).",
	},
	{
		Tag:      "llama3.2:3b",
		Name:     "Llama 3.2 3B",
		SizeGB:   2.0,
		MinRAMGB: 4,
		Pitch:    "Fastest acceptable option; solid spelling and grammar, weakest at tone and engagement nuance.",
	},
	{
		Tag:      "qwen3:1.7b",
		Name:     "Qwen3 1.7B",
		SizeGB:   1.4,
		MinRAMGB: 3,
		Pitch:    "Low-end fallback for old or busy machines: correctness-only mode, style categories disabled.",
	},
}

// ByTag returns the curated entry for a tag, if present.
func ByTag(tag string) (Model, bool) {
	for _, m := range Curated {
		if m.Tag == tag {
			return m, true
		}
	}
	return Model{}, false
}
