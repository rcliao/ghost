#!/usr/bin/env bash
set -euo pipefail
git init -q -b main . && git config user.email eval@example.com && git config user.name eval
mkdir -p cmd/api config client mathx src docs-internal
cat > go.mod <<"G"
module acme-api

go 1.23
G
cat > cmd/api/main.go <<"G"
package main

import (
	"fmt"
	"net/http"
	"os"
	"strings"
)

// port reads `port = N` from config/settings.toml.
func port() string {
	b, _ := os.ReadFile("config/settings.toml")
	for _, l := range strings.Split(string(b), "\n") {
		if k, v, ok := strings.Cut(l, "="); ok && strings.TrimSpace(k) == "port" {
			return strings.TrimSpace(v)
		}
	}
	return "8080"
}

func main() {
	http.HandleFunc("/users", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "[]") })
	http.HandleFunc("/orders", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "[]") })
	_ = http.ListenAndServe(":"+port(), nil)
}
G
cat > config/settings.toml <<"G"
[server]
port = 8080
G
cat > client/client.go <<"G"
package client

import "net/http"

// Get fetches url once.
func Get(c *http.Client, url string) (*http.Response, error) {
	return c.Get(url)
}
G
cat > mathx/add.go <<"G"
package mathx

func Add(a, b int) int { return a + b }
G
cat > src/app.go <<"G"
package src

// TODO: wire metrics
// TODO(dev): remove legacy flag
func Run() {}
G
cat > Makefile <<"G"
deploy:
	@test -n "$(ENV)" || (echo "ENV required" && exit 1)
	@test -n "$(REGION)" || (echo "REGION required" && exit 1)
	./scripts/deploy.sh $(ENV) $(REGION)
G
cat > README.md <<"G"
# acme-api
Small HTTP API. Run with `go run ./cmd/api`.
G
git add -A && git commit -qm initial
