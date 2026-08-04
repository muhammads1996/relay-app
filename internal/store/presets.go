package store

import (
	"fmt"
	"time"
)

// PresetHeader is one header in a reusable preset. Secret headers are
// masked in every display surface and excluded from script exports.
type PresetHeader struct {
	Key         string `json:"key"`
	Value       string `json:"value"`
	Secret      bool   `json:"secret"`
	Description string `json:"description,omitempty"`
}

// Attachment binds a preset to a collection or a folder (exactly one set).
type Attachment struct {
	CollectionID *int64 `json:"collectionId,omitempty"`
	FolderID     *int64 `json:"folderId,omitempty"`
}

// Preset is a named, reusable header set (PRD §5.2).
type Preset struct {
	ID          int64          `json:"id"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Headers     []PresetHeader `json:"headers"`
	Attachments []Attachment   `json:"attachments"`
}

func (s *Store) CreatePreset(p *Preset) error {
	if p.Name == "" {
		return fmt.Errorf("preset needs a name")
	}
	res, err := s.db.Exec(`INSERT INTO presets (name, description, headers, updated_at) VALUES (?, ?, ?, ?)`,
		p.Name, p.Description, j(p.Headers), time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	p.ID, err = res.LastInsertId()
	if err != nil {
		return err
	}
	return s.saveAttachments(p)
}

func (s *Store) UpdatePreset(p *Preset) error {
	_, err := s.db.Exec(`UPDATE presets SET name = ?, description = ?, headers = ?, updated_at = ? WHERE id = ?`,
		p.Name, p.Description, j(p.Headers), time.Now().UTC().Format(time.RFC3339Nano), p.ID)
	if err != nil {
		return err
	}
	return s.saveAttachments(p)
}

func (s *Store) DeletePreset(id int64) error {
	_, err := s.db.Exec(`DELETE FROM presets WHERE id = ?`, id)
	return err
}

func (s *Store) saveAttachments(p *Preset) error {
	if _, err := s.db.Exec(`DELETE FROM preset_attachments WHERE preset_id = ?`, p.ID); err != nil {
		return err
	}
	for _, a := range p.Attachments {
		if a.CollectionID == nil && a.FolderID == nil {
			continue
		}
		if _, err := s.db.Exec(`INSERT INTO preset_attachments (preset_id, collection_id, folder_id) VALUES (?, ?, ?)`,
			p.ID, a.CollectionID, a.FolderID); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) Presets() ([]Preset, error) {
	rows, err := s.db.Query(`SELECT id, name, description, headers FROM presets ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Preset
	for rows.Next() {
		var p Preset
		var h string
		if err := rows.Scan(&p.ID, &p.Name, &p.Description, &h); err != nil {
			return nil, err
		}
		p.Headers = unj[[]PresetHeader](h)
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		if out[i].Attachments, err = s.attachments(out[i].ID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *Store) attachments(presetID int64) ([]Attachment, error) {
	rows, err := s.db.Query(`SELECT collection_id, folder_id FROM preset_attachments WHERE preset_id = ?`, presetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Attachment
	for rows.Next() {
		var a Attachment
		if err := rows.Scan(&a.CollectionID, &a.FolderID); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// PresetHeadersFor returns the merged preset headers that apply to a
// request in the given collection/folder, collection-level first (so
// folder-level presets override), plus the set of values that must be
// masked because their preset header is secret-flagged.
func (s *Store) PresetHeadersFor(collectionID int64, folderID *int64) (headers map[string]string, secretValues []string, err error) {
	presets, err := s.Presets()
	if err != nil {
		return nil, nil, err
	}
	headers = map[string]string{}
	apply := func(match func(Attachment) bool) {
		for _, p := range presets {
			attached := false
			for _, a := range p.Attachments {
				if match(a) {
					attached = true
					break
				}
			}
			if !attached {
				continue
			}
			for _, h := range p.Headers {
				headers[h.Key] = h.Value
				if h.Secret && h.Value != "" {
					secretValues = append(secretValues, h.Value)
				}
			}
		}
	}
	apply(func(a Attachment) bool { return a.CollectionID != nil && *a.CollectionID == collectionID })
	if folderID != nil {
		apply(func(a Attachment) bool { return a.FolderID != nil && *a.FolderID == *folderID })
	}
	return headers, secretValues, nil
}
