// Command skilllinks checks that every documentation link a builtin skill lists
// in its belai.resources metadata still answers. It reads the skills compiled
// into Belai (internal/skills), so it checks exactly what ships, and it needs the
// network, so it is not part of `just check`.
//
//	go run ./tools/skilllinks
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/vulnetix/belai/internal/skills"
)

func main() {
	type link struct{ skill, url string }
	var links []link
	for _, e := range skills.Builtin() {
		for _, u := range skills.Resources(e.Metadata[skills.MetaResources]) {
			links = append(links, link{e.Name, u})
		}
	}
	client := &http.Client{Timeout: 25 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return http.ErrUseLastResponse
		}
		return nil
	}}
	var (
		mu     sync.Mutex
		failed []string
		wg     sync.WaitGroup
		sem    = make(chan struct{}, 8)
	)
	for _, l := range links {
		wg.Add(1)
		sem <- struct{}{}
		go func(l link) {
			defer wg.Done()
			defer func() { <-sem }()
			if msg := check(client, l.url); msg != "" {
				mu.Lock()
				failed = append(failed, fmt.Sprintf("%s  %s  %s", l.skill, l.url, msg))
				mu.Unlock()
			}
		}(l)
	}
	wg.Wait()
	sort.Strings(failed)
	fmt.Printf("%d links in %d skills, %d failed\n", len(links), len(skills.Builtin()), len(failed))
	for _, f := range failed {
		fmt.Println(f)
	}
	if len(failed) > 0 {
		os.Exit(1)
	}
}

// check returns "" when url answers with a success or a redirect, else why not.
// It asks with GET after HEAD, because some documentation hosts refuse HEAD.
func check(c *http.Client, url string) string {
	var last string
	for _, method := range []string{http.MethodHead, http.MethodGet} {
		for attempt := 0; attempt < 2; attempt++ {
			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
			req, err := http.NewRequestWithContext(ctx, method, url, nil)
			if err != nil {
				cancel()
				return err.Error()
			}
			req.Header.Set("User-Agent", "belai-skilllinks/1")
			res, err := c.Do(req)
			cancel()
			if err != nil {
				last = err.Error()
				time.Sleep(time.Second)
				continue
			}
			res.Body.Close()
			if res.StatusCode < 400 {
				return ""
			}
			last = fmt.Sprintf("HTTP %d", res.StatusCode)
			break
		}
	}
	return last
}
