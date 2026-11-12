package oauth

import (
	"os"
	"strings"
)

// ConfigFromEnv returns a Config from the environment: STX_HOST (or
// STX_BASE_URL), STX_OAUTH_CLIENT_ID, STX_OAUTH_CLIENT_SECRET,
// STX_OAUTH_REDIRECT_URI and STX_OAUTH_SCOPE (space-delimited).
func ConfigFromEnv() Config {
	return Config{
		BaseURL:      os.Getenv("STX_HOST"),
		ClientID:     os.Getenv("STX_OAUTH_CLIENT_ID"),
		ClientSecret: os.Getenv("STX_OAUTH_CLIENT_SECRET"),
		RedirectURL:  os.Getenv("STX_OAUTH_REDIRECT_URI"),
		Scopes:       strings.Fields(os.Getenv("STX_OAUTH_SCOPE")),
	}
}
