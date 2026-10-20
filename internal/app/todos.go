package app

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/DeRusto/goCasa/internal/web"
	"github.com/teambition/rrule-go"
)

// getTodos shows the board, filtered by status (open, done, all) and a due
// range (a preset, or from/to dates that override it), in manual or due order.
func (a *App) getTodos(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	today := dayAt(a.Now().In(a.location(r.Context())), 0)
	p := web.TodosPage{Status: q.Get("status"), Due: q.Get("due"), Sort: q.Get("sort")}
	var f todoFilter
	switch p.Status {
	case "done", "all":
	default:
		p.Status = "open"
	}
	f.Status = p.Status
	switch p.Due {
	case "overdue":
		f.To = dayAt(today, -1).Format(dateLayout)
	case "today":
		f.To = today.Format(dateLayout)
	case "week":
		f.To = dayAt(today, 6).Format(dateLayout)
	case "none":
	default:
		p.Due = ""
	}
	if _, err := time.Parse(dateLayout, q.Get("from")); err == nil {
		p.From, f.From = q.Get("from"), q.Get("from")
	}
	if _, err := time.Parse(dateLayout, q.Get("to")); err == nil {
		p.To, f.To = q.Get("to"), q.Get("to")
	}
	f.NoDue = p.Due == "none" && f.From == "" && f.To == ""
	if f.ByDue = p.Sort == "due"; !f.ByDue {
		p.Sort = ""
	}
	p.Draggable = p.Status == "open" && p.Due == "" && f.From == "" && f.To == "" && p.Sort == ""

	m := todos{a}
	cols, err := m.Columns(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	ts, err := m.List(r.Context(), f, today.Format(dateLayout))
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	at := map[int64]int{}
	for i, c := range cols {
		at[c.ID] = i
	}
	for _, t := range ts {
		if i, ok := at[t.ColumnID]; ok {
			cols[i].Todos = append(cols[i].Todos, t)
		}
	}
	p.Columns = cols
	a.render(w, r, http.StatusOK, web.TodosBoard(p))
}

func columnName(r *http.Request) (string, bool) {
	name := strings.TrimSpace(r.PostFormValue("name"))
	return name, name != "" && len([]rune(name)) <= 50
}

func pathID(r *http.Request) int64 {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id
}

func (a *App) createTodoColumn(w http.ResponseWriter, r *http.Request) {
	name, ok := columnName(r)
	if !ok {
		http.Error(w, "A column needs a name of 1 to 50 characters.", http.StatusBadRequest)
		return
	}
	_, err := todos{a}.AddColumn(r.Context(), name)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, "/todos", http.StatusSeeOther)
}

func (a *App) renameTodoColumn(w http.ResponseWriter, r *http.Request) {
	name, ok := columnName(r)
	if !ok {
		http.Error(w, "A column needs a name of 1 to 50 characters.", http.StatusBadRequest)
		return
	}
	a.todoDone(w, r, true, todos{a}.RenameColumn(r.Context(), pathID(r), name))
}

func (a *App) deleteTodoColumn(w http.ResponseWriter, r *http.Request) {
	a.todoDone(w, r, true, todos{a}.DeleteColumn(r.Context(), pathID(r)))
}

// todoDone answers a to-do module write: the refusal's status and message,
// or back to the board. column says the write was to the column in the path.
func (a *App) todoDone(w http.ResponseWriter, r *http.Request, column bool, err error) {
	switch {
	case errors.Is(err, errTodoGone), errors.Is(err, errNoColumn) && column:
		http.NotFound(w, r)
	case errors.Is(err, errNoColumn):
		http.Error(w, "Pick a column.", http.StatusBadRequest)
	case errors.Is(err, errTodoCompleted):
		http.Error(w, "A completed to-do can't be edited; reopen it first.", http.StatusConflict)
	case errors.Is(err, errTodoChained):
		http.Error(w, "This to-do's next copy still exists: delete or re-open that one first.", http.StatusConflict)
	case errors.Is(err, errNextCompleted):
		http.Error(w, "The next copy of this to-do is already completed: re-open the later one first.", http.StatusConflict)
	case err != nil:
		a.serverError(w, r, err)
	default:
		http.Redirect(w, r, "/todos", http.StatusSeeOther)
	}
}

// completeTodo completes an open to-do (see todos.Complete). Completing a
// completed to-do changes nothing.
func (a *App) completeTodo(w http.ResponseWriter, r *http.Request) {
	_, err := todos{a}.Complete(r.Context(), pathID(r))
	if errors.Is(err, errTodoCompleted) {
		http.Redirect(w, r, "/todos", http.StatusSeeOther)
		return
	}
	a.todoDone(w, r, false, err)
}

// reopenTodo un-completes a to-do (see todos.Reopen).
func (a *App) reopenTodo(w http.ResponseWriter, r *http.Request) {
	_, err := todos{a}.Reopen(r.Context(), pathID(r))
	a.todoDone(w, r, false, err)
}

// deleteTodo deletes a to-do (see todos.Delete).
func (a *App) deleteTodo(w http.ResponseWriter, r *http.Request) {
	a.todoDone(w, r, false, todos{a}.Delete(r.Context(), pathID(r)))
}

// todoFields reads and checks the to-do form, or returns a message for the
// person. A Recurring to-do without a due date gets its repeat's first date
// from today.
func todoFields(r *http.Request, today time.Time) (t web.Todo, p todoRepeat, msg string) {
	t, msg = todoText(r)
	if msg != "" {
		return t, p, msg
	}
	t.Repeat, t.Interval, t.ByDay = r.PostFormValue("repeat"), r.PostFormValue("interval"), map[string]bool{}
	t.FromCompletion = r.PostFormValue("repeat_from") == "completion"
	switch t.Repeat {
	case "":
		return t, p, ""
	case "daily", "weekly", "monthly", "yearly":
	default:
		return t, p, "Pick how it repeats."
	}
	rule, msg := presetRule(r, t.ByDay)
	if msg != "" {
		return t, p, msg
	}
	if t.Due == "" {
		opt, err := rrule.StrToROption(rule)
		if err == nil {
			day := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, time.UTC)
			t.Due, err = ruleDate(opt, day, day, true)
		}
		if err != nil {
			return t, p, "That repeat rule isn't one GoCasa understands."
		}
	}
	return t, decodeRepeat(rule), ""
}

// todoText reads and checks the to-do form's column, title, notes and due date.
func todoText(r *http.Request) (t web.Todo, msg string) {
	t.ColumnID, _ = strconv.ParseInt(r.PostFormValue("column_id"), 10, 64)
	t.Title = strings.TrimSpace(r.PostFormValue("title"))
	t.Notes = strings.TrimSpace(r.PostFormValue("notes"))
	t.Due = r.PostFormValue("due_date")
	switch {
	case t.Title == "" || len([]rune(t.Title)) > 200:
		return t, "A to-do needs a title of 1 to 200 characters."
	case len([]rune(t.Notes)) > 5000:
		return t, "Notes are at most 5000 characters."
	}
	if _, err := time.Parse(dateLayout, t.Due); t.Due != "" && err != nil {
		return t, "Pick a due date, or leave it empty."
	}
	return t, ""
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (a *App) createTodo(w http.ResponseWriter, r *http.Request) {
	t, p, msg := todoFields(r, a.Now().In(a.location(r.Context())))
	if msg != "" {
		http.Error(w, msg, http.StatusBadRequest)
		return
	}
	_, err := todos{a}.Create(r.Context(), t, p)
	a.todoDone(w, r, false, err)
}

func (a *App) getTodo(w http.ResponseWriter, r *http.Request) {
	m := todos{a}
	t, err := m.Get(r.Context(), pathID(r))
	if errors.Is(err, errTodoGone) {
		http.NotFound(w, r)
		return
	} else if err != nil {
		a.serverError(w, r, err)
		return
	}
	cols, err := m.Columns(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	a.render(w, r, http.StatusOK, web.TodoEditPage(t, cols))
}

// updateTodo saves the form (see todos.Edit).
func (a *App) updateTodo(w http.ResponseWriter, r *http.Request) {
	t, p, msg := todoFields(r, a.Now().In(a.location(r.Context())))
	if msg != "" {
		http.Error(w, msg, http.StatusBadRequest)
		return
	}
	a.todoDone(w, r, false, todos{a}.Edit(r.Context(), pathID(r), t, p))
}

// reorderTodos sets a column's order (see todos.Reorder).
func (a *App) reorderTodos(w http.ResponseWriter, r *http.Request) {
	col, _ := strconv.ParseInt(r.PostFormValue("column_id"), 10, 64)
	switch err := (todos{a}).Reorder(r.Context(), col, parseIDs(r.PostForm["ids"])); {
	case errors.Is(err, errNoColumn):
		http.Error(w, "No such column.", http.StatusBadRequest)
	case errors.Is(err, errTodoGone):
		http.Error(w, "No such to-do.", http.StatusBadRequest)
	case err != nil:
		a.serverError(w, r, err)
	default:
		http.Redirect(w, r, "/todos", http.StatusSeeOther) // for the up/down buttons; a drag's fetch doesn't follow it
	}
}

// addMonthsClamped adds n months to day, keeping to the target month's last
// day when it is shorter (January 31 plus one month is February 28).
func addMonthsClamped(day time.Time, n int) time.Time {
	first := time.Date(day.Year(), day.Month()+time.Month(n), 1, 0, 0, 0, 0, time.UTC)
	return first.AddDate(0, 0, min(day.Day(), first.AddDate(0, 1, -1).Day())-1)
}

// ruleDate is the first date of rule, counted from start, after day (or on it, when inc).
func ruleDate(opt *rrule.ROption, start, day time.Time, inc bool) (string, error) {
	opt.Dtstart = start
	rr, err := rrule.NewRRule(*opt)
	if err != nil {
		return "", err
	}
	d := rr.After(day, inc)
	if d.IsZero() {
		return "", errors.New("repeat rule has no later date")
	}
	return d.Format(dateLayout), nil
}
