// catalog-check reads every "source" URL from models/catalog.json and performs
// a HEAD request against each one. Any URL that returns a 4xx or 5xx status,
// or fails to connect, is reported. The process exits with code 1 if any
// failures are found so the GitHub Action can open an issue.
//
// Usage:
//
// go run ./scripts/catalog-check              # uses models/catalog.json in repo root
// go run ./scripts/catalog-check -catalog /path/to/catalog.json
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

type catalogEntry struct {
	Source string `json:"source"`
}

func main() {
	catalogPath := flag.String("catalog", "", "path to catalog.json (default: models/catalog.json in cwd)")
	concurrency := flag.Int("concurrency", 10, "number of parallel HTTP requests")
	flag.Parse()

	if *catalogPath == "" {
		cwd, _ := os.Getwd()
		*catalogPath = cwd + "/models/catalog.json"
	}

	data, err := os.ReadFile(*catalogPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: cannot read catalog: %v\n", err)
		os.Exit(2)
	}

	var catalog map[string]catalogEntry
	if err := json.Unmarshal(data, &catalog); err != nil {
		fmt.Fprintf(os.Stderr, "error: cannot parse catalog: %v\n", err)
		os.Exit(2)
	}

	// Collect unique non-empty source URLs.
	seen := map[string]bool{}
	var urls []string
	for _, m := range catalog {
		u := strings.TrimSpace(m.Source)
		if u == "" || seen[u] {
			continue
		}
		seen[u] = true
		urls = append(urls, u)
	}
	sort.Strings(urls)

	fmt.Fprintf(os.Stderr, "Checking %d unique source URLs (concurrency=%d)...\n", len(urls), *concurrency)

	type result struct {
		url    string
		status int
		err    error
	}

	client := &http.Client{
		Timeout: 10 * time.Second,
		CheckRedirect: func(_ *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("too many redirects")
			}
			return nil
		},
	}

	sem := make(chan struct{}, *concurrency)
	results := make(chan result, len(urls))
	var wg sync.WaitGroup

	for _, u := range urls {
		u := u
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			req, err := http.NewRequest(http.MethodHead, u, nil)
			if err != nil {
				results <- result{url: u, err: err}
				return
			}
			req.Header.Set("User-Agent", "ferro-catalog-check/1.0 (+https://github.com/ferro-labs/ai-gateway)")

			resp, err := client.Do(req)
			if err != nil {
				// Some servers reject HEAD; retry with GET.
				req2, _ := http.NewRequest(http.MethodGet, u, nil)
				req2.Header.Set("User-Agent", req.Header.Get("User-Agent"))
				resp2, err2 := client.Do(req2)
				if err2 != nil {
					results <- result{url: u, err: err}
					return
				}
				_ = resp2.Body.Close()
				results <- result{url: u, status: resp2.StatusCode}
				return
			}
			_ = resp.Body.Close()
			results <- result{url: u, status: resp.StatusCode}
		}()
	}

	wg.Wait()
	close(results)

	var failures []string
	ok := 0
	for r := range results {
		switch {
		case r.err != nil:
			failures = append(failures, fmt.Sprintf("  CONN ERR  %s\n            %v", r.url, r.err))
		case r.status >= 400:
			failures = append(failures, fmt.Sprintf("  HTTP %-4d  %s", r.status, r.url))
		default:
			ok++
		}
	}

	sort.Strings(failures)
	fmt.Fprintf(os.Stderr, "%d OK, %d failed\n\n", ok, len(failures))

	if len(failures) > 0 {
		fmt.Fprintln(os.Stderr, "Failed URLs:")
		for _, f := range failures {
			fmt.Fprintln(os.Stderr, f)
		}
		os.Exit(1)
	}
}
