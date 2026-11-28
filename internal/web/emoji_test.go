package web

import "testing"

func TestChoreDisplayEmoji(t *testing.T) {
	for _, tc := range []struct {
		chore Chore
		want  string
	}{
		{Chore{Emoji: "🪥", Routine: "morning"}, "🪥"},
		{Chore{Routine: "morning"}, "🌅"},
		{Chore{Routine: "afternoon"}, "🎒"},
		{Chore{Routine: "evening"}, "🌙"},
		{Chore{}, "⭐"},
	} {
		if got := tc.chore.DisplayEmoji(); got != tc.want {
			t.Errorf("Chore emoji %q in Routine %q = %q, want %q", tc.chore.Emoji, tc.chore.Routine, got, tc.want)
		}
	}
}

func TestRewardDisplayEmoji(t *testing.T) {
	for _, tc := range []struct{ stored, want string }{
		{"🍿", "🍿"},
		{"", "🎁"},
	} {
		if got := (Reward{Emoji: tc.stored}).DisplayEmoji(); got != tc.want {
			t.Errorf("Reward emoji %q = %q, want %q", tc.stored, got, tc.want)
		}
	}
}
