/*
Copyright 2021 Gravitational, Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package webauthn

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"time"

	"github.com/duo-labs/webauthn/protocol"
	"github.com/gravitational/teleport/api/types"
	"github.com/gravitational/trace"

	wan "github.com/duo-labs/webauthn/webauthn"
	wantypes "github.com/gravitational/teleport/api/types/webauthn"
	log "github.com/sirupsen/logrus"
)

// loginSessionID is used as the per-user session identifier.
// A fixed identifier means, in essence, that only one concurrent login is
// allowed.
const loginSessionID = "login"

// LoginIdentity represents the subset of Identity methods used by LoginFlow.
// It exists to better scope LoginFlow's use of Identity and to facilitate
// testing.
type LoginIdentity interface {
	userIDStorage

	GetMFADevices(ctx context.Context, user string, withSecrets bool) ([]*types.MFADevice, error)
	UpsertMFADevice(ctx context.Context, user string, d *types.MFADevice) error
	UpsertWebauthnSessionData(ctx context.Context, user, sessionID string, sd *wantypes.SessionData) error
	GetWebauthnSessionData(ctx context.Context, user, sessionID string) (*wantypes.SessionData, error)
	DeleteWebauthnSessionData(ctx context.Context, user, sessionID string) error
}

// WithDevices returns a LoginIdentity backed by a fixed set of devices.
// The supplied devices are returned in all GetMFADevices calls.
func WithDevices(identity LoginIdentity, devs []*types.MFADevice) LoginIdentity {
	return &loginWithDevices{
		LoginIdentity: identity,
		devices:       devs,
	}
}

type loginWithDevices struct {
	LoginIdentity
	devices []*types.MFADevice
}

func (l *loginWithDevices) GetMFADevices(ctx context.Context, user string, withSecrets bool) ([]*types.MFADevice, error) {
	return l.devices, nil
}

// LoginFlow represents the WebAuthn login procedure (aka authentication).
//
// The login flow consists of:
//
// 1. Client requests a CredentialAssertion (containing, among other info, a
//    challenge to be signed)
// 2. Server runs Begin(), generates a credential assertion.
// 3. Client validates the assertion, performs a user presence test (usually by
//    asking the user to touch a secure token), and replies with
//    CredentialAssertionResponse (containing the signed challenge)
// 4. Server runs Finish()
// 5. If all server-side checks are successful, then login/authentication is
//    complete.
type LoginFlow struct {
	U2F      *types.U2F
	Webauthn *types.Webauthn
	// Identity is typically an implementation of the Identity service, ie, an
	// object with access to user, device and MFA storage.
	Identity LoginIdentity
}

// Begin is the first step of the LoginFlow.
// The CredentialAssertion created is relayed back to the client, who in turn
// performs a user presence check and signs the challenge contained within the
// assertion.
// As a side effect Begin may assign (and record in storage) a WebAuthn ID for
// the user.
func (f *LoginFlow) Begin(ctx context.Context, user string) (*CredentialAssertion, error) {
	switch {
	case f.Webauthn.Disabled:
		return nil, trace.BadParameter("webauthn disabled")
	case user == "":
		return nil, trace.BadParameter("user required")
	}

	// Fetch existing user devices. We need the devices both to set the allowed
	// credentials for the user (webUser.credentials) and to determine if the U2F
	// appid extension is necessary.
	devices, err := f.Identity.GetMFADevices(ctx, user, false /* withSecrets */)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	var opts []wan.LoginOption
	if f.U2F != nil && f.U2F.AppID != "" {
		// See https://www.w3.org/TR/webauthn-2/#sctn-appid-extension.
		opts = append(opts, wan.WithAssertionExtensions(protocol.AuthenticationExtensions{
			AppIDExtension: f.U2F.AppID,
		}))
	}

	webID, err := getOrCreateUserWebauthnID(ctx, user, f.Identity)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	u := newWebUser(user, webID, true /* credentialIDOnly */, devices)

	// Create the WebAuthn object and create a new challenge.
	web, err := newWebAuthn(f.Webauthn, f.Webauthn.RPID, "" /* origin */)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	assertion, sessionData, err := web.BeginLogin(u, opts...)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	// Store SessionData - it's checked against the user response by
	// LoginFlow.Finish().
	sessionDataPB, err := sessionToPB(sessionData)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	if err := f.Identity.UpsertWebauthnSessionData(ctx, user, loginSessionID, sessionDataPB); err != nil {
		return nil, trace.Wrap(err)
	}

	return (*CredentialAssertion)(assertion), nil
}

// Finish is the second and last step of the LoginFlow.
// It returns the MFADevice used to solve the challenge. If login is successful,
// Finish has the side effect of updating the counter and last used timestamp of
// the returned device.
func (f *LoginFlow) Finish(ctx context.Context, user string, resp *CredentialAssertionResponse) (*types.MFADevice, error) {
	switch {
	case f.Webauthn.Disabled:
		return nil, trace.BadParameter("webauthn disabled")
	case user == "":
		return nil, trace.BadParameter("user required")
	case resp == nil:
		// resp != nil is good enough to proceed, we leave remaining validations to
		// duo-labs/webauthn.
		return nil, trace.BadParameter("credential assertion response required")
	}

	parsedResp, err := parseCredentialResponse(resp)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	origin := parsedResp.Response.CollectedClientData.Origin
	if err := validateOrigin(origin, f.Webauthn.RPID); err != nil {
		log.WithError(err).Debugf("WebAuthn: origin validation failed")
		return nil, trace.Wrap(err)
	}

	// Find the device used to sign the credentials. It must be a previously
	// registered device.
	devices, err := f.Identity.GetMFADevices(ctx, user, false /* withSecrets */)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	dev, ok := findDeviceByID(devices, parsedResp.RawID)
	switch {
	case !ok:
		return nil, trace.BadParameter(
			"unknown device credential: %q", base64.RawURLEncoding.EncodeToString(parsedResp.RawID))
	}

	// Is an U2F device trying to login? If yes, use RPID = App ID.
	// Technically browsers should reply with the appid extension set to true[1],
	// but in actuality they don't send anything.
	// [1] https://www.w3.org/TR/webauthn-2/#sctn-appid-extension.
	rpID := f.Webauthn.RPID
	switch {
	case dev.GetU2F() != nil && f.U2F == nil:
		return nil, trace.BadParameter("U2F device attempted login, but U2F configuration not present")
	case dev.GetU2F() != nil:
		rpID = f.U2F.AppID
	}

	// Fetch the user web ID, it must exist if they got here.
	wla, err := f.Identity.GetWebauthnLocalAuth(ctx, user)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	u := newWebUser(user, wla.UserID, false /* credentialIDOnly */, []*types.MFADevice{dev})

	// Fetch the previously-stored SessionData, so it's checked against the user
	// response.
	sessionDataPB, err := f.Identity.GetWebauthnSessionData(ctx, user, loginSessionID)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	sessionData := sessionFromPB(sessionDataPB)

	// Create a WebAuthn matching the expected RPID and Origin, then verify the
	// signed challenge.
	web, err := newWebAuthn(f.Webauthn, rpID, origin)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	credential, err := web.ValidateLogin(u, *sessionData, parsedResp)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	if credential.Authenticator.CloneWarning {
		log.Warnf(
			"WebAuthn: Clone warning detected for user %q / device %q. Device counter may be malfunctioning.", user, dev.GetName())
	}

	// Update last used timestamp and device counter.
	if err := setCounterAndTimestamps(dev, credential); err != nil {
		return nil, trace.Wrap(err)
	}
	if err := f.Identity.UpsertMFADevice(ctx, user, dev); err != nil {
		return nil, trace.Wrap(err)
	}

	// The user just solved this challenge, so let's make sure it won't be used
	// again.
	if err := f.Identity.DeleteWebauthnSessionData(ctx, user, loginSessionID); err != nil {
		log.Warnf("WebAuthn: failed to delete login SessionData for user %v", user)
	}

	return dev, nil
}

func parseCredentialResponse(resp *CredentialAssertionResponse) (*protocol.ParsedCredentialAssertionData, error) {
	// Do not pass extensions on to duo-labs/webauthn, they won't go past JSON
	// unmarshal.
	exts := resp.Extensions
	resp.Extensions = nil
	defer func() { resp.Extensions = exts }()

	// This is a roundabout way of getting resp validated, but unfortunately the
	// APIs don't provide a better method (and it seems better than duplicating
	// library code here).
	body, err := json.Marshal(resp)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	return protocol.ParseCredentialRequestResponseBody(bytes.NewReader(body))
}

func findDeviceByID(devices []*types.MFADevice, id []byte) (*types.MFADevice, bool) {
	for _, dev := range devices {
		switch d := dev.Device.(type) {
		case *types.MFADevice_U2F:
			if bytes.Equal(d.U2F.KeyHandle, id) {
				return dev, true
			}
		case *types.MFADevice_Webauthn:
			if bytes.Equal(d.Webauthn.CredentialId, id) {
				return dev, true
			}
		}
	}
	return nil, false
}

func setCounterAndTimestamps(dev *types.MFADevice, credential *wan.Credential) error {
	switch d := dev.Device.(type) {
	case *types.MFADevice_U2F:
		d.U2F.Counter = credential.Authenticator.SignCount
	case *types.MFADevice_Webauthn:
		d.Webauthn.SignatureCounter = credential.Authenticator.SignCount
	default:
		return trace.BadParameter("unexpected device type for webauthn: %T", d)
	}
	dev.LastUsed = time.Now()
	return nil
}
