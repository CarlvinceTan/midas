package agent

import (
	"sync"

	"github.com/CarlvinceTan/midas/pkg/ai"
)

// SteeringQueue admits user messages into a running agent loop. The loop
// closes the queue atomically when it reaches a final boundary with no pending
// input, so a successful Push is guaranteed to be observed before the run ends.
type SteeringQueue struct {
	mu       sync.Mutex
	messages []ai.Message
	closed   bool
}

func NewSteeringQueue() *SteeringQueue { return new(SteeringQueue) }

// Push queues a message for the next agent-step boundary. It returns false
// once the run has committed to ending.
func (q *SteeringQueue) Push(message ai.Message) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return false
	}
	q.messages = append(q.messages, message)
	return true
}

func (q *SteeringQueue) drain() []ai.Message {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.drainLocked()
}

// drainOrClose atomically drains pending input or closes an empty queue. The
// boolean reports that the queue closed and the run may finish.
func (q *SteeringQueue) drainOrClose() ([]ai.Message, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.messages) == 0 {
		q.closed = true
		return nil, true
	}
	return q.drainLocked(), false
}

func (q *SteeringQueue) close() []ai.Message {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.closed = true
	return q.drainLocked()
}

func (q *SteeringQueue) drainLocked() []ai.Message {
	messages := q.messages
	q.messages = nil
	return messages
}
