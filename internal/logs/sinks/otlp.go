package sinks

import (
	"context"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"

	"github.com/g4f4r0/tender/internal/server"
)

// otlp sends records to an OpenTelemetry collector or service as OTLP/HTTP JSON, to its
// /v1/logs endpoint. Each record's resource has host.name, the machine, and service.name, the
// unit or the container.
type otlp struct{}

type otlpConfig struct {
	Endpoint string            `json:"endpoint"`
	Headers  map[string]string `json:"headers,omitempty"`
}

// headerName is an HTTP header name: a token.
var headerName = regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]{1,100}$")

// fixedHeaders are the headers Tender sets itself.
var fixedHeaders = map[string]bool{"content-type": true, "content-length": true, "host": true, "transfer-encoding": true, "user-agent": true}

const maxHeaders = 20

func (otlp) Fields() []string  { return []string{"endpoint", "headers"} }
func (otlp) Secrets() []string { return []string{"headers"} }
func (otlp) Generated() string { return "" }
func (otlp) URL() string       { return "endpoint" }

func (t otlp) Check(c Config) (Config, error) {
	var v otlpConfig
	if err := decode("otlp", t, c, &v); err != nil {
		return nil, err
	}
	var err error
	if v.Endpoint, err = checkURL("endpoint", v.Endpoint, "https://otel.example.com:4318/v1/logs"); err != nil {
		return nil, err
	}
	if len(v.Headers) > maxHeaders {
		return nil, server.Invalid("config.headers can hold at most %d headers.", maxHeaders)
	}
	for k, val := range v.Headers {
		switch {
		case !headerName.MatchString(k):
			return nil, server.Invalid("config.headers has %q, which is not an HTTP header name.", k)
		case fixedHeaders[strings.ToLower(k)]:
			return nil, server.Invalid("config.headers can't set %s: Tender sets it.", k)
		case strings.ContainsAny(val, "\r\n\x00"):
			return nil, server.Invalid("config.headers.%s can't hold a line break.", k)
		}
	}
	out := encode(v)
	if len(v.Headers) == 0 {
		delete(out, "headers")
	}
	return out, nil
}

func (t otlp) New(c Config, machine string) (Sender, error) {
	var v otlpConfig
	if err := decode("otlp", t, c, &v); err != nil {
		return nil, err
	}
	return otlpSender{config: v, machine: machine}, nil
}

type otlpSender struct {
	config  otlpConfig
	machine string
}

// The OTLP JSON encoding, as far as Tender uses it. 64-bit integers are strings in it.
type (
	otlpLogs struct {
		ResourceLogs []resourceLogs `json:"resourceLogs"`
	}
	resourceLogs struct {
		Resource  resource    `json:"resource"`
		ScopeLogs []scopeLogs `json:"scopeLogs"`
	}
	resource struct {
		Attributes []attribute `json:"attributes"`
	}
	scopeLogs struct {
		Scope      scope       `json:"scope"`
		LogRecords []logRecord `json:"logRecords"`
	}
	scope struct {
		Name string `json:"name"`
	}
	logRecord struct {
		TimeUnixNano         string      `json:"timeUnixNano"`
		ObservedTimeUnixNano string      `json:"observedTimeUnixNano"`
		SeverityNumber       int         `json:"severityNumber"`
		SeverityText         string      `json:"severityText"`
		Body                 anyValue    `json:"body"`
		Attributes           []attribute `json:"attributes"`
	}
	attribute struct {
		Key   string   `json:"key"`
		Value anyValue `json:"value"`
	}
	anyValue struct {
		StringValue string `json:"stringValue"`
	}
)

// severity is the OpenTelemetry severity number of a journal priority.
var severity = map[string]int{"emerg": 24, "alert": 23, "crit": 21, "err": 17, "warning": 13, "notice": 10, "info": 9, "debug": 5}

func str(k, v string) attribute { return attribute{Key: k, Value: anyValue{StringValue: v}} }

// Body is the OTLP JSON body for records: one resource for each unit or service, in the order
// they first appear.
func (s otlpSender) Body(records []Record) ([]byte, error) {
	var out otlpLogs
	index := map[string]int{}
	for _, r := range records {
		key := r.Source + "\x00" + r.Name()
		i, ok := index[key]
		if !ok {
			i = len(out.ResourceLogs)
			index[key] = i
			out.ResourceLogs = append(out.ResourceLogs, resourceLogs{
				Resource:  resource{Attributes: []attribute{str("host.name", s.machine), str("service.name", r.Name())}},
				ScopeLogs: []scopeLogs{{Scope: scope{Name: "tender"}}},
			})
		}
		ns := strconv.FormatInt(r.Time.UnixNano(), 10)
		sl := &out.ResourceLogs[i].ScopeLogs[0]
		sl.LogRecords = append(sl.LogRecords, logRecord{
			TimeUnixNano: ns, ObservedTimeUnixNano: ns, SeverityNumber: severity[r.Priority], SeverityText: r.Priority,
			Body: anyValue{StringValue: r.Message}, Attributes: []attribute{str("log.source", r.Source)},
		})
	}
	return json.Marshal(out)
}

func (s otlpSender) Send(ctx context.Context, records []Record) error {
	body, err := s.Body(records)
	if err != nil {
		return err
	}
	return post(ctx, s.config.Endpoint, body, s.config.Headers)
}
