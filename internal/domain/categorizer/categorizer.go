package categorizer

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/postalservice14/itemize-ynab/internal/domain/order"
)

const (
	// maxChunk is the most items sent in one LLM call.
	maxChunk = 60
	// maxTokens bounds one reply (60 items of a few tokens each, with headroom).
	maxTokens = 4096
)

var (
	// ErrNoCategories is returned when no category names were supplied.
	ErrNoCategories = errors.New("categorizer: no categories to choose from")
	// ErrInvalidResponse matches *InvalidResponseError.
	ErrInvalidResponse = errors.New("categorizer: model returned an invalid categorization")
)

// InvalidResponseError reports that the model still gave an unusable answer
// after one repair attempt. No category was assigned to any item.
type InvalidResponseError struct {
	Problems []string
}

// Error implements error.
func (e *InvalidResponseError) Error() string {
	return ErrInvalidResponse.Error() + " after one repair attempt: " + strings.Join(e.Problems, "; ")
}

// Is matches ErrInvalidResponse.
func (e *InvalidResponseError) Is(target error) bool { return target == ErrInvalidResponse }

// Assignment is the category chosen for one item.
type Assignment struct {
	ItemIndex int
	Name      string
	// Category is the final YNAB category name, after overrides.
	Category string
	// ModelCategory is the canonical category the model chose, before overrides.
	ModelCategory string
}

// Categorizer assigns items to categories through a ChatClient.
type Categorizer struct {
	chat      ChatClient
	overrides map[string]string
}

// Option configures a Categorizer.
type Option func(*Categorizer)

// WithOverrides maps categorizer output names to YNAB category names (applied
// after categorization). The keys are also acceptable model outputs.
func WithOverrides(overrides map[string]string) Option {
	return func(c *Categorizer) { c.overrides = overrides }
}

// New builds a Categorizer.
func New(chat ChatClient, opts ...Option) *Categorizer {
	c := &Categorizer{chat: chat}
	for _, o := range opts {
		o(c)
	}
	return c
}

// Categorize gives every item exactly one category. allowed is the set of YNAB
// category names the model may choose from; override keys are accepted too.
// It makes one LLM call per 60 items, and on an invalid reply at most one
// repair call. Any failure returns an error and no assignments: there is no
// default category.
func (c *Categorizer) Categorize(ctx context.Context, items []order.Item, allowed []string) ([]Assignment, error) {
	v := newVocab(allowed, c.overrides)
	if len(v.names) == 0 {
		return nil, ErrNoCategories
	}
	if len(items) == 0 {
		return nil, nil
	}
	system := systemPrompt(v.names)
	out := make([]Assignment, 0, len(items))
	for start := 0; start < len(items); start += maxChunk {
		end := min(start+maxChunk, len(items))
		cats, err := c.categorizeChunk(ctx, system, items[start:end], v)
		if err != nil {
			return nil, err
		}
		for i, cat := range cats {
			out = append(out, Assignment{
				ItemIndex:     start + i,
				Name:          items[start+i].Name,
				Category:      ApplyOverrides(cat, c.overrides),
				ModelCategory: cat,
			})
		}
	}
	return out, nil
}

func (c *Categorizer) categorizeChunk(ctx context.Context, system string, chunk []order.Item, v vocab) ([]string, error) {
	user := userPrompt(chunk)
	text, err := c.call(ctx, system, user)
	if err != nil {
		return nil, err
	}
	cats, problems := interpret(text, chunk, v)
	if len(problems) == 0 {
		return cats, nil
	}
	text, err = c.call(ctx, system, repairPrompt(user, text, problems))
	if err != nil {
		return nil, err
	}
	cats, problems = interpret(text, chunk, v)
	if len(problems) > 0 {
		return nil, &InvalidResponseError{Problems: problems}
	}
	return cats, nil
}

func (c *Categorizer) call(ctx context.Context, system, user string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	text, err := c.chat.Chat(ctx, ChatRequest{System: system, User: user, MaxTokens: maxTokens})
	if err != nil {
		return "", fmt.Errorf("categorizer: LLM call failed: %w", err)
	}
	return text, nil
}

// ApplyOverrides maps a categorizer output name to its configured YNAB
// category (key match is case-insensitive); names without an override pass
// through unchanged.
func ApplyOverrides(category string, overrides map[string]string) string {
	for k, v := range overrides {
		if strings.EqualFold(k, category) {
			return v
		}
	}
	return category
}

// vocab is the set of acceptable model outputs with canonical spellings.
type vocab struct {
	names []string
	canon map[string]string
}

func newVocab(allowed []string, overrides map[string]string) vocab {
	v := vocab{canon: map[string]string{}}
	add := func(n string) {
		key := strings.ToLower(n)
		if n == "" || v.canon[key] != "" {
			return
		}
		v.canon[key] = n
		v.names = append(v.names, n)
	}
	for _, n := range allowed {
		add(n)
	}
	keys := make([]string, 0, len(overrides))
	for k := range overrides {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		add(k)
	}
	return v
}

func (v vocab) canonical(name string) (string, bool) {
	c, ok := v.canon[strings.ToLower(name)]
	return c, ok
}
