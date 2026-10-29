package probe

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"
)

// proxyAnswering is a forward proxy answering every request itself with handle.
func proxyAnswering(t *testing.T, handle http.HandlerFunc) int {
	t.Helper()
	srv := httptest.NewServer(handle)
	t.Cleanup(srv.Close)
	return srv.Listener.Addr().(*net.TCPAddr).Port
}

func service(t *testing.T, name string) Service {
	t.Helper()
	i := slices.IndexFunc(Services, func(s Service) bool { return s.Name == name })
	if i < 0 {
		t.Fatalf("no service %s", name)
	}
	s := Services[i]
	s.URL = "http://target.example/" // the fake proxy answers in its place
	return s
}

// A service answering at all is reached; the AI services' own refusals of the exit IP are told apart
// from their authentication errors, and failures on a service's side are errors.
func TestCheckService(t *testing.T) {
	for _, c := range []struct {
		service string
		status  int
		body    string
		refused string
		err     bool
	}{
		{"Google", 204, "", "", false},
		{"GitHub", 403, "", "", false}, // a challenge page still means the site is reached
		{"ChatGPT", 401, `{"error":{"message":"Missing bearer authentication in header"}}`, "", false},
		{"ChatGPT", 403, `{"error":{"code":"unsupported_country_region_territory"}}`, "地区不支持", false},
		{"Claude", 401, `{"type":"error","error":{"type":"authentication_error"}}`, "", false},
		{"Claude", 403, `{"type":"error","error":{"type":"permission_error"}}`, "", false},
		{"Claude", 403, `{"type":"error","error":{"type":"forbidden","message":"Request not allowed"}}`, "地区不支持", false},
		{"Gemini", 502, "", "", true},
	} {
		port := proxyAnswering(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(c.status)
			w.Write([]byte(c.body))
		})
		r, err := CheckService(context.Background(), port, service(t, c.service), 5*time.Second)
		if r.Refused != c.refused || (err != nil) != c.err {
			t.Errorf("%s %d: %+v %v", c.service, c.status, r, err)
		}
	}
	if _, err := CheckService(context.Background(), freeClosedPort(t), service(t, "Google"), time.Second); err == nil {
		t.Error("no proxy should fail")
	}
}

func TestExitInfo(t *testing.T) {
	old := IPInfoURL
	t.Cleanup(func() { IPInfoURL = old })
	IPInfoURL = "http://ippure.example/v1/info"
	body := `{"ip":"203.0.113.24","asn":64500,"asOrganization":"Example Networks","country":"Japan","countryCode":"JP","region":"Osaka",
		"city":"Osaka","fraudScore":63,"isResidential":false,"isBroadcast":true}`
	port := proxyAnswering(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(body)) })
	info, err := ExitInfo(context.Background(), port, 5*time.Second)
	if err != nil || info.IP != "203.0.113.24" || *info.FraudScore != 63 || !info.Broadcast || info.Residential || info.ASN != 64500 {
		t.Fatalf("%+v %v", info, err)
	}
	// What the API in beta stops answering is an error, not an empty card.
	body = `{"ip":"203.0.113.24"}`
	if _, err := ExitInfo(context.Background(), port, 5*time.Second); err == nil || !strings.Contains(err.Error(), "风险值") {
		t.Fatalf("no score: %v", err)
	}
}
