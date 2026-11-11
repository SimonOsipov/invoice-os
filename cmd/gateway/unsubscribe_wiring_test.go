package main

import (
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// registrationHandlers turns its pool into the register handler's TokenFunc: the sign-up carries the table's token.
func TestRegistrationHandlers_SignupCarriesThePoolToken(t *testing.T) {
	gw, super := os.Getenv("DATABASE_GATEWAY_URL"), os.Getenv("DATABASE_SUPERUSER_URL")
	if gw == "" || super == "" {
		t.Skip("set DATABASE_GATEWAY_URL and DATABASE_SUPERUSER_URL (or run `make test-db PKGS=./cmd/gateway/...`)")
	}
	t.Setenv("DEPLOY_ENV", "")
	ctx := context.Background()
	open := func(dsn string) *pgxpool.Pool {
		p, err := pgxpool.New(ctx, dsn)
		if err != nil {
			t.Fatalf("open pool: %v", err)
		}
		t.Cleanup(p.Close)
		return p
	}
	pool, superPool := open(gw), open(super)
	email := "unsub-wiring-" + uuid.NewString() + "@example.test"
	t.Cleanup(func() {
		_, _ = superPool.Exec(context.Background(), `DELETE FROM mail_unsubscribes WHERE email = $1`, email)
	})

	bodies := make(chan []byte, 1)
	goTrue := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies <- b
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"u","email":"x"}`)
	}))
	t.Cleanup(goTrue.Close)
	authURL, _ := url.Parse(goTrue.URL)
	site, _ := url.Parse("https://site.example")
	reg := registrationHandlers(authURL, site, 0, slog.New(slog.DiscardHandler), nil, noPendingInvite, pool)

	if rec := serveJSON(reg.Register, "/auth/register", fmt.Sprintf(`{"email":%q,"password":"correct-horse-1"}`, email)); rec.Code != http.StatusAccepted {
		t.Fatalf("register = %d %s, want 202", rec.Code, rec.Body.String())
	}
	var sent struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(<-bodies, &sent); err != nil {
		t.Fatal(err)
	}
	var want string
	if err := superPool.QueryRow(ctx, `SELECT token::text FROM mail_unsubscribes WHERE email = $1`, email).Scan(&want); err != nil {
		t.Fatalf("the sign-up left no token row: %v", err)
	}
	if sent.Data["unsubscribe"] != want {
		t.Errorf("sign-up data.unsubscribe = %v, want the table token %q", sent.Data["unsubscribe"], want)
	}
}

// main hands the gateway pool to both sign-up builders; a nil TokenFunc signs every account up without a link.
func TestGatewayMain_HandsThePoolTokensToBothSignups(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatalf("parse cmd/gateway/main.go: %v", err)
	}
	var reg, inv []string
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Recv != nil || fn.Name.Name != "main" {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			c, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			switch id, _ := c.Fun.(*ast.Ident); {
			case id != nil && id.Name == "registrationHandlers" && len(c.Args) == 7:
				reg = append(reg, types.ExprString(c.Args[6]))
			case id != nil && id.Name == "invitationHandlers" && len(c.Args) == 7:
				inv = append(inv, types.ExprString(c.Args[5]))
			}
			return true
		})
	}
	if len(reg) != 1 || reg[0] != "gwPool" {
		t.Errorf("registrationHandlers pool argument = %v, want [gwPool]", reg)
	}
	if len(inv) != 1 || inv[0] != "poolTokens(gwPool)" {
		t.Errorf("invitationHandlers TokenFunc argument = %v, want [poolTokens(gwPool)]", inv)
	}
}
