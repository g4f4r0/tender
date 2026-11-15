package sinks

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"time"

	"github.com/g4f4r0/tender/internal/server"
)

// webhook POSTs each batch as Tender's log contract, version 1, signed with the sink's secret in
// the Tender-Signature header.
type webhook struct{}

type webhookConfig struct {
	URL    string `json:"url"`
	Secret string `json:"secret"`
}

// minSecret is the shortest secret a person may choose; the hub makes longer ones.
const minSecret = 16

func (webhook) Fields() []string  { return []string{"url", "secret"} }
func (webhook) Secrets() []string { return []string{"secret"} }
func (webhook) Generated() string { return "secret" }

func (t webhook) Check(c Config) (Config, error) {
	var v webhookConfig
	if err := decode("webhook", t, c, &v); err != nil {
		return nil, err
	}
	var err error
	if v.URL, err = checkURL("url", v.URL, "https://logs.example.com/tender"); err != nil {
		return nil, err
	}
	if len(v.Secret) < minSecret {
		return nil, server.Invalid("config.secret must be at least %d characters. Leave it out and the hub makes one.", minSecret)
	}
	return encode(v), nil
}

func (t webhook) New(c Config, machine string) (Sender, error) {
	var v webhookConfig
	if err := decode("webhook", t, c, &v); err != nil {
		return nil, err
	}
	return webhookSender{config: v, machine: machine, now: time.Now}, nil
}

type webhookSender struct {
	config  webhookConfig
	machine string
	now     func() time.Time
}

// Batch is the body of one webhook request: version 1 of the log contract.
type Batch struct {
	Version int      `json:"version"`
	Machine string   `json:"machine"`
	Records []Record `json:"records"`
}

// Signature is the Tender-Signature header for body, sent at t: the time, and the HMAC-SHA256
// of "<t>.<body>" under secret, in hex.
func Signature(secret string, t time.Time, body []byte) string {
	ts := strconv.FormatInt(t.Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + "."))
	mac.Write(body)
	return "t=" + ts + ",v1=" + hex.EncodeToString(mac.Sum(nil))
}

func (s webhookSender) Send(ctx context.Context, records []Record) error {
	utc := make([]Record, len(records))
	for i, r := range records {
		r.Time = r.Time.UTC()
		utc[i] = r
	}
	body, err := json.Marshal(Batch{Version: 1, Machine: s.machine, Records: utc})
	if err != nil {
		return err
	}
	return post(ctx, s.config.URL, body, map[string]string{"Tender-Signature": Signature(s.config.Secret, s.now(), body)})
}
