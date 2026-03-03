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
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type catalogEntry struct {
	Source string `json:"source"`
}

type failureHistory struct {
	ConsecutiveHardFailures map[string]int `json:"consecutive_hard_failures"`
	UpdatedAt               string         `json:"updated_at"`
}

//nolint:gocyclo // CLI orchestration is intentionally linear and explicit here.
func main() {
	catalogPath := flag.String("catalog", "", "path to catalog.json (default: models/catalog.json in cwd)")
	concurrency := flag.Int("concurrency", 10, "number of parallel HTTP requests")
	allowStatusFlag := flag.String("allow-status", "403", "comma-separated HTTP status codes to treat as OK (e.g. 403,429)")
	modeFlag := flag.String("mode", "strict", "checker mode: strict or warn-only")
	historyPathFlag := flag.String("history", "", "path to failure history JSON (default: .catalog-check-history.json in cwd)")
	failAfter := flag.Int("fail-after", 3, "in strict mode, fail only after this many consecutive hard failures")
	hardStatusFlag := flag.String("hard-status", "404,410", "comma-separated HTTP statuses considered hard failures")
	flag.Parse()

	if *concurrency < 1 {
		fmt.Fprintf(os.Stderr, "error: -concurrency must be >= 1, got %d\n", *concurrency)
		os.Exit(2)
	}
	if *failAfter < 1 {
		fmt.Fprintf(os.Stderr, "error: -fail-after must be >= 1, got %d\n", *failAfter)
		os.Exit(2)
	}
	if *modeFlag != "strict" && *modeFlag != "warn-only" {
		fmt.Fprintf(os.Stderr, "error: -mode must be one of strict or warn-only, got %q\n", *modeFlag)
		os.Exit(2)
	}

	allowedStatus, err := parseAllowedStatuses(*allowStatusFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: invalid -allow-status: %v\n", err)
		os.Exit(2)
	}

	hardStatus, err := parseAllowedStatuses(*hardStatusFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: invalid -hard-status: %v\n", err)
		os.Exit(2)
	}

	if *catalogPath == "" {
		cwd, _ := os.Getwd()
		*catalogPath = cwd + "/models/catalog.json"
	}
	if *historyPathFlag == "" {
		cwd, _ := os.Getwd()
		*historyPathFlag = filepath.Join(cwd, ".catalog-check-history.json")
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
	var invalidSources []string
	for _, m := range catalog {
		u := strings.TrimSpace(m.Source)
		if u == "" || seen[u] {
			continue
		}
		parsed, err := url.Parse(u)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			invalidSources = append(invalidSources, u)
			seen[u] = true
			continue
		}
		seen[u] = true
		urls = append(urls, u)
	}
	sort.Strings(urls)
	sort.Strings(invalidSources)

	fmt.Fprintf(os.Stderr, "Checking %d unique source URLs (mode=%s, concurrency=%d, allow-status=%s)...\n", len(urls), *modeFlag, *concurrency, *allowStatusFlag)
	if len(invalidSources) > 0 {
		fmt.Fprintf(os.Stderr, "Found %d invalid/non-HTTP source entries\n", len(invalidSources))
	}

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

			userAgent := "ferro-catalog-check/1.0 (+https://github.com/ferro-labs/ai-gateway)"

			headReq, err := http.NewRequest(http.MethodHead, u, nil)
			if err != nil {
				results <- result{url: u, err: err}
				return
			}
			headReq.Header.Set("User-Agent", userAgent)

			headResp, headErr := client.Do(headReq)
			headStatus := 0
			if headResp != nil {
				headStatus = headResp.StatusCode
				_ = headResp.Body.Close()
			}

			if headErr == nil && allowedStatus[headStatus] {
				results <- result{url: u, status: headStatus}
				return
			}

			needGetFallback := headErr != nil || headStatus >= 400
			if !needGetFallback {
				results <- result{url: u, status: headStatus}
				return
			}

			// Some servers return 4xx/5xx for HEAD but succeed on GET.
			// Use a lightweight GET probe first before reporting failure.
			getReq, err := http.NewRequest(http.MethodGet, u, nil)
			if err != nil {
				if headErr != nil {
					results <- result{url: u, err: fmt.Errorf("HEAD: %w; GET request build: %w", headErr, err)}
					return
				}
				results <- result{url: u, status: headStatus}
				return
			}
			getReq.Header.Set("User-Agent", userAgent)
			getReq.Header.Set("Range", "bytes=0-0")
			getReq.Header.Set("Accept-Encoding", "identity")

			getResp, getErr := client.Do(getReq)
			if getErr != nil {
				if getResp != nil {
					_ = getResp.Body.Close()
				}
				if headErr != nil {
					results <- result{url: u, err: fmt.Errorf("HEAD: %w; GET: %w", headErr, getErr)}
					return
				}
				results <- result{url: u, err: fmt.Errorf("HEAD: %d; GET: %w", headStatus, getErr)}
				return
			}
			const maxProbeBodyBytes = 64 * 1024
			_, _ = io.Copy(io.Discard, io.LimitReader(getResp.Body, maxProbeBodyBytes))
			_ = getResp.Body.Close()
			results <- result{url: u, status: getResp.StatusCode}
		}()
	}

	wg.Wait()
	close(results)

	var failures []string
	ok := 0
	hardFailuresThisRun := make(map[string]bool)
	hasInvalidSources := len(invalidSources) > 0
	for _, invalid := range invalidSources {
		failures = append(failures, fmt.Sprintf("  INVALID    %s", invalid))
	}
	for r := range results {
		switch {
		case r.err != nil:
			failures = append(failures, fmt.Sprintf("  CONN ERR  %s\n            %v", r.url, r.err))
		case allowedStatus[r.status]:
			ok++
		case r.status >= 400:
			failures = append(failures, fmt.Sprintf("  HTTP %-4d  %s", r.status, r.url))
			if hardStatus[r.status] {
				hardFailuresThisRun[r.url] = true
			}
		default:
			ok++
		}
	}

	previousHistory, err := loadFailureHistory(*historyPathFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: cannot read history file %s: %v\n", *historyPathFlag, err)
		previousHistory = map[string]int{}
	}
	updatedHistory := make(map[string]int, len(urls))
	for _, u := range urls {
		if hardFailuresThisRun[u] {
			updatedHistory[u] = previousHistory[u] + 1
		} else {
			updatedHistory[u] = 0
		}
	}
	if err := saveFailureHistory(*historyPathFlag, updatedHistory); err != nil {
		fmt.Fprintf(os.Stderr, "warning: cannot write history file %s: %v\n", *historyPathFlag, err)
	}

	sort.Strings(failures)
	fmt.Fprintf(os.Stderr, "%d OK, %d failed\n\n", ok, len(failures))
	fmt.Fprintf(os.Stderr, "Hard-failure policy: statuses=%s, fail-after=%d, history=%s\n\n", *hardStatusFlag, *failAfter, *historyPathFlag)

	if len(failures) > 0 {
		fmt.Fprintln(os.Stderr, "Failed URLs:")
		for _, f := range failures {
			fmt.Fprintln(os.Stderr, f)
		}
		fmt.Fprintln(os.Stderr)
	}

	if *modeFlag == "warn-only" {
		if len(failures) > 0 {
			fmt.Fprintln(os.Stderr, "warn-only mode: failures detected but exit code is 0")
		}
		return
	}

	triggered := make([]string, 0)
	for u := range hardFailuresThisRun {
		if updatedHistory[u] >= *failAfter {
			triggered = append(triggered, fmt.Sprintf("  HTTP hard-failure streak %d/%d  %s", updatedHistory[u], *failAfter, u))
		}
	}
	sort.Strings(triggered)

	if len(triggered) > 0 {
		fmt.Fprintln(os.Stderr, "Gate-triggering failures:")
		for _, t := range triggered {
			fmt.Fprintln(os.Stderr, t)
		}
		os.Exit(1)
	}

	if hasInvalidSources {
		fmt.Fprintln(os.Stderr, "strict mode: invalid source entries detected")
		os.Exit(1)
	}

	if len(failures) > 0 {
		fmt.Fprintf(os.Stderr, "strict mode: no hard-failure streak reached %d yet, exit code is 0\n", *failAfter)
	}
}

func parseAllowedStatuses(raw string) (map[int]bool, error) {
	allowed := make(map[int]bool)
	for _, part := range strings.Split(raw, ",") {
		p := strings.TrimSpace(part)
		if p == "" {
			continue
		}
		code, err := strconv.Atoi(p)
		if err != nil {
			return nil, fmt.Errorf("%q is not a valid status code", p)
		}
		if code < 100 || code > 599 {
			return nil, fmt.Errorf("%q is out of HTTP status code range", p)
		}
		allowed[code] = true
	}
	return allowed, nil
}

func loadFailureHistory(path string) (map[string]int, error) {
	// #nosec G304 -- path is controlled by CLI flag for an expected local history file.
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]int{}, nil
		}
		return nil, err
	}

	var history failureHistory
	if err := json.Unmarshal(data, &history); err != nil {
		return nil, err
	}
	if history.ConsecutiveHardFailures == nil {
		return map[string]int{}, nil
	}
	return history.ConsecutiveHardFailures, nil
}

func saveFailureHistory(path string, consecutive map[string]int) error {
	history := failureHistory{
		ConsecutiveHardFailures: consecutive,
		UpdatedAt:               time.Now().UTC().Format(time.RFC3339),
	}

	data, err := json.MarshalIndent(history, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}
