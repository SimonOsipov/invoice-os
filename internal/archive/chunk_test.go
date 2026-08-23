// chunk_test.go: RED specs for AUDIT-05-03 (Mode A) -- chunk is pure, no DB needed.
package archive

import (
	"fmt"
	"testing"
)

func TestChunk_SplitsAtFiveHundredAndKeepsOrder(t *testing.T) {
	ids := make([]string, 1201)
	for i := range ids {
		ids[i] = fmt.Sprintf("id-%04d", i)
	}

	got := chunk(ids, 500)
	if len(got) != 3 {
		t.Fatalf("chunk(1201 ids, 500) produced %d chunks, want 3", len(got))
	}
	wantSizes := []int{500, 500, 201}
	var reassembled []string
	for i, c := range got {
		if len(c) != wantSizes[i] {
			t.Errorf("chunk %d has %d ids, want %d", i, len(c), wantSizes[i])
		}
		reassembled = append(reassembled, c...)
	}
	if len(reassembled) != len(ids) {
		t.Fatalf("reassembled %d ids, want %d", len(reassembled), len(ids))
	}
	for i := range ids {
		if reassembled[i] != ids[i] {
			t.Errorf("reassembled[%d] = %q, want %q (order not preserved)", i, reassembled[i], ids[i])
			break
		}
	}
}

func TestChunk_EmptyInputYieldsNoChunks(t *testing.T) {
	got := chunk(nil, 500)
	if len(got) != 0 {
		t.Errorf("chunk(nil, 500) = %v, want zero chunks", got)
	}
}

// An exact multiple of size must not produce a trailing empty chunk.
func TestChunk_ExactMultipleOfSizeYieldsOneFullChunk(t *testing.T) {
	ids := make([]string, 500)
	for i := range ids {
		ids[i] = fmt.Sprintf("id-%04d", i)
	}
	got := chunk(ids, 500)
	if len(got) != 1 {
		t.Fatalf("chunk(500 ids, 500) produced %d chunks, want 1 (no trailing empty chunk)", len(got))
	}
	if len(got[0]) != 500 {
		t.Errorf("chunk(500 ids, 500)[0] has %d ids, want 500", len(got[0]))
	}
}

func TestChunk_OneOverSizeYieldsTwoChunks(t *testing.T) {
	ids := make([]string, 501)
	for i := range ids {
		ids[i] = fmt.Sprintf("id-%04d", i)
	}
	got := chunk(ids, 500)
	if len(got) != 2 {
		t.Fatalf("chunk(501 ids, 500) produced %d chunks, want 2", len(got))
	}
	if len(got[0]) != 500 || len(got[1]) != 1 {
		t.Errorf("chunk(501 ids, 500) sizes = [%d %d], want [500 1]", len(got[0]), len(got[1]))
	}
}

func TestChunk_SingleIDYieldsOneChunkOfOne(t *testing.T) {
	got := chunk([]string{"only-id"}, 500)
	if len(got) != 1 || len(got[0]) != 1 || got[0][0] != "only-id" {
		t.Errorf("chunk(1 id, 500) = %v, want one chunk containing exactly that id", got)
	}
}
