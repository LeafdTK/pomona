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
  // Pages can tell whether the server or the extension is serving them.
  window.__pomonaServed = true;
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

const webAppHead = `<link rel="manifest" href="/src/manifest.webmanifest">
<link rel="icon" href="/icons/128.png" type="image/png">
<meta name="theme-color" content="#b0394a">
`

// The pages are compiled into the binary, so a built server needs no files
// beside it. But if you're running from the source tree, the copy on disk wins:
// otherwise every CSS tweak needs a rebuild, which is a slow way to find out
// you've been looking at stale styles.
func assets() fs.FS {
	if hostedMode {
		return pomona.Files // never the working directory of a public server
	}
	if _, err := os.Stat("src/brief/brief.html"); err == nil {
		return devTree{}
	}
	return pomona.Files
}

// devTree is the source tree as the pages need it: src and icons, nothing
// else. Rooting the file server at "." served .env to anyone who asked.
type devTree struct{}

func (devTree) Open(name string) (fs.File, error) {
	if name != "src" && name != "icons" && !strings.HasPrefix(name, "src/") && !strings.HasPrefix(name, "icons/") {
		return nil, fs.ErrNotExist
	}
	return os.DirFS(".").Open(name)
}

func (s *Server) webRoutes(mux *http.ServeMux) {
	files := http.FileServer(http.FS(assets()))

	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		// Every deploy must take effect on the next load: nothing served here
		// is worth a browser keeping a stale copy of.
		w.Header().Set("Cache-Control", "no-cache")
		switch r.URL.Path {
		case "/":
			s.page(w, "src/brief/brief.html", "src/brief/")
		case "/settings":
			s.page(w, "src/options/options.html", "src/options/")
		case "/welcome", "/link":
			s.page(w, "src/welcome/welcome.html", "src/welcome/")
		case "/privacy":
			s.page(w, "src/privacy/privacy.html", "src/privacy/")

		// Anything that links to the pages by filename, from an older tab or a
		// bookmark, belongs on the route that actually carries the shim.
		case "/src/brief/brief.html":
			redirect(w, r, "/")
		case "/src/options/options.html":
			redirect(w, r, "/settings")
		case "/src/welcome/welcome.html":
			redirect(w, r, "/welcome")
		case "/src/privacy/privacy.html":
			redirect(w, r, "/privacy")

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
	// Served pages are a web app in their own right: installable, with a
	// dock icon, no extension needed. The extension's copy of the same page
	// does not carry this, so it is added here rather than in the file.
	html = strings.Replace(html, "</head>", webAppHead+chromeShim+"\n</head>", 1)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(bytes.NewBufferString(html).Bytes())
}
