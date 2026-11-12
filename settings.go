package stx

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Configuration is layered, highest precedence first:
//
//  1. Options passed to New.
//  2. Environment variables: STX_HOST (or STX_BASE_URL), STX_REGION, STX_ENV,
//     STX_KEY_ID, STX_PRIVATE_KEY, STX_VERIFY_TLS, STX_PROFILE.
//  3. A profile in ~/.stx/credentials (an INI file; STX_CREDENTIALS points
//     elsewhere). The profile comes from WithProfile, then STX_PROFILE, then
//     "default". Profile keys: region, env, host, key_id, private_key (PEM
//     text or a path), verify_tls. key_file is accepted for private_key and
//     base_url for host.
//  4. Defaults (verify TLS, profile "default").

// DefaultProfile is the profile read when none is named.
const DefaultProfile = "default"

type settings struct {
	host, region, env string
	keyID, privateKey string
	verifyTLS         bool
	profile           string
	profileUsed       bool
}

// settingsInput holds what the options set. A nil pointer falls through to
// the environment and the profile; a pointer to "" means "none".
type settingsInput struct {
	host, region, env *string
	keyID, privateKey *string
	verifyTLS         *bool
	profile           *string
	credentialsFile   string
	lookupEnv         func(string) (string, bool)
}

func parseBool(value, key string) (*bool, error) {
	v := strings.ToLower(strings.TrimSpace(value))
	t, f := true, false
	switch v {
	case "":
		return nil, nil
	case "1", "true", "yes", "on":
		return &t, nil
	case "0", "false", "no", "off":
		return &f, nil
	}
	return nil, configError("invalid boolean for %s: %q; use one of 0, 1, false, no, off, on, true, yes", key, value)
}

// parseINI reads the subset of INI the credentials file uses: [section]
// headers (case-sensitive), key = value or key: value split at the first = or
// :, keys lowercased, keys and values trimmed, whole-line # and ; comments.
func parseINI(data []byte) (map[string]map[string]string, []string, error) {
	sections := map[string]map[string]string{}
	var order []string
	var current map[string]string
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	n := 0
	for sc.Scan() {
		n++
		line := strings.TrimSpace(sc.Text())
		if line == "" || line[0] == '#' || line[0] == ';' {
			continue
		}
		if line[0] == '[' && line[len(line)-1] == ']' && len(line) > 2 {
			name := strings.TrimSpace(line[1 : len(line)-1])
			if _, dup := sections[name]; dup {
				return nil, nil, fmt.Errorf("section %q already exists (line %d)", name, n)
			}
			current = map[string]string{}
			sections[name] = current
			order = append(order, name)
			continue
		}
		sep := strings.IndexAny(line, "=:")
		if sep <= 0 || current == nil {
			// The line is not quoted: it may be part of a pasted private key.
			return nil, nil, fmt.Errorf("cannot parse line %d: expected [section] or key = value (a PEM key spans lines, so set private_key to the path of the PEM file)", n)
		}
		current[strings.ToLower(strings.TrimSpace(line[:sep]))] = strings.TrimSpace(line[sep+1:])
	}
	return sections, order, sc.Err()
}

func readProfile(name, path string) (map[string]string, bool, error) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return map[string]string{}, false, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false, configError("reading credentials file %s: %v", path, err)
	}
	sections, order, err := parseINI(data)
	if err != nil {
		return nil, false, configError("failed to parse credentials file %s: %v", path, err)
	}
	section, ok := sections[name]
	if !ok {
		if name == DefaultProfile {
			return map[string]string{}, true, nil
		}
		sort.Strings(order)
		return nil, false, configError("profile %q not found in %s; available profiles: %s", name, path, strings.Join(order, ", "))
	}
	return section, true, nil
}

func resolveSettings(in settingsInput) (settings, error) {
	lookup := in.lookupEnv
	if lookup == nil {
		lookup = os.LookupEnv
	}
	getenv := func(k string) string { v, _ := lookup(k); return v }

	profile := DefaultProfile
	switch {
	case in.profile != nil:
		if *in.profile != "" {
			profile = *in.profile
		}
	case getenv("STX_PROFILE") != "":
		profile = getenv("STX_PROFILE")
	}
	file := in.credentialsFile
	if file == "" {
		if override := getenv("STX_CREDENTIALS"); override != "" {
			file = expandHome(override)
		} else {
			file = filepath.Join(expandHome("~"), ".stx", "credentials")
		}
	}
	values, existed, err := readProfile(profile, file)
	if err != nil {
		return settings{}, err
	}
	_, envProfile := lookup("STX_PROFILE")
	s := settings{
		profile:     profile,
		profileUsed: in.profile != nil || envProfile || (existed && len(values) > 0),
	}
	pick := func(opt *string, envKeys []string, profileKeys ...string) string {
		if opt != nil {
			return *opt
		}
		for _, k := range envKeys {
			if v := getenv(k); v != "" {
				return v
			}
		}
		for _, k := range profileKeys {
			if v, ok := values[k]; ok {
				return v
			}
		}
		return ""
	}
	s.host = pick(in.host, []string{"STX_HOST", "STX_BASE_URL"}, "host", "base_url")
	s.region = pick(in.region, []string{"STX_REGION"}, "region")
	s.env = pick(in.env, []string{"STX_ENV"}, "env")
	s.keyID = pick(in.keyID, []string{"STX_KEY_ID"}, "key_id")
	s.privateKey = pick(in.privateKey, []string{"STX_PRIVATE_KEY"}, "private_key", "key_file")

	s.verifyTLS = true
	switch {
	case in.verifyTLS != nil:
		s.verifyTLS = *in.verifyTLS
	default:
		fromEnv, err := parseBool(getenv("STX_VERIFY_TLS"), "STX_VERIFY_TLS")
		if err != nil {
			return settings{}, err
		}
		if fromEnv != nil {
			s.verifyTLS = *fromEnv
		} else if v, ok := values["verify_tls"]; ok {
			fromProfile, err := parseBool(v, "verify_tls")
			if err != nil {
				return settings{}, err
			}
			if fromProfile != nil {
				s.verifyTLS = *fromProfile
			}
		}
	}
	return s, nil
}
