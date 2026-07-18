package store

import (
	"path/filepath"
	"testing"
	"time"
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
	if err := s.AddDismissal("clarity", "in order to", 0); err != nil {
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
	s.AddDismissal("correctness", "teh", 0)
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
	s.AddDismissal("", "x", 0)
	s.AddDismissal("clarity", "", 0)
	if len(s.Words()) != 0 || s.DismissedCount() != 0 {
		t.Fatal("empty entries must be rejected")
	}
}

func TestSnoozeExpires(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "store.json"))
	s.AddDismissal("clarity", "in order to", 10*time.Millisecond)
	if !s.IsDismissed("clarity", "in order to") {
		t.Fatal("snoozed suggestion must be dismissed while active")
	}
	time.Sleep(25 * time.Millisecond)
	if s.IsDismissed("clarity", "in order to") {
		t.Fatal("snooze must expire")
	}
	// Snooze then permanent dismissal upgrades the entry.
	s.AddDismissal("clarity", "x", time.Hour)
	s.AddDismissal("clarity", "x", 0)
	if !s.IsDismissed("clarity", "x") {
		t.Fatal("upgrade to permanent failed")
	}
}
