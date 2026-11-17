// Package cookiesync receives the platforms' login cookies from the
// companion Chrome extension (resources/extension), so the user's everyday
// Chrome logins carry over to the automation browser without scanning QR
// codes again.
//
// The server listens on 127.0.0.1 only and takes imports from extensions
// only: browsers put the Origin on every POST, a web page's being http(s),
// and a JSON body makes them ask first with a preflight this server
// refuses. An extension's GET carries no Origin, so the platform and domain
// lists, which are no secret, are open to any local caller but web pages.
package cookiesync

import (
	"encoding/json"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/go-rod/rod/lib/proto"
)

// Port is where the extension finds the app. resources/extension names it
// too.
const Port = 17891

// Cookie is a cookie as chrome.cookies.getAll returns it.
type Cookie struct {
	Name           string  `json:"name"`
	Value          string  `json:"value"`
	Domain         string  `json:"domain"`
	Path           string  `json:"path"`
	HostOnly       bool    `json:"hostOnly"`
	Secure         bool    `json:"secure"`
	HTTPOnly       bool    `json:"httpOnly"`
	Session        bool    `json:"session"`
	SameSite       string  `json:"sameSite"`
	ExpirationDate float64 `json:"expirationDate"`
}

// MatchesDomain reports whether c belongs to domain or a subdomain of it.
func (c Cookie) MatchesDomain(domain string) bool {
	d := strings.TrimPrefix(c.Domain, ".")
	return d == domain || strings.HasSuffix(d, "."+domain)
}

// Param converts c for Storage.setCookies.
func (c Cookie) Param() *proto.NetworkCookieParam {
	p := &proto.NetworkCookieParam{
		Name: c.Name, Value: c.Value, Path: c.Path, Secure: c.Secure, HTTPOnly: c.HTTPOnly,
	}
	if p.Path == "" {
		p.Path = "/"
	}
	host := strings.TrimPrefix(c.Domain, ".")
	if c.HostOnly {
		// A cookie set without a domain attribute: giving only the URL
		// keeps it to that host.
		scheme := "http://"
		if c.Secure {
			scheme = "https://"
		}
		p.URL = scheme + host + p.Path
	} else {
		p.Domain = "." + host
	}
	switch c.SameSite {
	case "no_restriction":
		p.SameSite = proto.NetworkCookieSameSiteNone
	case "lax":
		p.SameSite = proto.NetworkCookieSameSiteLax
	case "strict":
		p.SameSite = proto.NetworkCookieSameSiteStrict
	}
	if !c.Session && c.ExpirationDate > 0 {
		p.Expires = proto.TimeSinceEpoch(c.ExpirationDate)
	}
	return p
}

// Platform is a platform as the extension lists it, to sync all or some.
type Platform struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Domains hold its login cookies; empty when Note says why its login
	// cannot be copied.
	Domains []string `json:"domains"`
	Note    string   `json:"note,omitempty"`
}

// Handler serves the extension.
type Handler struct {
	// Platforms lists the platforms in the app's order.
	Platforms func() []Platform
	// Import stores cookies, already limited to the platforms' domains, and
	// returns the reply for the extension.
	Import func(cookies []Cookie) (any, error)
}

// domains lists the platforms' cookie domains once each.
func (h *Handler) domains() []string {
	out := []string{}
	for _, p := range h.Platforms() {
		for _, d := range p.Domains {
			if !slices.Contains(out, d) {
				out = append(out, d)
			}
		}
	}
	return out
}

func isExtension(origin string) bool {
	return strings.HasPrefix(origin, "chrome-extension://") || strings.HasPrefix(origin, "extension://")
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	o := r.Header.Get("Origin")
	if (o != "" || r.Method != http.MethodGet) && !isExtension(o) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/platforms":
		writeJSON(w, h.Platforms())
	case r.Method == http.MethodGet && r.URL.Path == "/domains":
		// For extensions before 0.3, which sync all platforms only.
		writeJSON(w, h.domains())
	case r.Method == http.MethodPost && r.URL.Path == "/import":
		if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			http.Error(w, "json expected", http.StatusUnsupportedMediaType)
			return
		}
		var body struct {
			Cookies []Cookie `json:"cookies"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20)).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		domains := h.domains()
		var keep []Cookie
		for _, c := range body.Cookies {
			for _, d := range domains {
				if c.Name != "" && c.MatchesDomain(d) {
					keep = append(keep, c)
					break
				}
			}
		}
		res, err := h.Import(keep)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, res)
	default:
		http.NotFound(w, r)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// Listen starts serving h on 127.0.0.1:Port. It fails when the port is
// taken, for example by another copy of the app.
func Listen(h *Handler) error {
	l, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(Port))
	if err != nil {
		return err
	}
	go http.Serve(l, h)
	return nil
}
