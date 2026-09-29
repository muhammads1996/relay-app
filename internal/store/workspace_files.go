package store

import (
	"database/sql"
	"fmt"
)

type WorkspaceFile struct {
	Kind     string
	FileID   string
	SQLiteID int64
	Path     string
	Hash     string
}

// WorkspaceRequestIndex is one canonical request and its derived index row.
// ExistingID is zero for a new request; Update controls whether an existing
// SQLite request must be refreshed from canonical files. BindFile is true for
// new or changed workspace_file_index rows.
type WorkspaceRequestIndex struct {
	Request    Request
	File       WorkspaceFile
	ExistingID int64
	Update     bool
	BindFile   bool
}

func (s *Store) ensureWorkspaceFileIndex() error {
	_, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS workspace_file_index (kind TEXT NOT NULL,file_id TEXT NOT NULL,sqlite_id INTEGER NOT NULL,path TEXT NOT NULL,content_hash TEXT NOT NULL DEFAULT '',PRIMARY KEY(kind,file_id),UNIQUE(kind,sqlite_id))`)
	return err
}

func (s *Store) WorkspaceFileByStableID(kind, fileID string) (WorkspaceFile, error) {
	if err := s.ensureWorkspaceFileIndex(); err != nil {
		return WorkspaceFile{}, err
	}
	var v WorkspaceFile
	err := s.db.QueryRow(`SELECT kind,file_id,sqlite_id,path,content_hash FROM workspace_file_index WHERE kind=? AND file_id=?`, kind, fileID).Scan(&v.Kind, &v.FileID, &v.SQLiteID, &v.Path, &v.Hash)
	return v, err
}
func (s *Store) WorkspaceFileBySQLiteID(kind string, id int64) (WorkspaceFile, error) {
	if err := s.ensureWorkspaceFileIndex(); err != nil {
		return WorkspaceFile{}, err
	}
	var v WorkspaceFile
	err := s.db.QueryRow(`SELECT kind,file_id,sqlite_id,path,content_hash FROM workspace_file_index WHERE kind=? AND sqlite_id=?`, kind, id).Scan(&v.Kind, &v.FileID, &v.SQLiteID, &v.Path, &v.Hash)
	return v, err
}
func (s *Store) WorkspaceFiles(kind string) ([]WorkspaceFile, error) {
	if err := s.ensureWorkspaceFileIndex(); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(`SELECT kind,file_id,sqlite_id,path,content_hash FROM workspace_file_index WHERE kind=? ORDER BY sqlite_id`, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WorkspaceFile
	for rows.Next() {
		var v WorkspaceFile
		if err := rows.Scan(&v.Kind, &v.FileID, &v.SQLiteID, &v.Path, &v.Hash); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *Store) BindWorkspaceFile(v WorkspaceFile) error {
	if v.Kind == "" || v.FileID == "" || v.SQLiteID <= 0 || v.Path == "" {
		return fmt.Errorf("workspace file identity is incomplete")
	}
	if err := s.ensureWorkspaceFileIndex(); err != nil {
		return err
	}
	_, err := s.db.Exec(`INSERT INTO workspace_file_index(kind,file_id,sqlite_id,path,content_hash) VALUES(?,?,?,?,?) ON CONFLICT(kind,file_id) DO UPDATE SET sqlite_id=excluded.sqlite_id,path=excluded.path,content_hash=excluded.content_hash`, v.Kind, v.FileID, v.SQLiteID, v.Path, v.Hash)
	return err
}

// ReconcileWorkspaceRequests applies request/index changes in one SQLite
// transaction. This is used when rebuilding the disposable index from a large
// canonical workspace; file hashes are still checked by the workspace loader.
func (s *Store) ReconcileWorkspaceRequests(items []WorkspaceRequestIndex) ([]int64, error) {
	if err := s.ensureWorkspaceFileIndex(); err != nil {
		return nil, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	insertRequest, err := tx.Prepare(`INSERT INTO requests (collection_id, folder_id, name, method, url, spec, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return nil, err
	}
	defer insertRequest.Close()
	updateRequest, err := tx.Prepare(`UPDATE requests SET collection_id=?,folder_id=?,name=?,method=?,url=?,spec=?,updated_at=? WHERE id=?`)
	if err != nil {
		return nil, err
	}
	defer updateRequest.Close()
	bindFile, err := tx.Prepare(`INSERT INTO workspace_file_index(kind,file_id,sqlite_id,path,content_hash) VALUES(?,?,?,?,?) ON CONFLICT(kind,file_id) DO UPDATE SET sqlite_id=excluded.sqlite_id,path=excluded.path,content_hash=excluded.content_hash`)
	if err != nil {
		return nil, err
	}
	defer bindFile.Close()
	ids := make([]int64, len(items))
	for i := range items {
		item := &items[i]
		if item.Request.Spec == nil || item.Request.CollectionID <= 0 {
			return nil, fmt.Errorf("workspace request index %d has incomplete request data", i)
		}
		if item.ExistingID > 0 && !item.Update && !item.BindFile {
			ids[i] = item.ExistingID
			continue
		}
		if err = normalizeSpec(&item.Request); err != nil {
			return nil, fmt.Errorf("workspace request index %d: %w", i, err)
		}
		values := []any{item.Request.CollectionID, item.Request.FolderID, item.Request.Spec.Name, item.Request.Spec.Method, item.Request.Spec.URL, j(item.Request.Spec), now()}
		id := item.ExistingID
		if id == 0 {
			var result sql.Result
			result, err = insertRequest.Exec(values...)
			if err == nil {
				id, err = result.LastInsertId()
			}
		} else if item.Update {
			var result sql.Result
			result, err = updateRequest.Exec(append(values, id)...)
			if err == nil {
				var affected int64
				affected, err = result.RowsAffected()
				if err == nil && affected == 0 {
					err = ErrRequestNotFound
				}
			}
		}
		if err != nil {
			return nil, err
		}
		if item.BindFile {
			if item.File.Kind != "request" || item.File.FileID == "" || item.File.Path == "" {
				return nil, fmt.Errorf("workspace request index %d has incomplete file identity", i)
			}
			if _, err = bindFile.Exec(item.File.Kind, item.File.FileID, id, item.File.Path, item.File.Hash); err != nil {
				return nil, err
			}
		}
		ids[i] = id
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return ids, nil
}
func (s *Store) DeleteWorkspaceFile(kind string, id int64) error {
	if err := s.ensureWorkspaceFileIndex(); err != nil {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM workspace_file_index WHERE kind=? AND sqlite_id=?`, kind, id)
	return err
}
