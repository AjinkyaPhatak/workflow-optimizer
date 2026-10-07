package observability

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
)

// ModelPrice is a model's price in USD per million tokens.
type ModelPrice struct {
	InputPerMillion  float64 `json:"input_per_million"`
	OutputPerMillion float64 `json:"output_per_million"`
}

// Pricing estimates LLM cost from token usage. The table is configuration
// (MODEL_PRICING), not built in: provider prices change, and a wrong
// built-in price would be worse than no estimate. Without a price for a
// model there is no estimate (nil), never a made-up value. Estimates are
// approximations, not billing.
type Pricing struct {
	prices map[string]ModelPrice
}

// ParsePricing reads MODEL_PRICING:
// {"gpt-5":{"input_per_million":1.25,"output_per_million":10}}. Empty means
// no prices.
func ParsePricing(raw string) (*Pricing, error) {
	p := &Pricing{prices: map[string]ModelPrice{}}
	if strings.TrimSpace(raw) == "" {
		return p, nil
	}
	if err := json.Unmarshal([]byte(raw), &p.prices); err != nil {
		return nil, fmt.Errorf("MODEL_PRICING is not valid JSON: %w", err)
	}
	for model, price := range p.prices {
		if model == "" || price.InputPerMillion < 0 || price.OutputPerMillion < 0 {
			return nil, errors.New("MODEL_PRICING: prices must be non-negative and models named")
		}
	}
	return p, nil
}

// Estimate returns the estimated cost in USD, or nil when the model has no
// configured price. Models are matched exactly, then by the longest
// configured prefix (so "gpt-5" prices "gpt-5-2025-08-07").
func (p *Pricing) Estimate(model string, inputTokens, outputTokens int64) *float64 {
	if p == nil || model == "" {
		return nil
	}
	price, ok := p.prices[model]
	if !ok {
		best := ""
		for m := range p.prices {
			if strings.HasPrefix(model, m) && len(m) > len(best) {
				best = m
			}
		}
		if best == "" {
			return nil
		}
		price = p.prices[best]
	}
	cost := float64(inputTokens)/1e6*price.InputPerMillion + float64(outputTokens)/1e6*price.OutputPerMillion
	cost = math.Round(cost*1e8) / 1e8
	return &cost
}
