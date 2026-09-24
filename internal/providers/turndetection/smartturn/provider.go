package smartturn

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"time"

	"github.com/uthuyomi/yukkuri-realtime-engine/internal/providers/turndetection"
)

type Provider struct {
	endpoint string
	client   *http.Client
}

func New(endpoint string) (*Provider, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "http" || (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost" && u.Hostname() != "::1") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("turn detector endpoint must be a loopback HTTP URL")
	}
	return &Provider{endpoint: endpoint, client: &http.Client{Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func (*Provider) Name() string               { return "smart-turn-v3.2" }
func (*Provider) AudioWindow() time.Duration { return 8 * time.Second }

func (p *Provider) Detect(ctx context.Context, req turndetection.Request) (turndetection.Result, error) {
	if len(req.Audio) == 0 || len(req.Audio)%2 != 0 || len(req.Audio) > 8*32000 {
		return turndetection.Result{}, fmt.Errorf("invalid detector PCM window")
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, bytes.NewReader(req.Audio))
	if err != nil {
		return turndetection.Result{}, err
	}
	r.Header.Set("Content-Type", "application/octet-stream")
	resp, err := p.client.Do(r)
	if err != nil {
		return turndetection.Result{}, fmt.Errorf("turn inference: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return turndetection.Result{}, fmt.Errorf("turn inference HTTP %d: %s", resp.StatusCode, b)
	}
	var data struct {
		Probability *float64 `json:"probability"`
		Complete    *bool    `json:"complete"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&data); err != nil {
		return turndetection.Result{}, fmt.Errorf("turn inference response: %w", err)
	}
	if data.Probability == nil || data.Complete == nil || math.IsNaN(*data.Probability) || *data.Probability < 0 || *data.Probability > 1 {
		return turndetection.Result{}, fmt.Errorf("invalid turn inference result")
	}
	return turndetection.Result{Complete: *data.Complete, Probability: *data.Probability}, nil
}
