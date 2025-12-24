// Package schema provides database schema introspection and caching for LLM context.
package schema

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Cache holds the database schema information for LLM context.
type Cache struct {
	Tables      []Table
	LastRefresh time.Time
	mu          sync.RWMutex
}

// Table represents a database table and its structure.
type Table struct {
	Name        string
	Columns     []Column
	ForeignKeys []ForeignKey
	RowEstimate int64
}

// Column represents a table column.
type Column struct {
	Name     string
	Type     string
	Nullable bool
	IsPK     bool
	Comment  string
}

// ForeignKey represents a foreign key relationship.
type ForeignKey struct {
	Column        string
	ForeignTable  string
	ForeignColumn string
}

// NewCache creates an empty schema cache.
func NewCache() *Cache {
	return &Cache{}
}

// Load fetches the schema from the database and caches it.
func (c *Cache) Load(ctx context.Context, db *sql.DB) error {
	tables, err := loadTables(ctx, db)
	if err != nil {
		return fmt.Errorf("load tables: %w", err)
	}

	c.mu.Lock()
	c.Tables = tables
	c.LastRefresh = time.Now()
	c.mu.Unlock()

	return nil
}

// GetTables returns a copy of the cached tables.
func (c *Cache) GetTables() []Table {
	c.mu.RLock()
	defer c.mu.RUnlock()

	tables := make([]Table, len(c.Tables))
	copy(tables, c.Tables)
	return tables
}

// HasTable checks if a table exists in the cache.
func (c *Cache) HasTable(name string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()

	lower := strings.ToLower(name)
	for _, t := range c.Tables {
		if strings.ToLower(t.Name) == lower {
			return true
		}
	}
	return false
}

// ToText serializes the schema to a text format suitable for LLM prompts.
func (c *Cache) ToText() string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if len(c.Tables) == 0 {
		return "(no tables found)"
	}

	var sb strings.Builder
	for i, table := range c.Tables {
		if i > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString(tableToText(table))
	}
	return sb.String()
}

// TableCount returns the number of cached tables.
func (c *Cache) TableCount() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.Tables)
}

// GetLastRefresh returns when the schema was last refreshed.
func (c *Cache) GetLastRefresh() time.Time {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.LastRefresh
}

func tableToText(t Table) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("TABLE: %s", t.Name))
	if t.RowEstimate > 0 {
		sb.WriteString(fmt.Sprintf(" (~%d rows)", t.RowEstimate))
	}
	sb.WriteString("\n")

	for _, col := range t.Columns {
		sb.WriteString(fmt.Sprintf("  - %s: %s", col.Name, col.Type))

		var attrs []string
		if col.IsPK {
			attrs = append(attrs, "PK")
		}
		if !col.Nullable {
			attrs = append(attrs, "NOT NULL")
		}
		if len(attrs) > 0 {
			sb.WriteString(", " + strings.Join(attrs, ", "))
		}

		// Show FK relationship inline
		for _, fk := range t.ForeignKeys {
			if fk.Column == col.Name {
				sb.WriteString(fmt.Sprintf(" -> %s.%s", fk.ForeignTable, fk.ForeignColumn))
				break
			}
		}

		if col.Comment != "" {
			sb.WriteString(fmt.Sprintf(" // %s", col.Comment))
		}
		sb.WriteString("\n")
	}

	return sb.String()
}

func loadTables(ctx context.Context, db *sql.DB) ([]Table, error) {
	tableNames, err := getTableNames(ctx, db)
	if err != nil {
		return nil, err
	}

	columns, err := getColumns(ctx, db)
	if err != nil {
		return nil, err
	}

	primaryKeys, err := getPrimaryKeys(ctx, db)
	if err != nil {
		return nil, err
	}

	foreignKeys, err := getForeignKeys(ctx, db)
	if err != nil {
		return nil, err
	}

	rowEstimates, err := getRowEstimates(ctx, db)
	if err != nil {
		// Non-fatal: continue without estimates
		rowEstimates = make(map[string]int64)
	}

	tables := make([]Table, 0, len(tableNames))
	for _, name := range tableNames {
		table := Table{
			Name:        name,
			Columns:     columns[name],
			ForeignKeys: foreignKeys[name],
			RowEstimate: rowEstimates[name],
		}

		// Mark primary key columns
		pkCols := primaryKeys[name]
		for i := range table.Columns {
			for _, pk := range pkCols {
				if table.Columns[i].Name == pk {
					table.Columns[i].IsPK = true
					break
				}
			}
		}

		tables = append(tables, table)
	}

	return tables, nil
}

func getTableNames(ctx context.Context, db *sql.DB) ([]string, error) {
	query := `
		SELECT table_name
		FROM information_schema.tables
		WHERE table_schema = 'public'
		  AND table_type = 'BASE TABLE'
		ORDER BY table_name`

	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

func getColumns(ctx context.Context, db *sql.DB) (map[string][]Column, error) {
	query := `
		SELECT
			c.table_name,
			c.column_name,
			c.data_type,
			c.is_nullable = 'YES' AS nullable,
			COALESCE(pgd.description, '') AS comment
		FROM information_schema.columns c
		LEFT JOIN pg_catalog.pg_statio_all_tables st
			ON st.schemaname = c.table_schema AND st.relname = c.table_name
		LEFT JOIN pg_catalog.pg_description pgd
			ON pgd.objoid = st.relid AND pgd.objsubid = c.ordinal_position
		WHERE c.table_schema = 'public'
		ORDER BY c.table_name, c.ordinal_position`

	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	columns := make(map[string][]Column)
	for rows.Next() {
		var tableName string
		var col Column
		if err := rows.Scan(&tableName, &col.Name, &col.Type, &col.Nullable, &col.Comment); err != nil {
			return nil, err
		}
		columns[tableName] = append(columns[tableName], col)
	}
	return columns, rows.Err()
}

func getPrimaryKeys(ctx context.Context, db *sql.DB) (map[string][]string, error) {
	query := `
		SELECT
			tc.table_name,
			kcu.column_name
		FROM information_schema.table_constraints tc
		JOIN information_schema.key_column_usage kcu
			ON tc.constraint_name = kcu.constraint_name
			AND tc.table_schema = kcu.table_schema
		WHERE tc.constraint_type = 'PRIMARY KEY'
		  AND tc.table_schema = 'public'
		ORDER BY tc.table_name, kcu.ordinal_position`

	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	pks := make(map[string][]string)
	for rows.Next() {
		var tableName, colName string
		if err := rows.Scan(&tableName, &colName); err != nil {
			return nil, err
		}
		pks[tableName] = append(pks[tableName], colName)
	}
	return pks, rows.Err()
}

func getForeignKeys(ctx context.Context, db *sql.DB) (map[string][]ForeignKey, error) {
	query := `
		SELECT
			tc.table_name,
			kcu.column_name,
			ccu.table_name AS foreign_table,
			ccu.column_name AS foreign_column
		FROM information_schema.table_constraints tc
		JOIN information_schema.key_column_usage kcu
			ON tc.constraint_name = kcu.constraint_name
			AND tc.table_schema = kcu.table_schema
		JOIN information_schema.constraint_column_usage ccu
			ON tc.constraint_name = ccu.constraint_name
			AND tc.table_schema = ccu.table_schema
		WHERE tc.constraint_type = 'FOREIGN KEY'
		  AND tc.table_schema = 'public'`

	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	fks := make(map[string][]ForeignKey)
	for rows.Next() {
		var tableName string
		var fk ForeignKey
		if err := rows.Scan(&tableName, &fk.Column, &fk.ForeignTable, &fk.ForeignColumn); err != nil {
			return nil, err
		}
		fks[tableName] = append(fks[tableName], fk)
	}
	return fks, rows.Err()
}

func getRowEstimates(ctx context.Context, db *sql.DB) (map[string]int64, error) {
	query := `
		SELECT relname, reltuples::bigint
		FROM pg_class
		WHERE relnamespace = 'public'::regnamespace
		  AND relkind = 'r'`

	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	estimates := make(map[string]int64)
	for rows.Next() {
		var name string
		var count int64
		if err := rows.Scan(&name, &count); err != nil {
			return nil, err
		}
		if count < 0 {
			count = 0
		}
		estimates[name] = count
	}
	return estimates, rows.Err()
}
