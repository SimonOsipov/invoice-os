package erp

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net"
	"reflect"
	"strings"
	"sync"
	"testing"

	secretmanager "cloud.google.com/go/secretmanager/apiv1"
	"cloud.google.com/go/secretmanager/apiv1/secretmanagerpb"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/SimonOsipov/invoice-os/internal/platform"
)

type smClient = secretmanager.Client

// lazyClient never dials: grpc connects on the first call.
func lazyClient(t *testing.T) *smClient {
	t.Helper()
	conn, err := grpc.NewClient("passthrough:///unused", grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	c, err := secretmanager.NewClient(ctx, option.WithGRPCConn(conn))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// smServer records each request by method; fail makes a method answer with a code and a message.
type smServer struct {
	secretmanagerpb.UnimplementedSecretManagerServiceServer
	mu    sync.Mutex
	calls []string
	reqs  map[string]any
	fail  map[string]error
}

func (s *smServer) rec(method string, req any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, method)
	s.reqs[method] = req
	return s.fail[method]
}

func (s *smServer) CreateSecret(_ context.Context, r *secretmanagerpb.CreateSecretRequest) (*secretmanagerpb.Secret, error) {
	return &secretmanagerpb.Secret{}, s.rec("Create", r)
}

func (s *smServer) AddSecretVersion(_ context.Context, r *secretmanagerpb.AddSecretVersionRequest) (*secretmanagerpb.SecretVersion, error) {
	return &secretmanagerpb.SecretVersion{Name: r.Parent + "/versions/7"}, s.rec("Add", r)
}

func (s *smServer) AccessSecretVersion(_ context.Context, r *secretmanagerpb.AccessSecretVersionRequest) (*secretmanagerpb.AccessSecretVersionResponse, error) {
	return &secretmanagerpb.AccessSecretVersionResponse{Name: "projects/9/secrets/x/versions/4", Payload: &secretmanagerpb.SecretPayload{Data: []byte("p")}}, s.rec("Access", r)
}

func (s *smServer) DisableSecretVersion(_ context.Context, r *secretmanagerpb.DisableSecretVersionRequest) (*secretmanagerpb.SecretVersion, error) {
	return &secretmanagerpb.SecretVersion{}, s.rec("Disable", r)
}

func (s *smServer) DeleteSecret(_ context.Context, r *secretmanagerpb.DeleteSecretRequest) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, s.rec("Delete", r)
}

func newGCPFixture(t *testing.T, labels map[string]string) (*gcpSecrets, *smServer) {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	srv := &smServer{reqs: map[string]any{}, fail: map[string]error{}}
	gs := grpc.NewServer()
	secretmanagerpb.RegisterSecretManagerServiceServer(gs, srv)
	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(gs.Stop)
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	c, err := secretmanager.NewClient(ctx, option.WithGRPCConn(conn))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return &gcpSecrets{c: c, project: "proj-x", region: "africa-south1", labels: labels}, srv
}

func TestGCPSecrets_RequestShapes(t *testing.T) {
	g, srv := newGCPFixture(t, map[string]string{"preview-pr": "12"})
	id := SecretName("slot-1", "u1")
	if err := g.Create(ctx, id, []byte("p")); err != nil {
		t.Fatal(err)
	}
	cs := srv.reqs["Create"].(*secretmanagerpb.CreateSecretRequest)
	reps := cs.GetSecret().GetReplication().GetUserManaged().GetReplicas()
	if cs.Parent != "projects/proj-x" || cs.SecretId != id || len(reps) != 1 || reps[0].Location != "africa-south1" ||
		!reflect.DeepEqual(cs.GetSecret().Labels, map[string]string{"preview-pr": "12"}) {
		t.Errorf("create = %v", cs)
	}
	if a := srv.reqs["Add"].(*secretmanagerpb.AddSecretVersionRequest); a.Parent != "projects/proj-x/secrets/"+id || string(a.Payload.Data) != "p" {
		t.Errorf("add = %v", a)
	}
	if _, v, err := g.Access(ctx, id); err != nil || v != "4" {
		t.Errorf("access version %q, %v", v, err)
	}
	if a := srv.reqs["Access"].(*secretmanagerpb.AccessSecretVersionRequest); a.Name != "projects/proj-x/secrets/"+id+"/versions/latest" {
		t.Errorf("access name = %q", a.Name)
	}
	if v, err := g.AddVersion(ctx, id, []byte("q")); err != nil || v != "7" {
		t.Errorf("AddVersion = %q, %v", v, err)
	}
	if err := g.DisableVersion(ctx, id, "3"); err != nil {
		t.Fatal(err)
	}
	if d := srv.reqs["Disable"].(*secretmanagerpb.DisableSecretVersionRequest); d.Name != "projects/proj-x/secrets/"+id+"/versions/3" {
		t.Errorf("disable name = %q", d.Name)
	}
	if err := g.Delete(ctx, id); err != nil {
		t.Fatal(err)
	}
	if d := srv.reqs["Delete"].(*secretmanagerpb.DeleteSecretRequest); d.Name != "projects/proj-x/secrets/"+id {
		t.Errorf("delete name = %q", d.Name)
	}
}

func TestGCPSecrets_CreateDeletesTheSecretWhenTheFirstVersionFails(t *testing.T) {
	g, srv := newGCPFixture(t, nil)
	srv.fail["Add"] = status.Error(codes.Unavailable, "down")
	if err := g.Create(ctx, "erp-grant-x", []byte("p")); err == nil {
		t.Fatal("Create succeeded")
	}
	d, ok := srv.reqs["Delete"].(*secretmanagerpb.DeleteSecretRequest)
	if !ok || d.Name != "projects/proj-x/secrets/erp-grant-x" {
		t.Errorf("delete request = %v", srv.reqs["Delete"])
	}
}

func TestGCPSecrets_DeleteNotFoundIsNil(t *testing.T) {
	g, srv := newGCPFixture(t, nil)
	srv.fail["Delete"] = status.Error(codes.NotFound, "gone")
	srv.fail["Access"] = status.Error(codes.NotFound, "gone")
	if err := g.Delete(ctx, "erp-grant-x"); err != nil {
		t.Errorf("Delete: %v", err)
	}
	if _, _, err := g.Access(ctx, "erp-grant-x"); !errors.Is(err, ErrSecretNotFound) {
		t.Errorf("Access: %v", err)
	}
}

func TestGCPSecrets_ErrorsCarryNoPayload(t *testing.T) {
	g, srv := newGCPFixture(t, nil)
	raw := make([]byte, 32)
	_, _ = rand.Read(raw)
	// Printable: gRPC mangles non-UTF-8 bytes in a status message, so raw random bytes could not leak into an error.
	raw = []byte(base64.RawURLEncoding.EncodeToString(raw))
	payload := string(raw)
	for _, m := range []string{"Create", "Add", "Access", "Disable", "Delete"} {
		srv.fail[m] = status.Error(codes.Internal, "echo "+payload)
	}
	_, _, aerr := g.Access(ctx, "i")
	_, verr := g.AddVersion(ctx, "i", raw)
	for name, err := range map[string]error{
		"create": g.Create(ctx, "i", raw), "access": aerr, "add": verr,
		"disable": g.DisableVersion(ctx, "i", "1"), "delete": g.Delete(ctx, "i"),
	} {
		if err == nil || strings.Contains(err.Error(), payload) || strings.Contains(err.Error(), string(raw[:8])) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestGCPSecrets_PreviewLabelFromDeployEnv(t *testing.T) {
	for env, want := range map[string]map[string]string{
		"pr-12":      {"preview-pr": "12"},
		"nightly":    {"preview-pr": "nightly"},
		"production": nil,
	} {
		if got := previewLabels(env, platform.Posture(env)); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: %v, want %v", env, got, want)
		}
	}
}
