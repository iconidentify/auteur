package studio

import "testing"

func TestNotesRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if notes, _ := ReadNotes(dir); len(notes) != 0 {
		t.Fatal("expected no notes in fresh workspace")
	}
	AppendNote(dir, "make it blue")
	AppendNote(dir, "shorter shots")
	notes, err := ReadNotes(dir)
	if err != nil || len(notes) != 2 {
		t.Fatalf("notes = %v err %v, want 2", notes, err)
	}
	if notes[0].Text != "make it blue" || notes[1].Text != "shorter shots" {
		t.Errorf("order/content wrong: %+v", notes)
	}
}
