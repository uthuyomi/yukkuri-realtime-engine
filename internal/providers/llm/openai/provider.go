package openai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/uthuyomi/yukkuri-realtime-engine/internal/providers/llm"
)

const defaultBaseURL = "https://api.openai.com/v1"

type Config struct {
	APIKey  string
	Model   string
	BaseURL string
}

type Provider struct {
	apiKey     string
	model      string
	baseURL    string
	httpClient *http.Client
}

func New(config Config) (*Provider, error) {
	if config.APIKey == "" {
		return nil, errors.New("OpenAI API key is required")
	}

	if config.Model == "" {
		config.Model = "gpt-5.6-luna"
	}

	if config.BaseURL == "" {
		config.BaseURL = defaultBaseURL
	}

	return &Provider{
		apiKey:     config.APIKey,
		model:      config.Model,
		baseURL:    strings.TrimRight(config.BaseURL, "/"),
		httpClient: &http.Client{},
	}, nil
}

func (p *Provider) Name() string {
	return "openai"
}

type responsesRequest struct {
	Model        string         `json:"model"`
	Input        []inputMessage `json:"input"`
	Stream       bool           `json:"stream"`
	Instructions string         `json:"instructions,omitempty"`
}

type inputMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func (p *Provider) Generate(
	ctx context.Context,
	req llm.Request,
) (llm.Stream, error) {
	messages := make(
		[]inputMessage,
		0,
		len(req.Messages),
	)

	for _, message := range req.Messages {
		messages = append(
			messages,
			inputMessage{
				Role:    message.Role,
				Content: message.Content,
			},
		)
	}

	body, err := json.Marshal(
		responsesRequest{
			Model:  p.model,
			Input:  messages,
			Stream: true,
			Instructions: "あなたは音声会話アシスタントです。" +
				"自然な日本語で簡潔に応答してください。" +
				"回答は音声合成されるため、Markdownを必要以上に使わないでください。",
		},
	)
	if err != nil {
		return nil, fmt.Errorf(
			"encode OpenAI request: %w",
			err,
		)
	}

	httpRequest, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		p.baseURL+"/responses",
		bytes.NewReader(body),
	)
	if err != nil {
		return nil, fmt.Errorf(
			"create OpenAI request: %w",
			err,
		)
	}

	httpRequest.Header.Set(
		"Authorization",
		"Bearer "+p.apiKey,
	)

	httpRequest.Header.Set(
		"Content-Type",
		"application/json",
	)

	httpRequest.Header.Set(
		"Accept",
		"text/event-stream",
	)

	response, err :=
		p.httpClient.Do(httpRequest)

	if err != nil {
		return nil, fmt.Errorf(
			"send OpenAI request: %w",
			err,
		)
	}

	if response.StatusCode < 200 ||
		response.StatusCode >= 300 {

		defer response.Body.Close()

		errorBody, _ :=
			io.ReadAll(
				io.LimitReader(
					response.Body,
					64*1024,
				),
			)

		return nil, fmt.Errorf(
			"OpenAI API returned %s: %s",
			response.Status,
			strings.TrimSpace(
				string(errorBody),
			),
		)
	}

	return &responseStream{
		body:    response.Body,
		scanner: bufio.NewScanner(response.Body),
	}, nil
}

type responseStream struct {
	body    io.ReadCloser
	scanner *bufio.Scanner
}

type streamEvent struct {
	Type  string `json:"type"`
	Delta string `json:"delta"`

	Response struct {
		Error *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	} `json:"response"`
}

func (s *responseStream) Recv() (
	llm.Delta,
	error,
) {
	for s.scanner.Scan() {
		line := s.scanner.Text()

		if !strings.HasPrefix(
			line,
			"data:",
		) {
			continue
		}

		data := strings.TrimSpace(
			strings.TrimPrefix(
				line,
				"data:",
			),
		)

		if data == "" {
			continue
		}

		if data == "[DONE]" {
			return llm.Delta{}, io.EOF
		}

		var event streamEvent

		if err := json.Unmarshal(
			[]byte(data),
			&event,
		); err != nil {
			return llm.Delta{}, fmt.Errorf(
				"decode OpenAI stream event: %w",
				err,
			)
		}

		switch event.Type {
		case "response.output_text.delta":
			if event.Delta == "" {
				continue
			}

			return llm.Delta{
				Text: event.Delta,
			}, nil

		case "response.completed":
			return llm.Delta{}, io.EOF

		case "response.failed":
			if event.Response.Error != nil {
				return llm.Delta{}, fmt.Errorf(
					"OpenAI response failed: %s: %s",
					event.Response.Error.Code,
					event.Response.Error.Message,
				)
			}

			return llm.Delta{},
				errors.New(
					"OpenAI response failed",
				)
		}
	}

	if err := s.scanner.Err(); err != nil {
		return llm.Delta{}, fmt.Errorf(
			"read OpenAI stream: %w",
			err,
		)
	}

	return llm.Delta{}, io.EOF
}

func (s *responseStream) Close() error {
	return s.body.Close()
}