package kiroauth

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

// Kiro's IDE and CLI keep their AWS sign-in in the AWS SSO cache: the token
// file names the client registration by the hash of its client id.
const kiroTokenFile = "kiro-auth-token.json"

const maxCacheFile = 64 << 10

// ErrSocialLogin means Kiro is signed in with GitHub or Google. Those logins
// refresh through Kiro's own auth service rather than AWS SSO-OIDC, so they
// cannot be imported.
var ErrSocialLogin = errors.New("Kiro is signed in with a social login; only AWS Builder ID and IAM Identity Center logins can be imported")

// CachePath returns the Kiro token file under home.
func CachePath(home string) string {
	return filepath.Join(home, ".aws", "sso", "cache", kiroTokenFile)
}

var clientHashRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// ImportKiroCache reads the Kiro sign-in under home and returns it as a Login.
// It returns os.ErrNotExist (wrapped) when Kiro has never signed in.
func ImportKiroCache(home string) (Login, error) {
	path := CachePath(home)
	data, err := readCapped(path)
	if err != nil {
		return Login{}, err
	}
	var tok struct {
		RefreshToken string `json:"refreshToken"`
		AuthMethod   string `json:"authMethod"`
		Provider     string `json:"provider"`
		Region       string `json:"region"`
		ClientIDHash string `json:"clientIdHash"`
		StartURL     string `json:"startUrl"`
		ProfileARN   string `json:"profileArn"`
	}
	if err := json.Unmarshal(data, &tok); err != nil {
		return Login{}, fmt.Errorf("unparseable %s", kiroTokenFile)
	}
	if tok.AuthMethod == "social" || tok.Provider == "Github" || tok.Provider == "Google" {
		return Login{}, ErrSocialLogin
	}
	if tok.RefreshToken == "" || tok.ClientIDHash == "" {
		return Login{}, fmt.Errorf("%s has no AWS sign-in to import", kiroTokenFile)
	}
	if !clientHashRE.MatchString(tok.ClientIDHash) {
		return Login{}, fmt.Errorf("%s names an invalid client registration", kiroTokenFile)
	}
	regData, err := readCapped(filepath.Join(filepath.Dir(path), tok.ClientIDHash+".json"))
	if err != nil {
		return Login{}, fmt.Errorf("Kiro's client registration is missing: %w", err)
	}
	var reg struct {
		ClientID     string `json:"clientId"`
		ClientSecret string `json:"clientSecret"`
		ExpiresAt    string `json:"expiresAt"`
	}
	if err := json.Unmarshal(regData, &reg); err != nil || reg.ClientID == "" || reg.ClientSecret == "" {
		return Login{}, errors.New("Kiro's client registration is unparseable")
	}
	l := Login{
		RefreshToken: tok.RefreshToken,
		ClientID:     reg.ClientID,
		ClientSecret: reg.ClientSecret,
		Region:       tok.Region,
		StartURL:     tok.StartURL,
		ProfileARN:   tok.ProfileARN,
	}
	if l.Region == "" {
		l.Region = DefaultRegion
	}
	if !ValidRegion(l.Region) {
		return Login{}, fmt.Errorf("%s has an invalid region", kiroTokenFile)
	}
	if t, err := time.Parse(time.RFC3339, reg.ExpiresAt); err == nil {
		l.ClientSecretExpiresAt = t.Unix()
	}
	if l.StartURL != "" && !ValidStartURL(l.StartURL) {
		l.StartURL = ""
	}
	return l, nil
}

func readCapped(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", filepath.Base(path))
	}
	data, err := io.ReadAll(io.LimitReader(f, maxCacheFile+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxCacheFile {
		return nil, fmt.Errorf("%s is too large", filepath.Base(path))
	}
	return data, nil
}
