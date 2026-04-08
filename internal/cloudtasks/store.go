package cloudtasks

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// TaskState tracks the lifecycle of a task.
type TaskState int

const (
	TaskPending    TaskState = iota
	TaskDispatching
	TaskCompleted
)

type queue struct {
	Name               string `json:"name"`
	MaxAttempts        int32  `json:"maxAttempts"`
	MinBackoffSeconds  int32  `json:"minBackoffSeconds"`
	MaxBackoffSeconds  int32  `json:"maxBackoffSeconds"`
	MaxDoublings       int32  `json:"maxDoublings"`
}

type task struct {
	Name           string            `json:"name"`
	Queue          string            `json:"queue"`
	HttpURL        string            `json:"httpUrl"`
	HttpMethod     string            `json:"httpMethod"`
	HttpBody       []byte            `json:"httpBody,omitempty"`
	HttpHeaders    map[string]string `json:"httpHeaders,omitempty"`
	ScheduleTime   time.Time         `json:"scheduleTime"`
	CreateTime     time.Time         `json:"createTime"`
	DispatchCount  int32             `json:"dispatchCount"`
	ResponseCount  int32             `json:"responseCount"`
	LastAttempt    time.Time         `json:"lastAttempt,omitempty"`
	State          TaskState         `json:"state"`
}

// Store is the in-memory backend for Cloud Tasks.
type Store struct {
	mu       sync.RWMutex
	queues   map[string]*queue           // queue name -> queue
	tasks    map[string]map[string]*task // queue name -> task name -> task
	counter  atomic.Int64
	dataDir  string
}

func NewStore(dataDir string) *Store {
	s := &Store{
		queues:  make(map[string]*queue),
		tasks:   make(map[string]map[string]*task),
		dataDir: dataDir,
	}
	if dataDir != "" {
		s.load()
	}
	return s
}

// --- Queue operations ---

func (s *Store) CreateQueue(name string, maxAttempts, minBackoff, maxBackoff, maxDoublings int32) (*queue, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.queues[name]; exists {
		return nil, fmt.Errorf("already exists: queue %q", name)
	}
	if maxAttempts <= 0 {
		maxAttempts = 3
	}
	if minBackoff <= 0 {
		minBackoff = 1
	}
	if maxBackoff <= 0 {
		maxBackoff = 10
	}

	q := &queue{
		Name:              name,
		MaxAttempts:       maxAttempts,
		MinBackoffSeconds: minBackoff,
		MaxBackoffSeconds: maxBackoff,
		MaxDoublings:      maxDoublings,
	}
	s.queues[name] = q
	s.tasks[name] = make(map[string]*task)
	s.persist()
	return q, nil
}

func (s *Store) GetQueue(name string) (*queue, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	q, ok := s.queues[name]
	return q, ok
}

func (s *Store) ListQueues(parent string) []*queue {
	s.mu.RLock()
	defer s.mu.RUnlock()

	prefix := parent + "/queues/"
	var result []*queue
	for _, q := range s.queues {
		if len(q.Name) >= len(prefix) && q.Name[:len(prefix)] == prefix {
			result = append(result, q)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

func (s *Store) DeleteQueue(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.queues[name]; !exists {
		return fmt.Errorf("not found: queue %q", name)
	}
	delete(s.queues, name)
	delete(s.tasks, name)
	s.persist()
	return nil
}

func (s *Store) PurgeQueue(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.queues[name]; !exists {
		return fmt.Errorf("not found: queue %q", name)
	}
	s.tasks[name] = make(map[string]*task)
	s.persist()
	return nil
}

// --- Task operations ---

func (s *Store) CreateTask(queueName, taskName, httpURL, httpMethod string, body []byte, headers map[string]string, scheduleTime time.Time) (*task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.queues[queueName]; !exists {
		return nil, fmt.Errorf("not found: queue %q", queueName)
	}

	if taskName == "" {
		taskName = fmt.Sprintf("%s/tasks/%d", queueName, s.counter.Add(1))
	}

	if _, exists := s.tasks[queueName][taskName]; exists {
		return nil, fmt.Errorf("already exists: task %q", taskName)
	}

	if scheduleTime.IsZero() {
		scheduleTime = time.Now()
	}
	if httpMethod == "" {
		httpMethod = "POST"
	}

	t := &task{
		Name:         taskName,
		Queue:        queueName,
		HttpURL:      httpURL,
		HttpMethod:   httpMethod,
		HttpBody:     body,
		HttpHeaders:  headers,
		ScheduleTime: scheduleTime,
		CreateTime:   time.Now(),
		State:        TaskPending,
	}
	s.tasks[queueName][taskName] = t
	s.persist()
	return t, nil
}

func (s *Store) GetTask(queueName, taskName string) (task, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	tasks, ok := s.tasks[queueName]
	if !ok {
		return task{}, false
	}
	t, ok := tasks[taskName]
	if !ok {
		return task{}, false
	}
	return *t, true // copy
}

func (s *Store) ListTasks(queueName string) []task {
	s.mu.RLock()
	defer s.mu.RUnlock()
	tasks, ok := s.tasks[queueName]
	if !ok {
		return nil
	}
	var result []task
	for _, t := range tasks {
		result = append(result, *t) // copy
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

func (s *Store) DeleteTask(queueName, taskName string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tasks, ok := s.tasks[queueName]
	if !ok {
		return fmt.Errorf("not found: queue %q", queueName)
	}
	if _, exists := tasks[taskName]; !exists {
		return fmt.Errorf("not found: task %q", taskName)
	}
	delete(tasks, taskName)
	s.persist()
	return nil
}

// DueTasks returns copies of all pending tasks across all queues whose schedule time has passed.
// It marks the originals as Dispatching.
func (s *Store) DueTasks() []task {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	var due []task
	for _, tasks := range s.tasks {
		for _, t := range tasks {
			if t.State == TaskPending && !t.ScheduleTime.After(now) {
				t.State = TaskDispatching
				t.DispatchCount++
				t.LastAttempt = now
				due = append(due, *t) // copy
			}
		}
	}
	return due
}

// CompleteTask removes a successfully dispatched task.
func (s *Store) CompleteTask(queueName, taskName string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if tasks, ok := s.tasks[queueName]; ok {
		delete(tasks, taskName)
	}
	s.persist()
}

// RetryTask puts a failed task back to pending with a backoff delay.
func (s *Store) RetryTask(queueName, taskName string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	tasks, ok := s.tasks[queueName]
	if !ok {
		return false
	}
	t, ok := tasks[taskName]
	if !ok {
		return false
	}
	q := s.queues[queueName]
	if q != nil && q.MaxAttempts > 0 && t.DispatchCount >= q.MaxAttempts {
		// Max attempts reached, drop the task.
		delete(tasks, taskName)
		s.persist()
		return false
	}
	// Exponential backoff.
	backoff := time.Duration(q.MinBackoffSeconds) * time.Second
	for i := int32(1); i < t.DispatchCount && i <= q.MaxDoublings; i++ {
		backoff *= 2
	}
	if max := time.Duration(q.MaxBackoffSeconds) * time.Second; backoff > max {
		backoff = max
	}
	t.ScheduleTime = time.Now().Add(backoff)
	t.State = TaskPending
	s.persist()
	return true
}

// --- Persistence ---

type persistedState struct {
	Queues []*queue            `json:"queues"`
	Tasks  map[string][]*task  `json:"tasks"`
}

func (s *Store) persist() {
	if s.dataDir == "" {
		return
	}
	dir := filepath.Join(s.dataDir, "cloudtasks")
	os.MkdirAll(dir, 0o755)

	state := persistedState{
		Queues: make([]*queue, 0, len(s.queues)),
		Tasks:  make(map[string][]*task),
	}
	for _, q := range s.queues {
		state.Queues = append(state.Queues, q)
	}
	for qName, tasks := range s.tasks {
		for _, t := range tasks {
			state.Tasks[qName] = append(state.Tasks[qName], t)
		}
	}
	data, _ := json.MarshalIndent(state, "", "  ")
	os.WriteFile(filepath.Join(dir, "state.json"), data, 0o644)
}

func (s *Store) load() {
	path := filepath.Join(s.dataDir, "cloudtasks", "state.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var state persistedState
	if err := json.Unmarshal(data, &state); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: corrupt data in %s, starting with empty state\n", path)
		return
	}
	for _, q := range state.Queues {
		s.queues[q.Name] = q
		if _, ok := s.tasks[q.Name]; !ok {
			s.tasks[q.Name] = make(map[string]*task)
		}
	}
	for qName, tasks := range state.Tasks {
		if _, ok := s.tasks[qName]; !ok {
			s.tasks[qName] = make(map[string]*task)
		}
		for _, t := range tasks {
			s.tasks[qName][t.Name] = t
		}
	}
}
