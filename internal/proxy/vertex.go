package proxy

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// adcCredentials represents the Application Default Credentials file format
// used by Google Cloud SDK (gcloud auth application-default login).
type adcCredentials struct {
	Type         string `json:"type"`
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	RefreshToken string `json:"refresh_token"`
}

// cachedToken holds a Google OAuth2 access token with its expiry.
type cachedToken struct {
	accessToken string
	expiresAt   time.Time
}

// VertexTokenSource returns a TokenFunc that exchanges Google ADC
// credentials for access tokens. Tokens are cached and refreshed
// automatically when within 60 seconds of expiry.
//
// adcPath is the path to the ADC JSON file. If empty, it defaults
// to ~/.config/gcloud/application_default_credentials.json.
func VertexTokenSource(adcPath string) (func() string, error) {
	if adcPath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("proxy: user home dir: %w", err)
		}
		adcPath = filepath.Join(home, ".config", "gcloud", "application_default_credentials.json")
	}

	data, err := os.ReadFile(adcPath)
	if err != nil {
		return nil, fmt.Errorf("proxy: read ADC %s: %w", adcPath, err)
	}

	var creds adcCredentials
	if err := json.Unmarshal(data, &creds); err != nil {
		return nil, fmt.Errorf("proxy: parse ADC %s: %w", adcPath, err)
	}

	if creds.RefreshToken == "" || creds.ClientID == "" || creds.ClientSecret == "" {
		return nil, fmt.Errorf("proxy: ADC missing required fields (refresh_token, client_id, client_secret)")
	}

	var mu sync.Mutex
	var cached cachedToken

	return func() string {
		mu.Lock()
		defer mu.Unlock()

		if cached.accessToken != "" && time.Now().Before(cached.expiresAt.Add(-60*time.Second)) {
			return cached.accessToken
		}

		token, expiresIn, err := exchangeRefreshToken(creds)
		if err != nil {
			return cached.accessToken // return stale token on error
		}

		cached.accessToken = token
		cached.expiresAt = time.Now().Add(time.Duration(expiresIn) * time.Second)
		return token
	}, nil
}

// exchangeRefreshToken performs the OAuth2 token exchange against Google's
// token endpoint. Returns the access token and its TTL in seconds.
func exchangeRefreshToken(creds adcCredentials) (token string, expiresIn int, err error) {
	resp, err := http.PostForm("https://oauth2.googleapis.com/token", url.Values{
		"client_id":     {creds.ClientID},
		"client_secret": {creds.ClientSecret},
		"refresh_token": {creds.RefreshToken},
		"grant_type":    {"refresh_token"},
	})
	if err != nil {
		return "", 0, fmt.Errorf("proxy: token exchange: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return "", 0, fmt.Errorf("proxy: token exchange returned %d", resp.StatusCode)
	}

	var result struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", 0, fmt.Errorf("proxy: parse token response: %w", err)
	}

	if result.AccessToken == "" {
		return "", 0, fmt.Errorf("proxy: empty access token in response")
	}

	return result.AccessToken, result.ExpiresIn, nil
}
