package app

import (
	"math/rand/v2"
	"slices"
	"testing"
)

func seeded() *rand.Rand { return rand.New(rand.NewPCG(1, 2)) }

func flat(steps [][]int64) []int64 { return slices.Concat(steps...) }

func TestARoundDealsEveryCandidateOnceCachedFirst(t *testing.T) {
	cands := []photoCandidate{{id: 1}, {id: 2, cached: true}, {id: 3}, {id: 4, cached: true}, {id: 5, cached: true}}
	got := flat(deal(cands, seeded()))
	if s := slices.Sorted(slices.Values(got)); !slices.Equal(s, []int64{1, 2, 3, 4, 5}) {
		t.Fatalf("round %v doesn't hold each candidate exactly once", got)
	}
	for i, id := range got {
		if cached := id == 2 || id == 4 || id == 5; cached != (i < 3) {
			t.Fatalf("round %v: cached photos 2, 4, 5 don't come first", got)
		}
	}
	if again := flat(deal(cands, seeded())); !slices.Equal(again, got) {
		t.Errorf("same seed dealt %v then %v", got, again)
	}
}

func TestPortraitsPairAndLandscapesShowAlone(t *testing.T) {
	cands := []photoCandidate{{id: 1, portrait: true}, {id: 2}, {id: 3, portrait: true}, {id: 4, portrait: true}, {id: 5}}
	steps := deal(cands, seeded())
	if s := slices.Sorted(slices.Values(flat(steps))); !slices.Equal(s, []int64{1, 2, 3, 4, 5}) {
		t.Fatalf("pairing lost or repeated a photo: %v", steps)
	}
	pairs := 0
	for _, st := range steps {
		portrait := func(id int64) bool { return id == 1 || id == 3 || id == 4 }
		switch len(st) {
		case 1:
			if portrait(st[0]) {
				pairs-- // the one portrait left unpaired
			}
		case 2:
			if !portrait(st[0]) || !portrait(st[1]) {
				t.Errorf("step %v pairs a landscape photo", st)
			}
			pairs++
		default:
			t.Errorf("step %v isn't one or two photos", st)
		}
	}
	if pairs != 0 { // one pair, one portrait alone
		t.Errorf("three portraits made %v, want one pair and one alone", steps)
	}
}

func TestAdvanceSkipsPhotosGoneMidRound(t *testing.T) {
	steps := [][]int64{{1}, {2}, {3, 4}, {5}}
	present := map[int64]bool{1: true, 4: true, 5: true}
	redeal := func() [][]int64 { t.Fatal("dealt a new round with photos left"); return nil }
	steps, pos, st := advance(steps, 1, present, redeal)
	if !slices.Equal(st, []int64{4}) || pos != 3 {
		t.Fatalf("after 1 with 2 and 3 gone showed %v (pos %d), want [4]", st, pos)
	}
	_, pos, st = advance(steps, pos, present, redeal)
	if !slices.Equal(st, []int64{5}) || pos != 4 {
		t.Fatalf("showed %v, want [5]", st)
	}
	_, _, st = advance(steps, pos, present, func() [][]int64 { return [][]int64{{9}, {1}} })
	if !slices.Equal(st, []int64{1}) {
		t.Errorf("a new round showed %v, want [1]: 9 is gone", st)
	}
	if _, _, st = advance(nil, 0, map[int64]bool{}, func() [][]int64 { return nil }); st != nil {
		t.Errorf("no candidates showed %v", st)
	}
}
