package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	_ "modernc.org/sqlite"

	"github.com/sudesh856/LoadForge/internal/dashboard"
)

type RunRecord struct {
	ID        int64
	Name      string
	Timestamp time.Time
	Config    string
	Summary   string
}

// Store manages the SQLite database
type Store struct {
	db *sql.DB
}

// New creates a new Store instance with SQLite DB
func New(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("cannot open sqlite db: %w", err)
	}

	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS runs (
		id        INTEGER PRIMARY KEY AUTOINCREMENT,
		name      TEXT,
		timestamp INTEGER,
		config    TEXT,
		summary   TEXT
	)`)
	if err != nil {
		return nil, fmt.Errorf("cannot create runs table: %w", err)
	}

	return &Store{db: db}, nil
}

// SaveRun inserts a new run into the DB
func (s *Store) SaveRun(name string, config interface{}, summary interface{}) (int64, error) {
	configJSON, err := json.Marshal(config)
	if err != nil {
		return 0, err
	}

	summaryJSON, err := json.Marshal(summary)
	if err != nil {
		return 0, err
	}

	res, err := s.db.Exec(
		`INSERT INTO runs (name, timestamp, config, summary) VALUES (?, ?, ?, ?)`,
		name,
		time.Now().Unix(),
		string(configJSON),
		string(summaryJSON),
	)
	if err != nil {
		return 0, err
	}

	return res.LastInsertId()
}

func (s *Store) GetRun(id int64) (*RunRecord, error) {
	row := s.db.QueryRow(`SELECT id, name, timestamp, config, summary FROM runs WHERE id = ?`, id)

	var r RunRecord
	var ts int64
	if err := row.Scan(&r.ID, &r.Name, &ts, &r.Config, &r.Summary); err != nil {
		return nil, fmt.Errorf("run %d not found", id)
	}

	r.Timestamp = time.Unix(ts, 0)
	return &r, nil
}


func (s *Store) ListRuns() ([]RunRecord, error) {
	rows, err := s.db.Query(`SELECT id, name, timestamp, config, summary FROM runs ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []RunRecord
	for rows.Next() {
		var r RunRecord
		var ts int64
		if err := rows.Scan(&r.ID, &r.Name, &ts, &r.Config, &r.Summary); err != nil {
			continue
		}
		r.Timestamp = time.Unix(ts, 0)
		records = append(records, r)
	}

	return records, nil
}

func (s *Store) ListRunsRaw() ([]dashboard.RunRecord, error) {
	rows, err := s.db.Query(`SELECT id, name, timestamp, summary FROM runs ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []dashboard.RunRecord
	for rows.Next() {
		var r dashboard.RunRecord
		if err := rows.Scan(&r.ID, &r.Name, &r.Timestamp, &r.Summary); err != nil {
			continue
		}
		records = append(records, r)
	}

	return records, nil
}

func (s *Store) GetRunRaw(id int64) (*dashboard.RunRecord, error) {
	row := s.db.QueryRow(`SELECT id, name, timestamp, summary FROM runs WHERE id = ?`, id)

	var r dashboard.RunRecord
	if err := row.Scan(&r.ID, &r.Name, &r.Timestamp, &r.Summary); err != nil {
		return nil, fmt.Errorf("run %d not found", id)
	}

	return &r, nil
}

func (s *Store) Close() {
	if s.db != nil {
		s.db.Close()
	}
}