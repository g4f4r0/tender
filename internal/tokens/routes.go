package tokens

import (
	"net/http"
	"net/url"

	"github.com/g4f4r0/tender/internal/server"
)

// Routes registers the tokens REST routes on srv. They are people routes, so they need a token too.
func (s *Store) Routes(srv *server.Server) {
	srv.Handle("GET /v1/tokens", srv.People(s.list))
	srv.Handle("POST /v1/tokens", srv.People(s.create))
	srv.Handle("DELETE /v1/tokens/{token}", srv.People(s.remove))
}

func (s *Store) list(w http.ResponseWriter, r *http.Request) error {
	list, err := s.List(r.Context())
	if err != nil {
		return err
	}
	server.WriteJSON(w, http.StatusOK, server.List[Token]{Data: list})
	return nil
}

func (s *Store) create(w http.ResponseWriter, r *http.Request) error {
	var in TokenInput
	if err := server.Decode(w, r, &in); err != nil {
		return err
	}
	created, err := s.Create(r.Context(), in.Name)
	if err != nil {
		return err
	}
	w.Header().Set("Location", "/v1/tokens/"+url.PathEscape(created.Name))
	server.WriteJSON(w, http.StatusCreated, created)
	return nil
}

func (s *Store) remove(w http.ResponseWriter, r *http.Request) error {
	if err := s.Delete(r.Context(), r.PathValue("token")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
