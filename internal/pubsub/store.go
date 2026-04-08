package pubsub

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// topic is the internal representation of a Pub/Sub topic.
type topic struct {
	Name   string            `json:"name"`
	Labels map[string]string `json:"labels,omitempty"`
}

// subscription is the internal representation of a Pub/Sub subscription.
type subscription struct {
	Name               string `json:"name"`
	Topic              string `json:"topic"`
	AckDeadlineSeconds int32  `json:"ackDeadlineSeconds"`
}

// message is a message held in a subscription's queue.
type message struct {
	ID          string            `json:"id"`
	Data        []byte            `json:"data"`
	Attributes  map[string]string `json:"attributes,omitempty"`
	PublishTime time.Time         `json:"publishTime"`
	AckDeadline time.Time         `json:"ackDeadline"`
	AckID       string            `json:"ackId"`
	Delivered   bool              `json:"delivered"`
	OrderingKey string            `json:"orderingKey,omitempty"`
}

// Store is the in-memory backend for the Pub/Sub emulator.
type Store struct {
	mu            sync.RWMutex
	topics        map[string]*topic                  // topic name -> topic
	subscriptions map[string]*subscription           // subscription name -> subscription
	topicSubs     map[string]map[string]struct{}      // topic name -> set of subscription names
	messages      map[string]map[string]*message      // subscription name -> ackID -> message
	msgCounter    atomic.Int64
	dataDir       string
}

// NewStore creates a new Pub/Sub store. If dataDir is non-empty, it loads
// persisted state and flushes on writes.
func NewStore(dataDir string) *Store {
	s := &Store{
		topics:        make(map[string]*topic),
		subscriptions: make(map[string]*subscription),
		topicSubs:     make(map[string]map[string]struct{}),
		messages:      make(map[string]map[string]*message),
		dataDir:       dataDir,
	}
	if dataDir != "" {
		s.load()
	}
	return s
}

// --- Topic operations ---

func (s *Store) CreateTopic(name string, labels map[string]string) (*topic, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.topics[name]; exists {
		return nil, fmt.Errorf("already exists: topic %q", name)
	}

	t := &topic{
		Name:   name,
		Labels: labels,
	}
	s.topics[name] = t
	if _, ok := s.topicSubs[name]; !ok {
		s.topicSubs[name] = make(map[string]struct{})
	}
	s.persist()
	return t, nil
}

func (s *Store) GetTopic(name string) (*topic, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.topics[name]
	return t, ok
}

func (s *Store) ListTopics(project string) []*topic {
	s.mu.RLock()
	defer s.mu.RUnlock()

	prefix := fmt.Sprintf("projects/%s/topics/", project)
	var result []*topic
	for _, t := range s.topics {
		if len(t.Name) >= len(prefix) && t.Name[:len(prefix)] == prefix {
			result = append(result, t)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

func (s *Store) DeleteTopic(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.topics[name]; !exists {
		return fmt.Errorf("not found: topic %q", name)
	}

	delete(s.topics, name)

	// Mark subscriptions as orphaned (set topic to _deleted-topic_).
	if subs, ok := s.topicSubs[name]; ok {
		for subName := range subs {
			if sub, exists := s.subscriptions[subName]; exists {
				sub.Topic = "_deleted-topic_"
			}
		}
		delete(s.topicSubs, name)
	}

	s.persist()
	return nil
}

// --- Subscription operations ---

func (s *Store) CreateSubscription(name, topicName string, ackDeadlineSeconds int32) (*subscription, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.subscriptions[name]; exists {
		return nil, fmt.Errorf("already exists: subscription %q", name)
	}

	if _, exists := s.topics[topicName]; !exists {
		return nil, fmt.Errorf("not found: topic %q", topicName)
	}

	if ackDeadlineSeconds <= 0 {
		ackDeadlineSeconds = 10
	}

	sub := &subscription{
		Name:               name,
		Topic:              topicName,
		AckDeadlineSeconds: ackDeadlineSeconds,
	}
	s.subscriptions[name] = sub
	s.messages[name] = make(map[string]*message)

	if _, ok := s.topicSubs[topicName]; !ok {
		s.topicSubs[topicName] = make(map[string]struct{})
	}
	s.topicSubs[topicName][name] = struct{}{}

	s.persist()
	return sub, nil
}

func (s *Store) GetSubscription(name string) (*subscription, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sub, ok := s.subscriptions[name]
	return sub, ok
}

func (s *Store) ListSubscriptions(project string) []*subscription {
	s.mu.RLock()
	defer s.mu.RUnlock()

	prefix := fmt.Sprintf("projects/%s/subscriptions/", project)
	var result []*subscription
	for _, sub := range s.subscriptions {
		if len(sub.Name) >= len(prefix) && sub.Name[:len(prefix)] == prefix {
			result = append(result, sub)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

func (s *Store) DeleteSubscription(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	sub, exists := s.subscriptions[name]
	if !exists {
		return fmt.Errorf("not found: subscription %q", name)
	}

	// Remove from topic's subscription set.
	if subs, ok := s.topicSubs[sub.Topic]; ok {
		delete(subs, name)
	}

	delete(s.subscriptions, name)
	delete(s.messages, name)

	s.persist()
	return nil
}

// --- Message operations ---

// Publish fans out messages to all subscriptions for the given topic.
// Returns the generated message IDs.
func (s *Store) Publish(topicName string, msgs []messageInput) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.topics[topicName]; !exists {
		return nil, fmt.Errorf("not found: topic %q", topicName)
	}

	now := time.Now()
	var ids []string

	for _, input := range msgs {
		id := fmt.Sprintf("%d", s.msgCounter.Add(1))
		ids = append(ids, id)

		// Fan out to all subscriptions for this topic.
		subs, ok := s.topicSubs[topicName]
		if !ok {
			continue
		}
		for subName := range subs {
			sub := s.subscriptions[subName]
			if sub == nil {
				continue
			}
			ackID := fmt.Sprintf("%s:%s", subName, id)
			m := &message{
				ID:          id,
				Data:        input.Data,
				Attributes:  input.Attributes,
				PublishTime: now,
				AckDeadline: time.Time{}, // zero value = not yet delivered
				AckID:       ackID,
				Delivered:   false,
				OrderingKey: input.OrderingKey,
			}
			if s.messages[subName] == nil {
				s.messages[subName] = make(map[string]*message)
			}
			s.messages[subName][ackID] = m
		}
	}

	s.persist()
	return ids, nil
}

// messageInput is the data needed to publish a single message.
type messageInput struct {
	Data        []byte
	Attributes  map[string]string
	OrderingKey string
}

// Pull returns up to maxMessages messages from the subscription that are
// available for delivery (either not yet delivered, or past their ack deadline).
func (s *Store) Pull(subName string, maxMessages int32) ([]*message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	sub, exists := s.subscriptions[subName]
	if !exists {
		return nil, fmt.Errorf("not found: subscription %q", subName)
	}

	msgs, ok := s.messages[subName]
	if !ok {
		return nil, nil
	}

	now := time.Now()
	var result []*message

	// Collect messages sorted by ID for deterministic ordering.
	var candidates []*message
	for _, m := range msgs {
		// Available if: never delivered, or ack deadline has passed.
		if !m.Delivered || now.After(m.AckDeadline) {
			candidates = append(candidates, m)
		}
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].ID < candidates[j].ID })

	for _, m := range candidates {
		if int32(len(result)) >= maxMessages {
			break
		}
		// Mark as delivered and set ack deadline.
		m.Delivered = true
		m.AckDeadline = now.Add(time.Duration(sub.AckDeadlineSeconds) * time.Second)
		result = append(result, m)
	}

	s.persist()
	return result, nil
}

// Acknowledge removes the specified messages from the subscription.
func (s *Store) Acknowledge(subName string, ackIDs []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.subscriptions[subName]; !exists {
		return fmt.Errorf("not found: subscription %q", subName)
	}

	msgs, ok := s.messages[subName]
	if !ok {
		return nil
	}

	for _, ackID := range ackIDs {
		delete(msgs, ackID)
	}

	s.persist()
	return nil
}

// ModifyAckDeadline updates the ack deadline for the specified messages.
func (s *Store) ModifyAckDeadline(subName string, ackIDs []string, ackDeadlineSeconds int32) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.subscriptions[subName]; !exists {
		return fmt.Errorf("not found: subscription %q", subName)
	}

	msgs, ok := s.messages[subName]
	if !ok {
		return nil
	}

	now := time.Now()
	for _, ackID := range ackIDs {
		if m, exists := msgs[ackID]; exists {
			if ackDeadlineSeconds == 0 {
				// Make immediately available for re-delivery.
				m.AckDeadline = time.Time{}
				m.Delivered = false
			} else {
				m.AckDeadline = now.Add(time.Duration(ackDeadlineSeconds) * time.Second)
			}
		}
	}

	s.persist()
	return nil
}

// --- Persistence ---

type persistedState struct {
	Topics        []*topic                        `json:"topics"`
	Subscriptions []*subscription                 `json:"subscriptions"`
	Messages      map[string][]*persistedMessage  `json:"messages"` // sub name -> messages
	MsgCounter    int64                           `json:"msgCounter"`
}

type persistedMessage struct {
	ID          string            `json:"id"`
	Data        string            `json:"data"` // base64
	Attributes  map[string]string `json:"attributes,omitempty"`
	PublishTime time.Time         `json:"publishTime"`
	AckDeadline time.Time         `json:"ackDeadline"`
	AckID       string            `json:"ackId"`
	Delivered   bool              `json:"delivered"`
	OrderingKey string            `json:"orderingKey,omitempty"`
}

func (s *Store) persist() {
	if s.dataDir == "" {
		return
	}

	dir := filepath.Join(s.dataDir, "pubsub")
	os.MkdirAll(dir, 0o755)

	state := persistedState{
		Topics:        make([]*topic, 0, len(s.topics)),
		Subscriptions: make([]*subscription, 0, len(s.subscriptions)),
		Messages:      make(map[string][]*persistedMessage),
		MsgCounter:    s.msgCounter.Load(),
	}

	for _, t := range s.topics {
		state.Topics = append(state.Topics, t)
	}

	for _, sub := range s.subscriptions {
		state.Subscriptions = append(state.Subscriptions, sub)
	}

	for subName, msgs := range s.messages {
		var pMsgs []*persistedMessage
		for _, m := range msgs {
			pMsgs = append(pMsgs, &persistedMessage{
				ID:          m.ID,
				Data:        base64.StdEncoding.EncodeToString(m.Data),
				Attributes:  m.Attributes,
				PublishTime: m.PublishTime,
				AckDeadline: m.AckDeadline,
				AckID:       m.AckID,
				Delivered:   m.Delivered,
				OrderingKey: m.OrderingKey,
			})
		}
		state.Messages[subName] = pMsgs
	}

	data, _ := json.MarshalIndent(state, "", "  ")
	os.WriteFile(filepath.Join(dir, "state.json"), data, 0o644)
}

func (s *Store) load() {
	path := filepath.Join(s.dataDir, "pubsub", "state.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return // No persisted state, start fresh.
	}

	var state persistedState
	if err := json.Unmarshal(data, &state); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: corrupt data in %s, starting with empty state\n", path)
		return
	}

	s.msgCounter.Store(state.MsgCounter)

	for _, t := range state.Topics {
		s.topics[t.Name] = t
		if _, ok := s.topicSubs[t.Name]; !ok {
			s.topicSubs[t.Name] = make(map[string]struct{})
		}
	}

	for _, sub := range state.Subscriptions {
		s.subscriptions[sub.Name] = sub
		if sub.Topic != "_deleted-topic_" {
			if _, ok := s.topicSubs[sub.Topic]; !ok {
				s.topicSubs[sub.Topic] = make(map[string]struct{})
			}
			s.topicSubs[sub.Topic][sub.Name] = struct{}{}
		}
		if _, ok := s.messages[sub.Name]; !ok {
			s.messages[sub.Name] = make(map[string]*message)
		}
	}

	for subName, pMsgs := range state.Messages {
		if _, ok := s.messages[subName]; !ok {
			s.messages[subName] = make(map[string]*message)
		}
		for _, pm := range pMsgs {
			msgData, err := base64.StdEncoding.DecodeString(pm.Data)
			if err != nil {
				continue
			}
			s.messages[subName][pm.AckID] = &message{
				ID:          pm.ID,
				Data:        msgData,
				Attributes:  pm.Attributes,
				PublishTime: pm.PublishTime,
				AckDeadline: pm.AckDeadline,
				AckID:       pm.AckID,
				Delivered:   pm.Delivered,
				OrderingKey: pm.OrderingKey,
			}
		}
	}
}
