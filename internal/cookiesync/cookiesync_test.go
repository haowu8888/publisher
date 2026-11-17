package cookiesync

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-rod/rod/lib/proto"
)

func TestParam(t *testing.T) {
	p := Cookie{Name: "SESSDATA", Value: "v", Domain: ".bilibili.com", Path: "/", Secure: true, HTTPOnly: true,
		SameSite: "no_restriction", ExpirationDate: 1900000000.5}.Param()
	if p.Domain != ".bilibili.com" || p.URL != "" || p.SameSite != proto.NetworkCookieSameSiteNone || p.Expires != 1900000000.5 {
		t.Errorf("domain cookie: %+v", p)
	}
	p = Cookie{Name: "a", Value: "v", Domain: "mp.weixin.qq.com", HostOnly: true, Session: true, SameSite: "unspecified"}.Param()
	if p.Domain != "" || p.URL != "http://mp.weixin.qq.com/" || p.SameSite != "" || p.Expires != 0 {
		t.Errorf("host-only session cookie: %+v", p)
	}
}

func TestHandler(t *testing.T) {
	var got []Cookie
	h := &Handler{
		Platforms: func() []Platform {
			return []Platform{{ID: "bilibili", Name: "哔哩哔哩", Domains: []string{"bilibili.com"}}, {ID: "douyin", Note: "扫码"}}
		},
		Import: func(c []Cookie) (any, error) { got = c; return map[string]int{"n": len(c)}, nil },
	}
	body := `{"cookies":[{"name":"SESSDATA","value":"1","domain":".bilibili.com"},
		{"name":"x","value":"1","domain":"member.bilibili.com"},
		{"name":"y","value":"1","domain":".notbilibili.com"},
		{"name":"z","value":"1","domain":".evil.com"}]}`

	for _, tc := range []struct {
		origin, ctype string
		want          int
	}{
		{"chrome-extension://abc", "application/json", 200},
		{"https://evil.com", "application/json", 403},
		{"", "application/json", 403},
		{"chrome-extension://abc", "text/plain", 415},
	} {
		got = nil
		r := httptest.NewRequest("POST", "/import", strings.NewReader(body))
		r.Header.Set("Content-Type", tc.ctype)
		if tc.origin != "" {
			r.Header.Set("Origin", tc.origin)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Errorf("origin %q type %q: status %d, want %d", tc.origin, tc.ctype, w.Code, tc.want)
		}
		if tc.want == http.StatusOK && len(got) != 2 {
			t.Errorf("imported %d cookies, want the 2 of bilibili.com: %+v", len(got), got)
		}
	}

	for _, path := range []string{"/domains", "/platforms"} {
		for origin, want := range map[string]int{"": 200, "chrome-extension://abc": 200, "https://evil.com": 403} {
			r := httptest.NewRequest("GET", path, nil)
			if origin != "" {
				r.Header.Set("Origin", origin)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != want {
				t.Errorf("GET %s from %q: status %d, want %d", path, origin, w.Code, want)
			}
			if want == http.StatusOK && path == "/domains" && strings.TrimSpace(w.Body.String()) != `["bilibili.com"]` {
				t.Errorf("GET /domains: %s", w.Body)
			}
		}
	}
}
