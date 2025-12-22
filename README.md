# WebDbReader

A lightweight web UI for running ad-hoc SQL queries against PostgreSQL databases.

![Go](https://img.shields.io/badge/Go-1.22+-00ADD8?logo=go&logoColor=white)

## Features

- **Browser-based SQL editor** with syntax-friendly monospace input
- **Live results table** with sticky headers and horizontal scroll
- **CSV export** for any query result
- **Read-only by design** — only `SELECT` and `WITH` (CTE) queries allowed
- **Query timeout** (8s) and row limits (default 200, max 1000)
- **Keyboard shortcut** — `Cmd/Ctrl + Enter` to run

## Quick Start

```bash
# Clone and enter the project
git clone https://github.com/JonMunkholm/WebDbReader.git
cd WebDbReader

# Configure your database connection
cp .env.example .env
# Edit .env with your credentials

# Run
go run main.go
```

Open [http://localhost:8080](http://localhost:8080) in your browser.

## Configuration

Create a `.env` file (or set environment variables):

| Variable    | Default                                         | Description              |
|-------------|-------------------------------------------------|--------------------------|
| `DB_DRIVER` | `postgres`                                      | Database driver          |
| `DB_DSN`    | `postgres://localhost/postgres?sslmode=disable` | Connection string        |
| `ADDR`      | `:8080`                                         | Server listen address    |

## Requirements

- Go 1.22+
- PostgreSQL (or compatible database)

## License

MIT
