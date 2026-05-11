package main

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

type customheaders []string

func (h *customheaders) String() string { return "Custom headers" }
func (h *customheaders) Set(val string) error {
	*h = append(*h, val)
	return nil
}

var (
	headers     customheaders
	paramList   string
	payload     string
	matchStr    string
	proxy       string
	method      string
	onlyPOC     bool
	htmlOnly    bool
	concurrency int
	wordlist    []string
)

func init() {
	flag.StringVar(&paramList, "lp", "", "URL paths separated by comma (e.g., blog,login,embed)")
	flag.StringVar(&payload, "p", "", "Query params cluster (e.g., ?url=XXX&user=XSS)")
	flag.StringVar(&matchStr, "m", "", "String to match in response body")
	flag.StringVar(&proxy, "x", "", "Proxy URL")
	flag.StringVar(&method, "X", "", "HTTP method: GET, POST or PUT (default: all three)")
	flag.BoolVar(&onlyPOC, "s", false, "Show only PoC output")
	flag.BoolVar(&htmlOnly, "html", false, "Only match if response is HTML")
	flag.Var(&headers, "H", "Add headers (can be used multiple times)")
	flag.IntVar(&concurrency, "t", 50, "Number of threads (minimum 15)")
}

// parsePayload parses "?url=XXX&user=XSS" or "url=XXX&user=XSS"
// into a map of key -> value
func parsePayload(p string) map[string]string {
	p = strings.TrimPrefix(p, "?")
	params := map[string]string{}
	for _, part := range strings.Split(p, "&") {
		kv := strings.SplitN(part, "=", 2)
		if len(kv) == 2 {
			params[strings.TrimSpace(kv[0])] = strings.TrimSpace(kv[1])
		}
	}
	return params
}

// buildGETURL appends params as query string to the URL
func buildGETURL(base string, params map[string]string) string {
	u, err := url.Parse(base)
	if err != nil {
		return base
	}
	q := u.Query()
	for k, v := range params {
		q.Set(k, v)
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// buildFormBody encodes params as application/x-www-form-urlencoded
func buildFormBody(params map[string]string) (io.Reader, string) {
	form := url.Values{}
	for k, v := range params {
		form.Set(k, v)
	}
	return strings.NewReader(form.Encode()), "application/x-www-form-urlencoded"
}

// buildJSONBody encodes params as JSON
func buildJSONBody(params map[string]string) (io.Reader, string) {
	// convert map[string]string to map[string]interface{} for cleaner JSON
	m := make(map[string]interface{}, len(params))
	for k, v := range params {
		m[k] = v
	}
	b, _ := json.Marshal(m)
	return bytes.NewReader(b), "application/json"
}

func main() {
	flag.Parse()

	if concurrency < 15 {
		concurrency = 15
	}

	var methods []string
	if method == "" {
		methods = []string{"GET", "POST", "PUT"}
	} else {
		m := strings.ToUpper(strings.TrimSpace(method))
		if m != "GET" && m != "POST" && m != "PUT" {
			fmt.Fprintf(os.Stderr, "Invalid method '%s'. Use GET, POST or PUT.\n", method)
			os.Exit(1)
		}
		methods = []string{m}
	}

	if paramList == "" || payload == "" || matchStr == "" {
		fmt.Println("Missing required parameters: -lp, -p, -m")
		os.Exit(1)
	}

	wordlist = parseParams(paramList)
	if len(wordlist) == 0 {
		fmt.Fprintln(os.Stderr, "Parameters list is empty")
		os.Exit(1)
	}

	payloadParams := parsePayload(payload)
	if len(payloadParams) == 0 {
		fmt.Fprintln(os.Stderr, "Could not parse payload params. Use format: ?key=val&key2=val2")
		os.Exit(1)
	}

	rand.Seed(time.Now().UnixNano())

	type task struct {
		baseURL string
		word    string
		method  string
	}

	stdin := bufio.NewScanner(os.Stdin)
	targets := make(chan task)
	var wg sync.WaitGroup

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for t := range targets {
				base := strings.TrimRight(t.baseURL, "/") + "/" + t.word
				testURL(base, t.method, payloadParams)
			}
		}()
	}

	for stdin.Scan() {
		u := strings.TrimSpace(stdin.Text())
		if u == "" {
			continue
		}
		for _, word := range wordlist {
			for _, m := range methods {
				targets <- task{baseURL: u, word: word, method: m}
			}
		}
	}

	close(targets)
	wg.Wait()
}

func parseParams(paramStr string) []string {
	var params []string
	for _, part := range strings.Split(paramStr, ",") {
		param := strings.TrimSpace(part)
		if param != "" {
			params = append(params, param)
		}
	}
	return params
}

func testURL(baseURL, method string, params map[string]string) {
	client := buildClient()

	var (
		reqURL      string
		reqBody     io.Reader
		contentType string
	)

	switch method {
	case "GET":
		// params go in the query string
		reqURL = buildGETURL(baseURL, params)
		reqBody = nil
		contentType = ""

	case "POST":
		// params go in the body as form-urlencoded
		reqURL = baseURL
		reqBody, contentType = buildFormBody(params)

	case "PUT":
		// params go in the body as JSON
		reqURL = baseURL
		reqBody, contentType = buildJSONBody(params)
	}

	req, err := http.NewRequest(method, reqURL, reqBody)
	if err != nil {
		return
	}

	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	applyHeaders(req)

	resp, err := client.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()

	if htmlOnly && !strings.Contains(resp.Header.Get("Content-Type"), "text/html") {
		return
	}

	respBody, _ := io.ReadAll(resp.Body)

	if strings.Contains(string(respBody), matchStr) {
		if onlyPOC {
			fmt.Printf("[%s] %s\n", method, reqURL)
		} else {
			fmt.Printf("\033[1;31m[+] Match Found [%s] %s\033[0;0m\n", method, reqURL)
		}
	} else if !onlyPOC {
		fmt.Printf("\033[1;30m[-] Not Vulnerable [%s] %s\033[0;0m\n", method, reqURL)
	}
}

func buildClient() *http.Client {
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		DialContext:     (&net.Dialer{Timeout: 4 * time.Second}).DialContext,
	}
	if proxy != "" {
		if proxyURL, err := url.Parse(proxy); err == nil {
			tr.Proxy = http.ProxyURL(proxyURL)
		}
	}
	return &http.Client{
		Transport: tr,
		Timeout:   6 * time.Second,
	}
}

func applyHeaders(req *http.Request) {
	req.Header.Set("Connection", "close")
	for _, h := range headers {
		parts := strings.SplitN(h, ":", 2)
		if len(parts) == 2 {
			req.Header.Set(strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]))
		}
	}
}
