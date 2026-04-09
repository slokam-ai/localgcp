package logging

import (
	"sort"
	"strings"
	"sync"
	"time"

	"cloud.google.com/go/logging/apiv2/loggingpb"
)

// maxEntries is the maximum number of log entries retained in memory.
const maxEntries = 10000

// Store is the in-memory log entry store.
type Store struct {
	mu      sync.RWMutex
	entries []*loggingpb.LogEntry
}

func NewStore() *Store {
	return &Store{}
}

// Write appends log entries to the store, trimming if over capacity.
func (s *Store) Write(entries []*loggingpb.LogEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = append(s.entries, entries...)
	if len(s.entries) > maxEntries {
		s.entries = s.entries[len(s.entries)-maxEntries:]
	}
}

// List returns log entries matching the filter for the given resource names.
// Supports simple filters: severity>=LEVEL, textPayload contains, logName match.
func (s *Store) List(resourceNames []string, filter string, pageSize int) []*loggingpb.LogEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if pageSize <= 0 || pageSize > 1000 {
		pageSize = 1000
	}

	var result []*loggingpb.LogEntry
	for _, entry := range s.entries {
		if !matchesResources(entry, resourceNames) {
			continue
		}
		if filter != "" && !matchesFilter(entry, filter) {
			continue
		}
		result = append(result, entry)
		if len(result) >= pageSize {
			break
		}
	}
	return result
}

// ListLogs returns distinct log names.
func (s *Store) ListLogs(parent string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	seen := make(map[string]bool)
	for _, entry := range s.entries {
		if strings.HasPrefix(entry.GetLogName(), parent+"/") || parent == "" {
			seen[entry.GetLogName()] = true
		}
	}

	var logs []string
	for name := range seen {
		logs = append(logs, name)
	}
	sort.Strings(logs)
	return logs
}

// DeleteLog removes all entries for a given log name.
func (s *Store) DeleteLog(logName string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var remaining []*loggingpb.LogEntry
	for _, entry := range s.entries {
		if entry.GetLogName() != logName {
			remaining = append(remaining, entry)
		}
	}
	s.entries = remaining
}

// matchesResources checks if a log entry's resource matches any of the resource names.
func matchesResources(entry *loggingpb.LogEntry, resourceNames []string) bool {
	if len(resourceNames) == 0 {
		return true
	}
	logName := entry.GetLogName()
	for _, rn := range resourceNames {
		if strings.HasPrefix(logName, rn+"/") || strings.HasPrefix(logName, "projects/"+rn+"/") {
			return true
		}
	}
	return false
}

// matchesFilter applies a simple filter to a log entry.
// Supports: severity>=LEVEL, textPayload:"substring", logName="name".
func matchesFilter(entry *loggingpb.LogEntry, filter string) bool {
	parts := strings.Fields(filter)
	for _, part := range parts {
		if strings.HasPrefix(part, "severity>=") {
			level := strings.TrimPrefix(part, "severity>=")
			if !severityGTE(entry.GetSeverity().String(), level) {
				return false
			}
		} else if strings.Contains(part, ":") {
			kv := strings.SplitN(part, ":", 2)
			if kv[0] == "textPayload" {
				text := entry.GetTextPayload()
				search := strings.Trim(kv[1], "\"")
				if !strings.Contains(text, search) {
					return false
				}
			}
		} else if strings.Contains(part, "=") {
			kv := strings.SplitN(part, "=", 2)
			if kv[0] == "logName" {
				if entry.GetLogName() != strings.Trim(kv[1], "\"") {
					return false
				}
			}
		}
	}
	return true
}

var severityOrder = map[string]int{
	"DEFAULT":   0,
	"DEBUG":     100,
	"INFO":      200,
	"NOTICE":    300,
	"WARNING":   400,
	"ERROR":     500,
	"CRITICAL":  600,
	"ALERT":     700,
	"EMERGENCY": 800,
}

func severityGTE(entrySev, filterSev string) bool {
	return severityOrder[entrySev] >= severityOrder[filterSev]
}

// Now returns the current time — used for testing seams.
var Now = time.Now
