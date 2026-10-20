package web

import "testing"

func TestCanDoReflectsEachLockUnlessAnAdminHasUnlocked(t *testing.T) {
	for _, tc := range []struct {
		name  string
		lock  Lock
		locks Locks
	}{
		{"Calendar", LockCalendar, Locks{Calendar: true}},
		{"Chores", LockChores, Locks{Chores: true}},
		{"To-dos", LockTodos, Locks{Todos: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if !(Access{}).CanDo(tc.lock) {
				t.Error("with the Lock off, the action is locked")
			}
			if (Access{Locks: tc.locks}).CanDo(tc.lock) {
				t.Error("with the Lock on and no Admin unlocked, the action is allowed")
			}
			if !(Access{Locks: tc.locks, Unlocked: true}).CanDo(tc.lock) {
				t.Error("with an Admin unlocked, the Lock still holds")
			}
			others := Locks{Calendar: true, Chores: true, Todos: true}
			switch tc.lock { // every other Lock on leaves this action free
			case LockCalendar:
				others.Calendar = false
			case LockChores:
				others.Chores = false
			case LockTodos:
				others.Todos = false
			}
			if !(Access{Locks: others}).CanDo(tc.lock) {
				t.Error("another Lock locks this action")
			}
		})
	}
}
