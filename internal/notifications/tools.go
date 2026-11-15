package notifications

import (
	"context"
	"strconv"

	"github.com/g4f4r0/tender/internal/mcp"
	"github.com/g4f4r0/tender/internal/server"
)

type listInput struct {
	Machine  string `json:"machine,omitempty" jsonschema:"only this machine's notifications; leave out for every machine"`
	Status   string `json:"status,omitempty" jsonschema:"only notifications with this status: pending, sent or resolved"`
	Severity string `json:"severity,omitempty" jsonschema:"only notifications with this severity: info, warning or critical"`
	Limit    int    `json:"limit,omitempty" jsonschema:"most notifications to return, 1 to 500, default 50"`
	Cursor   string `json:"cursor,omitempty" jsonschema:"next_cursor from the previous page"`
}

type addRecipientInput struct {
	Email string `json:"email" jsonschema:"bare email address, such as alerts@example.com"`
}

type recipientIDInput struct {
	ID int64 `json:"id" jsonschema:"recipient id, as list_recipients shows it"`
}

type noInput struct{}

// RegisterTools adds the notifications tools to s. They use the same store methods as the REST
// routes. lookup resolves machine names to ids. send_test_email sends through sender, which is nil
// when email is off, and names hub, the hub's host name, in the message.
func (s *Store) RegisterTools(m *mcp.Server, lookup mcp.MachineLookup, sender Sender, hub string) {
	mcp.AddReadOnly(m, "list_notifications", "List notifications",
		"List alerts, newest first, optionally of one machine, status or severity. Each has id, machine (null for the hub itself), "+
			"service (the Docker service it is about, else null), type, severity (info, warning or critical), status (pending and sent are open, resolved is closed), content and "+
			"created_at and updated_at (RFC 3339 UTC). Leave status out to get all of them. At most limit a page, 1 to 500, default 50; "+
			"pass next_cursor as cursor for older ones.",
		func(ctx context.Context, in listInput) (server.List[Notification], error) {
			var machineID int64
			if in.Machine != "" {
				id, err := mcp.Machine(ctx, lookup, in.Machine)
				if err != nil {
					return server.List[Notification]{}, err
				}
				machineID = id
			}
			page, err := server.NewPage(in.Limit, in.Cursor)
			if err != nil {
				return server.List[Notification]{}, err
			}
			items, err := s.List(ctx, machineID, in.Status, in.Severity, page)
			return server.NewList(items, page, cursor), err
		})

	mcp.AddReadOnly(m, "list_recipients", "List alert recipients",
		"List the email addresses that get every alert email, in the order they were added. Each has id (for get_recipient and remove_recipient), email, created_at and updated_at (RFC 3339 UTC).",
		func(ctx context.Context, _ noInput) (server.List[Recipient], error) {
			rs, err := s.Recipients(ctx)
			return server.List[Recipient]{Data: rs}, err
		})

	mcp.AddReadOnly(m, "get_recipient", "Get an alert recipient",
		"Get one recipient by id: id, email, created_at and updated_at (RFC 3339 UTC).",
		func(ctx context.Context, in recipientIDInput) (Recipient, error) {
			return s.Recipient(ctx, in.ID)
		})

	mcp.AddWrite(m, "add_recipient", "Add an alert recipient",
		"Add an email address that gets every alert email from now on. It must be a bare address, like alerts@example.com. "+
			"Call send_test_email to check that mail reaches it. The result is the recipient: id, email, created_at and updated_at.",
		mcp.WriteOptions{},
		func(ctx context.Context, in addRecipientInput) (Recipient, error) {
			return s.AddRecipient(ctx, in.Email)
		})

	mcp.AddWrite(m, "remove_recipient", "Remove an alert recipient",
		"Remove a recipient by id, so they stop getting alert emails. Alerts are still recorded when nobody is left. The result names what was removed.",
		mcp.WriteOptions{Destructive: true, Idempotent: true},
		func(ctx context.Context, in recipientIDInput) (mcp.Removed, error) {
			if err := s.DeleteRecipient(ctx, in.ID); err != nil {
				return mcp.Removed{}, err
			}
			return mcp.Removed{Removed: "recipient " + strconv.FormatInt(in.ID, 10)}, nil
		})

	mcp.AddWrite(m, "send_test_email", "Send a test email",
		"Send the test email to every recipient, to check that alert emails arrive. It is refused when email is off on the hub "+
			"or when there are no recipients, and the error says what to set. The result is sent_to, the addresses it went to.",
		mcp.WriteOptions{OpenWorld: true},
		func(ctx context.Context, _ noInput) (TestResult, error) {
			return s.SendTest(ctx, sender, hub)
		})

}

func cursor(n Notification) server.Cursor {
	return server.Cursor{CreatedAt: n.CreatedAt.Unix(), ID: n.ID}
}
