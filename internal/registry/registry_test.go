package registry

import "testing"

func TestSourcesDeterministicOrder(t *testing.T) {
	reg := NewRegistry(nil, nil)
	want := []string{"azora", "mangalek", "teamx"}

	for range 50 {
		got := reg.Sources()
		if len(got) != len(want) {
			t.Fatalf("got %d sources, want %d", len(got), len(want))
		}
		for i, id := range want {
			if got[i].ID != id {
				t.Fatalf("Sources()[%d] = %q, want %q (full=%+v)", i, got[i].ID, id, got)
			}
		}

		all := reg.AllSources()
		for i, id := range want {
			if all[i].Info().ID != id {
				t.Fatalf("AllSources()[%d] = %q, want %q", i, all[i].Info().ID, id)
			}
		}
	}
}
