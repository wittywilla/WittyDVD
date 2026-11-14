// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	_ "modernc.org/sqlite"
)

// The names people gave a disc's titles and chapters are this module's own: a database in its data
// folder (WITTYMODULE_DATA), starting empty (module API 5: no import from the server's old column).

// DataFormat is this database's form, as module.json's dataFormat says.
const DataFormat = 1

// Store is the module's database.
type Store struct {
	db *sql.DB
}

// File is the database's name in the module's data folder.
const File = "labels.db"

// Open opens (or makes) the database in a folder.
func Open(dir string) (*Store, error) {
	if dir == "" {
		dir = os.TempDir()
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, File)+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS labels (
	   item INTEGER PRIMARY KEY, titles TEXT NOT NULL DEFAULT '{}', chapters TEXT NOT NULL DEFAULT '{}');
	 PRAGMA user_version = ` + strconv.Itoa(DataFormat)); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

// Close closes it.
func (s *Store) Close() error { return s.db.Close() }

// Labels are names given to a disc's titles and chapters, keyed by title number and chapter number
// (as strings, for JSON).
type Labels struct {
	Titles   map[string]string            `json:"titles"`
	Chapters map[string]map[string]string `json:"chapters"`
}

// maxLabel is the longest name kept.
const maxLabel = 200

// Clean drops empty names and trims the rest.
func (l Labels) Clean() Labels {
	out := Labels{Titles: map[string]string{}, Chapters: map[string]map[string]string{}}
	clip := func(v string) string {
		v = strings.TrimSpace(v)
		if r := []rune(v); len(r) > maxLabel {
			v = string(r[:maxLabel])
		}
		return v
	}
	num := func(k string) bool { n, err := strconv.Atoi(k); return err == nil && n >= 0 && n < 100000 }
	for k, v := range l.Titles {
		if v = clip(v); v != "" && num(k) {
			out.Titles[k] = v
		}
	}
	for t, m := range l.Chapters {
		if !num(t) {
			continue
		}
		for k, v := range m {
			if v = clip(v); v != "" && num(k) {
				if out.Chapters[t] == nil {
					out.Chapters[t] = map[string]string{}
				}
				out.Chapters[t][k] = v
			}
		}
	}
	return out
}

// Get is a disc's names (never nil maps).
func (s *Store) Get(ctx context.Context, item int64) Labels {
	var t, c string
	var l Labels
	if s != nil && s.db.QueryRowContext(ctx, `SELECT titles, chapters FROM labels WHERE item = ?`, item).Scan(&t, &c) == nil {
		_ = json.Unmarshal([]byte(t), &l.Titles)
		_ = json.Unmarshal([]byte(c), &l.Chapters)
	}
	return l.Clean()
}

// Set replaces a disc's names.
func (s *Store) Set(ctx context.Context, item int64, l Labels) error {
	l = l.Clean()
	t, _ := json.Marshal(l.Titles)
	c, _ := json.Marshal(l.Chapters)
	_, err := s.db.ExecContext(ctx, `INSERT INTO labels(item, titles, chapters) VALUES (?, ?, ?)
	   ON CONFLICT(item) DO UPDATE SET titles = excluded.titles, chapters = excluded.chapters`, item, string(t), string(c))
	return err
}

// Forget drops a disc's names, when its item is gone.
func (s *Store) Forget(ctx context.Context, item int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM labels WHERE item = ?`, item)
	return err
}

// title is a title's name, or "".
func (l Labels) title(ttn int) string { return l.Titles[strconv.Itoa(ttn)] }

// apply puts the names on a disc's page.
func (l Labels) apply(p *Page) {
	one := func(t *Title) {
		t.Label = l.title(t.TTN)
		names := l.Chapters[strconv.Itoa(t.TTN)]
		for i := range t.Chapters {
			t.Chapters[i].Label = names[strconv.Itoa(t.Chapters[i].N)]
		}
	}
	if p.Main != nil {
		one(p.Main)
	}
	for i := range p.Extras {
		one(&p.Extras[i])
	}
	if p.Extras == nil {
		p.Extras = []Title{}
	}
	if p.Menus == nil {
		p.Menus = []Menu{}
	}
}
