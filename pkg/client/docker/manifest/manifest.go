package docker

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/sirupsen/logrus"
	"io/ioutil"
	"net/http"
	"net/url"
	"time"
)

const (
	tokenURL = "https://auth.docker.io/token"
	manifestURL = "https://registry.hub.docker.com/v2/%s/%s/manifests/%s"
)

type Options struct {
	Username string
	Password string
}

type AuthResponse struct {
	Token string `json:"token"`
}

type ManifestClient struct {
	*http.Client
	Options
}

func New(options Options) (*ManifestClient, error) {
	client := &http.Client{
		Timeout: time.Second * 5,
	}	

	return &ManifestClient{
		Options: options,
		Client:  client,
	}, nil
}

func (c *ManifestClient) Digest(ctx context.Context, repo, image, tag string) (string, error) {
	token, err := c.getAuthToken(ctx, repo, image)
	if err != nil {
		return "", err
	}

	url := fmt.Sprintf(manifestURL, repo, image, tag)

	req, err := http.NewRequest("HEAD", url, nil)
	if err != nil {
		return "", err
	}

	req = req.WithContext(ctx)
	req.Header.Add("Authorization", "Bearer " + token)
	req.Header.Add("Accept", "application/vnd.docker.distribution.manifest.v2+json")
	req.Header.Add("Accept", "application/vnd.docker.distribution.manifest.list.v2+json")
	req.Header.Add("Accept", "application/vnd.docker.distribution.manifest.v1+json")

	logrus.WithField("url", url).Debug("Doing a HEAD request to fetch a digest")

	res, err := c.Client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to get docker manifest: %s", err)
	}

	defer res.Body.Close()

	if res.StatusCode != 200 {
		wwwAuthHeader := res.Header.Get("www-authenticate")
		if wwwAuthHeader == "" {
			wwwAuthHeader = "not present"
		}
		return "", fmt.Errorf("registry responded to head request to %s with %q, auth: %q", url, res.Status, wwwAuthHeader)
	}

	logrus.WithField("digest", res.Header.Get("Docker-Content-Digest")).Debug("Retrieved digest")

	return res.Header.Get("Docker-Content-Digest"), nil
}

func authUrl(repo, image string) (string, error) {
	u, err := url.Parse(tokenURL)
	if err != nil {
		return "", err
	}

	scope := fmt.Sprintf("repository:%s/%s:pull", repo, image)

	q := u.Query()
	q.Set("service", "registry.docker.io")
	q.Set("scope", scope)
	u.RawQuery = q.Encode()

	return u.String(), nil
}

func (c *ManifestClient) getAuthToken(ctx context.Context, repo, image string) (string, error) {
	url, err := authUrl(repo, image)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}

	if c.Options.Username != "" && c.Options.Password != "" {
		ba := []byte(fmt.Sprintf("%s:%s", c.Options.Username, c.Options.Password))

		req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString(ba))
	}

	req = req.WithContext(ctx)

	resp, err := c.Client.Do(req)
	if err != nil {
		return "", err
	}

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	if resp.StatusCode != http.StatusOK {
		return "", errors.New(string(body))
	}

	response := new(AuthResponse)
	if err := json.Unmarshal(body, response); err != nil {
		return "", err
	}

	return response.Token, nil
}
