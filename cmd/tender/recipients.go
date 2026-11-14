package main

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/g4f4r0/tender/internal/notifications"
)

var recipientsUsage = groupUsage("recipients", "Choose who gets the alert emails. Every recipient gets every alert.", []row{
	{"list", "List the recipients"},
	{"add EMAIL", "Add a recipient"},
	{"remove EMAIL", "Remove a recipient"},
})

var emailUsage = groupUsage("email", "Check that alert emails arrive.", []row{
	{"test", "Send a test email to every recipient"},
})

// runRecipients runs "tender recipients list|add|remove".
func runRecipients(args []string, s session) error {
	return runGroup("recipients", recipientsUsage, args, s.out, map[string]func([]string) error{
		"list":   func(a []string) error { return recipientsList(a, s) },
		"add":    func(a []string) error { return recipientsAdd(a, s) },
		"remove": func(a []string) error { return recipientsRemove(a, s) },
	})
}

// runEmail runs "tender email test".
func runEmail(args []string, s session) error {
	return runGroup("email", emailUsage, args, s.out, map[string]func([]string) error{
		"test": func(a []string) error { return emailTest(a, s) },
	})
}

func recipientsList(args []string, s session) error {
	fs := newFlagSet("recipients list", "[--hub URL]", "List who gets the alert emails.")
	hubURL := hubFlag(fs)
	rest, err := parseWithArgs(fs, args, s.out)
	if err != nil {
		return err
	}
	if err := noArgs(fs, rest); err != nil {
		return err
	}
	hub, err := newHubAPI(fs, *hubURL)
	if err != nil {
		return err
	}
	list, err := listAll[notifications.Recipient](hub, "/v1/notifications/recipients")
	if err != nil {
		return err
	}
	if len(list) == 0 {
		say(s.out, "No recipients yet, so no alert is emailed. Add one with: tender recipients add EMAIL")
		return nil
	}
	rows := make([][]string, len(list))
	for i, r := range list {
		rows[i] = []string{r.Email, ago(s.now(), r.CreatedAt)}
	}
	printTable(s.out, []string{"EMAIL", "ADDED"}, rows)
	return nil
}

func recipientsAdd(args []string, s session) error {
	fs := newFlagSet("recipients add", "EMAIL [--hub URL]", "Add an address that gets every alert email.")
	hubURL := hubFlag(fs)
	rest, err := parseWithArgs(fs, args, s.out)
	if err != nil {
		return err
	}
	email, err := oneArg(fs, rest, "an email address")
	if err != nil {
		return err
	}
	hub, err := newHubAPI(fs, *hubURL)
	if err != nil {
		return err
	}
	var r notifications.Recipient
	if err := hub.do(http.MethodPost, "/v1/notifications/recipients", notifications.RecipientInput{Email: email}, &r); err != nil {
		return err
	}
	say(s.out, "Added %s. It gets every alert. Check that the email arrives with: tender email test", r.Email)
	return nil
}

func recipientsRemove(args []string, s session) error {
	fs := newFlagSet("recipients remove", "EMAIL [--hub URL]", "Remove an address from the alert emails.")
	hubURL := hubFlag(fs)
	rest, err := parseWithArgs(fs, args, s.out)
	if err != nil {
		return err
	}
	email, err := oneArg(fs, rest, "an email address")
	if err != nil {
		return err
	}
	hub, err := newHubAPI(fs, *hubURL)
	if err != nil {
		return err
	}
	list, err := listAll[notifications.Recipient](hub, "/v1/notifications/recipients")
	if err != nil {
		return err
	}
	for _, r := range list {
		if strings.EqualFold(r.Email, email) {
			if err := hub.do(http.MethodDelete, "/v1/notifications/recipients/"+strconv.FormatInt(r.ID, 10), nil, nil); err != nil {
				return err
			}
			say(s.out, "Removed %s. It no longer gets alerts.", r.Email)
			return nil
		}
	}
	return fmt.Errorf("%s is not a recipient. Run \"tender recipients list\" to see who is.", email)
}

func emailTest(args []string, s session) error {
	fs := newFlagSet("email test", "[--hub URL]", "Send a test email to every recipient, to check that alerts arrive.")
	hubURL := hubFlag(fs)
	rest, err := parseWithArgs(fs, args, s.out)
	if err != nil {
		return err
	}
	if err := noArgs(fs, rest); err != nil {
		return err
	}
	hub, err := newHubAPI(fs, *hubURL)
	if err != nil {
		return err
	}
	var r notifications.TestResult
	if err := hub.do(http.MethodPost, "/v1/notifications/test", nil, &r); err != nil {
		return err
	}
	say(s.out, "Sent a test email to %s.", strings.Join(r.SentTo, ", "))
	return nil
}
