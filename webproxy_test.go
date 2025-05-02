package chromekiosk

import (
	"testing"
)

func TestInternalChromeRequests(t *testing.T) {
	var testcases = []struct {
		expect bool
		urlStr string
	}{
		{true, "CONNECT //accounts.google.com:443"},
		{true, "CONNECT //optimizationguide-pa.googleapis.com:443"},
		{true, "CONNECT //safebrowsingohttpgateway.googleapis.com:443"},
		{true, "CONNECT //update.googleapis.com:443"},
		{true, "CONNECT //www.google.com:443"},

		{true, "GET http://clients2.google.com/time/1/current?"},
		{true, "GET http://clients2.google.com/time/1/current?foo&bar&quux"},

		{true, "POST http://update.googleapis.com/service/update2/json?x"},
		{true, "POST http://update.googleapis.com/service/update2/json?foo&bar&quux"},

		{false, "GET http://google.com/"},
		{false, "GET https://google.com/"},
		{false, "GET https://example.com/"},
	}

	for _, tc := range testcases {
		got := isInternalChromeRequestStr(tc.urlStr)
		if tc.expect != got {
			t.Errorf("mismatch %s", tc.urlStr)
		}
	}
}
