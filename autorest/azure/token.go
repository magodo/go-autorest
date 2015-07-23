package azure

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/azure/go-autorest/autorest"
)

const (
	defaultRefresh = 5 * time.Minute
	oauthUrl       = "https://login.microsoftonline.com/{tenantId}/oauth2/{requestType}?api-version=1.0"
	tokenBaseDate  = "1970-01-01T00:00:00Z"
)

var expirationBase time.Time

func init() {
	expirationBase, _ = time.Parse(time.RFC3339, tokenBaseDate)
}

// Token encapsulates the access token used to authorize Azure requests.
type Token struct {
	AccessToken string `json:"access_token"`

	ExpiresIn string `json:"expires_in"`
	ExpiresOn string `json:"expires_on"`
	NotBefore string `json:"not_before"`

	Resource string `json:"resource"`
	Type     string `json:"token_type"`
}

// Expires returns the time.Time when the Token expires.
func (t Token) Expires() time.Time {
	s, err := strconv.Atoi(t.ExpiresOn)
	if err != nil {
		s = -3600
	}
	return expirationBase.Add(time.Duration(s) * time.Second).UTC()
}

// IsExpired returns true if the Token is expired, false otherwise.
func (t Token) IsExpired() bool {
	return t.WillExpireIn(0)
}

// WillExpireIn returns true if the Token will expire after the passed time.Duration interval
// from now, false otherwise.
func (t Token) WillExpireIn(d time.Duration) bool {
	return !t.Expires().After(time.Now().Add(d))
}

// WithAuthorization returns a PrepareDecorator that adds an HTTP Authorization header whose
// value is "Bearer " followed by the AccessToken of the Token.
func (t *Token) WithAuthorization() autorest.PrepareDecorator {
	return func(p autorest.Preparer) autorest.Preparer {
		return autorest.PreparerFunc(func(r *http.Request) (*http.Request, error) {
			return (autorest.WithBearerAuthorization(t.AccessToken)(p)).Prepare(r)
		})
	}
}

// ServicePrincipalToken encapsulates a Token created for a Service Principal.
type ServicePrincipalToken struct {
	Token

	clientId      string
	clientSecret  string
	resource      string
	tenantId      string
	autoRefresh   bool
	refreshWithin time.Duration
	sender        autorest.Sender
}

// NewTokenForServicePrincipal creates a ServicePrincipalToken from the supplied Service Principal
// credentials scoped to the named resource.
func NewServicePrincipalToken(id string, secret string, tenentId string, resource string) (*ServicePrincipalToken, error) {
	spt := &ServicePrincipalToken{
		clientId:      id,
		clientSecret:  secret,
		resource:      resource,
		tenantId:      tenentId,
		autoRefresh:   true,
		refreshWithin: defaultRefresh,
		sender:        &http.Client{}}
	return spt, nil
}

// EnsureFresh will refresh the token if it will expire within the refresh window (as set by
// RefreshWithin).
func (spt *ServicePrincipalToken) EnsureFresh() error {
	if spt.WillExpireIn(spt.refreshWithin) {
		return spt.Refresh()
	}
	return nil
}

// Refresh obtains a fresh token for the Service Principal.
func (spt *ServicePrincipalToken) Refresh() error {
	p := map[string]interface{}{
		"tenantId":    spt.tenantId,
		"requestType": "token",
	}

	v := url.Values{}
	v.Set("client_id", spt.clientId)
	v.Set("client_secret", spt.clientSecret)
	v.Set("grant_type", "client_credentials")
	v.Set("resource", spt.resource)

	req, err := autorest.Prepare(&http.Request{},
		autorest.AsPost(),
		autorest.AsFormUrlEncoded(),
		autorest.WithBaseURL(oauthUrl),
		autorest.WithPathParameters(p),
		autorest.WithFormData(v))
	if err != nil {
		return fmt.Errorf("azure: Failed to create refresh request for Service Principal %s (%v)", spt.clientId, err)
	}

	resp, err := autorest.SendWithSender(spt.sender, req)
	if err != nil {
		return fmt.Errorf("azure: Token request for Service Principal %s failed (%v)", spt.clientId, err)
	}

	err = autorest.Respond(resp,
		autorest.WithErrorUnlessOK(),
		autorest.ByUnmarshallingJSON(spt),
		autorest.ByClosing())
	if err != nil {
		return fmt.Errorf("azure: Token request for Service Principal %s returned an unexpected error (%v)", spt.clientId, err)
	}

	return nil
}

// SetAutoRefresh enables or disables automatic refreshing of stale tokens.
func (spt *ServicePrincipalToken) SetAutoRefresh(autoRefresh bool) {
	spt.autoRefresh = autoRefresh
}

// SetRefreshWithin sets the interval within which if the token will expire, EnsureFresh will
// refresh the token.
func (spt *ServicePrincipalToken) SetRefreshWithin(d time.Duration) {
	spt.refreshWithin = d
	return
}

// SetSender sets the autorest.Sender used when obtaining the Service Principal token. An
// undecorated http.Client is used by default.
func (spt *ServicePrincipalToken) SetSender(s autorest.Sender) {
	spt.sender = s
}

// WithAuthorization returns a PrepareDecorator that adds an HTTP Authorization header whose
// value is "Bearer " followed by the AccessToken of the ServicePrincipalToken.
//
// By default, the token will automatically refresh if nearly expired (as determined by the
// RefreshWithin interval). Use the AutoRefresh method to enable or disable automatically refreshing
// tokens.
func (spt *ServicePrincipalToken) WithAuthorization() autorest.PrepareDecorator {
	return func(p autorest.Preparer) autorest.Preparer {
		return autorest.PreparerFunc(func(r *http.Request) (*http.Request, error) {
			if spt.autoRefresh {
				err := spt.EnsureFresh()
				if err != nil {
					return r, fmt.Errorf("azure: Failed to refresh Service Principal Token for request to %s (%v)", r.URL, err)
				}
			}
			return (autorest.WithBearerAuthorization(spt.AccessToken)(p)).Prepare(r)
		})
	}
}
