package sbxclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"sandbox/pkg/api"
)

type Client struct {
	baseURL string
	client  *http.Client
}

func New(baseURL string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		client:  &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *Client) Create(ctx context.Context, req api.CreateSandboxRequest) (*api.CreateSandboxResponse, error) {
	var resp api.CreateSandboxResponse
	if err := c.do(ctx, http.MethodPost, "/sandboxes", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) Exec(ctx context.Context, id string, req api.ExecRequest) (*api.ExecResponse, error) {
	var resp api.ExecResponse
	path := fmt.Sprintf("/sandboxes/%s/exec", id)
	if err := c.do(ctx, http.MethodPost, path, req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) Delete(ctx context.Context, id string) error {
	path := fmt.Sprintf("/sandboxes/%s", id)
	return c.do(ctx, http.MethodDelete, path, nil, nil)
}

func (c *Client) Status(ctx context.Context, id string) (map[string]string, error) {
	path := fmt.Sprintf("/sandboxes/%s", id)
	var resp map[string]string
	if err := c.do(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

func (c *Client) do(ctx context.Context, method, path string, reqBody any, out any) error {
	var body io.Reader
	if reqBody != nil {
		buf, err := json.Marshal(reqBody)
		if err != nil {
			return err
		}
		body = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return err
	}
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
