package main

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"
)

// The broker lets the app ask a cluster's agent for live data (resource lists, logs)
// while the agent only ever makes outbound calls:
//
//	app  --GET /v1/clusters/{id}/...-->  relay  (request queued for that cluster)
//	agent --GET /v1/agent/requests ---> relay  (long-poll, receives the request)
//	agent --POST /v1/agent/responses/{reqId}--> relay --> answer returned to the app

// AgentRequest is handed to the agent.
type AgentRequest struct {
	ID     string            `json:"id"`
	Kind   string            `json:"kind"` // kinds | list | get | logs
	Params map[string]string `json:"params,omitempty"`
}

// AgentResponse comes back from the agent. Body is passed through to the app as-is.
type AgentResponse struct {
	Status int             `json:"status"`
	Error  string          `json:"error,omitempty"`
	Body   json.RawMessage `json:"body,omitempty"`
}

const (
	pollWait    = 25 * time.Second // how long an agent long-poll is held open
	callTimeout = 20 * time.Second // how long the app waits for the agent's answer
	onlineGrace = 45 * time.Second // agent counts as online if it polled this recently
)

var (
	errAgentOffline = errors.New("agent is offline for this cluster")
	errAgentBusy    = errors.New("agent request queue is full, try again")
	errAgentTimeout = errors.New("agent did not answer in time")
)

type Broker struct {
	mu       sync.Mutex
	queues   map[string]chan *AgentRequest // cluster -> pending requests
	waiting  map[string]chan AgentResponse // request id -> app waiting for the answer
	lastPoll map[string]time.Time          // cluster -> last time its agent polled
	polling  map[string]int                // cluster -> polls currently held open
}

func NewBroker() *Broker {
	return &Broker{
		queues:   map[string]chan *AgentRequest{},
		waiting:  map[string]chan AgentResponse{},
		lastPoll: map[string]time.Time{},
		polling:  map[string]int{},
	}
}

func (b *Broker) queueLocked(cluster string) chan *AgentRequest {
	q, ok := b.queues[cluster]
	if !ok {
		q = make(chan *AgentRequest, 64)
		b.queues[cluster] = q
	}
	return q
}

// Online reports whether an agent for the cluster is polling.
func (b *Broker) Online(cluster string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.polling[cluster] > 0 || time.Since(b.lastPoll[cluster]) < onlineGrace
}

// Call sends a request to the cluster's agent and waits for its answer.
func (b *Broker) Call(ctx context.Context, cluster, kind string, params map[string]string) (AgentResponse, error) {
	if !b.Online(cluster) {
		return AgentResponse{}, errAgentOffline
	}
	req := &AgentRequest{ID: newID(), Kind: kind, Params: params}
	answer := make(chan AgentResponse, 1)

	b.mu.Lock()
	q := b.queueLocked(cluster)
	b.waiting[req.ID] = answer
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		delete(b.waiting, req.ID)
		b.mu.Unlock()
	}()

	select {
	case q <- req:
	default:
		return AgentResponse{}, errAgentBusy
	}

	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	select {
	case resp := <-answer:
		return resp, nil
	case <-ctx.Done():
		return AgentResponse{}, errAgentTimeout
	}
}

// Next is the agent's long-poll: it returns the next request, or nil after pollWait.
func (b *Broker) Next(ctx context.Context, cluster string) *AgentRequest {
	b.mu.Lock()
	q := b.queueLocked(cluster)
	b.polling[cluster]++
	b.lastPoll[cluster] = time.Now()
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		b.polling[cluster]--
		b.lastPoll[cluster] = time.Now()
		b.mu.Unlock()
	}()

	ctx, cancel := context.WithTimeout(ctx, pollWait)
	defer cancel()
	for {
		select {
		case req := <-q:
			b.mu.Lock()
			_, stillWaiting := b.waiting[req.ID]
			b.mu.Unlock()
			if stillWaiting {
				return req
			}
			// the app already gave up on this one; skip it
		case <-ctx.Done():
			return nil
		}
	}
}

// Deliver hands the agent's answer to the waiting app request.
func (b *Broker) Deliver(id string, resp AgentResponse) bool {
	b.mu.Lock()
	answer, ok := b.waiting[id]
	b.mu.Unlock()
	if !ok {
		return false
	}
	select {
	case answer <- resp:
	default:
	}
	return true
}
