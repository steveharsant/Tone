package store

import (
	"path/filepath"
	"testing"
)

func TestPersistenceAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddWord("Avetta"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddDismissal("clarity", "in order to"); err != nil {
		t.Fatal(err)
	}

	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if !s2.HasWord("avetta") { // case-insensitive
		t.Error("dictionary word lost across reopen")
	}
	if !s2.IsDismissed("clarity", "in order to") {
		t.Error("dismissal lost across reopen")
	}
	if s2.HasWord("kubernetes") || s2.IsDismissed("clarity", "other") {
		t.Error("false positives")
	}
}

func TestAddWordIdempotentAndRemovable(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "store.json"))
	s.AddWord("Tone")
	s.AddWord("tone")
	if got := len(s.Words()); got != 1 {
		t.Fatalf("duplicate word stored: %d entries", got)
	}
	s.RemoveWord("TONE")
	if len(s.Words()) != 0 {
		t.Fatal("remove failed")
	}
}

func TestClearDismissals(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "store.json"))
	s.AddDismissal("correctness", "teh")
	if s.DismissedCount() != 1 {
		t.Fatal("dismissal not recorded")
	}
	s.ClearDismissals()
	if s.DismissedCount() != 0 || s.IsDismissed("correctness", "teh") {
		t.Fatal("clear failed")
	}
}

func TestRejectsEmptyAndOversized(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "store.json"))
	s.AddWord("   ")
	s.AddDismissal("", "x")
	s.AddDismissal("clarity", "")
	if len(s.Words()) != 0 || s.DismissedCount() != 0 {
		t.Fatal("empty entries must be rejected")
	}
}
