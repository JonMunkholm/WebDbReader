# WebDbReader

A lightweight web UI for running ad-hoc SQL queries against PostgreSQL databases, with optional AI-powered natural language to SQL conversion.

![Go](https://img.shields.io/badge/Go-1.22+-00ADD8?logo=go&logoColor=white)

## Features

- **Natural language to SQL** — ask questions in plain English, get SQL queries (optional)
- **Browser-based SQL editor** with syntax-friendly monospace input
- **Live results table** with sticky headers and horizontal scroll
- **CSV export** for any query result
- **Read-only by design** — only `SELECT` and `WITH` (CTE) queries allowed
- **Query timeout** (8s) and row limits (default 200, max 1000)
- **Keyboard shortcuts** — `Enter` to generate SQL, `Cmd/Ctrl + Enter` to run

## Quick Start

```bash
# Clone and enter the project
git clone https://github.com/JonMunkholm/WebDbReader.git
cd WebDbReader

# Configure your database connection
cp .env.example .env
# Edit .env with your credentials

# Run
go run .
```

Open [http://localhost:8080](http://localhost:8080) in your browser.

## Configuration

Create a `.env` file (or set environment variables):

### Database

| Variable    | Default                                         | Description              |
|-------------|-------------------------------------------------|--------------------------|
| `DB_DRIVER` | `postgres`                                      | Database driver          |
| `DB_DSN`    | `postgres://localhost/postgres?sslmode=disable` | Connection string        |
| `ADDR`      | `:8080`                                         | Server listen address    |

### LLM (Optional)

Enable natural language to SQL by configuring an LLM provider:

| Variable       | Default   | Description                          |
|----------------|-----------|--------------------------------------|
| `LLM_PROVIDER` | `openai`  | Provider: `openai` or `anthropic`    |
| `LLM_API_KEY`  | —         | API key for your provider            |
| `LLM_MODEL`    | `gpt-4o`  | Model to use (see below)             |
| `LLM_BASE_URL` | —         | Override API URL (for proxies)       |

#### Supported Models

**OpenAI:**
- `gpt-4o` (recommended)
- `gpt-4-turbo`
- `gpt-3.5-turbo`

**Anthropic:**
- `claude-sonnet-4-20250514` (recommended)
- `claude-opus-4-20250514`
- `claude-3-5-haiku-20241022`

#### Using OpenRouter (100+ Models)

Access models from multiple providers through a single API:

```env
LLM_PROVIDER=openai
LLM_BASE_URL=https://openrouter.ai/api/v1
LLM_API_KEY=sk-or-your-key
LLM_MODEL=anthropic/claude-sonnet-4-20250514
```

Other compatible proxies: Together.ai, Groq, local Ollama.

## API Endpoints

| Endpoint           | Method | Description                        |
|--------------------|--------|------------------------------------|
| `/`                | GET    | Web UI                             |
| `/query`           | POST   | Execute SQL query                  |
| `/export`          | POST   | Export query results as CSV        |
| `/generate-sql`    | POST   | Convert natural language to SQL    |
| `/schema`          | GET    | View cached database schema        |
| `/schema/refresh`  | POST   | Reload schema from database        |

## How It Works

1. On startup, the app introspects your database schema (tables, columns, relationships)
2. When you enter a natural language question, it's sent to the LLM with the schema context
3. The LLM generates a SQL query based on your available tables
4. You can review, edit, and run the generated query
5. If the request can't be fulfilled with available data, the LLM explains what's missing

## Requirements

- Go 1.22+
- PostgreSQL (or compatible database)
- LLM API key (optional, for natural language features)

## License

MIT
