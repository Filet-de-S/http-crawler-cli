package main

import (
	"bufio"
	"flag"
	"fmt"
	"io/ioutil"
	"net/http"
	url_ "net/url"
	"os"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	log "github.com/sirupsen/logrus"
)

var (
	workersNum              int
	rootPage                string
	totalDepth              int
	header                  http.Header
	tryAllowed              int
	omitExt                 []string
	defaultExtNeeded        bool
	deltaSuccess            float32
	deltaFail               float32
	chanCapOnDepthHeuristic int
	uniqueCounter           int
	uniqueStorage           parsed
	minHalt                 int64
	maxHalt                 int64
	fdRes                   *os.File
	logFile                 *os.File
	client                  *http.Client
	rgx                     *regexp.Regexp
	wrongProto              *regexp.Regexp
)

const (
	workersPerLogCPUdef = 6
	clientTimeoutDef    = 2000
	retryDef            = 0
	haltMinMaxDef       = "200,500"
	deltaOKfailDef      = "1,-2"
)

type url = string

type urlStruct struct {
	url    url
	depth  int
	trying int
}

type urlPrint struct {
	url        url
	depth      int
	delOnDepth int
}

type newURLs struct {
	urls  []url
	depth int
}

type stdMAP struct {
	hash map[url]int
}

type parsed interface {
	getByURL(url url) (depth int, exists bool, err error)
	saveByURL(url url, depth int) error
	//getTotal
}

type ctx struct {
	parsed      parsed
	reqURLs     chan urlStruct
	printer     chan urlPrint
	forwarder   chan newURLs
	queue       int32
	printQue    []int32
	reqStatus   chan float32
	dynamicHalt int64
}

func main() {
	defer fdRes.Close()
	defer logFile.Close()

	ctx, done, now := start()

	for range time.Tick(100 * time.Millisecond) {
		if atomic.LoadInt32(&ctx.queue) == 0 {
			close(ctx.reqURLs)
			close(ctx.forwarder)
			close(ctx.printer)
			close(ctx.reqStatus)

			break
		}
	}

	<-done
	_, err := fmt.Fprintln(fdRes, uniqueCounter)
	if err != nil {
		log.WithFields(log.Fields{
			"type": "writing result",
			"err":  err,
		}).Warn()
	}

	log.WithFields(log.Fields{
		"total": uniqueCounter,
		"time":  time.Since(now)}).
		Info("done")
}

func start() (*ctx, chan bool, time.Time) {
	ctx := &ctx{
		parsed:    uniqueStorage,
		reqURLs:   make(chan urlStruct, chanCapOnDepthHeuristic),
		forwarder: make(chan newURLs, chanCapOnDepthHeuristic),
		printer:   make(chan urlPrint, 1024+workersNum),
		reqStatus: make(chan float32, workersNum),
		printQue:  make([]int32, totalDepth+1),
	}

	ctx.forwarder <- newURLs{
		urls:  []string{rootPage},
		depth: 1,
	}
	ctx.queue = 1

	done := make(chan bool)

	now := time.Now()
	for i := 0; i < workersNum; i++ {
		go queryWorker(ctx)
	}
	go forwardNewURLs(ctx)
	go haltCtrl(ctx)
	go printURLsInLive(ctx, done)

	return ctx, done, now
}


//case: uniqueUrlName (lvl = 10) <= 10 --> !visited
//									   --> map[url]depth
//									   --> newDepth > totalDepth
//									   --> not parse

//case: uniqueUrlName (lvl = 02) <= 10 --> visited, but is not parsed,
//										   if oldDepth is totalDepth
//									   --> upd to newDepth
//									   --> parse
func queryWorker(ctx *ctx) {
	for toVisit := range ctx.reqURLs {

		newDepth := toVisit.depth + 1
		if newDepth <= totalDepth && needParse(toVisit.url){
			go func(u urlStruct) {
				ctx.forwarder <- newURLs{
					depth: newDepth,
					urls:  parseURL(ctx, u),
				}
			}(toVisit)
			time.Sleep(time.Duration(atomic.LoadInt64(&ctx.dynamicHalt)))

		} else {
			atomic.AddInt32(&ctx.printQue[toVisit.depth], -1)
			atomic.AddInt32(&ctx.queue, -1)
		}
	}
}

func forwardNewURLs(ctx *ctx) {
	var err error
	var n int32 = 0
	var oldDepth int
	var urlVisited bool

	for newURLsPack := range ctx.forwarder {

		n = 0
		for _, url := range newURLsPack.urls {
			oldDepth, urlVisited, err = getByURL(ctx, url)
			if err != nil {
				log.WithFields(log.Fields{
					"type": "unique storage get",
					"err:": err,
				}).Warn()
				continue
			}

			// !urlVisited || url[pseudo]Visited, not parsed
			if !urlVisited || (oldDepth == totalDepth && newURLsPack.depth != oldDepth) {
				err = saveByURL(ctx, url, newURLsPack.depth)
				if err != nil {
					log.WithFields(log.Fields{
						"type": "unique storage save",
						"err:": err,
					}).Warn()
					continue
				}

				forward(ctx, url, urlVisited, newURLsPack.depth, oldDepth)
				n++
			}
		}

		atomic.AddInt32(&ctx.printQue[newURLsPack.depth-1], -1)
		atomic.AddInt32(&ctx.queue, n-1)
	}

}

func forward(ctx *ctx, url url, urlVisited bool, newDepth, oldDepth int) {
	//To exclude case when data is sent and depth is 0, which allows to print. Incr before sent
	atomic.AddInt32(&ctx.printQue[newDepth], 1)

	ctx.reqURLs <- urlStruct{
		depth: newDepth,
		url:   url,
	}

	if urlVisited {
		ctx.printer <- urlPrint{
			depth:      newDepth,
			url:        url,
			delOnDepth: oldDepth,
		}
	} else {
		uniqueCounter++

		ctx.printer <- urlPrint{
			depth: newDepth,
			url:   url,
		}
	}
}

func saveByURL(ctx *ctx, url url, depth int) (err error) {
	for i := 0; i < 2; i++ {
		err = ctx.parsed.saveByURL(url, depth)
		if err == nil {
			return
		}

		time.Sleep(100*time.Millisecond)
	}
	return
}

func getByURL(ctx *ctx, url url) (oldDepth int, urlVisited bool, err error) {
	for i := 0; i < 2; i++ {
		oldDepth, urlVisited, err = ctx.parsed.getByURL(url)
		if err == nil {
			return
		}

		time.Sleep(100*time.Millisecond)
	}
	return
}

func needParse(u url) bool {
	if defaultExtNeeded {
		ext := ""
		if len(u) > 4 {
			ext = u[len(u)-4:]
		}

		switch ext {
		case ".png", ".ico", ".svg", ".jpg", ".ogv", ".mp4", ".aac", ".mp3", ".mov",
			".gif", ".css", ".pdf":
			return false
		}
	}

	for i := range omitExt {
		if strings.HasSuffix(u, omitExt[i]) {
			return false
		}
	}
	return true
}

func parseURL(ctx *ctx, url urlStruct) (foundURLs []string) {
	req, err := http.NewRequest("GET", url.url, nil)
	if err != nil {
		log.WithFields(log.Fields{
			"type": "can't create request",
			"url":  url.url,
		}).Warn()
		return nil
	}
	req.Header = header

	resp, err := client.Do(req)
	if err != nil { //|| resp.StatusCode != 200 {
		ctx.reqStatus <- deltaFail
		again := retry(ctx, url)
		log.WithFields(log.Fields{
			"type":  "bad request",
			"err":   err,
			"retry": again,
		}).Warn()
		return nil
	}
	defer resp.Body.Close()

	b, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		again := retry(ctx, url)
		log.WithFields(log.Fields{
			"type":  "response read",
			"err":   err,
			"retry": again,
			"url":   url.url,
		}).Warn()
		return nil
	}

	ctx.reqStatus <- deltaSuccess
	return filter(url.url, rgx.FindAll(b, -1))
}

func retry(ctx *ctx, url urlStruct) (again bool) {
	url.trying++
	if url.trying <= tryAllowed {
		again = true
		atomic.AddInt32(&ctx.printQue[url.depth], 1)
		atomic.AddInt32(&ctx.queue, 1)
		go func() {
			ctx.reqURLs <- url
		}()
	}
	return again
}

func filter(root url, newURLs [][]byte) []string {
	parsedURL, base, this, that := initFilter(root)
	formated := make([]string, 0, len(newURLs))
	root = strings.TrimSuffix(root, "/")

	for i := range newURLs {
		fmtStr := strings.TrimPrefix(string(newURLs[i]), ` href="`)
		fmtStr = strings.Trim(fmtStr, " ")
		l := len(fmtStr)

		switch {
		case l == 0:
			continue
		case l > 3 && strings.HasPrefix(fmtStr, "//"):
			//save proto http[s]:
			formated = append(formated, parsedURL.Scheme+":"+fmtStr)
		case l > 1 && strings.HasPrefix(fmtStr, "/"):
			//href="/page.html" => base.url/page.html
			formated = append(formated, base+fmtStr)
		case l > 2 && strings.HasPrefix(fmtStr, "./"):
			//base.url/something/also+ /fmtStr
			formated = append(formated, root+fmtStr[1:])
		case l > 3 && strings.HasPrefix(fmtStr, "../"):
			// base.url/abc/123/444/index => base.url/abc/123/NewPage
			formated = append(formated, that+fmtStr[3:])
		case fmtStr[0] != '#' && !wrongProto.MatchString(fmtStr):
			if strings.HasPrefix(fmtStr, "http://") ||
				strings.HasPrefix(fmtStr, "https://") {
				formated = append(formated, fmtStr)
			} else { //href="page.html" => base.url/abc/ex => base.url/abc/page.hmtl
				formated = append(formated, this+fmtStr)
			}
		}
	}

	return formated
}

func initFilter(root url) (*url_.URL, string, string, string) {
	parsedURL, _ := url_.Parse(root)
	base := parsedURL.Scheme + "://" + parsedURL.Host

	i := strings.LastIndex(parsedURL.Path, "/")
	if i == -1 {
		i = len(parsedURL.Path)
	}
	thisPath := parsedURL.Path[:i]
	this := base + thisPath + "/"

	i = strings.LastIndex(thisPath, "/")
	if i == -1 {
		i = len(thisPath)
	}
	that := base + thisPath[:i] + "/"

	return parsedURL, base, this, that
}

func haltCtrl(ctx *ctx) {
	var successRate, newRate float32
	var ok bool
	lastHalt := minHalt

	for delta := range ctx.reqStatus {
		newRate = successRate + delta
		switch {
		case successRate > 0 && delta > 0 && newRate < 0:
			successRate = 51
		case successRate < 0 && delta < 0 && newRate > 0:
			successRate = -11
		default:
			successRate = newRate
		}

		lastHalt, ok = needHaltUpd(successRate, lastHalt)
		if ok {
			atomic.StoreInt64(&ctx.dynamicHalt, lastHalt)
			successRate = 0

			log.WithField("time", time.Duration(lastHalt)).
				Info("new halt")
		}
	}
}

func needHaltUpd(sr float32, halt int64) (int64, bool) {
	switch {
	case sr < -10:
		return maxHalt, true
	case sr < 0:
		return (halt + maxHalt) / 2, true
	case sr > 50:
		return (halt + minHalt) / 2, true
	default:
		return 0, false
	}
}

func printURLsInLive(ctx *ctx, done chan bool) {
	toPrint := make([][]string, totalDepth+1)
	w := bufio.NewWriter(fdRes)
	var url urlPrint
	var lastLevel = 1
	var starve = 1

	for url = range ctx.printer {
		toPrint[url.depth] = append(toPrint[url.depth], url.url)
		if url.delOnDepth > 1 {
			deleteOldValue(url.url, url.delOnDepth, &toPrint)
		}
		if starve%127 == 0 {
			lastLevel = bufferAppend(ctx, w, &toPrint, lastLevel)
		}
		starve++
	}
	bufferAppend(ctx, w, &toPrint, lastLevel)
	done <- true
}

func deleteOldValue(url url, depth int, toPrint *[][]string) {
	for i, u := range (*toPrint)[depth] {
		if u == url {
			arr := (*toPrint)[depth]
			arr[i] = arr[len(arr)-1]
			arr[len(arr)-1] = ""
			(*toPrint)[depth] = arr[:len(arr)-1]
			break
		}
	}
}

func bufferAppend(ctx *ctx, w *bufio.Writer, toPrint *[][]string, lastLevel int) int {
	for depth := lastLevel; depth <= totalDepth; depth++ {
		readiness := atomic.LoadInt32(&ctx.printQue[depth])

		if readiness == 0 && (*toPrint)[depth] != nil {
			append_(&(*toPrint)[depth], depth, w)
		} else if (*toPrint)[depth] != nil {
			append_(&(*toPrint)[depth], depth, w)
			return depth
		}
	}

	err := w.Flush()
	if err != nil {
		log.WithFields(log.Fields{
			"type": "flushing result",
			"err":  err,
		}).Warn()
	}

	return totalDepth
}

func append_(arr *[]string, depth int, w *bufio.Writer) {
	for i := range *arr {
		_, err := w.WriteString(strconv.Itoa(depth) + " " + (*arr)[i] + "\n")
		if err != nil {
			log.WithFields(log.Fields{
				"type": "writing result",
				"err":  err,
			}).Warn()
		}
	}
	*arr = nil
}

func printURLsDeprecated(urls map[url]int) {
	res := make([][]string, totalDepth+1)
	for url, level := range urls {
		res[level] = append(res[level], url)
	}

	for i := range res {
		for j := range res[i] {
			fmt.Fprintln(fdRes, i, res[i][j])
		}
	}
}

func (m stdMAP) getByURL(url url) (depth int, exists bool, err error) {
	oldDepth, urlVisited := m.hash[url]

	return oldDepth, urlVisited, nil
}

func (m stdMAP) saveByURL(url url, depth int) error {
	m.hash[url] = depth

	return nil
}

func init() {
	fl := flagsInit()

	client = &http.Client{Timeout: time.Millisecond * time.Duration(fl.clientTimeout)}

	if tryAllowed < 0 {
		tryAllowed = 0
	}

	chanCapOnDepthHeuristic = avgDepthNeeded()

	minHalt = int64(time.Millisecond * time.Duration(fl.minHalt))
	maxHalt = int64(time.Millisecond * time.Duration(fl.maxHalt))

	deltaSuccess = fl.deltaSuccess
	deltaFail = fl.deltaFail

	omitExt = fl.omitExtensions
	defaultExtNeeded = fl.defaultExtNeeded

	rgx = regexp.MustCompile(`(http(s)?://(([\p{L}0-9]+[-.\p{L}}0-9]+\.[\p{L}]+)|(([0-9]{1,3}\.){3}[0-9]{1,3}))(:[0-9]+)?(/(([\p{L}0-9-._~:/?#[\]@!$&'()*+,;=])+|([%[0-9a-fA-F]{3})+)+)?)|( href="[^"]+)`)
	wrongProto = regexp.MustCompile(`(data:text/)|(android-app://)|(tel:)|(mailto:)|(callto:)|(fax:)|(sms:)|(itms://)|(itms-apps://)`)

	headerInit(fl)

	if fl.resultFile != "" {
		var err error
		fdRes, err = os.OpenFile(fl.resultFile,
			os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			defer logFile.Close()
			log.Fatal("Can't open or create result file :", err)
		}
	} else {
		fdRes = os.Stdout
	}

	//todo if redis or other staff is empty
	// if not redis, initFrom implemented callingURL or gRPC :)
	uniqueStorage = stdMAP{map[url]int{}}
}

type flags struct {
	omitExtensions   []string
	defaultExtNeeded bool
	clientTimeout    int
	headerFile       string
	userAgent        string
	minHalt          int
	maxHalt          int
	resultFile       string
	logFile          string
	deltaSuccess     float32
	deltaFail        float32
}

func avgDepthNeeded() int {
	n := workersNum
	switch totalDepth {
	case 1:
		return n
	case 2:
		return 70 + n
	case 3:
		//70^2=4900
		return 70*70 + n
	default:
		//70^3=343000
		return 10000 + n
	}
}

func headerInit(fl flags) {
	if fl.headerFile != "" {
		f, err := os.Open(fl.headerFile)
		if err != nil {
			defer logFile.Close()
			log.Fatal("Can't open header file :", err)
		}
		defer f.Close()

		sc := bufio.NewScanner(f)

		header = http.Header{}
		var name string
		i := 1
		for sc.Scan() {
			if i%2 == 0 {
				header[name] = []string{sc.Text()}
			} else {
				name = sc.Text()
			}
			i++
		}
		if err := sc.Err(); err != nil {
			defer logFile.Close()
			log.Fatal("Can't read header file :", err)
		}
		if (i-1)%2 != 0 {
			defer logFile.Close()
			log.Fatal("Check header file format. Must have even lines: `HeaderName` newline `HeaderValue`", err)
		}

		if fl.userAgent != "" {
			header["User-Agent"] = []string{fl.userAgent}
		}
	} else if fl.userAgent != "" {
		header = map[string][]string{}
		header["User-Agent"] = []string{fl.userAgent}
	}
}

func flagsInit() flags {
	fl := flags{}
	haltMinMax := ""
	delta := ""
	omitExtensions := ""

	flag.IntVar(&workersNum, "n", runtime.NumCPU()*workersPerLogCPUdef, "Parallel requests num (workers); Default: `runtime.NumCPU() * 6` ->")
	flag.StringVar(&rootPage, "root", "", "URL from to start, scheme required")
	flag.IntVar(&totalDepth, "r", 0, "Recursion depth")
	flag.StringVar(&fl.userAgent, "user-agent", "", "HTTP User-Agent header")
	flag.StringVar(&fl.headerFile, "header-file", "", "HTTP headers in format `HeaderName` newline `HeaderValue`")
	flag.IntVar(&fl.clientTimeout, "client-timeout", clientTimeoutDef, "HTTP client timeout in ms")
	flag.StringVar(&haltMinMax, "halt-min-max", haltMinMaxDef, "Min,max halt time per worker in ms to slow down and get less refused requests. Initial halt time is min. Usage: `200,4000`")
	flag.StringVar(&delta, "delta-ok-fail", deltaOKfailDef, "Abstract deltas[knobs] in float for success/fail request. Usage: `1,-2`. Example: `overallSuccessRate += [ok/fail delta]; if (oSR <= -10) { haltTime *= constFor(-10) }`, watch `needHaltUpd()` and `haltCtrl()`")
	flag.StringVar(&fl.resultFile, "out-file", "", "File path for result to write. If file exists -> will append (default stdout)")
	flag.StringVar(&fl.logFile, "log-file", "", "File path for logs to write. If flag is set – will use JSON format. If file exists -> will append (default stdout)")
	flag.IntVar(&tryAllowed, "try", retryDef, "Times to retry refused URL request")
	flag.StringVar(&omitExtensions, "omit-ext", "", "Omit parsing URLs with extensions. Generally it checks a suffix of URL, so it can pass all `index.html`, but leave `.html`. Default is: `.png, .ico, .svg, .jpg, .ogv, .mp4, .aac, .mp3, .mov, .gif, .css, .pdf`; Usage: `.mp5 .mkv`; To off default add `-`, like `- .mp5 .mkv` or just `-`")
	flag.Parse()

	if fl.logFile != "" {
		var err error
		logFile, err = os.OpenFile(fl.logFile,
			os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			defer logFile.Close()
			log.Fatal("Can't open or create log file:", err)
		}

		log.SetFormatter(&log.JSONFormatter{})
		log.SetOutput(logFile)
	} else {
		log.SetOutput(os.Stdout)
	}

	checkFormat(&fl, haltMinMax, delta, omitExtensions)
	return fl
}

func checkFormat(fl *flags, haltMinMax, delta, omitExtensions string) {
	var err error
	var errs string

	if workersNum < 1 {
		errs += "–> Value of flag `n` must be > 0\n"
	}
	if rootPage == "" {
		errs += "–> Value of flag `root` can't be empty\n"
	} else if !isURLvalid(rootPage) {
		errs += "–> Value of `root` must be in valid format: http[s]://URL\n"
	}
	if totalDepth < 1 {
		errs += "–> Value of flag `r` must present and be > 0\n"
	}
	if fl.clientTimeout < 1 {
		errs += "–> Value of flag `client-timeout` must be > 0\n"
	}
	{
		if haltMinMax != "" {
			hltMinMAx := strings.Split(haltMinMax, ",")
			if len(hltMinMAx) != 2 {
				errs += "–> Value of flag `min-max-halt` must be in valid format: min,max\n"
			}
			fl.minHalt, err = strconv.Atoi(hltMinMAx[0])
			if err != nil {
				errs += "–> Value of flag `min-max-halt` must be num in ms, have min = " + hltMinMAx[0] + "\n"
			}
			fl.maxHalt, err = strconv.Atoi(hltMinMAx[1])
			if err != nil {
				errs += "–> Value of flag `min-max-halt` must be num in ms, have max = " + hltMinMAx[1] + "\n"
			}
			if fl.minHalt > fl.maxHalt || fl.maxHalt < 0 || fl.minHalt < 0 {
				errs += "–> Values of flag `min-max-halt` must be >= 0\n"
			}
		}
	}
	{
		if delta != "" {
			dlta := strings.Split(delta, ",")
			if len(dlta) != 2 {
				errs += "–> Flag `delta-ok-fail` must have 2 arguments and be in valid format: ok,fail\n"
			}
			dltOk, err := strconv.ParseFloat(dlta[0], 32)
			if err != nil {
				errs += "–> Value of flag `delta-ok-fail` must be num, have ok = " + dlta[0] + "\n"
			}
			fl.deltaSuccess = float32(dltOk)
			dltFail, err := strconv.ParseFloat(dlta[1], 32)
			if err != nil {
				errs += "–> Value of flag `delta-ok-fail` must be num, have fail = " + dlta[0] + "\n"
			}
			fl.deltaFail = float32(dltFail)
			if fl.deltaSuccess < 0 || fl.deltaFail > 0 {
				errs += "–> Values of flag `delta-ok-fail` bad format. Want: fail <= 0 >= ok \n"
			}
		}
	}
	{
		fl.defaultExtNeeded = true
		if omitExtensions != "" {
			fl.omitExtensions = strings.Split(omitExtensions, " ")
			for i, ext := range fl.omitExtensions {
				if ext == "-" && i == 0 {
					fl.defaultExtNeeded = false
					fl.omitExtensions = fl.omitExtensions[1:]
				} else {
					if ext[0] != '.' {
						errs += "–> Value of flag `omit-ext`: extension must start with [dot], problem with '" + ext + "' \n"
					}
				}
			}
		}
	}

	if errs != "" {
		defer logFile.Close()
		log.WithField("type", "bad format").Fatal(errs)
	}
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
