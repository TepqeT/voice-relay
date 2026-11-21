package web

import (
	"html/template"
	"net/http"
)

// stoppedPage is the only page left once the project is stopped. No script,
// one stylesheet: it fits the strict CSP.
var stoppedPage = template.Must(template.New("stopped").Parse(`<!doctype html>
<html lang="{{.Lang}}">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex">
<title>Voice Relay</title>
<link rel="icon" href="/static/logo.svg" type="image/svg+xml">
<link rel="stylesheet" href="/static/style.css">
</head>
<body>
<main class="stopped">
<img src="/static/logo.svg" alt="" width="64" height="64">
<h1>{{.Title}}</h1>
<p>{{.Text}}</p>
<p class="muted">{{.Thanks}}</p>
</main>
</body>
</html>
`))

var stoppedTexts = map[string]map[string]string{
	"fr": {
		"Title":  "Projet arrêté",
		"Text":   "Voice Relay n'est plus disponible. Les bots ont quitté les serveurs où ils étaient installés ; les rôles créés sont restés, vous pouvez les supprimer dans les paramètres du serveur.",
		"Thanks": "Merci à celles et ceux qui l'ont essayé.",
	},
	"en": {
		"Title":  "Project stopped",
		"Text":   "Voice Relay is no longer available. The bots left the servers they were installed on; the roles they created were kept, you can delete them in the server settings.",
		"Thanks": "Thanks to everyone who tried it.",
	},
}

// serveStopped answers every request with the "project stopped" notice.
func (s *Server) serveStopped(w http.ResponseWriter, r *http.Request) {
	lang := langFor(r)
	data := map[string]string{"Lang": lang}
	for k, v := range stoppedTexts[lang] {
		data[k] = v
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Language", lang)
	h.Set("Cache-Control", "no-store")
	_ = stoppedPage.Execute(w, data)
}
