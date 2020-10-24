# *http crawler*

Recursively URL parser

* Finds by regexp http[s] and href keywords

* Prints on readiness with order and depth

* Has dynamically changing halt time per worker to get less refused requests 

* Retries refused URLs `try` times

* Omit parsing URLs with media/custom extensions, or page name, like `index.html`
  
*If the URL was on the last depth, which is no sense to request, it would appear again on some previous depth due to async, what allows us to parse and move it into a lower depth*

## *--help*

|  |  |
|-|-|
| n | Parallel requests num (workers); Default: `runtime.NumCPU() * 6` |
| root | URL from to start, scheme required |
| r | Recursion depth |
| user-agent | HTTP `User-Agent` header |
| header-file | HTTP headers in format `HeaderName` newline `HeaderValue` |
| client-timeout | HTTP client timeout in ms |
| try | Times to retry refused URL request (default 0) |
| halt-min-max | Min,max halt time per worker in ms to slow down and get less refused requests. Initial halt time is min. Usage: `200,4000`. Default: `200,500` |
| delta-ok-fail | Abstract deltas[knobs] in float for success/fail request. Usage: `1,-2`. Example: `overallSuccessRate += [ok/fail delta]; if (oSR <= -10) { haltTime *= constFor(-10) }`, watch `needHaltUpd()` and `haltCtrl()` |
| omit-ext | Omit parsing URLs with extensions. Generally it checks a suffix of URL, so it can pass all `index.html`, but leave `.html`. Default is: `.png, .ico, .svg, .jpg, .ogv, .mp4, .aac, .mp3, .mov, .gif, .css, .pdf`; Usage: `.mp5 .mkv`; To off default add `-` as 0 argument, like `- .mp5 .mkv` or just `-` |
| out-file | File path for result to write. If file exists -> will append (default stdout) |
| log-file | File path for logs to write. If file exists -> will append (default stdout) |


Example: `./crawler -r 10 -root https://en.wikipedia.org/wiki/Money_Heist -n 15`


*ps don't waste the channel*