package main

import (
	"bufio"
	"context"
	_urlStore "crawler/uniqURL_db"
	"crawler/uniqURL_db/grpc_client"
	"crawler/uniqURL_db/stdmap"
	"flag"
	"fmt"
	"io/ioutil"
	"net/http"
	url_ "net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	log "github.com/sirupsen/logrus"
)

var (
	workersNum              int
	rootPage                string
	totalDepth              int32
	header                  http.Header
	tryAllowed              int32
	omitExt                 []string
	defaultExtNeeded        bool
	deltaSuccess            float32
	deltaFail               float32
	chanCapOnDepthHeuristic int32
	uniqueCounter           int64
	urlStore                _urlStore.Parsed
	minHalt                 int64
	maxHalt                 int64
	resFile                 *os.File
	logFile                 *os.File
	client                  *http.Client
	rgx                     *regexp.Regexp
	wrongProto              *regexp.Regexp
)

const (
	workersDef       = 48
	clientTimeoutDef = 5000
	retryDef         = 1
	haltMinMaxDef    = "0,500"
	deltaOKfailDef   = "1,-10"
)

type url = string

type urlStruct struct {
	url    url
	depth  int32
	trying int32
}

type urlPrint struct {
	url        url
	depth      int32
	delOnDepth int32
}

type newURLs struct {
	urls  []url
	depth int32
}

type ctx struct {
	ctx_ 		context.Context
	parsed      _urlStore.Parsed

	reqURLs     chan urlStruct
	printer     chan urlPrint
	forwarder   chan newURLs

	queue       int32
	printQue    []int32

	reqStatus   chan float32
	dynamicHalt int64
	lastHalt	int64

	now 		time.Time
}

func main() {
	ctx, done := start()

	defer resFile.Close()
	defer logFile.Close()
	defer ctx.parsed.Close()

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
	_, err := fmt.Fprintln(resFile, uniqueCounter)
	if err != nil {
		log.WithFields(log.Fields{
			"type": "writing result",
			"err":  err,
		}).Warn()
	}

	log.WithFields(log.Fields{
		"total": uniqueCounter,
		"in time":  time.Since(ctx.now)}).
		Info("done")
}

func start() (*ctx, chan bool) {
	ctx := &ctx{
		ctx_: context.Background(),
		parsed:    urlStore,
		reqURLs:   make(chan urlStruct, chanCapOnDepthHeuristic),
		forwarder: make(chan newURLs, chanCapOnDepthHeuristic),
		printer:   make(chan urlPrint, 1024+workersNum),
		printQue:  make([]int32, totalDepth+1),
		reqStatus: make(chan float32, workersNum),
		dynamicHalt: minHalt,
		lastHalt: minHalt,
	}

	ctx.forwarder <- newURLs{
		urls:  []string{rootPage},
		depth: 1,
	}
	ctx.queue = 1

	done := make(chan bool)

	log.WithFields(log.Fields{
		"workers": workersNum,
		"minHalt": minHalt,
		"maxHalt": maxHalt,
		"deltaOK": deltaSuccess,
		"deltaFAIL": deltaFail,
		"writingResTo": resFile.Name(),
		"header": header,
		"rootPage": rootPage,
		"depth": totalDepth,
		"client-timeout": client.Timeout,
		"retry": tryAllowed,
		"omit-ext": omitExt,
		"defaultExtNeeded": defaultExtNeeded,
	}).Info("start")

	ctx.now = time.Now()
	for i := 0; i < workersNum; i++ {
		go queryWorker(ctx)
	}
	go forwardNewURLs(ctx)
	go haltCtrl(ctx)
	go printURLsInLive(ctx, done)

	return ctx, done
}

func queryWorker(ctx *ctx) {
	var dyn int64
	for toVisit := range ctx.reqURLs {

		newDepth := toVisit.depth + 1
		if newDepth <= totalDepth && needParse(toVisit.url){
			go func(urls []url) {
				ctx.forwarder <- newURLs{
					depth: newDepth,
					urls:  urls,
				}
			}(parseURL(ctx, toVisit))

			dyn = atomic.LoadInt64(&ctx.dynamicHalt)
			if dyn != atomic.LoadInt64(&ctx.lastHalt) {
				atomic.StoreInt64(&ctx.lastHalt, dyn)

				log.Debug("sleep for", time.Duration(dyn))
				time.Sleep(time.Duration(dyn))
			}

		} else {
			atomic.AddInt32(&ctx.printQue[toVisit.depth], -1)
			atomic.AddInt32(&ctx.queue, -1)
		}
	}
}

func forwardNewURLs(ctx *ctx) {
	var err error
	var n int32 = 0
	var oldDepth int32
	var urlVisited bool

	for newURLsPack := range ctx.forwarder {

		n = 0
		for _, url := range newURLsPack.urls {
			oldDepth, urlVisited, err = getByURL(ctx, url)
			if err != nil {
				log.WithFields(log.Fields{
					"type": "unique storage get",
					"err:": err,
				}).Panic()
			}

			// !urlVisited || url[pseudo]Visited, not parsed
			if !urlVisited || (oldDepth == totalDepth && newURLsPack.depth != oldDepth) {
				err = saveByURL(ctx, url, newURLsPack.depth)
				if err != nil {
					log.WithFields(log.Fields{
						"type": "unique storage save",
						"err:": err,
					}).Panic()
				}

				forward(ctx, url, urlVisited, newURLsPack.depth, oldDepth)
				n++
			}
		}

		atomic.AddInt32(&ctx.printQue[newURLsPack.depth-1], -1)
		atomic.AddInt32(&ctx.queue, n-1)
	}

}

func forward(ctx *ctx, url url, urlVisited bool, newDepth, oldDepth int32) {
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
		atomic.AddInt32(&ctx.printQue[oldDepth], -1)

	} else {
		uniqueCounter++

		ctx.printer <- urlPrint{
			depth: newDepth,
			url:   url,
		}

		if uniqueCounter % 1001 == 0 {
			log.Debug("UNIQUE FOUND", uniqueCounter, time.Since(ctx.now))
		}
	}
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

	trySuccess := false
	if url.trying > 0 {
		trySuccess = true
		time.Sleep(time.Duration(
			atomic.LoadInt64(&ctx.dynamicHalt)))
	}

	resp, err := client.Do(req)
	if err != nil {
		ctx.reqStatus <- deltaFail
		again := retry(ctx, url)
		log.WithFields(log.Fields{
			"type":  "fail request",
			"err":   err,
			"retry": again,
		}).Warn()
		return nil
	}
	defer resp.Body.Close()

	if trySuccess {
		log.WithFields(log.Fields{
			"retry n": url.trying,
			"url": url.url,
		}).Info("retry success")
	}

	b, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		again := retry(ctx, url)
		log.WithFields(log.Fields{
			"type":  "response read fail",
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
	newHalt := lastHalt

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

		newHalt, ok = needHaltUpd(successRate, lastHalt)
		if ok && newHalt != lastHalt {
			atomic.StoreInt64(&ctx.dynamicHalt, newHalt)

			lastHalt = newHalt
			successRate = 0
			log.WithField("time", time.Duration(newHalt)).
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
	w := bufio.NewWriter(resFile)
	var url urlPrint
	var lastLevel int32 = 1
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

func deleteOldValue(url url, depth int32, toPrint *[][]string) {
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

func bufferAppend(ctx *ctx, w *bufio.Writer, toPrint *[][]string, lastLevel int32) int32 {
	for depth := lastLevel; depth <= totalDepth; depth++ {
		readiness := atomic.LoadInt32(&ctx.printQue[depth])

		if readiness == 0 && (*toPrint)[depth] != nil {
			append_(&(*toPrint)[depth], int(depth), w)
		} else if (*toPrint)[depth] != nil {
			append_(&(*toPrint)[depth], int(depth), w)
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

func saveByURL(ctx *ctx, url url, depth int32) (err error) {
	ratio := 500
	for i := 0; i < 2; i++ {
		err = ctx.parsed.Save(ctx.ctx_, url, depth)
		if err == nil {
			return
		}

		time.Sleep(time.Duration(ratio * i) * time.Millisecond)
	}
	return
}

func getByURL(ctx *ctx, url url) (oldDepth int32, urlVisited bool, err error) {
	ratio := 500
	for i := 1; i <= 2; i++ {
		oldDepth, urlVisited, err = ctx.parsed.Get(ctx.ctx_, url)
		if err == nil {
			return
		}

		time.Sleep(time.Duration(ratio * i) * time.Millisecond)
	}
	return
}

func init() {
	fl := flagsInit()

	client = &http.Client{Timeout: time.Millisecond * time.Duration(fl.clientTimeout)}

	tryAllowed = int32(fl.try)
	if fl.try < 0 {
		tryAllowed = 0
	}

	totalDepth = int32(fl.totalDepth)
	chanCapOnDepthHeuristic = getChanCapByDepth()

	minHalt = int64(time.Millisecond * time.Duration(fl.minHalt))
	maxHalt = int64(time.Millisecond * time.Duration(fl.maxHalt))

	deltaSuccess = fl.deltaSuccess
	deltaFail = fl.deltaFail

	omitExt = fl.omitExtensions
	defaultExtNeeded = fl.defaultExtNeeded

	rgx = regexp.MustCompile(`(http(s)?://(([\p{L}0-9]+[-.\p{L}}0-9]+\.[\p{L}]+)|(([0-9]{1,3}\.){3}[0-9]{1,3}))(:[0-9]+)?(/(([\p{L}0-9-._~:/?#[\]@!$&'()*+,;=])+|([%[0-9a-fA-F]{3})+)+)?)|( href="[^"]+)`)
	wrongProto = regexp.MustCompile(`(data:text/)|(android-app://)|(tel:)|(mailto:)|(callto:)|(fax:)|(sms:)|(itms://)|(itms-apps://)`)

	headerInit(fl)

	initDBs(fl)
}

func headerInit(fl flags) {
	if fl.headerFile != "" {
		f, err := os.Open(fl.headerFile)
		if err != nil {
			log.Error("Can't open header file :", err)
			logFile.Close()
			os.Exit(1)
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
			log.Error("Can't read header file :", err)
			logFile.Close()
			os.Exit(1)
		}
		if (i-1)%2 != 0 {
			log.Error("Check header file format. Must have even lines: `HeaderName` newline `HeaderValue`", err)
			logFile.Close()
			os.Exit(1)
		}

		if fl.userAgent != "" {
			header["User-Agent"] = []string{fl.userAgent}
		}
	} else if fl.userAgent != "" {
		header = map[string][]string{}
		header["User-Agent"] = []string{fl.userAgent}
	}
}

func initDBs(fl flags) {
	{
		if fl.resultFile != "" {
			var err error
			resFile, err = os.OpenFile(fl.resultFile,
				os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
			if err != nil {
				log.Error("Can't open or create result file :", err)
				logFile.Close()
				os.Exit(1)
			}
		} else {
			resFile = os.Stdout
		}
	}
	{
		if fl.grpcAddr != "" {
			p, err := grpc_client.New(fl.grpcAddr, "")//fl.grpcCertFile)
			if err != nil {
				log.Error("Can't connect to gRPC:", err)
				logFile.Close()
				resFile.Close()
				os.Exit(1)
			}

			urlStore = p
		} else {
			urlStore = stdmap.New()
		}
	}
}

type flags struct {
	omitExtensions   []string
	totalDepth       int
	try             int
	defaultExtNeeded bool
	clientTimeout int
	userAgent     string
	headerFile    string
	minHalt       int
	maxHalt       int
	deltaSuccess  float32
	deltaFail     float32
	resultFile    string
	logFile       string
	grpcAddr      string
	grpcCertFile  string
}

func flagsInit() flags {
	fl := flags{}
	haltMinMax := ""
	delta := ""
	omitExtensions := ""

	flag.Usage = func() {
		fmt.Fprintf(os.Stdout, "Usage: %s [OPTIONS]\n\nhttp crawler cli\n\nOptions:\n", os.Args[0])

		flag.VisitAll(func(f *flag.Flag) {
			if f.DefValue != "" {
				fmt.Fprintf(os.Stdout, "    -%-20s%s (default %s)\n\n", f.Name, f.Usage, f.DefValue)
			} else {
				fmt.Fprintf(os.Stdout, "    -%-20s%s\n\n", f.Name, f.Usage)
			}
		})
	}

	flag.IntVar(&workersNum, "n", workersDef, "Parallel requests num (workers)")
	flag.StringVar(&rootPage, "root", "", "URL from to start, scheme required")
	flag.IntVar(&fl.totalDepth, "r", 0, "Recursion depth")
	flag.StringVar(&fl.userAgent, "user-agent", "", "HTTP User-Agent header")
	flag.StringVar(&fl.headerFile, "header-file", "", "HTTP headers file in format 'HeaderName' newline 'HeaderValue'")
	flag.StringVar(&omitExtensions, "omit-ext", "", "Omit parsing URLs with extensions. Usage: '.mp5 .mkv'. Generally it checks a suffix of URL, so it can pass all 'index.html', but leave '.html'. Default: '.png, .ico, .svg, .jpg, .ogv, .mp4, .aac, .mp3, .mov, .gif, .css, .pdf'; To off default add '-', like '- .mp5 .mkv' or just '-'")
	flag.IntVar(&fl.clientTimeout, "client-timeout", clientTimeoutDef, "HTTP client-timeout in ms")
	flag.IntVar(&fl.try, "retry", retryDef, "Times to retry refused URL request")
	flag.StringVar(&haltMinMax, "halt-min-max", haltMinMaxDef, "'Min,max' halt time per worker in ms to slow down and get less refused requests. Initial halt time is min. Usage: '200,4000'")
	flag.StringVar(&delta, "delta-ok-fail", deltaOKfailDef, `Abstract deltas[knobs] in float for success/fail request. Usage: ok,fail='1,-2'. Example: 'overallSuccessRate += [ok/fail delta]; if (oSR <= -10) { haltTime *= constFor(-10) }', watch 'needHaltUpd()' and 'haltCtrl()'`)
	flag.StringVar(&fl.resultFile, "out-file", "", "File path for result to write. If file exists -> will append (default stdout)")
	flag.StringVar(&fl.logFile, "log-file", "", "File path for logs to write. If flag is set – will use JSON format. If file exists -> will append (default stdout)")
	flag.StringVar(&fl.grpcAddr, "grpc-addr", "", "Address for gRPC uniq URL store to connect. Must satisfy uniqURL_store/uniqURL_store.proto")
	//flag.StringVar(&fl.grpcCertFile, "grpc-cert-file", "", "SSL/TLS cert file for gRPC client (if needed)")
	flag.Parse()


	if fl.logFile != "" {
		var err error
		logFile, err = os.OpenFile(fl.logFile,
			os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
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
	if fl.totalDepth < 1 {
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
		log.WithField("type", "bad format").Error("\n", errs, "\n")
		logFile.Close()
		os.Exit(1)
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

func getChanCapByDepth() int32 {
	n := int32(workersNum)

	//actually need more,
	//need tests on big depth

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
