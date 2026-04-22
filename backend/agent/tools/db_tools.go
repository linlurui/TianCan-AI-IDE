package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/rocky233/tiancan-ai-ide/backend/agent/types"
)

// ── DBConnectTool ───────────────────────────────────────────────

type DBConnectTool struct{}

func (t *DBConnectTool) Name() string        { return "db_connect" }
func (t *DBConnectTool) IsReadOnly() bool     { return false }
func (t *DBConnectTool) IsConcurrencySafe() bool { return false }
func (t *DBConnectTool) Description() string {
	return "Connect to a database (sqlite/mysql/postgresql/mongodb/redis/clickhouse/mariadb/oracle). Args: type, host, port, user, password, database, filePath, sslmode"
}
func (t *DBConnectTool) InputSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"id":       map[string]interface{}{"type": "string"},
			"name":     map[string]interface{}{"type": "string"},
			"type":     map[string]interface{}{"type": "string", "description": "sqlite/mysql/postgresql/mongodb/redis/clickhouse/mariadb/oracle"},
			"host":     map[string]interface{}{"type": "string"},
			"port":     map[string]interface{}{"type": "integer"},
			"user":     map[string]interface{}{"type": "string"},
			"password": map[string]interface{}{"type": "string"},
			"database": map[string]interface{}{"type": "string"},
			"filePath": map[string]interface{}{"type": "string"},
			"sslmode":  map[string]interface{}{"type": "string"},
		},
		"required": []string{"type"},
	}
}
func (t *DBConnectTool) Execute(ctx context.Context, args map[string]interface{}) (types.ToolResult, error) {
	id, _ := args["id"].(string)
	nm, _ := args["name"].(string)
	tp, _ := args["type"].(string)
	if nm == "" {
		nm = tp
	}
	info, err := dbMgr.connect(id, nm, DBType(tp), args)
	if err != nil {
		return types.ToolResult{Content: fmt.Sprintf("Failed: %v", err), Success: false}, nil
	}
	d, _ := json.MarshalIndent(info, "", "  ")
	return types.ToolResult{Content: "Connected:\n" + string(d), Success: true}, nil
}

// ── DBDisconnectTool ────────────────────────────────────────────

type DBDisconnectTool struct{}

func (t *DBDisconnectTool) Name() string        { return "db_disconnect" }
func (t *DBDisconnectTool) IsReadOnly() bool     { return false }
func (t *DBDisconnectTool) IsConcurrencySafe() bool { return false }
func (t *DBDisconnectTool) Description() string { return "Disconnect from a database. Args: id" }
func (t *DBDisconnectTool) InputSchema() map[string]interface{} {
	return map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{"id": map[string]interface{}{"type": "string"}},
		"required":   []string{"id"},
	}
}
func (t *DBDisconnectTool) Execute(ctx context.Context, args map[string]interface{}) (types.ToolResult, error) {
	id, _ := args["id"].(string)
	if err := dbMgr.disconnect(id); err != nil {
		return types.ToolResult{Content: err.Error(), Success: false}, nil
	}
	return types.ToolResult{Content: "Disconnected: " + id, Success: true}, nil
}

// ── DBListConnectionsTool ───────────────────────────────────────

type DBListConnectionsTool struct{}

func (t *DBListConnectionsTool) Name() string        { return "db_list_connections" }
func (t *DBListConnectionsTool) IsReadOnly() bool     { return true }
func (t *DBListConnectionsTool) IsConcurrencySafe() bool { return true }
func (t *DBListConnectionsTool) Description() string { return "List all active database connections" }
func (t *DBListConnectionsTool) InputSchema() map[string]interface{} {
	return map[string]interface{}{"type": "object", "properties": map[string]interface{}{}}
}
func (t *DBListConnectionsTool) Execute(ctx context.Context, args map[string]interface{}) (types.ToolResult, error) {
	cs := dbMgr.listConnections()
	if len(cs) == 0 {
		return types.ToolResult{Content: "No active connections", Success: true}, nil
	}
	d, _ := json.MarshalIndent(cs, "", "  ")
	return types.ToolResult{Content: string(d), Success: true}, nil
}

// ── DBQueryTool ────────────────────────────────────────────────

type DBQueryTool struct{}

func (t *DBQueryTool) Name() string        { return "db_query" }
func (t *DBQueryTool) IsReadOnly() bool     { return true }
func (t *DBQueryTool) IsConcurrencySafe() bool { return true }
func (t *DBQueryTool) Description() string {
	return "Execute SQL SELECT query. Args: id (connection ID), query, maxRows (default 500)"
}
func (t *DBQueryTool) InputSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"id":      map[string]interface{}{"type": "string"},
			"query":   map[string]interface{}{"type": "string"},
			"maxRows": map[string]interface{}{"type": "integer"},
		},
		"required": []string{"id", "query"},
	}
}
func (t *DBQueryTool) Execute(ctx context.Context, args map[string]interface{}) (types.ToolResult, error) {
	id, _ := args["id"].(string)
	q, _ := args["query"].(string)
	mx := toInt(args["maxRows"], 500)

	e, err := dbMgr.getConn(id)
	if err != nil {
		return types.ToolResult{Content: err.Error(), Success: false}, nil
	}

	// NoSQL CLI dispatch
	switch DBType(e.Info.Type) {
	case DBMongoDB:
		return executeMongoQuery(e, q, mx)
	case DBRedis:
		return executeRedisCommand(e, q)
	}

	if e.DB == nil {
		return types.ToolResult{Content: "No SQL driver for this connection", Success: false}, nil
	}

	// Safety: only allow read queries
	u := strings.ToUpper(strings.TrimSpace(q))
	if !strings.HasPrefix(u, "SELECT") && !strings.HasPrefix(u, "PRAGMA") &&
		!strings.HasPrefix(u, "EXPLAIN") && !strings.HasPrefix(u, "SHOW") &&
		!strings.HasPrefix(u, "DESCRIBE") {
		return types.ToolResult{Content: "Only SELECT/SHOW/DESCRIBE allowed. Use db_execute for DML.", Success: false}, nil
	}

	rows, err := e.DB.QueryContext(ctx, q)
	if err != nil {
		return types.ToolResult{Content: fmt.Sprintf("Error: %v", err), Success: false}, nil
	}
	defer rows.Close()

	cols, _ := rows.Columns()
	type QR struct {
		Columns  []string        `json:"columns"`
		Rows     [][]interface{} `json:"rows"`
		RowCount int             `json:"rowCount"`
	}
	r := QR{Columns: cols}

	for rows.Next() {
		if r.RowCount >= mx {
			break
		}
		vals := make([]interface{}, len(cols))
		ptrs := make([]interface{}, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if rows.Scan(ptrs...) != nil {
			continue
		}
		row := make([]interface{}, len(cols))
		for i, v := range vals {
			switch b := v.(type) {
			case []byte:
				row[i] = string(b)
			case time.Time:
				row[i] = b.Format(time.RFC3339)
			default:
				row[i] = v
			}
		}
		r.Rows = append(r.Rows, row)
		r.RowCount++
	}
	d, _ := json.MarshalIndent(r, "", "  ")
	return types.ToolResult{Content: string(d), Success: true}, nil
}

// ── DBExecuteTool ──────────────────────────────────────────────

type DBExecuteTool struct{}

func (t *DBExecuteTool) Name() string        { return "db_execute" }
func (t *DBExecuteTool) IsReadOnly() bool     { return false }
func (t *DBExecuteTool) IsConcurrencySafe() bool { return false }
func (t *DBExecuteTool) Description() string {
	return "Execute SQL DML/DDL (INSERT/UPDATE/DELETE/CREATE/ALTER/DROP). Args: id, query"
}
func (t *DBExecuteTool) InputSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"id":    map[string]interface{}{"type": "string"},
			"query": map[string]interface{}{"type": "string"},
		},
		"required": []string{"id", "query"},
	}
}
func (t *DBExecuteTool) Execute(ctx context.Context, args map[string]interface{}) (types.ToolResult, error) {
	id, _ := args["id"].(string)
	q, _ := args["query"].(string)

	e, err := dbMgr.getConn(id)
	if err != nil {
		return types.ToolResult{Content: err.Error(), Success: false}, nil
	}
	if e.DB == nil {
		return types.ToolResult{Content: "No SQL driver for this connection", Success: false}, nil
	}

	res, err := e.DB.ExecContext(ctx, q)
	if err != nil {
		return types.ToolResult{Content: fmt.Sprintf("Error: %v", err), Success: false}, nil
	}
	af, _ := res.RowsAffected()
	return types.ToolResult{Content: fmt.Sprintf("OK, %d rows affected", af), Success: true}, nil
}

// ── DBListTablesTool ───────────────────────────────────────────

type DBListTablesTool struct{}

func (t *DBListTablesTool) Name() string        { return "db_list_tables" }
func (t *DBListTablesTool) IsReadOnly() bool     { return true }
func (t *DBListTablesTool) IsConcurrencySafe() bool { return true }
func (t *DBListTablesTool) Description() string { return "List tables in connected database. Args: id" }
func (t *DBListTablesTool) InputSchema() map[string]interface{} {
	return map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{"id": map[string]interface{}{"type": "string"}},
		"required":   []string{"id"},
	}
}
func (t *DBListTablesTool) Execute(ctx context.Context, args map[string]interface{}) (types.ToolResult, error) {
	id, _ := args["id"].(string)
	e, err := dbMgr.getConn(id)
	if err != nil {
		return types.ToolResult{Content: err.Error(), Success: false}, nil
	}
	if e.DB == nil {
		return types.ToolResult{Content: "No SQL driver", Success: false}, nil
	}

	var query string
	switch e.Driver {
	case "sqlite":
		query = `SELECT name, type FROM sqlite_master WHERE type IN ('table','view') AND name NOT LIKE 'sqlite_%' ORDER BY name`
	case "mysql":
		query = `SELECT TABLE_NAME, TABLE_TYPE FROM information_schema.TABLES WHERE TABLE_SCHEMA = DATABASE() ORDER BY TABLE_NAME`
	case "postgres":
		query = `SELECT table_name, table_type FROM information_schema.tables WHERE table_schema = 'public' ORDER BY table_name`
	case "clickhouse":
		query = `SELECT name, engine FROM system.tables WHERE database = currentDatabase() ORDER BY name`
	default:
		return types.ToolResult{Content: "List tables not supported for " + e.Driver, Success: false}, nil
	}

	rows, err := e.DB.QueryContext(ctx, query)
	if err != nil {
		return types.ToolResult{Content: fmt.Sprintf("Error: %v", err), Success: false}, nil
	}
	defer rows.Close()

	type TI struct{ Name, Type string }
	var tables []TI
	for rows.Next() {
		var name, typ string
		if rows.Scan(&name, &typ) == nil {
			tables = append(tables, TI{Name: name, Type: strings.ToLower(typ)})
		}
	}
	d, _ := json.MarshalIndent(tables, "", "  ")
	return types.ToolResult{Content: string(d), Success: true}, nil
}

// ── DBDescribeTableTool ────────────────────────────────────────

type DBDescribeTableTool struct{}

func (t *DBDescribeTableTool) Name() string        { return "db_describe_table" }
func (t *DBDescribeTableTool) IsReadOnly() bool     { return true }
func (t *DBDescribeTableTool) IsConcurrencySafe() bool { return true }
func (t *DBDescribeTableTool) Description() string { return "Describe table columns. Args: id, table" }
func (t *DBDescribeTableTool) InputSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"id":    map[string]interface{}{"type": "string"},
			"table": map[string]interface{}{"type": "string"},
		},
		"required": []string{"id", "table"},
	}
}
func (t *DBDescribeTableTool) Execute(ctx context.Context, args map[string]interface{}) (types.ToolResult, error) {
	id, _ := args["id"].(string)
	tbl, _ := args["table"].(string)
	e, err := dbMgr.getConn(id)
	if err != nil {
		return types.ToolResult{Content: err.Error(), Success: false}, nil
	}
	if e.DB == nil {
		return types.ToolResult{Content: "No SQL driver", Success: false}, nil
	}

	type CI struct {
		Name string `json:"name"`; Type string `json:"type"`
		Nullable bool `json:"nullable"`; Key string `json:"key,omitempty"`
		Default string `json:"default,omitempty"`
	}
	var cols []CI

	switch e.Driver {
	case "sqlite":
		rows, err := e.DB.QueryContext(ctx, fmt.Sprintf("PRAGMA table_info(%q)", tbl))
		if err != nil { return types.ToolResult{Content: fmt.Sprintf("Error: %v", err), Success: false}, nil }
		defer rows.Close()
		for rows.Next() {
			var cid int; var name, typ string; var notNull int
			var dflt interface{}; var pk int
			if rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk) != nil { continue }
			key := ""; if pk > 0 { key = "PRI" }
			df := ""; if s, ok := dflt.(string); ok { df = s }
			cols = append(cols, CI{Name: name, Type: typ, Nullable: notNull == 0, Key: key, Default: df})
		}
	case "mysql":
		rows, err := e.DB.QueryContext(ctx,
			`SELECT COLUMN_NAME, COLUMN_TYPE, IS_NULLABLE, COLUMN_KEY, COLUMN_DEFAULT FROM information_schema.COLUMNS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME=? ORDER BY ORDINAL_POSITION`, tbl)
		if err != nil { return types.ToolResult{Content: fmt.Sprintf("Error: %v", err), Success: false}, nil }
		defer rows.Close()
		for rows.Next() {
			var name, typ, nullable, key string; var dflt *string
			if rows.Scan(&name, &typ, &nullable, &key, &dflt) != nil { continue }
			df := ""; if dflt != nil { df = *dflt }
			cols = append(cols, CI{Name: name, Type: typ, Nullable: nullable == "YES", Key: key, Default: df})
		}
	case "postgres":
		rows, err := e.DB.QueryContext(ctx,
			`SELECT column_name, data_type, is_nullable, column_default FROM information_schema.columns WHERE table_schema='public' AND table_name=$1 ORDER BY ordinal_position`, tbl)
		if err != nil { return types.ToolResult{Content: fmt.Sprintf("Error: %v", err), Success: false}, nil }
		defer rows.Close()
		for rows.Next() {
			var name, typ, nullable string; var dflt *string
			if rows.Scan(&name, &typ, &nullable, &dflt) != nil { continue }
			df := ""; if dflt != nil { df = *dflt }
			cols = append(cols, CI{Name: name, Type: typ, Nullable: nullable == "YES", Default: df})
		}
	default:
		return types.ToolResult{Content: "Describe not supported for " + e.Driver, Success: false}, nil
	}

	d, _ := json.MarshalIndent(cols, "", "  ")
	return types.ToolResult{Content: string(d), Success: true}, nil
}
