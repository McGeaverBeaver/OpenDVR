package logbuf

import "testing"

func TestRingKeepsLastLines(t *testing.T) {
	b := New(3)
	b.Write([]byte("one\ntwo\nthr"))
	b.Write([]byte("ee\nfour\n"))
	got := b.Lines(0)
	want := []string{"two", "three", "four"}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
	if l := b.Lines(2); len(l) != 2 || l[0] != "three" {
		t.Fatalf("Lines(2) = %v", l)
	}
}
