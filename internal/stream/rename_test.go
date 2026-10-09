package stream

import "testing"

func TestRenameViewer(t *testing.T) {
	m := &Manager{viewers: map[string]*Viewer{"a": {ID: "a", Name: "Chrome on Windows"}}}
	if !m.RenameViewer("a", "Chrome on Windows", "DEN-PC") || m.viewers["a"].Name != "DEN-PC" {
		t.Fatal("rename from the joined name failed")
	}
	// A late answer never overwrites a name that changed since.
	if m.RenameViewer("a", "Chrome on Windows", "OTHER") || m.viewers["a"].Name != "DEN-PC" {
		t.Fatal("stale rename applied")
	}
	if m.RenameViewer("missing", "", "X") {
		t.Fatal("renamed an unknown viewer")
	}
}
