package main

import (
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

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
	db   *sql.DB
	tmpl *template.Template
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

	tmpl := template.Must(template.New("index").Parse(indexHTML))
	app := &app{db: db, tmpl: tmpl}

	r := chi.NewRouter()
	r.Get("/", app.handleIndex)
	r.Post("/query", app.handleQuery)
	r.Post("/export", app.handleExportCSV)

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
	rows, err := a.db.QueryContext(ctx, query)
	if err != nil {
		respondJSON(w, http.StatusBadRequest, queryResponse{Error: err.Error()})
		return
	}
	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		respondJSON(w, http.StatusInternalServerError, queryResponse{Error: err.Error()})
		return
	}

	resp := queryResponse{
		Columns: columns,
	}

	for rows.Next() {
		if len(resp.Rows) >= limit {
			resp.More = true
			break
		}
		values, err := scanRow(rows, len(columns))
		if err != nil {
			respondJSON(w, http.StatusInternalServerError, queryResponse{Error: err.Error()})
			return
		}
		resp.Rows = append(resp.Rows, normalizeRow(values))
	}
	if err := rows.Err(); err != nil {
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

	rows, err := a.db.QueryContext(ctx, query)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/csv")
	w.Header().Set("Content-Disposition", "attachment; filename=export.csv")

	csvWriter := csv.NewWriter(w)
	defer csvWriter.Flush()

	if err := csvWriter.Write(columns); err != nil {
		return
	}

	for rows.Next() {
		values, err := scanRow(rows, len(columns))
		if err != nil {
			return
		}
		record := make([]string, len(columns))
		for i, v := range values {
			record[i] = formatCSVValue(v)
		}
		if err := csvWriter.Write(record); err != nil {
			return
		}
	}
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

func respondJSON(w http.ResponseWriter, status int, payload queryResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

// indexHTML is a small, self-contained UI for running ad-hoc queries.
var indexHTML = `
<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1" />
  <title>DB Reader</title>
  <style>
    :root {
      --bg: #0f1219;
      --panel: #1a1f2e;
      --panel-2: #161b26;
      --text: #e2e8f0;
      --muted: #8892a6;
      --accent: #3b82f6;
      --border: #2a3142;
      --danger: #f87171;
      --success: #4ade80;
    }
    * { box-sizing: border-box; }
    body {
      margin: 0;
      font-family: system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
      background: var(--bg);
      color: var(--text);
      min-height: 100vh;
    }
    main {
      max-width: 1100px;
      margin: 0 auto;
      padding: 48px 24px 64px;
    }
    header {
      display: flex;
      justify-content: space-between;
      align-items: baseline;
      gap: 12px;
      margin-bottom: 22px;
    }
    h1 {
      font-size: 28px;
      letter-spacing: -0.02em;
      margin: 0;
    }
    p.lead {
      margin: 4px 0 0;
      color: var(--muted);
    }
    .card {
      background: var(--panel);
      border: 1px solid var(--border);
      border-radius: 12px;
      padding: 18px;
    }
    form {
      display: grid;
      gap: 12px;
    }
    label {
      font-size: 13px;
      color: var(--muted);
      text-transform: uppercase;
      letter-spacing: 0.04em;
    }
    textarea {
      width: 100%;
      min-height: 170px;
      resize: vertical;
      padding: 14px;
      border-radius: 14px;
      border: 1px solid var(--border);
      background: var(--panel);
      color: var(--text);
      font-size: 14px;
      line-height: 1.45;
      font-family: ui-monospace, "SF Mono", SFMono-Regular, Menlo, Consolas, monospace;
    }
    textarea:focus {
      outline: none;
      border-color: var(--accent);
    }
    .controls {
      display: flex;
      align-items: center;
      gap: 12px;
      flex-wrap: wrap;
      justify-content: space-between;
    }
    .control-group {
      display: flex;
      align-items: center;
      gap: 10px;
      color: var(--muted);
      font-size: 13px;
    }
    input[type="number"] {
      width: 90px;
      padding: 8px 10px;
      border-radius: 10px;
      border: 1px solid var(--border);
      background: var(--panel);
      color: var(--text);
    }
    button {
      appearance: none;
      border: 0;
      background: var(--accent);
      color: #fff;
      padding: 10px 18px;
      border-radius: 8px;
      font-weight: 600;
      font-size: 14px;
      cursor: pointer;
      min-width: 120px;
      transition: background 0.15s ease;
    }
    button:hover { background: #2563eb; }
    button:active { background: #1d4ed8; }
    button:disabled {
      opacity: 0.5;
      cursor: not-allowed;
    }
    .export-row {
      display: flex;
      justify-content: flex-end;
      margin-top: 12px;
    }
    .export-btn {
      display: inline-flex;
      align-items: center;
      gap: 6px;
      background: transparent;
      color: var(--muted);
      border: 1px solid var(--border);
      padding: 8px 12px;
      font-size: 13px;
      min-width: auto;
    }
    .export-btn:hover { color: var(--text); background: rgba(255,255,255,0.05); }
    .export-btn:active { background: rgba(255,255,255,0.08); }
    .status {
      font-size: 13px;
      color: var(--muted);
      display: flex;
      align-items: center;
      gap: 10px;
    }
    .status strong { color: var(--text); }
    .status .error { color: var(--danger); }
    .status .success { color: var(--success); }
    .preview {
      margin-top: 16px;
      background: var(--panel-2);
      border: 1px solid var(--border);
      border-radius: 12px;
      padding: 12px;
    }
    .table-wrap {
      max-height: 360px;
      overflow: auto;
      border-radius: 10px;
    }
    table {
      border-collapse: collapse;
      width: 100%;
      min-width: 420px;
      font-size: 13px;
    }
    th, td {
      text-align: left;
      padding: 10px 12px;
      border-bottom: 1px solid var(--border);
      white-space: nowrap;
      max-width: 300px;
      overflow: hidden;
      text-overflow: ellipsis;
    }
    th {
      position: sticky;
      top: 0;
      background: rgba(15, 23, 42, 0.95);
      z-index: 2;
      letter-spacing: 0.01em;
    }
    tbody tr:hover td { background: rgba(255, 255, 255, 0.04); }
    .empty {
      color: var(--muted);
      text-align: center;
      padding: 18px 0;
      font-size: 14px;
    }
  </style>
</head>
<body>
  <main>
    <header>
      <div>
        <h1>DB Reader</h1>
        <p class="lead">Run ad-hoc SQL and preview results right in the browser.</p>
      </div>
      <div class="status" id="statusText"></div>
    </header>

    <section class="card">
      <form id="queryForm">
        <div>
          <label for="queryInput">SQL Query</label>
          <textarea id="queryInput" name="query" spellcheck="false">{{.DefaultQuery}}</textarea>
        </div>
        <div class="controls">
          <div class="control-group">
          <label for="limitInput">Max rows</label>
          <input id="limitInput" name="limit" type="number" min="1" max="1000" value="{{.DefaultLimit}}" />
          <span aria-hidden="true" style="color: var(--muted);">•</span>
          <span class="muted">⌘/Ctrl + Enter to run</span>
        </div>
        <button type="submit" id="runButton">Run query</button>
        </div>
      </form>
    </section>

    <section class="preview">
      <div class="table-wrap">
        <table id="resultsTable" aria-live="polite"></table>
        <div class="empty" id="emptyState">Run a query to see rows.</div>
      </div>
      <div class="export-row">
        <button type="button" id="exportButton" class="export-btn" disabled>
          <svg width="16" height="16" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round">
            <path d="M8 10V2M8 10L5 7M8 10L11 7"/>
            <path d="M2 10v3a1 1 0 001 1h10a1 1 0 001-1v-3"/>
          </svg>
          CSV
        </button>
      </div>
    </section>
  </main>

  <script>
    const form = document.getElementById('queryForm');
    const queryInput = document.getElementById('queryInput');
    const limitInput = document.getElementById('limitInput');
    const resultsTable = document.getElementById('resultsTable');
    const emptyState = document.getElementById('emptyState');
    const statusText = document.getElementById('statusText');
    const runButton = document.getElementById('runButton');
    const exportButton = document.getElementById('exportButton');
    const fallbackLimit = {{.DefaultLimit}};

    form.addEventListener('submit', (e) => {
      e.preventDefault();
      runQuery();
    });

    queryInput.addEventListener('keydown', (e) => {
      if ((e.metaKey || e.ctrlKey) && e.key === 'Enter') {
        e.preventDefault();
        runQuery();
      }
    });

    exportButton.addEventListener('click', exportCSV);

    async function runQuery() {
      const query = queryInput.value.trim();
      const limit = Number.parseInt(limitInput.value, 10) || fallbackLimit;

      if (!query) {
        setStatus('Enter a query to run.', 'error');
        return;
      }

      setStatus('Running query...', 'muted');
      toggleLoading(true);
      clearResults();

      try {
        const res = await fetch('/query', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ query, limit })
        });

        const data = await res.json();
        if (!res.ok || data.error) {
          setStatus(data.error || 'Server error', 'error');
          exportButton.disabled = true;
          return;
        }

        const rows = data.rows || [];
        renderTable(data.columns || [], rows);
        exportButton.disabled = rows.length === 0;
        const parts = [];
        parts.push(data.count + ' row' + (data.count === 1 ? '' : 's'));
        if (data.more) parts.push('truncated');
        if (data.durationMs != null) parts.push(data.durationMs + ' ms');
        setStatus(parts.join(' · '), 'success');
      } catch (err) {
        console.error(err);
        setStatus('Request failed. Check the server logs.', 'error');
        exportButton.disabled = true;
      } finally {
        toggleLoading(false);
      }
    }

    async function exportCSV() {
      const query = queryInput.value.trim();
      if (!query) {
        setStatus('Enter a query to export.', 'error');
        return;
      }

      setStatus('Exporting CSV...', 'muted');
      exportButton.disabled = true;

      try {
        const res = await fetch('/export', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ query })
        });

        if (!res.ok) {
          const text = await res.text();
          setStatus(text || 'Export failed', 'error');
          return;
        }

        const blob = await res.blob();
        const url = URL.createObjectURL(blob);
        const a = document.createElement('a');
        a.href = url;
        a.download = 'export.csv';
        document.body.appendChild(a);
        a.click();
        document.body.removeChild(a);
        URL.revokeObjectURL(url);
        setStatus('CSV downloaded', 'success');
      } catch (err) {
        console.error(err);
        setStatus('Export failed. Check the server logs.', 'error');
      } finally {
        exportButton.disabled = false;
      }
    }

    function renderTable(columns, rows) {
      resultsTable.innerHTML = '';
      if (!rows.length) {
        emptyState.textContent = 'No rows returned.';
        emptyState.style.display = 'block';
        return;
      }

      emptyState.style.display = 'none';
      const thead = document.createElement('thead');
      const headerRow = document.createElement('tr');
      columns.forEach(col => {
        const th = document.createElement('th');
        th.textContent = col;
        headerRow.appendChild(th);
      });
      thead.appendChild(headerRow);

      const tbody = document.createElement('tbody');
      rows.forEach(row => {
        const tr = document.createElement('tr');
        row.forEach(cell => {
          const td = document.createElement('td');
          const text = cell === null ? 'NULL' : String(cell);
          td.textContent = text;
          td.title = text;
          tr.appendChild(td);
        });
        tbody.appendChild(tr);
      });

      resultsTable.appendChild(thead);
      resultsTable.appendChild(tbody);
    }

    function clearResults() {
      resultsTable.innerHTML = '';
      emptyState.textContent = 'Running...';
      emptyState.style.display = 'block';
    }

    function setStatus(message, tone) {
      statusText.textContent = message || '';
      statusText.className = 'status ' + (tone || '');
    }

    function toggleLoading(isLoading) {
      runButton.disabled = isLoading;
      runButton.textContent = isLoading ? 'Running...' : 'Run query';
    }

    // Autofocus for quick entry.
    queryInput.focus();
  </script>
</body>
</html>
`
