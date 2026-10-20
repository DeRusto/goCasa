package web

// Kind is what the bus carries: what sort of thing changed, so a page refetches
// only for the kinds it shows. It is the SSE event name.
type Kind string

const (
	KindEvent       Kind = "event" // events and calendars
	KindTodo        Kind = "todo"  // to-dos and their columns
	KindList        Kind = "list"  // shopping lists and items
	KindChore       Kind = "chore"
	KindPoints      Kind = "points" // balances, rewards and redemptions
	KindMember      Kind = "member"
	KindHousehold   Kind = "household"   // household settings, locks, the Fallback folder
	KindIntegration Kind = "integration" // an integration's health, settings or picks; the weather
)
