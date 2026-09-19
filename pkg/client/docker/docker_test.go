package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jetstack/version-checker/pkg/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sirupsen/logrus"
)

type hostnameOverride struct {
	Host string
	RT   http.RoundTripper
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func (r *hostnameOverride) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Host != r.Host {
		if testing.Verbose() {
			fmt.Printf("Overriding URI from: %s to %s\n", req.Host, strings.TrimPrefix(r.Host, "http://"))
		}
		req.Host = strings.TrimPrefix(r.Host, "http://")
		req.URL.Host = strings.TrimPrefix(r.Host, "http://")
		req.URL.Scheme = "http"
	}
	// fmt.Printf("Req: %+v", req)
	return r.RT.RoundTrip(req)
}

func TestManifestDigest(t *testing.T) {
	requests := 0
	client := &Client{
		Client: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			requests++
			switch req.URL.Host {
			case "auth.docker.io":
				assert.Equal(t, "repository:testrepo/testimage:pull", req.URL.Query().Get("scope"))
				return &http.Response{
					StatusCode: http.StatusOK,
					Status:     "200 OK",
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader(`{"token":"registrytoken"}`)),
					Request:    req,
				}, nil
			case "registry.hub.docker.com":
				assert.Equal(t, http.MethodHead, req.Method)
				assert.Equal(t, "Bearer registrytoken", req.Header.Get("Authorization"))
				assert.Contains(t, req.Header.Get("Accept"), "manifest.list.v2+json")
				header := make(http.Header)
				header.Set("Docker-Content-Digest", "sha256:compound")
				return &http.Response{
					StatusCode: http.StatusOK,
					Status:     "200 OK",
					Header:     header,
					Body:       http.NoBody,
					Request:    req,
				}, nil
			default:
				t.Fatalf("unexpected request host %q", req.URL.Host)
				return nil, nil
			}
		})},
		log: logrus.NewEntry(logrus.New()),
	}

	digest, err := client.manifestDigest(context.Background(), "testrepo", "testimage", "v1.0.0")
	require.NoError(t, err)
	assert.Equal(t, "sha256:compound", digest)
	assert.Equal(t, 2, requests)
}

func TestDoRequestRefreshesExpiredToken(t *testing.T) {
	tagsRequests := 0
	loginRequests := 0
	client := &Client{
		Client: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			switch req.URL.String() {
			case "https://registry.hub.docker.com/v2/repositories/testrepo/testimage/tags":
				tagsRequests++
				if tagsRequests == 1 {
					assert.Equal(t, "Bearer expired-token", req.Header.Get("Authorization"))
					return &http.Response{
						StatusCode: http.StatusUnauthorized,
						Status:     "401 Unauthorized",
						Header:     make(http.Header),
						Body:       io.NopCloser(strings.NewReader(`{"detail":"token expired"}`)),
						Request:    req,
					}, nil
				}

				assert.Equal(t, "Bearer refreshed-token", req.Header.Get("Authorization"))
				return &http.Response{
					StatusCode: http.StatusOK,
					Status:     "200 OK",
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader(`{"results":[]}`)),
					Request:    req,
				}, nil

			case loginURL:
				loginRequests++
				assert.Equal(t, http.MethodPost, req.Method)
				body, err := io.ReadAll(req.Body)
				require.NoError(t, err)
				assert.JSONEq(t, `{"username":"testuser","password":"testpassword"}`, string(body))
				return &http.Response{
					StatusCode: http.StatusOK,
					Status:     "200 OK",
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader(`{"token":"refreshed-token"}`)),
					Request:    req,
				}, nil

			default:
				t.Fatalf("unexpected request URL %q", req.URL.String())
				return nil, nil
			}
		})},
		Options: Options{
			Username: "testuser",
			Password: "testpassword",
			Token:    "expired-token",
		},
		log: logrus.NewEntry(logrus.New()),
	}

	response, err := client.doRequest(context.Background(),
		"https://registry.hub.docker.com/v2/repositories/testrepo/testimage/tags")
	require.NoError(t, err)
	assert.Empty(t, response.Results)
	assert.Equal(t, 2, tagsRequests)
	assert.Equal(t, 1, loginRequests)
	assert.Equal(t, "refreshed-token", client.authToken())
}

func TestDoRequestReportsHTTPStatus(t *testing.T) {
	client := &Client{
		Client: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusUnauthorized,
				Status:     "401 Unauthorized",
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"detail":"access denied"}`)),
				Request:    req,
			}, nil
		})},
		Options: Options{Token: "configured-token"},
		log:     logrus.NewEntry(logrus.New()),
	}

	response, err := client.doRequest(context.Background(),
		"https://registry.hub.docker.com/v2/repositories/testrepo/testimage/tags")
	assert.Nil(t, response)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Docker Hub tags request returned 401 Unauthorized")
	assert.Contains(t, err.Error(), "access denied")
}

func TestTags(t *testing.T) {
	log := logrus.NewEntry(logrus.New())
	ctx := context.Background()

	t.Run("successful Tags fetch", func(t *testing.T) {

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/v2/repositories/testrepo/testimage/tags":
				require.NotEmpty(t, r.Header.Get("Authorization"))
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(TagResponse{
					Results: []Result{
						{
							Name:      "v1.0.0",
							Timestamp: time.Now().Add(-24 * time.Hour).Format(time.RFC3339Nano),
							Digest:    "sha256:abcdef",
							Images: []Image{
								{Digest: "sha256:child1", OS: "linux", Architecture: "amd64"},
							},
						},
						{
							Name:      "v2.0.0",
							Timestamp: time.Now().Add(-48 * time.Hour).Format(time.RFC3339Nano),
							Images: []Image{
								{Digest: "sha256:child2", OS: "linux", Architecture: "amd64"},
							},
						},
						{
							Name:      "v3.0.0",
							Timestamp: time.Now().Format(time.RFC3339Nano),
							Images: []Image{
								{Digest: "sha256:child3-amd64", OS: "linux", Architecture: "amd64"},
								{Digest: "sha256:child3-arm64", OS: "linux", Architecture: "arm64"},
							},
						},
					},
				})
			case "/token":
				assert.Equal(t, "registry.docker.io", r.URL.Query().Get("service"))
				assert.Equal(t, "repository:testrepo/testimage:pull", r.URL.Query().Get("scope"))
				_ = json.NewEncoder(w).Encode(AuthResponse{Token: "registrytoken"})
			case "/v2/testrepo/testimage/manifests/v3.0.0":
				assert.Equal(t, http.MethodHead, r.Method)
				assert.Equal(t, "Bearer registrytoken", r.Header.Get("Authorization"))
				assert.Contains(t, r.Header.Get("Accept"), "manifest.list.v2+json")
				w.Header().Set("Docker-Content-Digest", "sha256:compound3")
			default:
				http.NotFound(w, r)
			}
		}))
		defer server.Close()

		client := &Client{
			Client: server.Client(),
			log:    log,
			Options: Options{
				Token: "testtoken",
			},
		}

		client.Transport = &hostnameOverride{RT: server.Client().Transport, Host: server.URL}

		tags, err := client.Tags(ctx, "NOT USED!", "testrepo", "testimage")
		require.NoError(t, err)
		require.Len(t, tags, 3)

		assert.Equal(t, "v1.0.0", tags[0].Tag)
		assert.Equal(t, "sha256:abcdef", tags[0].SHA)
		assert.Equal(t, api.OS("linux"), tags[0].Children[0].OS)
		assert.Equal(t, api.Architecture("amd64"), tags[0].Children[0].Architecture)

		assert.Equal(t, "v2.0.0", tags[1].Tag)
		assert.NotEmpty(t, tags[1].SHA)

		assert.Equal(t, "v3.0.0", tags[2].Tag)
		assert.Equal(t, "sha256:compound3", tags[2].SHA)
		assert.Len(t, tags[2].Children, 2)
	})

	t.Run("error on invalid response", func(t *testing.T) {

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("invalid json"))
		}))
		defer server.Close()

		client := &Client{
			Client: server.Client(),
			log:    log,
			Options: Options{
				Token: "testtoken",
			},
		}
		client.Transport = &hostnameOverride{RT: server.Client().Transport, Host: server.URL}

		tags, err := client.Tags(ctx, "NOT USED!", "testrepo", "testimage")
		assert.Nil(t, tags)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "unexpected image tags response")
	})

	t.Run("error on non-200 status code", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte("not found"))
		}))
		defer server.Close()

		client := &Client{
			Client: server.Client(),
			log:    log,
			Options: Options{
				Token: "testtoken",
			},
		}
		client.Transport = &hostnameOverride{RT: server.Client().Transport, Host: server.URL}

		tags, err := client.Tags(ctx, "NOT USED!", "testrepo", "testimage")
		assert.Nil(t, tags)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "Docker Hub tags request returned 404 Not Found")
	})
}
