package gateway

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/SimonOsipov/invoice-os/internal/platform"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

const (
	iapAssertionHeader = "X-Goog-Iap-Jwt-Assertion"
	maxAssertionBytes  = 8 << 10
	iapNoIdentity      = "no verified Google identity"
	iapNoStaff         = "no staff account"
	iapConsoleDown     = "console sign-in is unavailable"
)

// IAPSessionHandler answers POST /auth/iap-session: a verified IAP assertion becomes a hand-off code
// whose answer is a staff session for the account with that email. The assertion alone selects the account.
func IAPSessionHandler(v *auth.IAPVerifier, worker *url.URL, workerClient *http.Client, gatewayToken string, authURL *url.URL, gotrue *http.Client, store Handoffs, log *slog.Logger, events AccountEvents) http.Handler {
	recoveryURL := worker.JoinPath("internal", "console-sessions", "recovery-token").String()
	verifyURL := authURL.JoinPath("verify").String()
	logoutURL := authURL.JoinPath("logout")
	logoutURL.RawQuery = "scope=local"

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !postOnly(w, r) {
			return
		}
		if !jsonOnly(w, r) {
			return
		}
		var in struct {
			State string `json:"state"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxExchangeBodyBytes)).Decode(&in); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if !stateShape.MatchString(in.State) {
			writeError(w, http.StatusBadRequest, "state is required")
			return
		}
		vals := r.Header.Values(iapAssertionHeader)
		if len(vals) != 1 || vals[0] == "" || len(vals[0]) > maxAssertionBytes {
			writeError(w, http.StatusUnauthorized, iapNoIdentity)
			return
		}
		assertion := vals[0]
		id, err := v.Verify(r.Context(), assertion)
		if err != nil {
			writeError(w, http.StatusUnauthorized, iapNoIdentity)
			return
		}
		noStaff := func() {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": iapNoStaff, "email": id.Email})
		}

		// A concurrent mint for the same account spends the token this one holds: ask for a fresh one, once.
		var sess struct {
			AccessToken  string     `json:"access_token"`
			RefreshToken string     `json:"refresh_token"`
			User         gotrueUser `json:"user"`
		}
		for attempt := 0; ; attempt++ {
			hash, status, err := recoveryTokenHash(r, workerClient, recoveryURL, gatewayToken, assertion)
			switch {
			case err != nil:
				log.WarnContext(r.Context(), "iap session: worker unreachable", slog.String("error", err.Error()))
				writeError(w, http.StatusBadGateway, iapConsoleDown)
				return
			case status == http.StatusForbidden:
				noStaff()
				return
			case status != http.StatusOK || hash == "":
				log.WarnContext(r.Context(), "iap session: worker refused", slog.Int("upstream_status", status))
				writeError(w, http.StatusBadGateway, iapConsoleDown)
				return
			}
			sess.AccessToken, sess.RefreshToken = "", ""
			vstatus, gt, err := postGoTrue(r, gotrue, verifyURL, map[string]string{"type": "recovery", "token_hash": hash}, &sess)
			switch {
			case err != nil:
				log.WarnContext(r.Context(), "iap session: gotrue unreachable", slog.String("error", err.Error()))
				writeError(w, http.StatusBadGateway, iapConsoleDown)
				return
			case vstatus == http.StatusOK && sess.AccessToken != "" && sess.RefreshToken != "":
			case gt.ErrorCode == "otp_expired" && attempt == 0:
				continue
			case gt.ErrorCode == "user_banned":
				noStaff()
				return
			case vstatus == http.StatusTooManyRequests:
				writeError(w, http.StatusTooManyRequests, iapTooMany)
				return
			default:
				log.WarnContext(r.Context(), "iap session: gotrue verify failed", slog.Int("upstream_status", vstatus))
				writeError(w, http.StatusBadGateway, iapConsoleDown)
				return
			}
			break
		}

		// ceiling: each mint opens one GoTrue session; a non-staff one is ended at once, a staff one expires with its refresh token.
		if !hasStaffClaim(sess.AccessToken) {
			if _, _, err := postGoTrueBearer(r.Context(), gotrue, logoutURL.String(), sess.AccessToken, gotrueAddress(r)); err != nil {
				log.WarnContext(r.Context(), "iap session: ending the non-staff session failed", slog.String("error", err.Error()))
			}
			noStaff()
			return
		}
		answer, _ := json.Marshal(map[string]string{"access_token": sess.AccessToken, "refresh_token": sess.RefreshToken}) // cannot fail
		code, ok := store.Put(string(answer), sha256.Sum256([]byte(in.State)))
		if !ok {
			log.WarnContext(r.Context(), "iap session: hand-off store full")
			writeError(w, http.StatusServiceUnavailable, iapConsoleDown)
			return
		}
		recordAccountEvent(r, events, log, AccountEvent{Event: EventSignedIn, UserID: sess.User.ID, Email: sess.User.Email})
		log.InfoContext(r.Context(), "iap session: minted", slog.String("user_id", sess.User.ID))
		writeJSON(w, http.StatusOK, map[string]string{"code": code})
	})
}

const iapTooMany = "too many requests"

// recoveryTokenHash asks the worker for a recovery token hash for the assertion's account.
func recoveryTokenHash(r *http.Request, client *http.Client, target, gatewayToken, assertion string) (hash string, status int, err error) {
	body, _ := json.Marshal(map[string]string{"assertion": assertion}) // cannot fail
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(platform.HeaderGatewayToken, gatewayToken)
	resp, err := client.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()
	lr := io.LimitReader(resp.Body, maxGoTrueBodyBytes)
	var out struct {
		TokenHash string `json:"token_hash"`
	}
	if resp.StatusCode == http.StatusOK {
		_ = json.NewDecoder(lr).Decode(&out)
	}
	_, _ = io.Copy(io.Discard, lr)
	return out.TokenHash, resp.StatusCode, nil
}

// hasStaffClaim reads app_metadata.staff from a token GoTrue just issued to this gateway, so the
// signature is not checked. Keys match exactly and the value must be the boolean true.
func hasStaffClaim(accessToken string) bool {
	parts := strings.Split(accessToken, ".")
	if len(parts) != 3 {
		return false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}
	var claims map[string]json.RawMessage
	if json.Unmarshal(payload, &claims) != nil {
		return false
	}
	var meta map[string]any
	if json.Unmarshal(claims["app_metadata"], &meta) != nil {
		return false
	}
	staff, _ := meta["staff"].(bool)
	return staff
}
