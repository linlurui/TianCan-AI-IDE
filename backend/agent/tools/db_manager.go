package tools

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/rocky233/tiancan-ai-ide/backend/agent/types"
)

type DBType string

const (
	DBSQLite     DBType = "sqlite"
	DBMySQL      DBType = "mysql"
	DBPostgreSQL DBType = "postgresql"
	DBMongoDB    DBType = "mongodb"
	DBRedis      DBType = "redis"
	DBClickHouse DBType = "clickhouse"
	DBMariaDB    DBType = "mariadb"
	DBOracle     DBType = "oracle"
)

type DBConnectionInfo struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Type      DBType `json:"type"`
	Host      string `json:"host,omitempty"`
	Port      int    `json:"port,omitempty"`
	Database  string `json:"database,omitempty"`
	FilePath  string `json:"filePath,omitempty"`
	Connected bool   `json:"connected"`
}
type dbConnEntry struct {
	Driver string
	DSN    string
	DB     *sql.DB
	Info   DBConnectionInfo
}
type dbConnectionManager struct {
	mu    sync.RWMutex
	conns map[string]*dbConnEntry
}

var dbMgr = &dbConnectionManager{conns: make(map[string]*dbConnEntry)}

func SupportedDBTypes() []DBType {
	return []DBType{DBSQLite, DBMySQL, DBPostgreSQL, DBMongoDB, DBRedis, DBClickHouse, DBMariaDB, DBOracle}
}

func (m *dbConnectionManager) connect(id, name string, dbType DBType, cfg map[string]interface{}) (DBConnectionInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if id == "" {
		id = fmt.Sprintf("db-%d", time.Now().UnixNano())
	}
	info := DBConnectionInfo{ID: id, Name: name, Type: dbType}
	switch dbType {
	case DBSQLite:
		p, _ := cfg["filePath"].(string)
		if p == "" {
			return info, fmt.Errorf("sqlite requires filePath")
		}
		info.FilePath = p
		db, err := sql.Open("sqlite", p)
		if err != nil {
			return info, err
		}
		if err := db.Ping(); err != nil {
			db.Close()
			return info, err
		}
		info.Connected = true
		m.conns[id] = &dbConnEntry{Driver: "sqlite", DSN: p, DB: db, Info: info}
	case DBMySQL, DBMariaDB:
		h, _ := cfg["host"].(string)
		if h == "" {
			h = "127.0.0.1"
		}
		pt := toInt(cfg["port"], 3306)
		u, _ := cfg["user"].(string)
		pw, _ := cfg["password"].(string)
		dn, _ := cfg["database"].(string)
		dsn := fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?charset=utf8mb4&parseTime=True", u, pw, h, pt, dn)
		info.Host, info.Port, info.Database = h, pt, dn
		db, err := sql.Open("mysql", dsn)
		if err != nil {
			return info, err
		}
		if err := db.Ping(); err != nil {
			db.Close()
			return info, err
		}
		info.Connected = true
		m.conns[id] = &dbConnEntry{Driver: "mysql", DSN: dsn, DB: db, Info: info}
	case DBPostgreSQL:
		h, _ := cfg["host"].(string)
		if h == "" {
			h = "127.0.0.1"
		}
		pt := toInt(cfg["port"], 5432)
		u, _ := cfg["user"].(string)
		pw, _ := cfg["password"].(string)
		dn, _ := cfg["database"].(string)
		ss, _ := cfg["sslmode"].(string)
		if ss == "" {
			ss = "disable"
		}
		dsn := fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=%s", h, pt, u, pw, dn, ss)
		info.Host, info.Port, info.Database = h, pt, dn
		db, err := sql.Open("postgres", dsn)
		if err != nil {
			return info, err
		}
		if err := db.Ping(); err != nil {
			db.Close()
			return info, err
		}
		info.Connected = true
		m.conns[id] = &dbConnEntry{Driver: "postgres", DSN: dsn, DB: db, Info: info}
	case DBClickHouse:
		h, _ := cfg["host"].(string)
		if h == "" {
			h = "127.0.0.1"
		}
		pt := toInt(cfg["port"], 9000)
		u, _ := cfg["user"].(string)
		pw, _ := cfg["password"].(string)
		dn, _ := cfg["database"].(string)
		dsn := fmt.Sprintf("tcp://%s:%d?username=%s&password=%s&database=%s", h, pt, u, pw, dn)
		info.Host, info.Port, info.Database = h, pt, dn
		db, err := sql.Open("clickhouse", dsn)
		if err != nil {
			return info, err
		}
		if err := db.Ping(); err != nil {
			db.Close()
			return info, err
		}
		info.Connected = true
		m.conns[id] = &dbConnEntry{Driver: "clickhouse", DSN: dsn, DB: db, Info: info}
	case DBMongoDB, DBRedis, DBOracle:
		h, _ := cfg["host"].(string)
		if h == "" {
			h = "127.0.0.1"
		}
		pt := toInt(cfg["port"], 0)
		dn, _ := cfg["database"].(string)
		switch dbType {
		case DBMongoDB:
			if pt == 0 {
				pt = 27017
			}
		case DBRedis:
			if pt == 0 {
				pt = 6379
			}
		case DBOracle:
			if pt == 0 {
				pt = 1521
			}
		}
		info.Host, info.Port, info.Database = h, pt, dn
		info.Connected = true
		m.conns[id] = &dbConnEntry{Driver: string(dbType), DSN: fmt.Sprintf("%s:%d/%s", h, pt, dn), Info: info}
	default:
		return info, fmt.Errorf("unsupported: %s", dbType)
	}
	return info, nil
}

func (m *dbConnectionManager) disconnect(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.conns[id]
	if !ok {
		return fmt.Errorf("not found: %s", id)
	}
	if e.DB != nil {
		e.DB.Close()
	}
	delete(m.conns, id)
	return nil
}
func (m *dbConnectionManager) getConn(id string) (*dbConnEntry, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	e, ok := m.conns[id]
	if !ok {
		return nil, fmt.Errorf("not found: %s", id)
	}
	return e, nil
}
func (m *dbConnectionManager) listConnections() []DBConnectionInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var l []DBConnectionInfo
	for _, e := range m.conns {
		l = append(l, e.Info)
	}
	return l
}

func executeMongoQuery(e *dbConnEntry, query string, maxRows int) (types.ToolResult, error) {
	cmd := fmt.Sprintf("mongosh 'mongodb://%s' --quiet --eval '%s'", e.DSN, strings.ReplaceAll(query, "'", "'\\''"))
	return runCLICommand(cmd, "mongodb")
}
func executeRedisCommand(e *dbConnEntry, cmd string) (types.ToolResult, error) {
	c := fmt.Sprintf("redis-cli -h %s -p %d %s", e.Info.Host, e.Info.Port, cmd)
	return runCLICommand(c, "redis")
}
func runCLICommand(cmdStr, dbType string) (types.ToolResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", cmdStr)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return types.ToolResult{Content: fmt.Sprintf("%s error: %v\n%s", dbType, err, string(out)), Success: false}, nil
	}
	return types.ToolResult{Content: string(out), Success: true}, nil
}

func dbResultJSON(v interface{}) string {
	d, _ := json.MarshalIndent(v, "", "  ")
	return string(d)
}
