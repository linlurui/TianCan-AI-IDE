package database

import (
	"database/sql"
	"fmt"
	"strings"
	"sync"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/lib/pq"
	_ "modernc.org/sqlite"
)

// Service manages database connections and exposes operations to the frontend.
type Service struct {
	mu    sync.RWMutex
	conns map[string]*connEntry
}

type connEntry struct {
	info ConnectionInfo
	db   *sql.DB
}

// ConnectionType enumerates supported database types.
type ConnectionType string

const (
	ConnSQLite   ConnectionType = "sqlite"
	ConnMySQL     ConnectionType = "mysql"
	ConnPostgres  ConnectionType = "postgres"
)

// ConnectionConfig holds parameters for a new connection.
type ConnectionConfig struct {
	ID       string         `json:"id"`
	Name     string         `json:"name"`
	Type     ConnectionType `json:"type"`
	Host     string         `json:"host"`
	Port     int            `json:"port"`
	User     string         `json:"user"`
	Password string         `json:"password"`
	Database string         `json:"database"`
	FilePath string         `json:"filePath"`
}

// ConnectionInfo is the public view of a connection (no credentials).
type ConnectionInfo struct {
	ID       string         `json:"id"`
	Name     string         `json:"name"`
	Type     ConnectionType `json:"type"`
	Database string         `json:"database"`
	Host     string         `json:"host"`
	Port     int            `json:"port"`
	FilePath string         `json:"filePath"`
	Connected bool          `json:"connected"`
}

// TableInfo represents a table or view name.
type TableInfo struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// ColumnInfo describes one column of a table.
type ColumnInfo struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Nullable bool   `json:"nullable"`
	Key      string `json:"key"`
	Default  string `json:"default"`
}

// QueryResult holds rows returned by a query.
type QueryResult struct {
	Columns []string        `json:"columns"`
	Rows    [][]interface{} `json:"rows"`
	RowCount int            `json:"rowCount"`
	Error   string          `json:"error"`
}

// NewService creates an empty database service.
func NewService() *Service {
	return &Service{conns: make(map[string]*connEntry)}
}

// Connect opens a connection using the given config, stores it, and returns its ID.
func (s *Service) Connect(cfg ConnectionConfig) (ConnectionInfo, error) {
	var dsn string
	var driverName string

	switch cfg.Type {
	case ConnSQLite:
		dsn = cfg.FilePath
		driverName = "sqlite"
	case ConnMySQL:
		port := cfg.Port
		if port == 0 {
			port = 3306
		}
		dsn = fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?charset=utf8mb4&parseTime=True",
			cfg.User, cfg.Password, cfg.Host, port, cfg.Database)
		driverName = "mysql"
	case ConnPostgres:
		port := cfg.Port
		if port == 0 {
			port = 5432
		}
		dsn = fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=disable",
			cfg.Host, port, cfg.User, cfg.Password, cfg.Database)
		driverName = "postgres"
	default:
		return ConnectionInfo{}, fmt.Errorf("unsupported database type: %s", cfg.Type)
	}

	db, err := sql.Open(driverName, dsn)
	if err != nil {
		return ConnectionInfo{}, fmt.Errorf("open: %w", err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return ConnectionInfo{}, fmt.Errorf("ping: %w", err)
	}

	if cfg.ID == "" {
		cfg.ID = fmt.Sprintf("%s-%s", cfg.Type, cfg.Name)
	}

	info := ConnectionInfo{
		ID:        cfg.ID,
		Name:      cfg.Name,
		Type:      cfg.Type,
		Database:  cfg.Database,
		Host:      cfg.Host,
		Port:      cfg.Port,
		FilePath:  cfg.FilePath,
		Connected: true,
	}

	s.mu.Lock()
	s.conns[cfg.ID] = &connEntry{info: info, db: db}
	s.mu.Unlock()

	return info, nil
}

// Disconnect closes and removes a connection by ID.
func (s *Service) Disconnect(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.conns[id]
	if !ok {
		return fmt.Errorf("connection not found: %s", id)
	}
	e.db.Close()
	delete(s.conns, id)
	return nil
}

// ListConnections returns all active connections.
func (s *Service) ListConnections() []ConnectionInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]ConnectionInfo, 0, len(s.conns))
	for _, e := range s.conns {
		out = append(out, e.info)
	}
	return out
}

// ListTables returns the tables (and views) in the connected database.
func (s *Service) ListTables(connID string) ([]TableInfo, error) {
	e, err := s.getConn(connID)
	if err != nil {
		return nil, err
	}

	var query string
	switch e.info.Type {
	case ConnSQLite:
		query = `SELECT name, type FROM sqlite_master WHERE type IN ('table','view') AND name NOT LIKE 'sqlite_%' ORDER BY name`
	case ConnMySQL:
		query = `SELECT TABLE_NAME, TABLE_TYPE FROM information_schema.TABLES WHERE TABLE_SCHEMA = DATABASE() ORDER BY TABLE_NAME`
	case ConnPostgres:
		query = `SELECT table_name, table_type FROM information_schema.tables WHERE table_schema = 'public' ORDER BY table_name`
	}

	rows, err := e.db.Query(query)
	if err != nil {
		return nil, fmt.Errorf("list tables: %w", err)
	}
	defer rows.Close()

	var tables []TableInfo
	for rows.Next() {
		var name, typ string
		if err := rows.Scan(&name, &typ); err != nil {
			continue
		}
		tables = append(tables, TableInfo{Name: name, Type: strings.ToLower(typ)})
	}
	return tables, nil
}

// DescribeTable returns column information for the given table.
func (s *Service) DescribeTable(connID, tableName string) ([]ColumnInfo, error) {
	e, err := s.getConn(connID)
	if err != nil {
		return nil, err
	}

	var cols []ColumnInfo

	switch e.info.Type {
	case ConnSQLite:
		rows, err := e.db.Query(fmt.Sprintf("PRAGMA table_info(%q)", tableName))
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var cid int
			var name, typ string
			var notNull int
			var dflt sql.NullString
			var pk int
			if err := rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk); err != nil {
				continue
			}
			key := ""
			if pk > 0 {
				key = "PRI"
			}
			cols = append(cols, ColumnInfo{Name: name, Type: typ, Nullable: notNull == 0, Key: key, Default: dflt.String})
		}
	case ConnMySQL:
		rows, err := e.db.Query(
			`SELECT COLUMN_NAME, COLUMN_TYPE, IS_NULLABLE, COLUMN_KEY, COLUMN_DEFAULT FROM information_schema.COLUMNS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME=? ORDER BY ORDINAL_POSITION`,
			tableName)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var name, typ, nullable, key string
			var dflt sql.NullString
			if err := rows.Scan(&name, &typ, &nullable, &key, &dflt); err != nil {
				continue
			}
			cols = append(cols, ColumnInfo{Name: name, Type: typ, Nullable: nullable == "YES", Key: key, Default: dflt.String})
		}
	case ConnPostgres:
		rows, err := e.db.Query(
			`SELECT column_name, data_type, is_nullable, column_default FROM information_schema.columns WHERE table_schema='public' AND table_name=$1 ORDER BY ordinal_position`,
			tableName)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var name, typ, nullable string
			var dflt sql.NullString
			if err := rows.Scan(&name, &typ, &nullable, &dflt); err != nil {
				continue
			}
			cols = append(cols, ColumnInfo{Name: name, Type: typ, Nullable: nullable == "YES", Default: dflt.String})
		}
	}
	return cols, nil
}

// RunQuery executes a SQL query and returns up to maxRows rows.
func (s *Service) RunQuery(connID, query string, maxRows int) QueryResult {
	e, err := s.getConn(connID)
	if err != nil {
		return QueryResult{Error: err.Error()}
	}
	if maxRows <= 0 {
		maxRows = 500
	}

	rows, err := e.db.Query(query)
	if err != nil {
		return QueryResult{Error: err.Error()}
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return QueryResult{Error: err.Error()}
	}

	var result QueryResult
	result.Columns = cols

	for rows.Next() {
		if result.RowCount >= maxRows {
			break
		}
		vals := make([]interface{}, len(cols))
		ptrs := make([]interface{}, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			continue
		}
		row := make([]interface{}, len(cols))
		for i, v := range vals {
			switch b := v.(type) {
			case []byte:
				row[i] = string(b)
			default:
				row[i] = v
			}
		}
		result.Rows = append(result.Rows, row)
		result.RowCount++
	}
	return result
}

func (s *Service) getConn(id string) (*connEntry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.conns[id]
	if !ok {
		return nil, fmt.Errorf("connection not found: %s", id)
	}
	return e, nil
}
