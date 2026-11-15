package notifications

import (
	"net/http"
	"net/url"
	"strconv"

	"github.com/g4f4r0/tender/internal/server"
)

// Routes registers the notifications REST routes on srv. The test route sends email through
// sender, which is nil when email is off, and names hub in what it sends.
func (s *Store) Routes(srv *server.Server, sender Sender, hub Hub) {
	t := s.sending(sender, hub)
	srv.Handle("GET /v1/notifications", srv.People(s.list))
	srv.Handle("GET /v1/machines/{machine}/notifications", srv.People(s.list))
	srv.Handle("GET /v1/machines/{machine}/notifications/{id}", srv.People(s.get))
	srv.Handle("GET /v1/notifications/channels", srv.People(s.listChannels))
	srv.Handle("POST /v1/notifications/channels", srv.People(s.addChannel))
	srv.Handle("GET /v1/notifications/channels/{channel}", srv.People(s.getChannel))
	srv.Handle("PATCH /v1/notifications/channels/{channel}", srv.People(s.patchChannel))
	srv.Handle("DELETE /v1/notifications/channels/{channel}", srv.People(s.removeChannel))
	srv.Handle("POST /v1/notifications/test", srv.People(t.test))
}

func (s *Store) listChannels(w http.ResponseWriter, r *http.Request) error {
	cs, err := s.Channels(r.Context())
	if err != nil {
		return err
	}
	server.WriteJSON(w, http.StatusOK, server.List[Channel]{Data: cs})
	return nil
}

func (s *Store) addChannel(w http.ResponseWriter, r *http.Request) error {
	var in ChannelInput
	if err := server.Decode(w, r, &in); err != nil {
		return err
	}
	c, err := s.AddChannel(r.Context(), in)
	if err != nil {
		return err
	}
	w.Header().Set("Location", "/v1/notifications/channels/"+url.PathEscape(c.Name))
	server.WriteJSON(w, http.StatusCreated, c)
	return nil
}

func (s *Store) getChannel(w http.ResponseWriter, r *http.Request) error {
	c, err := s.Channel(r.Context(), r.PathValue("channel"))
	if err != nil {
		return err
	}
	server.WriteJSON(w, http.StatusOK, c)
	return nil
}

func (s *Store) patchChannel(w http.ResponseWriter, r *http.Request) error {
	var p ChannelPatch
	if err := server.Decode(w, r, &p); err != nil {
		return err
	}
	c, err := s.PatchChannel(r.Context(), r.PathValue("channel"), p)
	if err != nil {
		return err
	}
	server.WriteJSON(w, http.StatusOK, c)
	return nil
}

func (s *Store) removeChannel(w http.ResponseWriter, r *http.Request) error {
	if err := s.DeleteChannel(r.Context(), r.PathValue("channel")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// test sends the test event. The body is optional: no body tests every enabled channel.
func (s *Store) test(w http.ResponseWriter, r *http.Request) error {
	var in TestInput
	if r.ContentLength != 0 {
		if err := server.Decode(w, r, &in); err != nil {
			return err
		}
	}
	res, err := s.SendTest(r.Context(), in.Channel)
	if err != nil {
		return err
	}
	server.WriteJSON(w, http.StatusOK, res)
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
