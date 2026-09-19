package ghcr

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/google/go-github/v70/github"
	"github.com/jarcoal/httpmock"
	"github.com/jetstack/version-checker/pkg/api"
	"github.com/stretchr/testify/assert"
)

type tagListerFunc func(context.Context, string, string, string) ([]api.ImageTag, error)

func (f tagListerFunc) Tags(ctx context.Context, host, repo, image string) ([]api.ImageTag, error) {
	return f(ctx, host, repo, image)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func registryResponse(req *http.Request, status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}
}

func setup() {
	httpmock.Activate()
}

func teardown() {
	httpmock.DeactivateAndReset()
}

func registerCommonResponders() {
	httpmock.RegisterResponder("GET", "https://api.github.com/users/test-user-owner",
		func(req *http.Request) (*http.Response, error) {
			return httpmock.NewStringResponse(200, `{"type":"User"}`), nil
		})
	httpmock.RegisterResponder("GET", "https://api.github.com/users/test-org-owner",
		func(req *http.Request) (*http.Response, error) {
			return httpmock.NewStringResponse(200, `{"type":"Organization"}`), nil
		})
}

func registerTagResponders() {
	httpmock.RegisterResponder("GET", "https://api.github.com/users/test-user-owner/packages/container/test-repo/versions",
		func(req *http.Request) (*http.Response, error) {
			return httpmock.NewStringResponse(200, `[
				{
					"name": "sha123",
					"metadata": {
						"container": {
							"tags": ["tag1", "tag2"]
						}
					},
					"created_at": "2023-07-08T12:34:56Z"
				}
			]`), nil
		})
	httpmock.RegisterResponder("GET", "https://api.github.com/orgs/test-org-owner/packages/container/test-repo/versions",
		func(req *http.Request) (*http.Response, error) {
			return httpmock.NewStringResponse(200, `[
				{
					"name": "sha123",
					"metadata": {
						"container": {
							"tags": ["tag1", "tag2"]
						}
					},
					"created_at": "2023-07-08T12:34:56Z"
				}
			]`), nil
		})
}

func TestClient_Tags(t *testing.T) {
	setup()
	defer teardown()

	ctx := context.Background()
	host := "ghcr.io"

	t.Run("successful tags fetch", func(t *testing.T) {
		httpmock.Reset()
		registerCommonResponders()
		registerTagResponders()

		client := New(Options{Token: "test-token"})
		client.client = github.NewClient(nil) // Use the default HTTP client

		tags, err := client.Tags(ctx, host, "test-user-owner", "test-repo")
		assert.NoError(t, err)
		assert.Len(t, tags, 2)
		assert.ElementsMatch(t, []string{"tag1", "tag2"}, []string{tags[0].Tag, tags[1].Tag})
	})

	t.Run("failed to fetch owner type", func(t *testing.T) {
		httpmock.Reset()
		httpmock.RegisterResponder("GET", "https://api.github.com/users/test-user-owner",
			func(req *http.Request) (*http.Response, error) {
				return httpmock.NewStringResponse(404, `{"message": "Not Found"}`), nil
			})

		client := New(Options{Token: "test-token"})
		client.client = github.NewClient(nil) // Use the default HTTP client

		_, err := client.Tags(ctx, host, "test-user-owner", "test-repo")
		assert.Error(t, err)
	})

	t.Run("token not set uses anonymous registry client", func(t *testing.T) {
		client := New(Options{})
		client.anonymous = tagListerFunc(func(_ context.Context, gotHost, gotOwner, gotRepo string) ([]api.ImageTag, error) {
			assert.Equal(t, "ghcr.io", gotHost)
			assert.Equal(t, "test-user-owner", gotOwner)
			assert.Equal(t, "test-repo", gotRepo)
			return []api.ImageTag{{Tag: "latest"}}, nil
		})

		tags, err := client.Tags(ctx, host, "test-user-owner", "test-repo")
		assert.NoError(t, err)
		assert.Equal(t, []api.ImageTag{{Tag: "latest"}}, tags)
	})

	t.Run("token set, authorization header sent", func(t *testing.T) {
		token := "test-token"
		httpmock.Reset()
		httpmock.RegisterResponder("GET", "https://api.github.com/users/test-user-owner",
			func(req *http.Request) (*http.Response, error) {
				authHeader := req.Header.Get("Authorization")
				expectedAuthHeader := "Bearer " + token
				if authHeader != expectedAuthHeader {
					t.Errorf("expected Authorization header %s, got %s", expectedAuthHeader, authHeader)
				}
				return httpmock.NewStringResponse(200, `{"type":"User"}`), nil
			})

		registerTagResponders()

		client := New(Options{Token: token})

		_, err := client.Tags(ctx, host, "test-user-owner", "test-repo")
		assert.NoError(t, err)
	})

	t.Run("ownerType returns user", func(t *testing.T) {
		httpmock.Reset()
		registerCommonResponders()
		registerTagResponders()

		client := New(Options{Token: "test-token"})
		client.client = github.NewClient(nil) // Use the default HTTP client

		tags, err := client.Tags(ctx, host, "test-user-owner", "test-repo")
		assert.NoError(t, err)
		assert.Len(t, tags, 2)
		assert.ElementsMatch(t, []string{"tag1", "tag2"}, []string{tags[0].Tag, tags[1].Tag})
	})

	t.Run("ownerType returns org", func(t *testing.T) {
		httpmock.Reset()
		registerCommonResponders()
		registerTagResponders()

		client := New(Options{Token: "test-token"})
		client.client = github.NewClient(nil) // Use the default HTTP client

		tags, err := client.Tags(ctx, host, "test-org-owner", "test-repo")
		assert.NoError(t, err)
		assert.Len(t, tags, 2)
		assert.ElementsMatch(t, []string{"tag1", "tag2"}, []string{tags[0].Tag, tags[1].Tag})
	})
}

func TestAnonymousTagsBearerRenewalAndPagination(t *testing.T) {
	tokenRequests := 0
	tagRequests := 0
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/v2/":
			resp := registryResponse(req, http.StatusUnauthorized, `{"errors":[{"code":"UNAUTHORIZED"}]}`)
			resp.Header.Set("WWW-Authenticate", `Bearer realm="https://ghcr.io/token",service="ghcr.io",scope="repository:matter-js/python-matter-server:pull"`)
			return resp, nil

		case "/token":
			tokenRequests++
			assert.Equal(t, "ghcr.io", req.URL.Query().Get("service"))
			assert.Equal(t, "repository:matter-js/python-matter-server:pull", req.URL.Query().Get("scope"))
			return registryResponse(req, http.StatusOK,
				fmt.Sprintf(`{"token":"token-%d","expires_in":3600}`, tokenRequests)), nil

		case "/v2/matter-js/python-matter-server/tags/list":
			tagRequests++
			switch tagRequests {
			case 1:
				assert.Equal(t, "Bearer token-1", req.Header.Get("Authorization"))
				resp := registryResponse(req, http.StatusOK,
					`{"name":"matter-js/python-matter-server","tags":[]}`)
				resp.Header.Set("Link", `</v2/matter-js/python-matter-server/tags/list?n=1000&last=page-1>; rel="next"`)
				return resp, nil
			case 2:
				assert.Equal(t, "Bearer token-1", req.Header.Get("Authorization"))
				resp := registryResponse(req, http.StatusUnauthorized,
					`{"errors":[{"code":"UNAUTHORIZED","message":"token expired"}]}`)
				resp.Header.Set("WWW-Authenticate", `Bearer realm="https://ghcr.io/token",service="ghcr.io",scope="repository:matter-js/python-matter-server:pull"`)
				return resp, nil
			default:
				assert.Equal(t, "Bearer token-2", req.Header.Get("Authorization"))
				return registryResponse(req, http.StatusOK,
					`{"name":"matter-js/python-matter-server","tags":[]}`), nil
			}

		default:
			t.Fatalf("unexpected anonymous GHCR request %s %s", req.Method, req.URL.String())
			return nil, nil
		}
	})

	client := New(Options{Transporter: transport})
	tags, err := client.Tags(context.Background(), "ghcr.io", "matter-js", "python-matter-server")
	assert.NoError(t, err)
	assert.Empty(t, tags)
	assert.Equal(t, 2, tokenRequests)
	assert.Equal(t, 3, tagRequests)
}

func TestAnonymousTagsReportsRegistryStatus(t *testing.T) {
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/v2/":
			resp := registryResponse(req, http.StatusUnauthorized, `{"errors":[{"code":"UNAUTHORIZED"}]}`)
			resp.Header.Set("WWW-Authenticate", `Bearer realm="https://ghcr.io/token",service="ghcr.io",scope="repository:homeassistant-ai/ha-mcp:pull"`)
			return resp, nil
		case "/token":
			return registryResponse(req, http.StatusOK, `{"token":"anonymous-token","expires_in":3600}`), nil
		case "/v2/homeassistant-ai/ha-mcp/tags/list":
			return registryResponse(req, http.StatusForbidden,
				`{"errors":[{"code":"DENIED","message":"package access denied"}]}`), nil
		default:
			t.Fatalf("unexpected anonymous GHCR request %s %s", req.Method, req.URL.String())
			return nil, nil
		}
	})

	client := New(Options{Transporter: transport})
	tags, err := client.Tags(context.Background(), "ghcr.io", "homeassistant-ai", "ha-mcp")
	assert.Nil(t, tags)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "403 Forbidden")
	assert.Contains(t, err.Error(), "package access denied")
}
