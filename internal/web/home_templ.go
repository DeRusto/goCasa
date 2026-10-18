// shortcut: hand-written equivalent of `templ generate` output for home.templ (the CLI
// could not be fetched when the skeleton was built); the next generate run replaces it.

package web

import (
	"context"
	"io"

	"github.com/a-h/templ"
)

func layout(title string) templ.Component {
	return templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
		children := templ.GetChildren(ctx)
		if children == nil {
			children = templ.NopComponent
		}
		ctx = templ.ClearChildren(ctx)
		if _, err := io.WriteString(w, `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>`); err != nil {
			return err
		}
		if _, err := io.WriteString(w, templ.EscapeString(title)); err != nil {
			return err
		}
		if _, err := io.WriteString(w, `</title></head><body>`); err != nil {
			return err
		}
		if err := children.Render(ctx, w); err != nil {
			return err
		}
		_, err := io.WriteString(w, `</body></html>`)
		return err
	})
}

func Home() templ.Component {
	return templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
		body := templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
			_, err := io.WriteString(w, `<main><h1>GoCasa</h1><p>Your family dashboard is running.</p></main>`)
			return err
		})
		return layout("GoCasa").Render(templ.WithChildren(ctx, body), w)
	})
}
