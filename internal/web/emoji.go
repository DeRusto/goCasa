package web

// RoutineEmoji returns the emoji for a Routine or for Other chores.
func RoutineEmoji(routine string) string {
	switch routine {
	case "morning":
		return "🌅"
	case "afternoon":
		return "🎒"
	case "evening":
		return "🌙"
	default:
		return "⭐"
	}
}

// DisplayEmoji returns the Chore's emoji for Kid style.
func (c Chore) DisplayEmoji() string {
	if c.Emoji != "" {
		return c.Emoji
	}
	return RoutineEmoji(c.Routine)
}

// DisplayEmoji returns the Reward's emoji for Kid style.
func (r Reward) DisplayEmoji() string {
	if r.Emoji != "" {
		return r.Emoji
	}
	return "🎁"
}
