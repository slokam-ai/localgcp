package cloudrun

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"cloud.google.com/go/run/apiv2/runpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Store is the in-memory Cloud Run service store.
type Store struct {
	mu       sync.RWMutex
	services map[string]*runpb.Service // full name -> service
}

func NewStore() *Store {
	return &Store{
		services: make(map[string]*runpb.Service),
	}
}

func (s *Store) Create(name string, svc *runpb.Service) (*runpb.Service, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.services[name]; exists {
		return nil, fmt.Errorf("already exists")
	}

	now := timestamppb.Now()
	svc.Name = name
	svc.CreateTime = now
	svc.UpdateTime = now
	svc.Uid = fmt.Sprintf("uid-%d", len(s.services)+1)

	// Set URI based on service name.
	parts := strings.Split(name, "/")
	svcName := parts[len(parts)-1]
	svc.Uri = fmt.Sprintf("https://%s-localgcp.run.app", svcName)

	// Mark as ready.
	svc.Reconciling = false
	svc.Conditions = []*runpb.Condition{{
		Type:  "Ready",
		State: runpb.Condition_CONDITION_SUCCEEDED,
	}}

	s.services[name] = svc
	return svc, nil
}

func (s *Store) Get(name string) (*runpb.Service, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	svc, ok := s.services[name]
	return svc, ok
}

func (s *Store) List(parent string) []*runpb.Service {
	s.mu.RLock()
	defer s.mu.RUnlock()
	prefix := parent + "/services/"
	var result []*runpb.Service
	for name, svc := range s.services {
		if strings.HasPrefix(name, prefix) {
			result = append(result, svc)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

func (s *Store) Update(name string, svc *runpb.Service) (*runpb.Service, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	existing, ok := s.services[name]
	if !ok {
		return nil, fmt.Errorf("not found")
	}

	// Preserve immutable fields.
	svc.Name = existing.Name
	svc.CreateTime = existing.CreateTime
	svc.Uid = existing.Uid
	svc.Uri = existing.Uri
	svc.UpdateTime = timestamppb.Now()
	svc.Conditions = existing.Conditions

	s.services[name] = svc
	return svc, nil
}

func (s *Store) Delete(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.services[name]; !ok {
		return false
	}
	delete(s.services, name)
	return true
}
