#!/usr/bin/env bash
# Creates three throwaway CRUD projects under $1 (fresh each time).
set -euo pipefail
ROOT="$1"
SETTINGS="$2"
rm -rf "$ROOT"
mkdir -p "$ROOT"

# ---------------------------------------------------------------- go-crud
P="$ROOT/go-crud"
mkdir -p "$P/store" "$P/.vulnetix"
cp "$SETTINGS" "$P/.vulnetix/settings.json"
cat > "$P/go.mod" <<'EOF'
module example.com/notes

go 1.22
EOF
cat > "$P/README.md" <<'EOF'
# notes

A tiny HTTP notes service.

Run: `go run .` (listens on :8080)

Endpoints:
- `GET /notes` list notes
- `POST /notes` create `{"title": "...", "body": "..."}`
- `GET /notes/{id}` fetch one
- `PUT /notes/{id}` replace one
- `DELETE /notes/{id}` remove one

Test: `go test ./...`
EOF
cat > "$P/main.go" <<'EOF'
package main

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"

	"example.com/notes/store"
)

type server struct {
	st *store.Store
}

func main() {
	s := &server{st: store.New()}
	mux := http.NewServeMux()
	mux.HandleFunc("/notes", s.notes)
	mux.HandleFunc("/notes/", s.note)
	log.Println("listening on :8080")
	log.Fatal(http.ListenAndServe(":8080", mux))
}

func (s *server) notes(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, s.st.List())
	case http.MethodPost:
		var in store.Note
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		n := s.st.Create(in.Title, in.Body)
		writeJSON(w, http.StatusCreated, n)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *server) note(w http.ResponseWriter, r *http.Request) {
	idStr := strings.TrimPrefix(r.URL.Path, "/notes/")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	switch r.Method {
	case http.MethodGet:
		n, ok := s.st.Get(id)
		if !ok {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, http.StatusOK, n)
	case http.MethodPut:
		var in store.Note
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		n, ok := s.st.Update(id, in.Title, in.Body)
		if !ok {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, http.StatusOK, n)
	case http.MethodDelete:
		if !s.st.Delete(id) {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
EOF
cat > "$P/store/store.go" <<'EOF'
// Package store is an in-memory note store.
package store

import "sync"

// Note is one stored note.
type Note struct {
	ID    int    `json:"id"`
	Title string `json:"title"`
	Body  string `json:"body"`
}

// Store holds notes in memory.
type Store struct {
	mu    sync.Mutex
	next  int
	notes map[int]Note
}

// New returns an empty store.
func New() *Store { return &Store{next: 1, notes: map[int]Note{}} }

// List returns every note ordered by id.
func (s *Store) List() []Note {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Note, 0, len(s.notes))
	for i := 1; i < s.next; i++ {
		if n, ok := s.notes[i]; ok {
			out = append(out, n)
		}
	}
	return out
}

// Create adds a note and returns it.
func (s *Store) Create(title, body string) Note {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := Note{ID: s.next, Title: title, Body: body}
	s.notes[n.ID] = n
	s.next++
	return n
}

// Get returns a note by id.
func (s *Store) Get(id int) (Note, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, ok := s.notes[id]
	return n, ok
}

// Update replaces a note's title and body.
func (s *Store) Update(id int, title, body string) (Note, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, ok := s.notes[id]
	if !ok {
		return Note{}, false
	}
	n.Title, n.Body = title, body
	s.notes[id] = n
	return n, true
}

// Delete removes a note.
func (s *Store) Delete(id int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.notes[id]; !ok {
		return false
	}
	delete(s.notes, id)
	return true
}
EOF
cat > "$P/store/store_test.go" <<'EOF'
package store

import "testing"

func TestCRUD(t *testing.T) {
	s := New()
	n := s.Create("a", "b")
	if n.ID != 1 {
		t.Fatalf("id = %d", n.ID)
	}
	if got, ok := s.Get(1); !ok || got.Title != "a" {
		t.Fatalf("get = %+v %v", got, ok)
	}
	if _, ok := s.Update(1, "c", "d"); !ok {
		t.Fatal("update")
	}
	if len(s.List()) != 1 {
		t.Fatal("list")
	}
	if !s.Delete(1) || s.Delete(1) {
		t.Fatal("delete")
	}
}
EOF
(cd "$P" && git init -q . && git add -A && git -c user.email=t@t -c user.name=t commit -qm init)

# ---------------------------------------------------------------- py-crud
P="$ROOT/py-crud"
mkdir -p "$P/tests" "$P/.vulnetix"
cp "$SETTINGS" "$P/.vulnetix/settings.json"
cat > "$P/README.md" <<'EOF'
# items

A small Flask + SQLite items API.

Setup: `pip install -r requirements.txt`
Run: `flask --app app run`
Test: `pytest`

Endpoints: `GET /items`, `POST /items`, `GET /items/<id>`, `PUT /items/<id>`, `DELETE /items/<id>`
EOF
cat > "$P/requirements.txt" <<'EOF'
flask==3.0.3
pytest==8.3.2
EOF
cat > "$P/models.py" <<'EOF'
"""SQLite-backed item storage."""
import sqlite3
from contextlib import contextmanager

DB_PATH = "items.db"


def init_db(path=DB_PATH):
    with sqlite3.connect(path) as conn:
        conn.execute(
            "CREATE TABLE IF NOT EXISTS items (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL, qty INTEGER NOT NULL DEFAULT 0)"
        )


@contextmanager
def connect(path=DB_PATH):
    conn = sqlite3.connect(path)
    conn.row_factory = sqlite3.Row
    try:
        yield conn
        conn.commit()
    finally:
        conn.close()


def list_items(path=DB_PATH):
    with connect(path) as conn:
        rows = conn.execute("SELECT id, name, qty FROM items ORDER BY id").fetchall()
        return [dict(r) for r in rows]


def create_item(name, qty, path=DB_PATH):
    with connect(path) as conn:
        cur = conn.execute("INSERT INTO items (name, qty) VALUES (?, ?)", (name, qty))
        return {"id": cur.lastrowid, "name": name, "qty": qty}


def get_item(item_id, path=DB_PATH):
    with connect(path) as conn:
        row = conn.execute("SELECT id, name, qty FROM items WHERE id = ?", (item_id,)).fetchone()
        return dict(row) if row else None


def update_item(item_id, name, qty, path=DB_PATH):
    with connect(path) as conn:
        cur = conn.execute("UPDATE items SET name = ?, qty = ? WHERE id = ?", (name, qty, item_id))
        if cur.rowcount == 0:
            return None
        return {"id": item_id, "name": name, "qty": qty}


def delete_item(item_id, path=DB_PATH):
    with connect(path) as conn:
        cur = conn.execute("DELETE FROM items WHERE id = ?", (item_id,))
        return cur.rowcount > 0
EOF
cat > "$P/app.py" <<'EOF'
"""Flask items API."""
from flask import Flask, jsonify, request, abort

import models


def create_app(db_path=models.DB_PATH):
    app = Flask(__name__)
    app.config["DB_PATH"] = db_path
    models.init_db(db_path)

    @app.get("/items")
    def list_items():
        return jsonify(models.list_items(app.config["DB_PATH"]))

    @app.post("/items")
    def create_item():
        data = request.get_json(force=True, silent=True) or {}
        if "name" not in data:
            abort(400, "name is required")
        item = models.create_item(data["name"], int(data.get("qty", 0)), app.config["DB_PATH"])
        return jsonify(item), 201

    @app.get("/items/<int:item_id>")
    def get_item(item_id):
        item = models.get_item(item_id, app.config["DB_PATH"])
        if item is None:
            abort(404)
        return jsonify(item)

    @app.put("/items/<int:item_id>")
    def update_item(item_id):
        data = request.get_json(force=True, silent=True) or {}
        item = models.update_item(item_id, data.get("name", ""), int(data.get("qty", 0)), app.config["DB_PATH"])
        if item is None:
            abort(404)
        return jsonify(item)

    @app.delete("/items/<int:item_id>")
    def delete_item(item_id):
        if not models.delete_item(item_id, app.config["DB_PATH"]):
            abort(404)
        return "", 204

    return app


app = create_app()
EOF
cat > "$P/tests/test_app.py" <<'EOF'
import pytest

from app import create_app


@pytest.fixture
def client(tmp_path):
    app = create_app(str(tmp_path / "t.db"))
    app.testing = True
    return app.test_client()


def test_crud(client):
    r = client.post("/items", json={"name": "bolt", "qty": 3})
    assert r.status_code == 201
    item_id = r.get_json()["id"]
    assert client.get(f"/items/{item_id}").get_json()["name"] == "bolt"
    assert client.put(f"/items/{item_id}", json={"name": "nut", "qty": 1}).status_code == 200
    assert len(client.get("/items").get_json()) == 1
    assert client.delete(f"/items/{item_id}").status_code == 204
    assert client.get(f"/items/{item_id}").status_code == 404
EOF
(cd "$P" && git init -q . && git add -A && git -c user.email=t@t -c user.name=t commit -qm init)

# ---------------------------------------------------------------- ts-crud
P="$ROOT/ts-crud"
mkdir -p "$P/src/routes" "$P/test" "$P/.vulnetix"
cp "$SETTINGS" "$P/.vulnetix/settings.json"
cat > "$P/README.md" <<'EOF'
# tasks-api

Express + TypeScript tasks API with an in-memory store.

Install: `npm install`
Run: `npm run dev`
Test: `npm test`

Endpoints: `GET /tasks`, `POST /tasks`, `GET /tasks/:id`, `PUT /tasks/:id`, `DELETE /tasks/:id`
EOF
cat > "$P/package.json" <<'EOF'
{
  "name": "tasks-api",
  "version": "0.1.0",
  "private": true,
  "scripts": {
    "dev": "tsx src/server.ts",
    "build": "tsc -p .",
    "test": "vitest run"
  },
  "dependencies": {
    "express": "^4.19.2"
  },
  "devDependencies": {
    "@types/express": "^4.17.21",
    "@types/node": "^20.14.0",
    "supertest": "^7.0.0",
    "tsx": "^4.16.0",
    "typescript": "^5.5.0",
    "vitest": "^2.0.0"
  }
}
EOF
cat > "$P/tsconfig.json" <<'EOF'
{
  "compilerOptions": {
    "target": "ES2022",
    "module": "NodeNext",
    "moduleResolution": "NodeNext",
    "strict": true,
    "outDir": "dist",
    "rootDir": "src",
    "esModuleInterop": true
  },
  "include": ["src"]
}
EOF
cat > "$P/src/store.ts" <<'EOF'
export interface Task {
  id: number;
  title: string;
  done: boolean;
}

export class TaskStore {
  private next = 1;
  private tasks = new Map<number, Task>();

  list(): Task[] {
    return [...this.tasks.values()].sort((a, b) => a.id - b.id);
  }

  create(title: string): Task {
    const task: Task = { id: this.next++, title, done: false };
    this.tasks.set(task.id, task);
    return task;
  }

  get(id: number): Task | undefined {
    return this.tasks.get(id);
  }

  update(id: number, patch: Partial<Omit<Task, "id">>): Task | undefined {
    const existing = this.tasks.get(id);
    if (!existing) return undefined;
    const updated = { ...existing, ...patch };
    this.tasks.set(id, updated);
    return updated;
  }

  delete(id: number): boolean {
    return this.tasks.delete(id);
  }
}
EOF
cat > "$P/src/routes/tasks.ts" <<'EOF'
import { Router } from "express";
import { TaskStore } from "../store.js";

export function tasksRouter(store: TaskStore): Router {
  const r = Router();

  r.get("/", (_req, res) => {
    res.json(store.list());
  });

  r.post("/", (req, res) => {
    const task = store.create(String(req.body?.title ?? ""));
    res.status(201).json(task);
  });

  r.get("/:id", (req, res) => {
    const task = store.get(Number(req.params.id));
    if (!task) return res.sendStatus(404);
    res.json(task);
  });

  r.put("/:id", (req, res) => {
    const task = store.update(Number(req.params.id), {
      title: req.body?.title,
      done: req.body?.done,
    });
    if (!task) return res.sendStatus(404);
    res.json(task);
  });

  r.delete("/:id", (req, res) => {
    if (!store.delete(Number(req.params.id))) return res.sendStatus(404);
    res.sendStatus(204);
  });

  return r;
}
EOF
cat > "$P/src/app.ts" <<'EOF'
import express from "express";
import { TaskStore } from "./store.js";
import { tasksRouter } from "./routes/tasks.js";

export function createApp(store = new TaskStore()) {
  const app = express();
  app.use(express.json());
  app.use("/tasks", tasksRouter(store));
  return app;
}
EOF
cat > "$P/src/server.ts" <<'EOF'
import { createApp } from "./app.js";

const port = Number(process.env.PORT ?? 3000);
createApp().listen(port, () => {
  console.log(`tasks-api listening on ${port}`);
});
EOF
cat > "$P/test/tasks.test.ts" <<'EOF'
import { describe, it, expect } from "vitest";
import request from "supertest";
import { createApp } from "../src/app.js";

describe("tasks", () => {
  it("does CRUD", async () => {
    const app = createApp();
    const created = await request(app).post("/tasks").send({ title: "write" });
    expect(created.status).toBe(201);
    const id = created.body.id;
    expect((await request(app).get(`/tasks/${id}`)).body.title).toBe("write");
    expect((await request(app).put(`/tasks/${id}`).send({ done: true })).body.done).toBe(true);
    expect((await request(app).get("/tasks")).body).toHaveLength(1);
    expect((await request(app).delete(`/tasks/${id}`)).status).toBe(204);
  });
});
EOF
(cd "$P" && git init -q . && git add -A && git -c user.email=t@t -c user.name=t commit -qm init)

echo "projects created under $ROOT"
