package trafficquota

import "testing"

func TestNoopPersisterContract(t *testing.T) {
	persister := NewNoopPersister()

	if err := persister.Save("alice", "2026-04", 100); err != nil {
		t.Fatalf("save failed: %v", err)
	}
	if err := persister.IncrBy("alice", "2026-04", 50); err != nil {
		t.Fatalf("incr failed: %v", err)
	}
	if err := persister.Delete("alice", "2026-04"); err != nil {
		t.Fatalf("delete failed: %v", err)
	}

	loaded, err := persister.Load("alice", "2026-04")
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	if loaded != 0 {
		t.Fatalf("unexpected loaded value: %d", loaded)
	}

	all, err := persister.LoadAll("2026-04")
	if err != nil {
		t.Fatalf("load all failed: %v", err)
	}
	if len(all) != 0 {
		t.Fatalf("expected empty load-all result, got: %#v", all)
	}
}
