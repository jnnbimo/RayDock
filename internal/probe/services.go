package probe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"time"
)

// Service is a site whose reachability through the proxy CheckService tells.
type Service struct {
	Name   string
	AI     bool
	Method string
	URL    string // small to fetch: an empty response, a page's headers, an API's error
	// refused tells, from a response, that the service turns the exit IP away: why, or "" when it serves it.
	refused func(status int, body []byte) string
}

// Services are the sites the home page checks. The AI services' APIs are asked without a key: they
// answer with an authentication error where they serve the exit IP, and with an error of their own
// where they do not (see their refused).
var Services = []Service{
	{Name: "Google", Method: http.MethodGet, URL: "https://www.google.com/generate_204"},
	{Name: "GitHub", Method: http.MethodHead, URL: "https://github.com/"},
	// These answer HEAD with 400 or 405: a small file is fetched instead.
	{Name: "Facebook", Method: http.MethodGet, URL: "https://www.facebook.com/favicon.ico"},
	{Name: "Netflix", Method: http.MethodGet, URL: "https://www.netflix.com/robots.txt"},
	{Name: "Disney+", Method: http.MethodGet, URL: "https://www.disneyplus.com/robots.txt"},
	{Name: "ChatGPT", AI: true, Method: http.MethodGet, URL: "https://api.openai.com/v1/models", refused: func(status int, body []byte) string {
		// {"error":{"code":"unsupported_country_region_territory",...}}
		if status == http.StatusForbidden && bytes.Contains(body, []byte("unsupported_country")) {
			return "地区不支持"
		}
		return ""
	}},
	{Name: "Claude", AI: true, Method: http.MethodGet, URL: "https://api.anthropic.com/v1/models", refused: func(status int, body []byte) string {
		// {"type":"error","error":{"type":"forbidden","message":"Request not allowed"}}; a key problem
		// is a permission_error instead.
		if status == http.StatusForbidden && bytes.Contains(body, []byte(`"forbidden"`)) {
			return "地区不支持"
		}
		return ""
	}},
	{Name: "Gemini", AI: true, Method: http.MethodHead, URL: "https://gemini.google.com/"},
	{Name: "Grok", AI: true, Method: http.MethodHead, URL: "https://grok.com/"},
}

// ServiceResult is what CheckService found: a refusal, else the round trip to the service.
type ServiceResult struct {
	Latency time.Duration
	Refused string
}

// CheckService requests s through the local HTTP proxy at port, as Through does. A service answering at
// all is reached, unless it refuses the exit IP or fails on its side (5xx).
func CheckService(ctx context.Context, port int, s Service, timeout time.Duration) (ServiceResult, error) {
	var r ServiceResult
	d, err := twice(ctx, port, s.Method, s.URL, timeout, func(status int, body []byte) error {
		if status >= 500 {
			return fmt.Errorf("HTTP %d", status)
		}
		if s.refused != nil {
			r.Refused = s.refused(status, body)
		}
		if r.Refused != "" {
			return errRefused
		}
		return nil
	})
	if errors.Is(err, errRefused) {
		return r, nil
	}
	r.Latency = d
	return r, err
}

var errRefused = errors.New("refused")

// IPInfoURL describes the IP its requests come from (IPPure, no key needed).
var IPInfoURL = "https://my.ippure.com/v1/info"

// IPInfo describes an exit IP, in IPPure's words.
type IPInfo struct {
	IP          string `json:"ip"`
	ASN         int    `json:"asn"`
	ASOrg       string `json:"asOrganization"`
	Country     string `json:"country"`
	CountryCode string `json:"countryCode"`
	Region      string `json:"region"`
	City        string `json:"city"`
	// FraudScore is IPPure's risk score of the IP, from 0 to 100: the higher, the more sites distrust it.
	FraudScore  *int `json:"fraudScore"`
	Residential bool `json:"isResidential"` // a home ISP's, not a data center's
	// Broadcast is an IP announced away from where it is registered; false is a native IP.
	Broadcast bool `json:"isBroadcast"`
}

// ExitInfo asks IPPure through the local HTTP proxy at port about the exit IP.
func ExitInfo(ctx context.Context, port int, timeout time.Duration) (IPInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, IPInfoURL, nil)
	if err != nil {
		return IPInfo{}, err
	}
	resp, err := ProxyClient(port).Do(req)
	if err != nil {
		return IPInfo{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return IPInfo{}, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var info IPInfo
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64*1024)).Decode(&info); err != nil {
		return IPInfo{}, err
	}
	// The API is in beta: what it no longer answers is reported, not shown empty.
	if _, err := netip.ParseAddr(info.IP); err != nil || info.FraudScore == nil {
		return IPInfo{}, errors.New("IPPure 的返回缺少 IP 或风险值")
	}
	return info, nil
}
