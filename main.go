package main

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/JonMunkholm/WebDbReader/internal/llm"
	"github.com/JonMunkholm/WebDbReader/internal/schema"
	"github.com/go-chi/chi/v5"
	"github.com/joho/godotenv"
	_ "github.com/lib/pq"
)

const (
	defaultAddr         = ":8080"
	defaultLimit        = 200
	maxLimit            = 1000
	queryTimeout        = 8 * time.Second
	defaultExampleQuery = "SELECT 1 AS id, 'hello' AS greeting;"
)

type app struct {
	db     *sql.DB
	tmpl   *template.Template
	schema *schema.Cache
	llm    llm.Provider
}

type queryRequest struct {
	Query string `json:"query"`
	Limit int    `json:"limit"`
}

type queryResponse struct {
	Columns    []string `json:"columns"`
	Rows       [][]any  `json:"rows"`
	Count      int      `json:"count"`
	More       bool     `json:"more"`
	DurationMs int64    `json:"durationMs"`
	Error      string   `json:"error,omitempty"`
}

func main() {
	_ = godotenv.Load() // loads .env if present, silently ignores if not

	driver := env("DB_DRIVER", "postgres")
	dsn := env("DB_DSN", "postgres://localhost/postgres?sslmode=disable")
	addr := env("ADDR", defaultAddr)

	db, err := sql.Open(driver, dsn)
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	if err := db.Ping(); err != nil {
		log.Fatalf("ping database: %v", err)
	}

	// Initialize schema cache
	schemaCache := schema.NewCache()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	if err := schemaCache.Load(ctx, db); err != nil {
		log.Printf("warning: failed to load schema: %v", err)
	} else {
		log.Printf("loaded schema: %d tables", schemaCache.TableCount())
	}
	cancel()

	// Initialize LLM provider (optional - only if configured)
	var llmProvider llm.Provider
	if os.Getenv("LLM_API_KEY") != "" {
		llmProvider, err = llm.NewProviderFromEnv()
		if err != nil {
			log.Printf("warning: failed to initialize LLM: %v", err)
		} else {
			log.Printf("LLM provider initialized: %s", llmProvider.Name())
		}
	} else {
		log.Printf("LLM not configured (set LLM_API_KEY to enable)")
	}

	tmpl := template.Must(template.New("index").Parse(indexHTML))
	app := &app{
		db:     db,
		tmpl:   tmpl,
		schema: schemaCache,
		llm:    llmProvider,
	}

	r := chi.NewRouter()
	r.Get("/", app.handleIndex)
	r.Post("/query", app.handleQuery)
	r.Post("/export", app.handleExportCSV)
	r.Post("/generate-sql", app.handleGenerateSQL)
	r.Get("/schema", app.handleSchema)
	r.Post("/schema/refresh", app.handleSchemaRefresh)

	log.Printf("listening on %s (driver=%s)", addr, driver)
	if err := http.ListenAndServe(addr, r); err != nil {
		log.Fatal(err)
	}
}

func (a *app) handleIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	data := struct {
		DefaultQuery string
		DefaultLimit int
	}{
		DefaultQuery: defaultExampleQuery,
		DefaultLimit: defaultLimit,
	}
	if err := a.tmpl.Execute(w, data); err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
	}
}

func (a *app) handleQuery(w http.ResponseWriter, r *http.Request) {
	var req queryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondJSON(w, http.StatusBadRequest, queryResponse{Error: "invalid JSON body"})
		return
	}

	query, err := validateSelectQuery(req.Query)
	if err != nil {
		respondJSON(w, http.StatusBadRequest, queryResponse{Error: err.Error()})
		return
	}

	limit := clampLimit(req.Limit)

	ctx, cancel := context.WithTimeout(r.Context(), queryTimeout)
	defer cancel()

	start := time.Now()
	result, err := a.executeSelectQuery(ctx, query)
	if err != nil {
		respondJSON(w, http.StatusBadRequest, queryResponse{Error: err.Error()})
		return
	}
	defer result.rows.Close()

	resp := queryResponse{
		Columns: result.columns,
	}

	for result.rows.Next() {
		if len(resp.Rows) >= limit {
			resp.More = true
			break
		}
		values, err := scanRow(result.rows, len(result.columns))
		if err != nil {
			respondJSON(w, http.StatusInternalServerError, queryResponse{Error: err.Error()})
			return
		}
		resp.Rows = append(resp.Rows, normalizeRow(values))
	}
	if err := result.rows.Err(); err != nil {
		respondJSON(w, http.StatusInternalServerError, queryResponse{Error: err.Error()})
		return
	}

	resp.Count = len(resp.Rows)
	resp.DurationMs = time.Since(start).Milliseconds()

	respondJSON(w, http.StatusOK, resp)
}

// TODO(shared-use): Improve CSV filename to include table name and date (e.g., "anrok_transactions_2025-12-20.csv").
// Considerations: complex queries (joins, CTEs, subqueries) make table extraction non-trivial.
// Options: user-provided filename, timestamp-only fallback, or server-side SQL parsing.
func (a *app) handleExportCSV(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Query string `json:"query"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}

	query, err := validateSelectQuery(req.Query)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), queryTimeout)
	defer cancel()

	result, err := a.executeSelectQuery(ctx, query)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	defer result.rows.Close()

	w.Header().Set("Content-Type", "text/csv")
	w.Header().Set("Content-Disposition", "attachment; filename=export.csv")

	csvWriter := csv.NewWriter(w)
	defer csvWriter.Flush()

	if err := csvWriter.Write(result.columns); err != nil {
		return
	}

	for result.rows.Next() {
		values, err := scanRow(result.rows, len(result.columns))
		if err != nil {
			return
		}
		record := make([]string, len(result.columns))
		for i, v := range values {
			record[i] = formatCSVValue(v)
		}
		if err := csvWriter.Write(record); err != nil {
			return
		}
	}
}

type generateSQLRequest struct {
	Prompt string `json:"prompt"`
}

type generateSQLResponse struct {
	SQL     string `json:"sql,omitempty"`
	Missing string `json:"missing,omitempty"`
	Error   string `json:"error,omitempty"`
	Tokens  int    `json:"tokens,omitempty"`
}

func (a *app) handleGenerateSQL(w http.ResponseWriter, r *http.Request) {
	if a.llm == nil {
		respondJSON(w, http.StatusServiceUnavailable, generateSQLResponse{
			Error: "LLM not configured. Set LLM_API_KEY environment variable.",
		})
		return
	}

	var req generateSQLRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondJSON(w, http.StatusBadRequest, generateSQLResponse{Error: "invalid JSON body"})
		return
	}

	if strings.TrimSpace(req.Prompt) == "" {
		respondJSON(w, http.StatusBadRequest, generateSQLResponse{Error: "prompt is required"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	llmReq := llm.GenerateRequest{
		Prompt: req.Prompt,
		Schema: a.schema.ToText(),
	}

	resp, err := a.llm.GenerateSQL(ctx, llmReq)
	if err != nil {
		respondJSON(w, http.StatusInternalServerError, generateSQLResponse{Error: resp.Error})
		return
	}

	if resp.IsMissing() {
		respondJSON(w, http.StatusOK, generateSQLResponse{Missing: resp.Missing, Tokens: resp.Tokens})
		return
	}

	// Validate the generated SQL
	if _, err := validateSelectQuery(resp.SQL); err != nil {
		respondJSON(w, http.StatusBadRequest, generateSQLResponse{
			Error: "LLM generated invalid query: " + err.Error(),
		})
		return
	}

	respondJSON(w, http.StatusOK, generateSQLResponse{SQL: resp.SQL, Tokens: resp.Tokens})
}

type schemaResponse struct {
	Tables      []schema.Table `json:"tables"`
	TableCount  int            `json:"tableCount"`
	LastRefresh string         `json:"lastRefresh"`
}

func (a *app) handleSchema(w http.ResponseWriter, r *http.Request) {
	respondJSON(w, http.StatusOK, schemaResponse{
		Tables:      a.schema.GetTables(),
		TableCount:  a.schema.TableCount(),
		LastRefresh: a.schema.GetLastRefresh().Format(time.RFC3339),
	})
}

func (a *app) handleSchemaRefresh(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	if err := a.schema.Load(ctx, a.db); err != nil {
		respondJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	respondJSON(w, http.StatusOK, schemaResponse{
		Tables:      a.schema.GetTables(),
		TableCount:  a.schema.TableCount(),
		LastRefresh: a.schema.GetLastRefresh().Format(time.RFC3339),
	})
}

func formatCSVValue(v any) string {
	switch val := v.(type) {
	case nil:
		return ""
	case []byte:
		return string(val)
	case time.Time:
		return val.Format(time.RFC3339)
	default:
		return fmt.Sprintf("%v", val)
	}
}

func normalizeRow(values []any) []any {
	row := make([]any, len(values))
	for i, v := range values {
		switch val := v.(type) {
		case nil:
			row[i] = nil
		case []byte:
			row[i] = string(val)
		case time.Time:
			row[i] = val.Format(time.RFC3339Nano)
		default:
			row[i] = val
		}
	}
	return row
}

func clampLimit(limit int) int {
	if limit <= 0 {
		return defaultLimit
	}
	if limit > maxLimit {
		return maxLimit
	}
	return limit
}

var errEmptyQuery = fmt.Errorf("query is required")
var errNotSelectQuery = fmt.Errorf("only SELECT / CTE queries are allowed")

func validateSelectQuery(raw string) (string, error) {
	query := strings.TrimSpace(raw)
	if query == "" {
		return "", errEmptyQuery
	}
	lower := strings.ToLower(query)
	if !strings.HasPrefix(lower, "select") && !strings.HasPrefix(lower, "with") {
		return "", errNotSelectQuery
	}
	return query, nil
}

// queryResult holds the result of executing a SELECT query.
type queryResult struct {
	rows    *sql.Rows
	columns []string
}

// executeSelectQuery executes a SELECT query and returns the rows with column names.
// The caller is responsible for closing the rows.
func (a *app) executeSelectQuery(ctx context.Context, query string) (*queryResult, error) {
	rows, err := a.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}

	columns, err := rows.Columns()
	if err != nil {
		rows.Close()
		return nil, err
	}

	return &queryResult{rows: rows, columns: columns}, nil
}

func scanRow(rows *sql.Rows, numCols int) ([]any, error) {
	values := make([]any, numCols)
	ptrs := make([]any, numCols)
	for i := range values {
		ptrs[i] = &values[i]
	}
	if err := rows.Scan(ptrs...); err != nil {
		return nil, err
	}
	return values, nil
}

func env(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func respondJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

//go:embed templates/index.html
var indexHTML string
