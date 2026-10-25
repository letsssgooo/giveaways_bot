package random

import (
	"bytes"
	"errors"
	"reflect"
	"testing"

	drawentity "github.com/letsssgooo/giveaways_bot/internal/entity/draw"
)

func TestSelectionUsesWeightsWithoutReplacement(t *testing.T) {
	participants := []drawentity.Participant{{ID: 1, Weight: 1}, {ID: 2, Weight: 3}, {ID: 3, Weight: 1}}
	counts := map[int64]int{}
	// Every ticket in [0, totalWeight) maps to exactly one participant.
	for ticket := byte(0); ticket < 5; ticket++ {
		selected, err := selectWithReader(bytes.NewReader([]byte{ticket}), participants, 1)
		if err != nil {
			t.Fatal(err)
		}
		counts[selected[0].ID]++
	}
	if !reflect.DeepEqual(counts, map[int64]int{1: 1, 2: 3, 3: 1}) {
		t.Fatalf("ticket allocation = %v", counts)
	}
	selected, err := selectWithReader(bytes.NewReader([]byte{1, 0, 0}), participants, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != 3 || selected[0].ID != 2 || selected[1].ID != 1 || selected[2].ID != 3 {
		t.Fatalf("selection = %+v", selected)
	}
	if participants[0].ID != 1 || participants[1].Weight != 3 {
		t.Fatal("selection changed the participant snapshot")
	}
}

type failingRandom struct{ err error }

func (r failingRandom) Read([]byte) (int, error) { return 0, r.err }

func TestSelectionFailsWithoutRandomness(t *testing.T) {
	want := errors.New("entropy source failed")
	selected, err := selectWithReader(failingRandom{want}, []drawentity.Participant{{ID: 1, Weight: 2}}, 1)
	if !errors.Is(err, want) || selected != nil {
		t.Fatalf("selection = %+v, %v", selected, err)
	}
}

func TestSelectionEmptyAndInvalidInputs(t *testing.T) {
	selected, err := Select(nil, 5)
	if err != nil || len(selected) != 0 {
		t.Fatalf("empty selection = %+v, %v", selected, err)
	}
	for _, weight := range []int{0, 11} {
		if _, err := Select([]drawentity.Participant{{Weight: weight}}, 1); err == nil {
			t.Fatalf("accepted weight %d", weight)
		}
	}
	if _, err := Select(nil, -1); err == nil {
		t.Fatal("accepted negative selection count")
	}
}
