package main

import (
	"bufio"
	"crypto/tls"
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
	paramFile   string
	payload     string
	matchStr    string
	proxy       string
	onlyPOC     bool
	htmlOnly    bool
	concurrency int
	paramCount  int
	wordlist    []string
)

func init() {
	flag.StringVar(&paramFile, "lp", "", "Path to parameter list file (wordlist)")
	flag.StringVar(&payload, "p", "", "Payload to append")
	flag.StringVar(&matchStr, "m", "", "String to match in response body")
	flag.StringVar(&proxy, "x", "", "Proxy URL")
	flag.BoolVar(&onlyPOC, "s", false, "Show only PoC output")
	flag.BoolVar(&htmlOnly, "html", false, "Only match if response is HTML")
	flag.Var(&headers, "H", "Add headers")
	flag.IntVar(&concurrency, "t", 50, "Number of threads (minimum 15)")
	flag.IntVar(&paramCount, "params", 0, "Number of words to randomly pick from wordlist (1 per request)")
}

func main() {
	flag.Parse()
	if concurrency < 15 {
		concurrency = 15
	}
	if paramFile == "" || payload == "" || matchStr == "" || paramCount <= 0 {
		fmt.Println("Missing required parameters: -lp, -p, -m, -params")
		os.Exit(1)
	}

	var err error
	wordlist, err = readLines(paramFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Failed to read wordlist:", err)
		os.Exit(1)
	}
	if len(wordlist) == 0 {
		fmt.Fprintln(os.Stderr, "Wordlist is empty")
		os.Exit(1)
	}

	rand.Seed(time.Now().UnixNano())

	stdin := bufio.NewScanner(os.Stdin)
	targets := make(chan string)
	var wg sync.WaitGroup

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for baseURL := range targets {
				selected := getRandomWords(wordlist, paramCount)
				for _, word := range selected {
					fullURL := strings.TrimRight(baseURL, "/") + "/" + word + "/" + payload
					testURL(fullURL)
				}
			}
		}()
	}

	for stdin.Scan() {
		url := strings.TrimSpace(stdin.Text())
		if url != "" {
			targets <- url
		}
	}
	close(targets)
	wg.Wait()
}

func getRandomWords(words []string, count int) []string {
	if count >= len(words) {
		return words
	}
	shuffled := make([]string, len(words))
	copy(shuffled, words)
	rand.Shuffle(len(shuffled), func(i, j int) {
		shuffled[i], shuffled[j] = shuffled[j], shuffled[i]
	})
	return shuffled[:count]
}

func testURL(testURL string) {
	client := buildClient()
	req, err := http.NewRequest("GET", testURL, nil)
	if err != nil {
		return
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

	body, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(body), matchStr) {
		if onlyPOC {
			fmt.Println(testURL)
		} else {
			fmt.Printf("\033[1;31m[+] Match Found: %s\033[0;0m\n", testURL)
		}
	} else if !onlyPOC {
		fmt.Printf("\033[1;30m[-] Not Vulnerable: %s\033[0;0m\n", testURL)
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

func readLines(path string) ([]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var lines []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines, scanner.Err()
}
