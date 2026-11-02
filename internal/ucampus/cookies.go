package ucampus

import (
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"sort"
	"strings"
	"sync"
)

// trackedJar preserves Cookie attributes that net/http's request-cookie projection drops.
// Only upstream Set-Cookie values and trusted imported cookies enter this state.
type trackedJar struct {
	jar   *cookiejar.Jar
	mu    sync.Mutex
	saved map[string]map[string]*http.Cookie
}

func newTrackedJar() *trackedJar {
	j, _ := cookiejar.New(nil)
	return &trackedJar{jar: j, saved: map[string]map[string]*http.Cookie{}}
}
func (j *trackedJar) Cookies(u *url.URL) []*http.Cookie { return j.jar.Cookies(u) }
func (j *trackedJar) SetCookies(u *url.URL, cs []*http.Cookie) {
	j.jar.SetCookies(u, cs)
	j.mu.Lock()
	defer j.mu.Unlock()
	origin := u.Scheme + "://" + u.Host
	if j.saved[origin] == nil {
		j.saved[origin] = map[string]*http.Cookie{}
	}
	for _, c := range cs {
		copy := *c
		if copy.Path == "" {
			path := u.Path
			i := strings.LastIndex(path, "/")
			if i <= 0 {
				copy.Path = "/"
			} else {
				copy.Path = path[:i]
			}
		}
		key := copy.Domain + "\x00" + copy.Path + "\x00" + copy.Name
		j.saved[origin][key] = &copy
	}
}
func (j *trackedJar) snapshot(origin string) []*http.Cookie {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := []*http.Cookie{}
	for _, c := range j.saved[origin] {
		copy := *c
		out = append(out, &copy)
	}
	// Stable session encoding keeps cursor bindings reproducible.
	sort.Slice(out, func(a, b int) bool {
		return out[a].Domain+out[a].Path+out[a].Name < out[b].Domain+out[b].Path+out[b].Name
	})
	return out
}
