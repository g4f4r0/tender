package notifications

import (
	"net/http"
	"strconv"

	"github.com/g4f4r0/tender/internal/server"
)

// Routes registers the notifications REST routes on srv. The test route sends through sender,
// which is nil when email is off, and names hub, the hub's host name, in the message.
func (s *Store) Routes(srv *server.Server, sender Sender, hub string) {
	srv.Handle("GET /v1/notifications", srv.People(s.list))
	srv.Handle("GET /v1/machines/{machine}/notifications", srv.People(s.list))
	srv.Handle("GET /v1/machines/{machine}/notifications/{id}", srv.People(s.get))
	srv.Handle("GET /v1/notifications/recipients", srv.People(s.listRecipients))
	srv.Handle("POST /v1/notifications/recipients", srv.People(s.addRecipient))
	srv.Handle("DELETE /v1/notifications/recipients/{id}", srv.People(s.removeRecipient))
	srv.Handle("POST /v1/notifications/test", srv.People(func(w http.ResponseWriter, r *http.Request) error {
		res, err := s.SendTest(r.Context(), sender, hub)
		if err != nil {
			return err
		}
		server.WriteJSON(w, http.StatusAccepted, res)
		return nil
	}))
}

func (s *Store) listRecipients(w http.ResponseWriter, r *http.Request) error {
	rs, err := s.Recipients(r.Context())
	if err != nil {
		return err
	}
	server.WriteJSON(w, http.StatusOK, server.List[Recipient]{Data: rs})
	return nil
}

func (s *Store) addRecipient(w http.ResponseWriter, r *http.Request) error {
	var in RecipientInput
	if err := server.Decode(w, r, &in); err != nil {
		return err
	}
	rec, err := s.AddRecipient(r.Context(), in.Email)
	if err != nil {
		return err
	}
	w.Header().Set("Location", "/v1/notifications/recipients/"+strconv.FormatInt(rec.ID, 10))
	server.WriteJSON(w, http.StatusCreated, rec)
	return nil
}

func (s *Store) removeRecipient(w http.ResponseWriter, r *http.Request) error {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		return server.NotFound("%q is not an id. List the recipients to see their ids.", r.PathValue("id"))
	}
	if err := s.DeleteRecipient(r.Context(), id); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// list serves both list routes: server.MachineID is 0 on /v1/notifications, which means every machine.
func (s *Store) list(w http.ResponseWriter, r *http.Request) error {
	page, err := server.ParsePage(r)
	if err != nil {
		return err
	}
	q := r.URL.Query()
	items, err := s.List(r.Context(), server.MachineID(r.Context()), q.Get("status"), q.Get("severity"), page)
	if err != nil {
		return err
	}
	server.WriteJSON(w, http.StatusOK, server.NewList(items, page, cursor))
	return nil
}

func (s *Store) get(w http.ResponseWriter, r *http.Request) error {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		return server.NotFound("%q is not an id.", r.PathValue("id"))
	}
	n, err := s.Get(r.Context(), server.MachineID(r.Context()), id)
	if err != nil {
		return err
	}
	server.WriteJSON(w, http.StatusOK, n)
	return nil
}
