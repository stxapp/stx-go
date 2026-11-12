package stx

import (
	"net/url"
	"regexp"
	"strings"
)

// Environment is a published STX environment. The list lives at
// https://docs.stxapp.io/environments/.
type Environment struct {
	// Name is a short name for logs and configuration, such as "ontario-demo".
	Name string
	// Region is "us" or "ontario".
	Region string
	// Env is "demo" or "production".
	Env string
	// Host is the API hostname.
	Host string
	// RealMoney is true where orders settle against live balances.
	RealMoney bool
}

// The published environments. Any other host (a local server, a host added
// after this version shipped) is reachable with [WithHost]. API keys do not
// carry across exchanges: an Ontario key does not authenticate in the United
// States.
var (
	// USDemo is the United States demo, shared by every integrator. No real money.
	USDemo = Environment{Name: "us-demo", Region: "us", Env: "demo", Host: "demo.stxapp.io"}
	// OntarioDemo is the Ontario demo, shared. No real money.
	OntarioDemo = Environment{Name: "ontario-demo", Region: "ontario", Env: "demo", Host: "demo.stxapp.ca"}
	// OntarioProduction is Ontario production. Real money: exercise your
	// integration against OntarioDemo first.
	OntarioProduction = Environment{Name: "ontario-production", Region: "ontario", Env: "production", Host: "api.on.stxapp.ca", RealMoney: true}
)

// Environments returns every published environment.
func Environments() []Environment {
	return []Environment{USDemo, OntarioDemo, OntarioProduction}
}

// EnvironmentNamed returns the environment called name, such as "us-demo".
func EnvironmentNamed(name string) (Environment, error) {
	want := strings.ToLower(strings.TrimSpace(name))
	var names []string
	for _, e := range Environments() {
		if e.Name == want {
			return e, nil
		}
		names = append(names, e.Name)
	}
	return Environment{}, configError("unknown environment %q; known: %s; any other host is reachable with WithHost", name, strings.Join(names, ", "))
}

var envAliases = map[string]string{"prod": "production", "live": "production"}

// resolveHost maps a region and env to a hostname.
func resolveHost(region, env string) (string, error) {
	r := strings.ToLower(region)
	e := strings.ToLower(env)
	if alias, ok := envAliases[e]; ok {
		e = alias
	}
	if r == "" || e == "" {
		return "", configError("no host configured; use WithEnvironment, WithRegion, WithHost, or set them in a profile in ~/.stx/credentials")
	}
	if r != "us" && r != "ontario" {
		return "", configError("unknown region %q; known: ontario, us", region)
	}
	if e != "demo" && e != "production" {
		return "", configError("unknown env %q; known: demo, production; any other host is reachable with WithHost", env)
	}
	for _, x := range Environments() {
		if x.Region == r && x.Env == e {
			return x.Host, nil
		}
	}
	return "", configError("no host configured for region=%s, env=%s; use WithHost", r, e)
}

var schemeRe = regexp.MustCompile(`(?i)^https?://`)

// baseURLFor returns the API base URL (scheme and authority, no path). host
// may be a bare hostname, which gets https://, or a URL whose scheme is kept.
func baseURLFor(region, env, host string) (string, error) {
	host = strings.TrimSpace(host)
	if host == "" {
		h, err := resolveHost(region, env)
		if err != nil {
			return "", err
		}
		return "https://" + h, nil
	}
	if schemeRe.MatchString(host) {
		u, err := url.Parse(host)
		if err != nil || u.Host == "" {
			return "", configError("cannot parse host %q", host)
		}
		return strings.ToLower(u.Scheme) + "://" + u.Host, nil
	}
	return "https://" + strings.TrimRight(host, "/"), nil
}

// socketURLFor returns the WebSocket URL for a base URL: https maps to wss,
// http to ws.
func socketURLFor(baseURL string) string {
	u, _ := url.Parse(baseURL)
	scheme := "wss"
	if u.Scheme == "http" {
		scheme = "ws"
	}
	return scheme + "://" + u.Host + socketPath + "?vsn=2.0.0"
}
