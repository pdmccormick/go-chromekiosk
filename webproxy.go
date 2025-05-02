package chromekiosk

import (
	"net/http"
	"slices"
	"strings"
)

var InternalChromeRequests = []string{
	"CONNECT //accounts.google.com:443",
	"CONNECT //optimizationguide-pa.googleapis.com:443",
	"CONNECT //safebrowsingohttpgateway.googleapis.com:443",
	"CONNECT //update.googleapis.com:443",
	"CONNECT //www.google.com:443",
	"GET http://clients2.google.com/time/1/current?",
	"POST http://update.googleapis.com/service/update2/json?",
}

func IsInternalChromeRequest(r *http.Request) bool {
	return isInternalChromeRequestStr(r.Method + " " + r.URL.String())
}

func isInternalChromeRequestStr(urlStr string) bool {
	urls := InternalChromeRequests
	n, _ := slices.BinarySearch(urls, urlStr)

	if n > 1 {
		if strings.HasPrefix(urlStr, urls[n-1]) {
			return true
		}
	}

	if n == len(urls) {
		n--
	}

	if urlStr == urls[n] {
		return true
	}

	return false
}
