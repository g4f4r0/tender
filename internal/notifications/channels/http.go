package channels

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// SendTimeout bounds one delivery: one HTTP request, or one run of an exec adapter. Tests shorten it.
var SendTimeout = 10 * time.Second

// httpClient sends webhook and Slack requests. It never follows a redirect, so a body and its
// signature only ever go to the URL in the config.
var httpClient = &http.Client{
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// post sends body to target. A 2xx answer is delivered; a 4xx other than 429 is Permanent, with
// the start of the answer; anything else is worth trying again. what names the receiver in
// errors, which never hold the URL, since it can carry a key.
func post(ctx context.Context, target, what string, body []byte, header http.Header) error {
	ctx, cancel := context.WithTimeout(ctx, SendTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return &Permanent{fmt.Errorf("the %s URL is not valid", what)}
	}
	for k, v := range header {
		req.Header[k] = v
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Tender")
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("could not reach the %s: %s", what, reason(err))
	}
	defer func() { _ = resp.Body.Close() }()
	start, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBytes))
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20)) // lets the connection be used again
	if resp.StatusCode/100 == 2 {
		return nil
	}
	msg := fmt.Sprintf("the %s answered %d", what, resp.StatusCode)
	if text := cut(string(start)); text != "" {
		msg += ": " + text
	}
	err = errors.New(msg)
	if resp.StatusCode/100 == 4 && resp.StatusCode != http.StatusTooManyRequests {
		return &Permanent{err}
	}
	return err
}

// reason is why a request failed, without the URL that net/http puts in its errors.
func reason(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	if errors.Is(err, context.DeadlineExceeded) || isTimeout(err) {
		return fmt.Sprintf("no answer within %d seconds", int(SendTimeout/time.Second))
	}
	return strings.TrimSpace(err.Error())
}

func isTimeout(err error) bool {
	var t interface{ Timeout() bool }
	return errors.As(err, &t) && t.Timeout()
}

// checkURL is an error unless raw is an http or https URL with a host. field names it.
func checkURL(raw, field string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return fmt.Errorf("%s must be an http or https URL, such as https://example.com/tender.", field)
	}
	return nil
}
