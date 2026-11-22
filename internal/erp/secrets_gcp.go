package erp

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"cloud.google.com/go/compute/metadata"
	secretmanager "cloud.google.com/go/secretmanager/apiv1"
	"cloud.google.com/go/secretmanager/apiv1/secretmanagerpb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/SimonOsipov/invoice-os/internal/platform"
)

// Seams for the boot read and the client, so a test needs neither a metadata server nor credentials.
var (
	metadataGet = metadata.GetWithContext
	newSMClient = func(ctx context.Context) (*secretmanager.Client, error) { return secretmanager.NewClient(ctx) }
)

type gcpSecrets struct {
	c       *secretmanager.Client
	project string
	region  string
	labels  map[string]string
}

// previewLabels marks a preview's secrets so preview.sh teardown deletes them with the PR.
func previewLabels(deployEnv string, posture platform.PostureKind) map[string]string {
	switch posture {
	case platform.PosturePreview:
		return map[string]string{"preview-pr": strings.TrimPrefix(deployEnv, "pr-")}
	case platform.PostureNightly:
		return map[string]string{"preview-pr": "nightly"}
	}
	return nil
}

func newGCPSecretsFromMetadata(ctx context.Context, labels map[string]string) (SecretStore, error) {
	project, err := metadataGet(ctx, "project/project-id")
	if err != nil || project == "" {
		return nil, errors.New("erp: secret store: cannot read the project id from the metadata server")
	}
	// Cloud Run answers projects/<number>/regions/<region>.
	region, err := metadataGet(ctx, "instance/region")
	if err != nil || region == "" {
		return nil, errors.New("erp: secret store: cannot read the region from the metadata server")
	}
	c, err := newSMClient(ctx)
	if err != nil {
		return nil, errors.New("erp: secret store: cannot build the Secret Manager client")
	}
	return &gcpSecrets{c: c, project: project, region: region[strings.LastIndex(region, "/")+1:], labels: labels}, nil
}

// gcpErr keeps the operation and the gRPC code only: a server message could echo a payload.
func gcpErr(op string, err error) error {
	return fmt.Errorf("erp: secret store %s: %s", op, status.Code(err))
}

func (g *gcpSecrets) secretPath(id string) string { return "projects/" + g.project + "/secrets/" + id }

func versionOf(name string) string { return name[strings.LastIndex(name, "/")+1:] }

func (g *gcpSecrets) Create(ctx context.Context, id string, payload []byte) error {
	_, err := g.c.CreateSecret(ctx, &secretmanagerpb.CreateSecretRequest{
		Parent:   "projects/" + g.project,
		SecretId: id,
		Secret: &secretmanagerpb.Secret{
			Labels: g.labels,
			Replication: &secretmanagerpb.Replication{Replication: &secretmanagerpb.Replication_UserManaged_{
				UserManaged: &secretmanagerpb.Replication_UserManaged{
					Replicas: []*secretmanagerpb.Replication_UserManaged_Replica{{Location: g.region}},
				},
			}},
		},
	})
	if err != nil {
		return gcpErr("create", err)
	}
	if _, err := g.AddVersion(ctx, id, payload); err != nil {
		// An undeleted empty secret would block a retry under the same id.
		_ = g.Delete(context.WithoutCancel(ctx), id)
		return err
	}
	return nil
}

func (g *gcpSecrets) Access(ctx context.Context, id string) ([]byte, string, error) {
	resp, err := g.c.AccessSecretVersion(ctx, &secretmanagerpb.AccessSecretVersionRequest{Name: g.secretPath(id) + "/versions/latest"})
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return nil, "", ErrSecretNotFound
		}
		return nil, "", gcpErr("access", err)
	}
	return resp.GetPayload().GetData(), versionOf(resp.GetName()), nil
}

func (g *gcpSecrets) AddVersion(ctx context.Context, id string, payload []byte) (string, error) {
	v, err := g.c.AddSecretVersion(ctx, &secretmanagerpb.AddSecretVersionRequest{
		Parent:  g.secretPath(id),
		Payload: &secretmanagerpb.SecretPayload{Data: payload},
	})
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return "", ErrSecretNotFound
		}
		return "", gcpErr("add version", err)
	}
	return versionOf(v.GetName()), nil
}

func (g *gcpSecrets) DisableVersion(ctx context.Context, id, version string) error {
	_, err := g.c.DisableSecretVersion(ctx, &secretmanagerpb.DisableSecretVersionRequest{Name: g.secretPath(id) + "/versions/" + version})
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return ErrSecretNotFound
		}
		return gcpErr("disable version", err)
	}
	return nil
}

func (g *gcpSecrets) Delete(ctx context.Context, id string) error {
	err := g.c.DeleteSecret(ctx, &secretmanagerpb.DeleteSecretRequest{Name: g.secretPath(id)})
	if err != nil && status.Code(err) != codes.NotFound {
		return gcpErr("delete", err)
	}
	return nil
}
