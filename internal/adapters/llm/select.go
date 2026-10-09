// Package llm selects and builds the LLM backend for the categorizer from
// environment variables.
//
// Environment:
//
//	CATEGORIZER_PROVIDER  "openai" or "anthropic": force a backend. Unset: auto-detect.
//	ANTHROPIC_API_KEY     Anthropic key (alias CLAUDE_API_KEY).
//	ANTHROPIC_MODEL       Anthropic model; default DefaultAnthropicModel.
//	OPENAI_API_KEY        OpenAI key (alias OPENAI_APIKEY).
//	OPENAI_MODEL          OpenAI model; required, there is no default.
//	ANTHROPIC_BASE_URL, OPENAI_BASE_URL
//	                      Override the API host. For tests only.
//
// Auto-detection: with only one key present that backend is used. When both
// keys are present and CATEGORIZER_PROVIDER is unset, Anthropic wins (its
// model has a default; OpenAI's must be configured), deterministically.
package llm

import (
	"errors"
	"fmt"
	"strings"

	"github.com/postalservice14/itemize-ynab/internal/adapters/llm/anthropic"
	"github.com/postalservice14/itemize-ynab/internal/adapters/llm/openai"
	"github.com/postalservice14/itemize-ynab/internal/domain/categorizer"
)

// DefaultAnthropicModel is used when ANTHROPIC_MODEL is unset.
const DefaultAnthropicModel = "claude-haiku-5-5"

// Backend names returned by NewChatClientFromEnv.
const (
	NameAnthropic = "anthropic"
	NameOpenAI    = "openai"
)

// ErrConfig marks an environment configuration problem. The CLI maps it to
// exit code 1. Messages name variables, never values.
var ErrConfig = errors.New("llm configuration error")

func configErr(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrConfig, fmt.Sprintf(format, args...))
}

// NewChatClientFromEnv builds the ChatClient chosen by the environment and
// returns it with the backend name ("anthropic" or "openai").
func NewChatClientFromEnv(getenv func(string) string) (categorizer.ChatClient, string, error) {
	get := func(keys ...string) string {
		for _, k := range keys {
			if v := strings.TrimSpace(getenv(k)); v != "" {
				return v
			}
		}
		return ""
	}
	anthropicKey := get("ANTHROPIC_API_KEY", "CLAUDE_API_KEY")
	openaiKey := get("OPENAI_API_KEY", "OPENAI_APIKEY")

	name, err := chooseBackend(get("CATEGORIZER_PROVIDER"), anthropicKey, openaiKey)
	if err != nil {
		return nil, "", err
	}
	if name == NameAnthropic {
		return newAnthropic(anthropicKey, get), NameAnthropic, nil
	}
	model := get("OPENAI_MODEL")
	if model == "" {
		return nil, "", configErr("OPENAI_MODEL is required when using the openai backend (there is no default model)")
	}
	var opts []openai.Option
	if u := get("OPENAI_BASE_URL"); u != "" {
		opts = append(opts, openai.WithBaseURL(u))
	}
	return openai.NewClient(openaiKey, model, opts...), NameOpenAI, nil
}

func newAnthropic(key string, get func(...string) string) categorizer.ChatClient {
	model := get("ANTHROPIC_MODEL")
	if model == "" {
		model = DefaultAnthropicModel
	}
	var opts []anthropic.Option
	if u := get("ANTHROPIC_BASE_URL"); u != "" {
		opts = append(opts, anthropic.WithBaseURL(u))
	}
	return anthropic.NewClient(key, model, opts...)
}

func chooseBackend(forced, anthropicKey, openaiKey string) (string, error) {
	switch strings.ToLower(forced) {
	case "":
		switch {
		case anthropicKey != "":
			return NameAnthropic, nil
		case openaiKey != "":
			return NameOpenAI, nil
		default:
			return "", configErr("no LLM API key found: set ANTHROPIC_API_KEY (or CLAUDE_API_KEY) or OPENAI_API_KEY (or OPENAI_APIKEY)")
		}
	case NameAnthropic:
		if anthropicKey == "" {
			return "", configErr("CATEGORIZER_PROVIDER=anthropic but ANTHROPIC_API_KEY (or CLAUDE_API_KEY) is not set")
		}
		return NameAnthropic, nil
	case NameOpenAI:
		if openaiKey == "" {
			return "", configErr("CATEGORIZER_PROVIDER=openai but OPENAI_API_KEY (or OPENAI_APIKEY) is not set")
		}
		return NameOpenAI, nil
	default:
		return "", configErr("unknown CATEGORIZER_PROVIDER %q (want openai or anthropic)", forced)
	}
}
