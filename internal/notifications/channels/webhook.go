package channels

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"
)

// SignatureHeader carries the signature of a webhook's body.
const SignatureHeader = "Tender-Signature"

// Webhook POSTs the event as JSON to a URL, signed with the channel's secret like Stripe's
// webhooks: Tender-Signature: t=<unix seconds>,v1=<hex HMAC-SHA256(secret, "<t>.<body>")>.
type Webhook struct {
	Now func() time.Time // the signature's time; nil means time.Now
}

// Fields: url, and secret, which the hub makes.
func (Webhook) Fields() []Field {
	return []Field{{Name: "url", Required: true}, {Name: "secret", Secret: true, Required: true}}
}

// minSecretBytes is the shortest secret a PATCH may set.
const minSecretBytes = 24

// Check needs an http or https url and a secret of at least 24 characters.
func (Webhook) Check(cfg Config) error {
	if err := checkURL(cfg["url"], "url"); err != nil {
		return err
	}
	if len(cfg["secret"]) < minSecretBytes {
		return errors.New("secret must be at least 24 characters. Leave it out and the hub makes one.")
	}
	return nil
}

// Send POSTs ev to the url with its signature.
func (w Webhook) Send(ctx context.Context, cfg Config, ev Event) error {
	body, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	now := time.Now
	if w.Now != nil {
		now = w.Now
	}
	h := http.Header{}
	h.Set(SignatureHeader, Sign(cfg["secret"], now(), body))
	return post(ctx, cfg["url"], "webhook", body, h)
}

// Sign returns the Tender-Signature value for body at t.
func Sign(secret string, t time.Time, body []byte) string {
	ts := strconv.FormatInt(t.Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + "."))
	mac.Write(body)
	return "t=" + ts + ",v1=" + hex.EncodeToString(mac.Sum(nil))
}

// NewSecret makes a webhook secret: "whsec_" and 32 random bytes in URL-safe base64.
func NewSecret() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return "whsec_" + base64.RawURLEncoding.EncodeToString(raw), nil
}

// Make makes the secret. A secret in the create is refused: the hub makes it, so it is strong.
func (Webhook) Make(cfg Config) ([]string, error) {
	if _, ok := cfg["secret"]; ok {
		return nil, errors.New("Leave secret out: the hub makes it and shows it once.")
	}
	s, err := NewSecret()
	if err != nil {
		return nil, err
	}
	cfg["secret"] = s
	return []string{"secret"}, nil
}
