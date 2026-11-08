package web

import (
	"net/http"
	"strings"
)

// Search engines: the home page and the guide are public and described
// (meta description, canonical URL, Open Graph, JSON-LD on the home page);
// every other page carries noindex. robots.txt and sitemap.xml below.

// publicPaths are the pages search engines may list.
var publicPaths = []string{"/", "/guide"}

func (s *Server) handleRobots(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	var b strings.Builder
	b.WriteString("User-agent: *\n")
	for _, p := range []string{"/servers", "/admin", "/login", "/callback", "/logout", "/donate/", "/lang/", "/healthz"} {
		b.WriteString("Disallow: " + p + "\n")
	}
	b.WriteString("Sitemap: " + s.base + "/sitemap.xml\n")
	_, _ = w.Write([]byte(b.String()))
}

func (s *Server) handleSitemap(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">` + "\n")
	for _, p := range publicPaths {
		b.WriteString("  <url><loc>" + s.base + p + "</loc></url>\n")
	}
	b.WriteString("</urlset>\n")
	_, _ = w.Write([]byte(b.String()))
}
