package main

import (
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	url_ "net/url"
	"os"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var (
	parallelQueries int
	rootPage        string
	totalDepth      int
	userAgent       string
	userAgentHeader map[string][]string
	clientTimeout   int
	client          *http.Client
	rgx             *regexp.Regexp
	wrongProto      *regexp.Regexp
)

type url = string

type urlStruct struct {
	depth int
	url   url
}

type parsed struct {
	urls map[url]int
	mtx  sync.RWMutex
}

func main() {
	parsed := &parsed{
		urls: map[url]int{},
		mtx:  sync.RWMutex{},
	}

	reqURLs := make(chan urlStruct, 256+parallelQueries)
	reqURLs <- urlStruct{
		depth: 1,
		url:   rootPage,
	}

	var toRead int32 = 1
	for i := 0; i < parallelQueries; i++ {
		go queryWorker(parsed, reqURLs, &toRead)
	}

	for range time.Tick(100 * time.Millisecond) {
		if atomic.LoadInt32(&toRead) == 0 {
			close(reqURLs)
			break
		}
	}

	printURLs(parsed.urls)
	fmt.Println("total:", len(parsed.urls))
}

//case: uniqueUrlName (lvl = 10) <= 10 --> !visited
//									   --> map[url]depth
//									   --> newDepth > totalDepth
//									   --> not parse

//case: uniqueUrlName (lvl = 02) <= 10 --> visited, but is not parsed,
//										   if oldDepth is totalDepth
//									   --> upd to newDepth
//									   --> parse
func queryWorker(p *parsed, reqURLs chan urlStruct, toRead *int32) {
	for toVisit := range reqURLs {
		p.mtx.Lock()
		p.urls[toVisit.url] = toVisit.depth
		p.mtx.Unlock()

		if !needParse(toVisit.url) {
			atomic.AddInt32(toRead, -1)
			continue
		}

		newDepth := toVisit.depth + 1
		if newDepth <= totalDepth {
			newURLs := parseURL(toVisit.url)
			go func() {
				n := queueNewURLs(p, reqURLs, newURLs, newDepth)
				atomic.AddInt32(toRead, n-1)
			}()
		} else {
			atomic.AddInt32(toRead, -1)
		}
	}
}

func needParse(u url) bool {
	ext := ""
	if len(u) > 4 {
		ext = u[len(u)-4:]
	}

	switch ext {
	case ".png", ".ico", ".svg", ".jpg", ".ogv", ".mp4", ".aac", ".mp3", ".mov":
		return false
	}
	return true
}

func parseURL(url url) (foundURLs []string) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		log.Println("Can't create request for URL:", url)
		return nil
	}

	resp, err := client.Do(req)
	if err != nil { //|| resp.StatusCode != 200 {
		log.Printf("Some trouble during new request\nError: %s\n\n", err)
		return nil
	}
	defer resp.Body.Close()

	b, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		log.Printf("Some trouble during response read\nURL: %s\nError: %s\n\n", url, err)
		return nil
	}

	return filter(url, rgx.FindAll(b, -1))
}

func filter(root url, newURLs [][]byte) []string {
	formated := make([]string, 0, len(newURLs))
	parsedURL, _ := url_.Parse(root)
	base := parsedURL.Scheme + "://" + parsedURL.Host
	root = strings.TrimSuffix(root, "/")

	for i := range newURLs {
		fmtStr := strings.TrimPrefix(string(newURLs[i]), ` href="`)
		if len(fmtStr) == 0 {
			continue
		}

		if strings.HasPrefix(fmtStr, "//") {
			formated = append(formated, parsedURL.Scheme+":"+fmtStr)
		} else if strings.HasPrefix(fmtStr, "./"){
			if len(fmtStr) > 2 {
				formated = append(formated, root+fmtStr[1:])
			}
		} else if strings.HasPrefix(fmtStr, "/") {
			if len(fmtStr) > 1 {
				formated = append(formated, base+fmtStr)
			}
		} else if fmtStr[0] != '#' && !wrongProto.MatchString(fmtStr) {
			if strings.HasPrefix(fmtStr, "http://") ||
				strings.HasPrefix(fmtStr, "https://") {
				formated = append(formated, fmtStr)
			} else {
				formated = append(formated, base+"/"+fmtStr)
			}
		}
	}

	return formated
}

func queueNewURLs(p *parsed, reqURLs chan urlStruct, newURLs []string, newDepth int) (n int32) {
	for _, url := range newURLs {
		p.mtx.RLock()
		oldDepth, urlVisited := p.urls[url]
		p.mtx.RUnlock()

		if !urlVisited || (oldDepth == totalDepth && newDepth != oldDepth) {
			// !urlVisited || url[pseudo]Visited, not parsed
			reqURLs <- urlStruct{
				depth: newDepth,
				url:   url,
			}
			n++
		}
	}

	return n
}

func printURLs(urls map[url]int) {
	out := make([][]string, totalDepth+1)
	for url, level := range urls {
		out[level] = append(out[level], url)
	}

	for i := range out {
		for j := range out[i] {
			fmt.Println(i, out[i][j])
		}
	}
}

func init() {
	flag.IntVar(&parallelQueries, "n", runtime.NumCPU()*4, "Parallel queries num; def is runtime.NumCPU() * 4 ->")
	flag.StringVar(&rootPage, "root", "", "URL from to start")
	flag.IntVar(&totalDepth, "r", 0, "Recursion depth")
	flag.StringVar(&userAgent, "user-agent", "", "HTTP User-Agent header")
	flag.IntVar(&clientTimeout, "client-timeout", 2000, "HTTP client timeout in ms")
	flag.Parse()

	errs := "Error:\n"

	if rootPage == "" {
		errs += "–> Value of flag `root` can't be empty\n"
	} else if !isURLvalid(rootPage) {
		errs += "–> Value of `root` must be in valid format: http[s]://URL\n"
	}
	if totalDepth <= 0 {
		errs += "–> Value of flag `r` must present or be > 0\n"
	}
	if parallelQueries < 0 {
		errs += "–> Value of flag `n` must be >= 0\n"
	}
	if clientTimeout <= 0 {
		errs += "–> Value of flag `client-timeout` must be > 0\n"
	}

	if errs != "Error:\n" {
		fmt.Fprint(os.Stderr, errs)
		os.Exit(1)
	}

	if parallelQueries == 0 {
		parallelQueries = 1
	}
	if userAgent != "" {
		userAgentHeader = map[string][]string{}
		userAgentHeader["User-Agent"] = []string{userAgent}
	}

	client = &http.Client{
		Timeout: time.Millisecond * time.Duration(clientTimeout),
	}

	rgx = regexp.MustCompile(`(http(s)?://(([\p{L}0-9]+[-.\p{L}}0-9]+\.[\p{L}]+)|(([0-9]{1,3}\.){3}[0-9]{1,3}))(:[0-9]+)?(/(([\p{L}0-9-._~:/?#[\]@!$&'()*+,;=])+|([%[0-9a-fA-F]{3})+)+)?)|( href="[^"]+)`)
	wrongProto = regexp.MustCompile(`(data:text/)|(android-app://)|(tel:)|(mailto:)|(callto:)|(fax:)|(sms:)|(itms://)|(itms-apps://)`)
}

func isURLvalid(toTest string) bool {
	_, err := url_.ParseRequestURI(toTest)
	if err != nil {
		return false
	}

	u, err := url_.Parse(toTest)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return false
	}

	return true
}
