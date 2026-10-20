package web

// Lock is a kind of action an Admin can put behind their PIN (spec §9).
type Lock int

const (
	LockCalendar Lock = iota // changing the calendar
	LockChores               // completing chores
	LockTodos                // completing to-dos
)

// On is whether the Lock l is on.
func (ls Locks) On(l Lock) bool {
	switch l {
	case LockCalendar:
		return ls.Calendar
	case LockChores:
		return ls.Chores
	case LockTodos:
		return ls.Todos
	}
	panic("no such Lock")
}

// Access is what this device may do now: the Household's Locks, and whether
// an Admin has unlocked it.
type Access struct {
	Locks    Locks
	Unlocked bool
}

// CanDo is whether an action behind Lock l goes ahead without the PIN pad.
// One unlock covers every Lock.
func (a Access) CanDo(l Lock) bool { return a.Unlocked || !a.Locks.On(l) }
