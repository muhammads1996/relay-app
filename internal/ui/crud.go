package ui

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/muhaymien96/relay/internal/dsl"
	"github.com/muhaymien96/relay/internal/store"
)

func atoi64(s string) (int64, error) { return strconv.ParseInt(s, 10, 64) }

func pathID(r *http.Request) (int64, error) {
	id, err := atoi64(r.PathValue("id"))
	if err != nil {
		return 0, fmt.Errorf("bad id %q", r.PathValue("id"))
	}
	return id, nil
}

func readBody(r *http.Request, limit int64) ([]byte, error) {
	return io.ReadAll(io.LimitReader(r.Body, limit))
}

func decode[T any](r *http.Request) (*T, error) {
	var v T
	if err := json.NewDecoder(r.Body).Decode(&v); err != nil {
		return nil, err
	}
	return &v, nil
}

// requestMeta is the listing shape for the sidebar tree.
type requestMeta struct {
	ID       int64  `json:"id"`
	FolderID *int64 `json:"folderId"`
	Name     string `json:"name"`
	Method   string `json:"method"`
	URL      string `json:"url"`
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	cols, err := s.DB.Collections()
	if err != nil {
		httpError(w, 500, err)
		return
	}
	type colState struct {
		store.Collection
		Folders  []store.Folder `json:"folders"`
		Requests []requestMeta  `json:"requests"`
	}
	out := struct {
		Collections  []colState          `json:"collections"`
		Environments []store.Environment `json:"environments"`
		Presets      []store.Preset      `json:"presets"`
	}{Collections: []colState{}, Environments: []store.Environment{}, Presets: []store.Preset{}}

	for _, c := range cols {
		cs := colState{Collection: c, Folders: []store.Folder{}, Requests: []requestMeta{}}
		folders, err := s.DB.Folders(c.ID)
		if err != nil {
			httpError(w, 500, err)
			return
		}
		if folders != nil {
			cs.Folders = folders
		}
		reqs, err := s.DB.Requests(c.ID)
		if err != nil {
			httpError(w, 500, err)
			return
		}
		for _, q := range reqs {
			cs.Requests = append(cs.Requests, requestMeta{
				ID: q.ID, FolderID: q.FolderID,
				Name: q.Spec.Name, Method: q.Spec.Method, URL: q.Spec.URL,
			})
		}
		out.Collections = append(out.Collections, cs)
	}
	envs, err := s.DB.Environments()
	if err != nil {
		httpError(w, 500, err)
		return
	}
	if envs != nil {
		out.Environments = envs
	}
	presets, err := s.DB.Presets()
	if err != nil {
		httpError(w, 500, err)
		return
	}
	if presets != nil {
		out.Presets = presets
	}
	// Secret preset values never reach the browser.
	for pi := range out.Presets {
		for hi := range out.Presets[pi].Headers {
			if out.Presets[pi].Headers[hi].Secret {
				out.Presets[pi].Headers[hi].Value = ""
			}
		}
	}
	writeJSON(w, out)
}

// --- collections ---

func (s *Server) handleCollectionCreate(w http.ResponseWriter, r *http.Request) {
	c, err := decode[store.Collection](r)
	if err != nil {
		httpError(w, 400, err)
		return
	}
	if c.Name == "" {
		httpError(w, 422, fmt.Errorf("collection needs a name"))
		return
	}
	if err := s.DB.CreateCollection(c); err != nil {
		httpError(w, 500, err)
		return
	}
	writeJSON(w, c)
}

func (s *Server) handleCollectionUpdate(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		httpError(w, 400, err)
		return
	}
	c, err := decode[store.Collection](r)
	if err != nil {
		httpError(w, 400, err)
		return
	}
	c.ID = id
	if err := s.DB.UpdateCollection(c); err != nil {
		httpError(w, 500, err)
		return
	}
	writeJSON(w, c)
}

func (s *Server) handleCollectionDelete(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		httpError(w, 400, err)
		return
	}
	if err := s.DB.DeleteCollection(id); err != nil {
		httpError(w, 500, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// --- folders ---

func (s *Server) handleFolderCreate(w http.ResponseWriter, r *http.Request) {
	f, err := decode[store.Folder](r)
	if err != nil {
		httpError(w, 400, err)
		return
	}
	if f.Name == "" || f.CollectionID == 0 {
		httpError(w, 422, fmt.Errorf("folder needs a name and collectionId"))
		return
	}
	if err := s.DB.CreateFolder(f); err != nil {
		httpError(w, 500, err)
		return
	}
	writeJSON(w, f)
}

func (s *Server) handleFolderUpdate(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		httpError(w, 400, err)
		return
	}
	f, err := decode[store.Folder](r)
	if err != nil {
		httpError(w, 400, err)
		return
	}
	f.ID = id
	if err := s.DB.UpdateFolder(f); err != nil {
		httpError(w, 500, err)
		return
	}
	writeJSON(w, f)
}

func (s *Server) handleFolderDelete(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		httpError(w, 400, err)
		return
	}
	if err := s.DB.DeleteFolder(id); err != nil {
		httpError(w, 500, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// --- requests ---

func (s *Server) handleRequestCreate(w http.ResponseWriter, r *http.Request) {
	req, err := decode[store.Request](r)
	if err != nil {
		httpError(w, 400, err)
		return
	}
	if req.CollectionID == 0 {
		httpError(w, 422, fmt.Errorf("request needs a collectionId"))
		return
	}
	if req.Spec == nil {
		req.Spec = &dsl.Request{Name: "Untitled", Method: "GET", URL: "{{baseUrl}}/"}
	}
	if err := s.DB.CreateRequest(req); err != nil {
		httpError(w, 422, err)
		return
	}
	writeJSON(w, req)
}

func (s *Server) handleRequestGet(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		httpError(w, 400, err)
		return
	}
	req, err := s.DB.Request(id)
	if err != nil {
		httpError(w, 404, fmt.Errorf("request %d not found", id))
		return
	}
	writeJSON(w, req)
}

func (s *Server) handleRequestUpdate(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		httpError(w, 400, err)
		return
	}
	req, err := decode[store.Request](r)
	if err != nil {
		httpError(w, 400, err)
		return
	}
	existing, err := s.DB.Request(id)
	if err != nil {
		httpError(w, 404, fmt.Errorf("request %d not found", id))
		return
	}
	req.ID = id
	if req.CollectionID == 0 {
		req.CollectionID = existing.CollectionID
	}
	if err := s.DB.UpdateRequest(req); err != nil {
		httpError(w, 422, err)
		return
	}
	writeJSON(w, req)
}

func (s *Server) handleRequestDelete(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		httpError(w, 400, err)
		return
	}
	if err := s.DB.DeleteRequest(id); err != nil {
		httpError(w, 500, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) handleRequestStats(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		httpError(w, 400, err)
		return
	}
	stats, err := s.DB.RequestStats(id)
	if err != nil {
		httpError(w, 500, err)
		return
	}
	writeJSON(w, stats)
}

// --- environments ---

func (s *Server) handleEnvList(w http.ResponseWriter, r *http.Request) {
	envs, err := s.DB.Environments()
	if err != nil {
		httpError(w, 500, err)
		return
	}
	if envs == nil {
		envs = []store.Environment{}
	}
	writeJSON(w, envs)
}

func (s *Server) handleEnvPut(w http.ResponseWriter, r *http.Request) {
	e, err := decode[store.Environment](r)
	if err != nil {
		httpError(w, 400, err)
		return
	}
	e.Name = r.PathValue("name")
	if err := s.DB.UpsertEnvironment(e); err != nil {
		httpError(w, 422, err)
		return
	}
	writeJSON(w, e)
}

func (s *Server) handleEnvDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.DB.DeleteEnvironment(r.PathValue("name")); err != nil {
		httpError(w, 500, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// --- presets ---

func (s *Server) handlePresetList(w http.ResponseWriter, r *http.Request) {
	ps, err := s.DB.Presets()
	if err != nil {
		httpError(w, 500, err)
		return
	}
	if ps == nil {
		ps = []store.Preset{}
	}
	for pi := range ps {
		for hi := range ps[pi].Headers {
			if ps[pi].Headers[hi].Secret {
				ps[pi].Headers[hi].Value = ""
			}
		}
	}
	writeJSON(w, ps)
}

func (s *Server) handlePresetCreate(w http.ResponseWriter, r *http.Request) {
	p, err := decode[store.Preset](r)
	if err != nil {
		httpError(w, 400, err)
		return
	}
	if err := s.DB.CreatePreset(p); err != nil {
		httpError(w, 422, err)
		return
	}
	writeJSON(w, p)
}

func (s *Server) handlePresetUpdate(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		httpError(w, 400, err)
		return
	}
	p, err := decode[store.Preset](r)
	if err != nil {
		httpError(w, 400, err)
		return
	}
	p.ID = id
	// Empty values on secret headers mean "keep the stored value" so the
	// masked UI round-trip doesn't wipe secrets.
	if existing, err := s.DB.Presets(); err == nil {
		for _, ex := range existing {
			if ex.ID != id {
				continue
			}
			for hi := range p.Headers {
				if p.Headers[hi].Secret && p.Headers[hi].Value == "" {
					for _, eh := range ex.Headers {
						if eh.Key == p.Headers[hi].Key {
							p.Headers[hi].Value = eh.Value
						}
					}
				}
			}
		}
	}
	if err := s.DB.UpdatePreset(p); err != nil {
		httpError(w, 422, err)
		return
	}
	writeJSON(w, p)
}

func (s *Server) handlePresetDelete(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		httpError(w, 400, err)
		return
	}
	if err := s.DB.DeletePreset(id); err != nil {
		httpError(w, 500, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// --- history ---

func (s *Server) handleHistoryList(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	list, err := s.DB.History(limit)
	if err != nil {
		httpError(w, 500, err)
		return
	}
	if list == nil {
		list = []store.HistoryEntry{}
	}
	writeJSON(w, list)
}

func (s *Server) handleHistoryGet(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		httpError(w, 400, err)
		return
	}
	h, err := s.DB.HistoryEntry(id)
	if err != nil {
		httpError(w, 404, fmt.Errorf("history entry %d not found", id))
		return
	}
	writeJSON(w, map[string]any{
		"entry": h,
		"body":  string(h.RespBody),
	})
}
