package site

import (
	"bytes"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
)

type renderer struct {
	template *template.Template
}

type viewLink struct {
	Label string
	Href  string
}

type viewData struct {
	Title string
	Text  string
	Links []viewLink
}

func newRenderer() (renderer, error) {
	pageTemplate, err := template.New("page").Parse(documentTemplate)
	if err != nil {
		return renderer{}, err
	}
	return renderer{template: pageTemplate}, nil
}

func (r renderer) writePage(w http.ResponseWriter, request *http.Request, status int, currentPage page) {
	links := make([]viewLink, 0, len(currentPage.links))
	for _, currentLink := range currentPage.links {
		href := currentLink.href
		if currentLink.absoluteInternal {
			href = absoluteURL(request, currentLink.href)
		}
		links = append(links, viewLink{Label: currentLink.label, Href: href})
	}

	data := viewData{
		Title: currentPage.title,
		Text:  currentPage.text,
		Links: links,
	}

	var body bytes.Buffer
	if err := r.template.Execute(&body, data); err != nil {
		http.Error(w, "cannot render HTML", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=UTF-8")
	w.Header().Set("Content-Length", strconv.Itoa(body.Len()))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	if request.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(body.Bytes())
}

func absoluteURL(request *http.Request, path string) string {
	scheme := "http"
	if request.TLS != nil {
		scheme = "https"
	}

	return (&url.URL{
		Scheme: scheme,
		Host:   request.Host,
		Path:   path,
	}).String()
}

const documentTemplate = `<!doctype html>
<html lang="ru">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>{{.Title}}</title>
  <style>
    :root { color-scheme: dark; --bg: #09111f; --card: #111d31; --line: #2a3b57; --text: #eaf2ff; --muted: #aab9cf; --accent: #69d2ff; }
    * { box-sizing: border-box; }
    body { margin: 0; min-height: 100vh; display: grid; place-items: center; padding: 32px; font: 17px/1.6 system-ui, sans-serif; background: radial-gradient(circle at top, #193052, var(--bg) 55%); color: var(--text); }
    main { width: min(760px, 100%); padding: 36px; border: 1px solid var(--line); border-radius: 20px; background: rgba(17, 29, 49, .96); box-shadow: 0 24px 70px #0008; }
    .eyebrow { margin: 0; color: var(--accent); font-size: .78rem; font-weight: 800; letter-spacing: .14em; text-transform: uppercase; }
    h1 { margin: .35rem 0 .7rem; font-size: clamp(2rem, 6vw, 3.7rem); line-height: 1.05; }
    h2 { margin-top: 2rem; font-size: 1.05rem; color: var(--muted); }
    p { color: var(--muted); }
    ul { display: grid; gap: 10px; padding-left: 22px; }
    a { color: var(--accent); text-underline-offset: 4px; }
    a:hover { color: #fff; }
  </style>
</head>
<body>
  <main>
    <p class="eyebrow">Go crawler test site</p>
    <h1>{{.Title}}</h1>
    <p>{{.Text}}</p>
    <section>
      <h2>Исходящие ссылки</h2>
      <ul>
        {{range .Links}}<li><a href="{{.Href}}">{{.Label}}</a></li>{{end}}
      </ul>
    </section>
  </main>
</body>
</html>`
