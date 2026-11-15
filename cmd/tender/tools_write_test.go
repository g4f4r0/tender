package main

import (
	"context"
	"encoding/base64"
	"errors"
	"slices"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/g4f4r0/tender/internal/backups"
	"github.com/g4f4r0/tender/internal/machines"
	"github.com/g4f4r0/tender/internal/mcp"
	"github.com/g4f4r0/tender/internal/notifications"
	"github.com/g4f4r0/tender/internal/server"
)

// fails asserts that a tool call came back as an isError result whose text has every part.
func fails(t *testing.T, cs *sdk.ClientSession, tool string, args map[string]any, parts ...string) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &sdk.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		// The SDK may refuse input that breaks the schema before the tool runs.
		if len(parts) > 0 {
			t.Errorf("%s %v: protocol error %v, want an isError result", tool, args, err)
		}
		return
	}
	msg := errorText(res)
	if !res.IsError {
		t.Errorf("%s %v: want a tool error, got %s", tool, args, msg)
		return
	}
	for _, part := range parts {
		if !strings.Contains(msg, part) {
			t.Errorf("%s %v: error %q lacks %q", tool, args, msg, part)
		}
	}
}

func TestMachineTools(t *testing.T) {
	d := schema(t)
	cs := connect(t, d)

	var added machines.MachineCreated
	structured(t, callTool(t, cs, "add_machine", map[string]any{"name": "g4f4r0-code"}), &added)
	if added.Name != "g4f4r0-code" || added.ID == 0 || added.CreatedAt.IsZero() || len(added.Token) != 43 {
		t.Fatalf("add_machine = %+v, want the machine and a 43 character token", added)
	}
	if _, name, err := machines.NewStore(d).Authenticate(context.Background(), added.Token); err != nil || name != "g4f4r0-code" {
		t.Errorf("the token the tool returned does not authenticate: %q, %v", name, err)
	}
	fails(t, cs, "add_machine", map[string]any{"name": "g4f4r0-code"}, "already exists", "rotate")
	fails(t, cs, "add_machine", map[string]any{"name": "Bad Name"}, "lowercase letters")
	fails(t, cs, "add_machine", map[string]any{})

	var rotated machines.Token
	structured(t, callTool(t, cs, "rotate_machine_token", map[string]any{"machine": "g4f4r0-code"}), &rotated)
	if len(rotated.Token) != 43 || rotated.Token == added.Token {
		t.Errorf("rotate_machine_token = %+v", rotated)
	}
	if _, _, err := machines.NewStore(d).Authenticate(context.Background(), added.Token); err == nil {
		t.Error("the old token still works after rotation")
	}
	fails(t, cs, "rotate_machine_token", map[string]any{"machine": "nope"}, `"nope" doesn't exist`, "list_machines")

	var removed mcp.Removed
	structured(t, callTool(t, cs, "remove_machine", map[string]any{"machine": "g4f4r0-code"}), &removed)
	if removed.Removed != "g4f4r0-code" {
		t.Errorf("remove_machine = %+v", removed)
	}
	var list server.List[machines.Overview]
	structured(t, callTool(t, cs, "list_machines", nil), &list)
	if len(list.Data) != 0 {
		t.Errorf("machines after the removal = %+v", list.Data)
	}
	fails(t, cs, "remove_machine", map[string]any{"machine": "g4f4r0-code"}, "list_machines")
}

func TestDestinationAndBackupTools(t *testing.T) {
	d := schema(t)
	cs := connect(t, d)
	structured(t, callTool(t, cs, "add_machine", map[string]any{"name": "a"}), &machines.MachineCreated{})

	dest := func(name string, extra map[string]any) map[string]any {
		config := map[string]any{"endpoint": "https://acct.r2.cloudflarestorage.com", "bucket": name,
			"access_key_id": "marker-key-id", "secret_access_key": "marker-secret-key"}
		args := map[string]any{"name": name, "config": config}
		for k, v := range extra {
			if k == "password" || k == "type" {
				args[k] = v
			} else {
				config[k] = v
			}
		}
		return args
	}
	creds := func(id int64) backups.Credentials {
		st, err := backups.NewStore(d, testKey).DaemonSettings(context.Background(), id)
		if err != nil || st.Credentials == nil {
			t.Fatalf("daemon settings: %v", err)
		}
		return *st.Credentials
	}

	// Without a password, the hub makes one and returns it once.
	var made backups.DestinationCreated
	structured(t, callTool(t, cs, "add_destination", dest("r2", nil)), &made)
	raw, err := base64.StdEncoding.DecodeString(made.Password)
	if made.Name != "r2" || made.Type != "s3" || made.ID == 0 || err != nil || len(raw) != 32 {
		t.Fatalf("add_destination = %+v (%v), want the destination and a generated 32 byte password", made, err)
	}
	// With one, nothing comes back.
	var given backups.DestinationCreated
	structured(t, callTool(t, cs, "add_destination", dest("r2-two", map[string]any{"password": "marker-given", "path": "/hosts/"})), &given)
	if given.Password != "" || given.Config["path"] != "hosts" || given.Config["secret_access_key"] != "***" {
		t.Errorf("add_destination with a password = %+v, want no password back and the trimmed path", given)
	}
	fails(t, cs, "add_destination", dest("r2", map[string]any{"bucket": "other"}), "already exists")
	fails(t, cs, "add_destination", dest("bad", map[string]any{"endpoint": "nope"}), "endpoint must be a URL")
	fails(t, cs, "add_destination", dest("bad", map[string]any{"type": "gcs"}), "type must be one of local, rclone, rest, s3, sftp")
	fails(t, cs, "add_destination", map[string]any{"name": "bad", "config": map[string]any{"endpoint": "https://x.example.com", "bucket": "b"}}, "access_key_id is required")
	fails(t, cs, "add_destination", map[string]any{"name": "bad", "type": "local", "config": map[string]any{"path": "relative"}}, "absolute")

	// The daemon's side shows the tool stored what it was given, sealed.
	structured(t, callTool(t, cs, "set_backup", map[string]any{"machine": "a", "destination": "r2-two", "schedule": "30 3 * * *", "retention_days": 30}), &backups.Settings{})
	id, err := machines.NewStore(d).Lookup(context.Background(), "a")
	if err != nil {
		t.Fatal(err)
	}
	if c := creds(id); c.AccessKeyID != "marker-key-id" || c.SecretAccessKey != "marker-secret-key" || c.Password != "marker-given" {
		t.Errorf("stored credentials = %+v", c)
	}

	// Update: new keys and a new bucket; the password can't change.
	var upd backups.Destination
	structured(t, callTool(t, cs, "update_destination", map[string]any{"destination": "r2-two",
		"config": map[string]any{"bucket": "moved", "access_key_id": "marker-new-id"}}), &upd)
	if upd.Config["bucket"] != "moved" || upd.Config["access_key_id"] != "***" {
		t.Errorf("update_destination = %+v", upd)
	}
	if c := creds(id); c.AccessKeyID != "marker-new-id" || c.SecretAccessKey != "marker-secret-key" || c.Password != "marker-given" {
		t.Errorf("credentials after the update = %+v, want the new id and the old secret and password", c)
	}
	fails(t, cs, "update_destination", map[string]any{"destination": "r2-two", "password": "marker-other"}, "can't change", "encrypted with it")
	fails(t, cs, "update_destination", map[string]any{"destination": "r2-two", "type": "local"}, "type can't change")
	fails(t, cs, "update_destination", map[string]any{"destination": "nope", "config": map[string]any{"bucket": "x"}}, `"nope" doesn't exist`)
	fails(t, cs, "update_destination", map[string]any{"destination": "r2-two", "config": map[string]any{"bucket": ""}}, "bucket is required")

	// set_backup answers with settings and no credentials.
	var st backups.Settings
	structured(t, callTool(t, cs, "set_backup", map[string]any{"machine": "a", "destination": "r2", "schedule": "0 4 * * *", "retention_days": 7}), &st)
	if st.Destination.Name != "r2" || st.Schedule != "0 4 * * *" || st.RetentionDays != 7 || st.Credentials != nil {
		t.Errorf("set_backup = %+v", st)
	}
	fails(t, cs, "set_backup", map[string]any{"machine": "nope", "destination": "r2", "schedule": "0 4 * * *", "retention_days": 7}, "list_machines")
	fails(t, cs, "set_backup", map[string]any{"machine": "a", "destination": "nope", "schedule": "0 4 * * *", "retention_days": 7}, `Destination "nope" doesn't exist`)
	fails(t, cs, "set_backup", map[string]any{"machine": "a", "destination": "r2", "schedule": "daily", "retention_days": 7}, "5-field cron")
	fails(t, cs, "set_backup", map[string]any{"machine": "a", "destination": "r2", "schedule": "0 4 * * *", "retention_days": 0}, "retention_days")

	// A destination in use stays until the machine stops backing up.
	fails(t, cs, "remove_destination", map[string]any{"destination": "r2"}, "used by a", "backup settings")
	var removed mcp.Removed
	structured(t, callTool(t, cs, "unset_backup", map[string]any{"machine": "a"}), &removed)
	if !strings.Contains(removed.Removed, "a") {
		t.Errorf("unset_backup = %+v", removed)
	}
	fails(t, cs, "unset_backup", map[string]any{"machine": "a"}, "no backup settings")
	fails(t, cs, "unset_backup", map[string]any{"machine": "nope"}, "list_machines")
	structured(t, callTool(t, cs, "remove_destination", map[string]any{"destination": "r2"}), &removed)
	if removed.Removed != "r2" {
		t.Errorf("remove_destination = %+v", removed)
	}
	fails(t, cs, "remove_destination", map[string]any{"destination": "r2"}, `"r2" doesn't exist`)
}

// mailbox records what send_test_email sends.
type mailbox struct {
	to   [][]string
	fail bool
}

func (m *mailbox) Send(_ context.Context, to []string, _ notifications.Message) error {
	if m.fail {
		return errors.New("smtp down")
	}
	m.to = append(m.to, to)
	return nil
}

func TestRecipientToolsAndTestEmail(t *testing.T) {
	box := &mailbox{}
	cs := connectSending(t, schema(t), box)

	var list server.List[notifications.Recipient]
	structured(t, callTool(t, cs, "list_recipients", nil), &list)
	if list.Data == nil || len(list.Data) != 0 {
		t.Errorf("no recipients = %#v, want []", list.Data)
	}
	fails(t, cs, "send_test_email", nil, "no recipients", "Add one")

	var one, two notifications.Recipient
	structured(t, callTool(t, cs, "add_recipient", map[string]any{"email": "johan@example.com"}), &one)
	structured(t, callTool(t, cs, "add_recipient", map[string]any{"email": "ops@example.com"}), &two)
	if one.ID == 0 || one.Email != "johan@example.com" || two.ID == one.ID {
		t.Fatalf("add_recipient = %+v, %+v", one, two)
	}
	fails(t, cs, "add_recipient", map[string]any{"email": "johan@example.com"}, "already a recipient")
	fails(t, cs, "add_recipient", map[string]any{"email": "Johan <johan@example.com>"}, "bare address")
	fails(t, cs, "add_recipient", map[string]any{"email": "nope"}, "bare address")
	structured(t, callTool(t, cs, "list_recipients", nil), &list)
	if len(list.Data) != 2 || list.Data[0].Email != "johan@example.com" {
		t.Errorf("recipients = %+v", list.Data)
	}

	var sent notifications.TestResult
	structured(t, callTool(t, cs, "send_test_email", nil), &sent)
	if !slices.Equal(sent.SentTo, []string{"johan@example.com", "ops@example.com"}) || len(box.to) != 1 {
		t.Errorf("send_test_email = %+v after %d sends, want both addresses in one", sent, len(box.to))
	}
	box.fail = true
	fails(t, cs, "send_test_email", nil, "smtp down", "TENDER_SMTP_")

	var removed mcp.Removed
	structured(t, callTool(t, cs, "remove_recipient", map[string]any{"id": one.ID}), &removed)
	if removed.Removed == "" {
		t.Errorf("remove_recipient = %+v", removed)
	}
	fails(t, cs, "remove_recipient", map[string]any{"id": one.ID}, "doesn't exist", "List the recipients")
	structured(t, callTool(t, cs, "list_recipients", nil), &list)
	if len(list.Data) != 1 || list.Data[0].Email != "ops@example.com" {
		t.Errorf("recipients after the removal = %+v", list.Data)
	}
}

func TestTestEmailWhenEmailIsOff(t *testing.T) {
	cs := connect(t, schema(t))
	structured(t, callTool(t, cs, "add_recipient", map[string]any{"email": "johan@example.com"}), &notifications.Recipient{})
	fails(t, cs, "send_test_email", nil, "Email is off", "TENDER_SMTP_HOST")
}

// A process with no key (tender mcp --stdio without it) still reads, and says what is missing for credentials.
func TestToolsWithoutAKey(t *testing.T) {
	d := schema(t)
	ctx, cancel := context.WithCancel(context.Background())
	st, ct := sdk.NewInMemoryTransports()
	done := make(chan struct{})
	go func() { _ = newTools(d, nil, nil, quiet).Run(ctx, st); close(done) }()
	cs, err := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "1"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close(); cancel(); <-done })

	structured(t, callTool(t, cs, "list_machines", nil), &server.List[machines.Overview]{})
	fails(t, cs, "add_destination", map[string]any{"name": "r2", "config": map[string]any{"endpoint": "https://x.example.com", "bucket": "b",
		"access_key_id": "k", "secret_access_key": "s"}}, "TENDER_ENCRYPTION_KEY")
}
