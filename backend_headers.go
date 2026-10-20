package main

import (
	"net/http"
	"strings"
)

// filterBackendHeaders snapshots application headers, excluding hop-by-hop and
// representation headers that FlowBridge's generated requests must own.
func filterBackendHeaders(headers http.Header) http.Header {
	blocked := map[string]bool{
		"connection": true, "proxy-connection": true, "keep-alive": true,
		"proxy-authenticate": true, "proxy-authorization": true,
		"te": true, "trailer": true, "transfer-encoding": true, "upgrade": true,
		"host": true, "content-length": true, "content-type": true,
		"content-encoding": true, "content-md5": true, "content-range": true, "digest": true,
		"accept": true, "accept-encoding": true, "user-agent": true, "expect": true,
	}
	for name, values := range headers {
		if strings.EqualFold(name, "Connection") {
			for _, value := range values {
				for _, token := range strings.Split(value, ",") {
					blocked[strings.ToLower(strings.TrimSpace(token))] = true
				}
			}
		}
	}
	var forwarded http.Header
	for name, values := range headers {
		if blocked[strings.ToLower(name)] {
			continue
		}
		if forwarded == nil {
			forwarded = make(http.Header)
		}
		for _, value := range values {
			forwarded.Add(name, value)
		}
	}
	return forwarded
}

func applyBackendHeaders(req *http.Request, apiKey string, headers http.Header) {
	for name, values := range filterBackendHeaders(headers) {
		req.Header[name] = values
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "curl/8.7.1")
	applyAPIKey(req, apiKey)
}
