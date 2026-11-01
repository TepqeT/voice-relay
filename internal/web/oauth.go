package web

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/disgoorg/snowflake/v2"
)

// Discord permission bits read from the OAuth guild list.
const (
	permAdministrator = 1 << 3
	permManageGuild   = 1 << 5
)

// oauth talks to Discord's OAuth2 endpoints.
type oauth struct {
	clientID     snowflake.ID
	clientSecret string
	redirectURI  string
	authorizeURL string // https://discord.com/oauth2/authorize
	apiBase      string // https://discord.com/api/v10
	http         *http.Client
}

func newOAuth(clientID snowflake.ID, secret, redirectURI string) *oauth {
	return &oauth{
		clientID:     clientID,
		clientSecret: secret,
		redirectURI:  redirectURI,
		authorizeURL: "https://discord.com/oauth2/authorize",
		apiBase:      "https://discord.com/api/v10",
		http:         &http.Client{Timeout: 10 * time.Second},
	}
}

// loginURL is the consent page: identity and server list only.
func (o *oauth) loginURL(state string) string {
	q := url.Values{
		"client_id":     {o.clientID.String()},
		"redirect_uri":  {o.redirectURI},
		"response_type": {"code"},
		"scope":         {"identify guilds"},
		"state":         {state},
		"prompt":        {"none"},
	}
	return o.authorizeURL + "?" + q.Encode()
}

// discordUser is the part of /users/@me the site uses.
type discordUser struct {
	ID         snowflake.ID `json:"id"`
	Username   string       `json:"username"`
	GlobalName string       `json:"global_name"`
}

func (u discordUser) display() string {
	if u.GlobalName != "" {
		return u.GlobalName
	}
	return u.Username
}

// discordGuild is one entry of /users/@me/guilds.
type discordGuild struct {
	ID          snowflake.ID `json:"id"`
	Name        string       `json:"name"`
	Owner       bool         `json:"owner"`
	Permissions string       `json:"permissions"`
}

// manageable reports whether the user may configure the server.
func (g discordGuild) manageable() bool {
	if g.Owner {
		return true
	}
	p, err := strconv.ParseUint(g.Permissions, 10, 64)
	return err == nil && (p&permAdministrator != 0 || p&permManageGuild != 0)
}

// exchange trades the code for an access token, reads the user and their
// servers, and forgets the token.
func (o *oauth) exchange(ctx context.Context, code string) (discordUser, []discordGuild, error) {
	form := url.Values{
		"grant_type":   {"authorization_code"},
		"code":         {code},
		"redirect_uri": {o.redirectURI},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.apiBase+"/oauth2/token", strings.NewReader(form.Encode()))
	if err != nil {
		return discordUser{}, nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(o.clientID.String(), o.clientSecret)
	var tok struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
	}
	if err := o.do(req, &tok); err != nil {
		return discordUser{}, nil, fmt.Errorf("token: %w", err)
	}
	if tok.AccessToken == "" {
		return discordUser{}, nil, fmt.Errorf("token: empty access token")
	}
	var user discordUser
	if err := o.get(ctx, tok.AccessToken, "/users/@me", &user); err != nil {
		return discordUser{}, nil, fmt.Errorf("user: %w", err)
	}
	var guilds []discordGuild
	if err := o.get(ctx, tok.AccessToken, "/users/@me/guilds", &guilds); err != nil {
		return discordUser{}, nil, fmt.Errorf("guilds: %w", err)
	}
	return user, guilds, nil
}

func (o *oauth) get(ctx context.Context, token, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, o.apiBase+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	return o.do(req, out)
}

func (o *oauth) do(req *http.Request, out any) error {
	req.Header.Set("User-Agent", "VoiceRelay (private, 1.0)")
	resp, err := o.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("discord answered %d", resp.StatusCode)
	}
	return json.Unmarshal(body, out)
}
