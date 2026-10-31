package proc

import "testing"

func TestSnapshotLookup(t *testing.T) {
	s := Snapshot{{Info: Info{PID: 5, Alive: true, StartUnix: 10, StartMicro: 7}}, {Info: Info{PID: 6}}}
	if got := s.Lookup(5); !got.Alive || got.StartUnix != 10 {
		t.Fatalf("listed process: %+v", got)
	}
	if got := s.Lookup(6); got.Alive {
		t.Fatalf("a zombie is not alive: %+v", got)
	}
	if got := s.Lookup(9); got.Alive || got.PID != 9 || got.StartUnix != 0 {
		t.Fatalf("a PID nobody of ours holds is not alive, and keeps its number: %+v", got)
	}
}

func TestStartKey(t *testing.T) {
	if got := (Info{StartUnix: 1798000000, StartMicro: 42}).StartKey(); got != "1798000000.000042" {
		t.Fatalf("StartKey = %q", got)
	}
}
