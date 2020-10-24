# *http crawler*

Recursively URL parser

* Finds by regexp http[s] and href keywords

* Prints on readiness with order and depth

* Has dynamically changing halt time for workers to get less refused requests 

* Retries refused URLs by wish

* Omit parsing URLs with media/custom extensions, or page name, like `index.html`

* Features "gRPC storage" for unique URLs (NB: the current version is not ACID, so storage is using for one session and can't service for replica-workers)

## *--help*

|  |  |
|-|-|
| n | Parallel requests num (workers); (default 48) |
| root | URL from to start, scheme required |
| r | Recursion depth |
| user-agent | HTTP `User-Agent` header |
| header-file | HTTP headers in format `HeaderName` newline `HeaderValue` |
| client-timeout | Request timeout per worker in ms (default 5s) |
| retry | Times to retry refused URL request (default 1) |
| halt-min-max | Min,max halt time for workers in ms to slow down and get less refused requests. Initial halt time is min. Usage: `200,4000`. (default `0,500`) |
| delta-ok-fail | Abstract deltas[knobs] in float for success/fail request. Usage: `1,-2`. Example: `overallSuccessRate += [ok/fail delta]; if (oSR <= -10) { haltTime *= constFor(-10) }`, watch `needHaltUpd()` and `haltCtrl()` (default `1,-10`) |
| omit-ext | Omit parsing URLs with extensions. Generally it checks a suffix of URL, so crawler can pass all `index.html`, but leave `.html`. Default: `.png, .ico, .svg, .jpg, .ogv, .mp4, .aac, .mp3, .mov, .gif, .css, .pdf`; Usage: `.mp5 .mkv`; To off default add `-` as 0 argument, like `- .mp5 .mkv` or just `-` |
| out-file | File path for result to write. If file exists -> will append (default stdout) |
| log-file | File path for logs to write. If file exists -> will append (default stdout) |
| grpc-addr | Address for uniq URL store to connect. Must satisfy uniqURL_store/uniqURL_store.proto |

***Example***
```
./crawler -root="https://en.wikipedia.org/wiki/Money_Heist" \ 
    -user-agent="Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_6) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/86.0.4240.75 Safari/537.36" \
    -out-file="resultFile.txt" -log-file="logFile.txt" \
    -client-timeout=5000 -halt-min-max="0,500" -delta-ok-fail="1,-10" \
    -r=3 -n=48 -retry=2
```

[***Docker image (linux/amd64)***](https://hub.docker.com/r/sav4ik/http-crawler-cli)
```
docker run -v $(pwd)/output:/output sav4ik/http-crawler-cli:basic \
    -root="https://en.wikipedia.org/wiki/Money_Heist" \
    -user-agent="Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_6) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/86.0.4240.75 Safari/537.36" \
    -log-file="/output/logFile.txt" -out-file="/output/outFile.txt" \
    -client-timeout=5000 -halt-min-max="0,500" -delta-ok-fail="1,-10" \
    -r=3
```
## A*C*ID and *Schrödinger's URL*

We can't mark (save in parsed URL db) the last depth (LD) of URLs as parsed or visited *(cause they don't, only found)*, then the previous depth (LD-1) is neither visited, so 

**––>** if we consider prev depth (LD-1) as visited, then URLs on the last depth (LD) must be also visited, otherwise

**––>** in some next session while checking is URL visited, and this URL is from prev depth (LD-1), and we say yes –– get inconsistency, cause the next layer (LD) is not visited.

**<––>** From now on we can observe the heuristic: in some next session, when we meet URL (LD-1), add early(!) found URLs *referred to this URL (LD-1)* (EFU) to the next depth to satisfy consistency (C), but

**<––>** actually (C) with certain reservations. 1) (EFU) could be unavailable at this moment, as well as they could be at that moment, what we can't define now. 2) We can't prove that in some next sessions we will meet again all the URLs from (LD-1) (or LD) to make all (LD) be visited, so we can't assert that (LD-1) is visited or parsed.  

**<––** Accordingly we can't assert that (1-st depth) is visited, which physically is, until we'll

**––** close the circuit (CtC) and get inconsistency, or

**––** permit a crawler to parse until it (CtC) by "happy case" or by parser settings (only root page host as ex.), or

**––** presumably parse the whole WEB: what at the end will be in the inconsistent state by itself. ***Meow***
___
*Secondly,* when one of the depth's URL can't be reached not because of a wrong host, we lose a consistent state
___
*Thirdly,* when we have 'parsed URL', it could be parsed for 2 levels deep as ex., but current session allows us to fall further, which changes the whole course of subsequent work due to its recurse nature 
___
***We reach a consistent state when URL closes the circle by itself. Otherwise, giving limited depth we cannot know about further unparsed depths.*** 
___
#### *realisation arch*

We have to arrange that as soon as we use external storage for unique parsed URLs, as we desire ACID and to really get result of the whole idea: 1) consider the constant state of existing URLs or set expire date; 2) allow in case of non-available URL to satisfy consistency; 3) **store** every URL (LD-1) with referring [arr] (LD) –– as pseudo-parsed state, where (LD) is not visited.

* **It inducts to use directed graphs**, where

    any URL will save its level of parsed == state, so in future getting the existing URL we could fall into (LD) –– not parsed state, if existing and needed, to parse further
    
* To reach consistency with refused requests – retry it by user-allowed times. If it's in a refused state still after, leave parent as partly-parsed, so every next check to parent URL or its child will call a request
  
* Async: workers (W) could parse the same URL, and DB will pass the second+ save, returning state of 'intending to parse N-depth further, I'm Q-th in line'   
 
* DB must update parents depth in case of independently parsing its children


## *todo:*
- [ ] TESTS PLEASE
- [ ] ACID w/ workers
– [ ] gRPC SSL/TLS cert
- [ ] custom parser module: read the regexp from file, which will be used to parse the payload
- [ ] think about halt time in case of gRPC (connDelay+haltTime+readFrom1Chan): *leave it for user tune?*
- [ ] .yaml config
- [ ] *$$$ ?*

#
*ps don't waste the channel*
