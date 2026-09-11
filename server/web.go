package main

import (
	"bytes"
	"io/fs"
	"net/http"
	"os"
	"regexp"
	"strings"

	pomona "pomona"
)

// The extension's pages, served straight from the binary.
//
// They were written against chrome.storage and chrome.runtime, so a small shim
// stands in for those: storage becomes localStorage, "open the options page"
// becomes a navigation. Nothing else changes, which is the point. One set of
// pages, one design, two ways to reach it.
const chromeShim = `<script>
(() => {
  const read = () => { try { return JSON.parse(localStorage.getItem("pomona") ?? "{}"); } catch { return {}; } };
  const write = (v) => localStorage.setItem("pomona", JSON.stringify(v));

  // Served by the server itself, so talk to the origin we came from.
  const store = read();
  store.server = { url: location.origin, token: store.server?.token ?? "" };
  write(store);

  window.chrome = {
    storage: { local: {
      get: (key) => {
        const all = read();
        return Promise.resolve(typeof key === "string" ? { [key]: all[key] } : all);
      },
      set: (patch) => (write({ ...read(), ...patch }), Promise.resolve()),
    } },
    runtime: {
      getURL: (path) => "/" + path,
      openOptionsPage: () => { location.href = "/settings"; },
      sendMessage: () => Promise.resolve({ ok: true }),
      onInstalled: { addListener() {} },
      onStartup: { addListener() {} },
      onMessage: { addListener() {} },
    },
    tabs: {
      create: ({ url }) => { location.href = url; return Promise.resolve({ id: 1 }); },
      query: () => Promise.resolve([]),
      update: () => Promise.resolve(),
    },
    notifications: { create: () => Promise.resolve(), onClicked: { addListener() {} } },
    alarms: { create: () => Promise.resolve(), clear: () => Promise.resolve(), get: () => Promise.resolve(null), onAlarm: { addListener() {} } },
    permissions: { contains: () => Promise.resolve(true), request: () => Promise.resolve(true) },
  };
})();
</script>`

// The pages are compiled into the binary, so a built server needs no files
// beside it. But if you're running from the source tree, the copy on disk wins:
// otherwise every CSS tweak needs a rebuild, which is a slow way to find out
// you've been looking at stale styles.
func assets() fs.FS {
	if _, err := os.Stat("src/brief/brief.html"); err == nil {
		return os.DirFS(".")
	}
	return pomona.Files
}

func (s *Server) webRoutes(mux *http.ServeMux) {
	files := http.FileServer(http.FS(assets()))

	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			s.page(w, "src/brief/brief.html", "src/brief/")
		case "/settings":
			s.page(w, "src/options/options.html", "src/options/")
		case "/welcome":
			s.page(w, "src/welcome/welcome.html", "src/welcome/")

		// Anything that links to the pages by filename, from an older tab or a
		// bookmark, belongs on the route that actually carries the shim.
		case "/src/brief/brief.html":
			redirect(w, r, "/")
		case "/src/options/options.html":
			redirect(w, r, "/settings")
		case "/src/welcome/welcome.html":
			redirect(w, r, "/welcome")

		default:
			files.ServeHTTP(w, r)
		}
	})
}

func redirect(w http.ResponseWriter, r *http.Request, to string) {
	if r.URL.RawQuery != "" {
		to += "?" + r.URL.RawQuery
	}
	http.Redirect(w, r, to, http.StatusFound)
}

// A src or href pointing at a plain filename next to the page: not absolute,
// not a url, not an anchor.
var relativeAsset = regexp.MustCompile(`(src|href)="([^"/:#][^":]*\.(?:css|js))"`)

// page rewrites a page's relative asset links to absolute ones and injects the
// shim, so the same file works whether Chrome or the server is serving it.
func (s *Server) page(w http.ResponseWriter, path, base string) {
	raw, err := fs.ReadFile(assets(), path)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	// Any link the page makes to a file beside it. Naming the files instead,
	// which is what this used to do, means every new page silently serves
	// itself without styles until somebody remembers to extend the list.
	html := relativeAsset.ReplaceAllString(string(raw), `$1="/`+base+`$2"`)
	html = strings.Replace(html, "</head>", chromeShim+"\n</head>", 1)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(bytes.NewBufferString(html).Bytes())
}
