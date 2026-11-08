// Command devproxy serves sigiro's static dashboard build next to a local
// sigiro's query API on one origin, so the dashboard runs locally:
//
//	devproxy -dist $SIGIRO_SRC/dashboard/dist -sigiro http://localhost:9999 -listen :8088
//
// Open http://localhost:8088/dashboard/ (the page needs any bearer token in
// sessionStorage; sigiro's open mode ignores it).
package main

import (
	"flag"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

func main() {
	dist := flag.String("dist", "", "dashboard dist directory (contains dashboard/)")
	upstream := flag.String("sigiro", "http://localhost:9999", "sigiro HTTP API")
	listen := flag.String("listen", "127.0.0.1:8088", "listen address")
	flag.Parse()
	u, err := url.Parse(*upstream)
	if err != nil {
		log.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(u)
	files := http.FileServer(http.Dir(*dist))
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/v1/"), strings.HasPrefix(r.URL.Path, "/api/"):
			proxy.ServeHTTP(w, r)
		case r.URL.Path == "/" || r.URL.Path == "/dashboard":
			http.Redirect(w, r, "/dashboard/", http.StatusFound)
		default:
			files.ServeHTTP(w, r)
		}
	})
	log.Printf("dashboard on http://%s/dashboard/ → %s", *listen, *upstream)
	log.Fatal(http.ListenAndServe(*listen, nil))
}
