package app_test

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/DeRusto/goCasa/internal/app"
	"github.com/DeRusto/goCasa/internal/apptest"
)

// groceries is a Shopping list named Groceries holding "2 Milk" and a checked
// "Eggs", behind the Shopping list interface, with the ids of those items.
type groceries struct {
	h          *apptest.Harness
	list       app.ShoppingList
	milk, eggs int64
}

func (g groceries) shows(t *testing.T) string {
	return strings.Join(listByName(t, g.h, "Groceries"), ",")
}

func (g groceries) id(t *testing.T, query string, args ...any) int64 {
	t.Helper()
	var id int64
	if err := g.h.App.DB.QueryRow(query, args...).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func (g *groceries) open(t *testing.T) {
	t.Helper()
	var err error
	if g.list, err = g.h.App.ShoppingList(context.Background(), g.id(t, "SELECT id FROM shopping_lists WHERE name = 'Groceries'")); err != nil {
		t.Fatal(err)
	}
}

func localGroceries(t *testing.T) groceries {
	h := apptest.New(t)
	setup(t, h)
	post(t, h, "/lists", url.Values{"name": {"Groceries"}})
	post(t, h, "/lists/1/items", url.Values{"note": {"2 Milk"}})
	post(t, h, "/lists/1/items", url.Values{"note": {"Eggs"}})
	post(t, h, "/lists/1/items/2", url.Values{"checked": {"true"}})
	g := groceries{h: h, milk: 1, eggs: 2}
	g.open(t)
	return g
}

func mirroredGroceries(t *testing.T) (groceries, *fakeMealie) {
	h, _, m := mealieHousehold(t)
	g := groceries{h: h}
	g.milk = g.id(t, "SELECT id FROM shopping_items WHERE external_id = 'I1'")
	g.eggs = g.id(t, "SELECT id FROM shopping_items WHERE external_id = 'I2'")
	g.open(t)
	return g, m
}

// TestShoppingListAdapters runs one table against both adapters: Local
// in-process, Mirrored against the fake Mealie.
func TestShoppingListAdapters(t *testing.T) {
	t.Parallel()
	adapters := map[string]func(t *testing.T) groceries{
		"Local":    localGroceries,
		"Mirrored": func(t *testing.T) groceries { g, _ := mirroredGroceries(t); return g },
	}
	ctx := context.Background()
	for name, open := range adapters {
		for _, c := range []struct {
			name    string
			do      func(g groceries) error
			want    string
			wantErr error
		}{
			{"add", func(g groceries) error { return g.list.Add(ctx, "Bread") }, "2 Milk,[x] Eggs,Bread", nil},
			{"check", func(g groceries) error { return g.list.Check(ctx, g.milk, true) }, "[x] 2 Milk,[x] Eggs", nil},
			{"uncheck", func(g groceries) error { return g.list.Check(ctx, g.eggs, false) }, "2 Milk,Eggs", nil},
			{"delete", func(g groceries) error { return g.list.Delete(ctx, g.milk) }, "[x] Eggs", nil},
			{"clear", func(g groceries) error { return g.list.Clear(ctx) }, "2 Milk", nil},
			{"check a missing item", func(g groceries) error { return g.list.Check(ctx, 999, true) }, "2 Milk,[x] Eggs", sql.ErrNoRows},
			{"delete a missing item", func(g groceries) error { return g.list.Delete(ctx, 999) }, "2 Milk,[x] Eggs", sql.ErrNoRows},
		} {
			t.Run(name+"/"+c.name, func(t *testing.T) {
				g := open(t)
				if err := c.do(g); !errors.Is(err, c.wantErr) {
					t.Fatalf("err = %v, want %v", err, c.wantErr)
				}
				if got := g.shows(t); got != c.want {
					t.Errorf("Groceries = %q, want %q", got, c.want)
				}
			})
		}
	}
}

func TestOnlyALocalListMoves(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	g := localGroceries(t)
	if err := g.list.Move(ctx, g.eggs, 0); err != nil {
		t.Fatal(err)
	}
	if got := g.shows(t); got != "[x] Eggs,2 Milk" {
		t.Errorf("Local after move = %q", got)
	}
	if err := g.list.Move(ctx, g.eggs, 5); !errors.Is(err, app.ErrBadPosition) {
		t.Errorf("move past the end = %v, want ErrBadPosition", err)
	}

	g, _ = mirroredGroceries(t)
	if err := g.list.Move(ctx, g.eggs, 0); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("Mirrored move = %v, want refused as not found", err)
	}
	if got := g.shows(t); got != "2 Milk,[x] Eggs" {
		t.Errorf("Mirrored after move = %q", got)
	}
}

func TestAMirroredListIsReadOnlyWhileMealieFails(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	g, m := mirroredGroceries(t)
	m.setDown(true)
	for range 4 { // past the poll count that marks Mealie Down
		if err := g.list.Add(ctx, "Bread"); !errors.Is(err, app.ErrReadOnly) {
			t.Fatalf("add with Mealie failing = %v, want ErrReadOnly", err)
		}
	}
	if got := g.shows(t); got != "2 Milk,[x] Eggs" {
		t.Errorf("Groceries = %q", got)
	}
}

func TestAMirroredItemGoneFromMealieRepullsTheList(t *testing.T) {
	t.Parallel()
	g, m := mirroredGroceries(t)
	m.remove("I1")
	if err := g.list.Check(context.Background(), g.milk, true); !errors.Is(err, app.ErrGoneFromMealie) {
		t.Fatalf("check = %v, want ErrGoneFromMealie", err)
	}
	if got := g.shows(t); got != "[x] Eggs" {
		t.Errorf("Groceries = %q, want the re-pulled list", got)
	}
}
