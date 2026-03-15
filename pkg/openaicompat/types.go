// Package openaicompat provides OpenAI-compatible types that external clients
// can import to build requests and parse responses for the LLM Gateway.
//
// All types are aliases of internal/model, so they stay in sync automatically.
package openaicompat

import "github.com/M4cr0Chen/llm-gateway/internal/model"

type ChatCompletionRequest = model.ChatCompletionRequest
type Message = model.Message
type ChatCompletionResponse = model.ChatCompletionResponse
type Choice = model.Choice
type Usage = model.Usage
type ChatCompletionChunk = model.ChatCompletionChunk
type ChunkChoice = model.ChunkChoice
type DeltaMessage = model.DeltaMessage
