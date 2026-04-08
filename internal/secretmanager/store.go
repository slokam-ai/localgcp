package secretmanager

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"cloud.google.com/go/secretmanager/apiv1/secretmanagerpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// secretData holds a secret and its versions in memory.
type secretData struct {
	Secret      *secretmanagerpb.Secret
	Versions    []*versionData // ordered by version number (1-indexed)
	NextVersion int32
}

// versionData holds a single secret version and its payload.
type versionData struct {
	Version *secretmanagerpb.SecretVersion
	Payload []byte // nil once destroyed
}

// Store is the storage backend for the Secret Manager emulator.
type Store struct {
	mu      sync.RWMutex
	secrets map[string]*secretData // key: "projects/{project}/secrets/{secret}"
	dataDir string
}

// NewStore creates a new secret manager store. If dataDir is non-empty,
// it loads persisted state and flushes on writes.
func NewStore(dataDir string) *Store {
	s := &Store{
		secrets: make(map[string]*secretData),
		dataDir: dataDir,
	}
	if dataDir != "" {
		s.load()
	}
	return s
}

// CreateSecret creates a new secret. Returns an error if it already exists.
func (s *Store) CreateSecret(parent, secretID string, labels map[string]string) (*secretmanagerpb.Secret, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	name := fmt.Sprintf("%s/secrets/%s", parent, secretID)
	if _, exists := s.secrets[name]; exists {
		return nil, fmt.Errorf("already exists: %s", name)
	}

	now := time.Now().UTC()
	secret := &secretmanagerpb.Secret{
		Name:       name,
		CreateTime: timestamppb.New(now),
		Labels:     labels,
		Replication: &secretmanagerpb.Replication{
			Replication: &secretmanagerpb.Replication_Automatic_{
				Automatic: &secretmanagerpb.Replication_Automatic{},
			},
		},
	}

	s.secrets[name] = &secretData{
		Secret:      secret,
		NextVersion: 1,
	}
	s.persist()
	return secret, nil
}

// GetSecret returns a secret by resource name.
func (s *Store) GetSecret(name string) (*secretmanagerpb.Secret, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	sd, ok := s.secrets[name]
	if !ok {
		return nil, fmt.Errorf("not found: %s", name)
	}
	return sd.Secret, nil
}

// ListSecrets returns all secrets under the given parent.
func (s *Store) ListSecrets(parent string) []*secretmanagerpb.Secret {
	s.mu.RLock()
	defer s.mu.RUnlock()

	prefix := parent + "/secrets/"
	var result []*secretmanagerpb.Secret
	for name, sd := range s.secrets {
		if len(name) > len(prefix) && name[:len(prefix)] == prefix {
			result = append(result, sd.Secret)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

// DeleteSecret deletes a secret and all its versions.
func (s *Store) DeleteSecret(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.secrets[name]; !ok {
		return fmt.Errorf("not found: %s", name)
	}
	delete(s.secrets, name)
	s.persist()
	return nil
}

// AddSecretVersion adds a new version with payload bytes.
func (s *Store) AddSecretVersion(secretName string, payload []byte) (*secretmanagerpb.SecretVersion, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	sd, ok := s.secrets[secretName]
	if !ok {
		return nil, fmt.Errorf("not found: %s", secretName)
	}

	versionNum := sd.NextVersion
	sd.NextVersion++

	now := time.Now().UTC()
	versionName := fmt.Sprintf("%s/versions/%d", secretName, versionNum)
	version := &secretmanagerpb.SecretVersion{
		Name:       versionName,
		CreateTime: timestamppb.New(now),
		State:      secretmanagerpb.SecretVersion_ENABLED,
	}

	data := make([]byte, len(payload))
	copy(data, payload)

	sd.Versions = append(sd.Versions, &versionData{
		Version: version,
		Payload: data,
	})

	s.persist()
	return version, nil
}

// resolveVersion resolves a version identifier (number or "latest") to a versionData.
// Must be called with at least a read lock held.
func (s *Store) resolveVersion(secretName, versionID string) (*versionData, error) {
	sd, ok := s.secrets[secretName]
	if !ok {
		return nil, fmt.Errorf("not found: %s", secretName)
	}

	if len(sd.Versions) == 0 {
		return nil, fmt.Errorf("not found: %s/versions/%s", secretName, versionID)
	}

	if versionID == "latest" {
		// Return the most recently created version (last in the slice).
		return sd.Versions[len(sd.Versions)-1], nil
	}

	// Look up by version name.
	fullName := fmt.Sprintf("%s/versions/%s", secretName, versionID)
	for _, vd := range sd.Versions {
		if vd.Version.Name == fullName {
			return vd, nil
		}
	}
	return nil, fmt.Errorf("not found: %s", fullName)
}

// GetSecretVersion returns version metadata.
func (s *Store) GetSecretVersion(secretName, versionID string) (*secretmanagerpb.SecretVersion, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	vd, err := s.resolveVersion(secretName, versionID)
	if err != nil {
		return nil, err
	}
	return vd.Version, nil
}

// AccessSecretVersion returns the payload for a version.
// Returns an error if the version is DISABLED or DESTROYED.
func (s *Store) AccessSecretVersion(secretName, versionID string) (*secretmanagerpb.SecretVersion, []byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	vd, err := s.resolveVersion(secretName, versionID)
	if err != nil {
		return nil, nil, err
	}

	switch vd.Version.State {
	case secretmanagerpb.SecretVersion_DISABLED:
		return nil, nil, fmt.Errorf("failed precondition: version %s is disabled", vd.Version.Name)
	case secretmanagerpb.SecretVersion_DESTROYED:
		return nil, nil, fmt.Errorf("failed precondition: version %s is destroyed", vd.Version.Name)
	}

	return vd.Version, vd.Payload, nil
}

// ListSecretVersions returns all versions of a secret.
func (s *Store) ListSecretVersions(secretName string) ([]*secretmanagerpb.SecretVersion, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	sd, ok := s.secrets[secretName]
	if !ok {
		return nil, fmt.Errorf("not found: %s", secretName)
	}

	result := make([]*secretmanagerpb.SecretVersion, len(sd.Versions))
	for i, vd := range sd.Versions {
		result[i] = vd.Version
	}
	return result, nil
}

// EnableSecretVersion sets a version's state to ENABLED.
func (s *Store) EnableSecretVersion(secretName, versionID string) (*secretmanagerpb.SecretVersion, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	vd, err := s.resolveVersion(secretName, versionID)
	if err != nil {
		return nil, err
	}

	if vd.Version.State == secretmanagerpb.SecretVersion_DESTROYED {
		return nil, fmt.Errorf("failed precondition: version %s is destroyed", vd.Version.Name)
	}

	vd.Version.State = secretmanagerpb.SecretVersion_ENABLED
	s.persist()
	return vd.Version, nil
}

// DisableSecretVersion sets a version's state to DISABLED.
func (s *Store) DisableSecretVersion(secretName, versionID string) (*secretmanagerpb.SecretVersion, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	vd, err := s.resolveVersion(secretName, versionID)
	if err != nil {
		return nil, err
	}

	if vd.Version.State == secretmanagerpb.SecretVersion_DESTROYED {
		return nil, fmt.Errorf("failed precondition: version %s is destroyed", vd.Version.Name)
	}

	vd.Version.State = secretmanagerpb.SecretVersion_DISABLED
	s.persist()
	return vd.Version, nil
}

// DestroySecretVersion sets a version's state to DESTROYED and clears payload.
func (s *Store) DestroySecretVersion(secretName, versionID string) (*secretmanagerpb.SecretVersion, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	vd, err := s.resolveVersion(secretName, versionID)
	if err != nil {
		return nil, err
	}

	vd.Version.State = secretmanagerpb.SecretVersion_DESTROYED
	vd.Version.DestroyTime = timestamppb.Now()
	vd.Payload = nil
	s.persist()
	return vd.Version, nil
}

// --- Persistence ---

type persistedState struct {
	Secrets []persistedSecret `json:"secrets"`
}

type persistedSecret struct {
	Name       string            `json:"name"`
	CreateTime int64             `json:"create_time_unix_nano"`
	Labels     map[string]string `json:"labels,omitempty"`
	Versions   []persistedVersion `json:"versions,omitempty"`
	NextVersion int32            `json:"next_version"`
}

type persistedVersion struct {
	Name        string `json:"name"`
	CreateTime  int64  `json:"create_time_unix_nano"`
	State       int32  `json:"state"`
	DestroyTime int64  `json:"destroy_time_unix_nano,omitempty"`
	Payload     string `json:"payload"` // base64 encoded
}

func (s *Store) persist() {
	if s.dataDir == "" {
		return
	}

	dir := filepath.Join(s.dataDir, "secretmanager")
	os.MkdirAll(dir, 0o755)

	state := persistedState{
		Secrets: make([]persistedSecret, 0, len(s.secrets)),
	}

	for _, sd := range s.secrets {
		ps := persistedSecret{
			Name:        sd.Secret.Name,
			CreateTime:  sd.Secret.CreateTime.AsTime().UnixNano(),
			Labels:      sd.Secret.Labels,
			NextVersion: sd.NextVersion,
		}
		for _, vd := range sd.Versions {
			pv := persistedVersion{
				Name:       vd.Version.Name,
				CreateTime: vd.Version.CreateTime.AsTime().UnixNano(),
				State:      int32(vd.Version.State),
			}
			if vd.Version.DestroyTime != nil {
				pv.DestroyTime = vd.Version.DestroyTime.AsTime().UnixNano()
			}
			if vd.Payload != nil {
				pv.Payload = base64.StdEncoding.EncodeToString(vd.Payload)
			}
			ps.Versions = append(ps.Versions, pv)
		}
		state.Secrets = append(state.Secrets, ps)
	}

	data, _ := json.MarshalIndent(state, "", "  ")
	os.WriteFile(filepath.Join(dir, "state.json"), data, 0o644)
}

func (s *Store) load() {
	path := filepath.Join(s.dataDir, "secretmanager", "state.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}

	var state persistedState
	if err := json.Unmarshal(data, &state); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: corrupt data in %s, starting with empty state\n", path)
		return
	}

	for _, ps := range state.Secrets {
		secret := &secretmanagerpb.Secret{
			Name:       ps.Name,
			CreateTime: timestamppb.New(time.Unix(0, ps.CreateTime)),
			Labels:     ps.Labels,
			Replication: &secretmanagerpb.Replication{
				Replication: &secretmanagerpb.Replication_Automatic_{
					Automatic: &secretmanagerpb.Replication_Automatic{},
				},
			},
		}

		sd := &secretData{
			Secret:      secret,
			NextVersion: ps.NextVersion,
		}

		for _, pv := range ps.Versions {
			version := &secretmanagerpb.SecretVersion{
				Name:       pv.Name,
				CreateTime: timestamppb.New(time.Unix(0, pv.CreateTime)),
				State:      secretmanagerpb.SecretVersion_State(pv.State),
			}
			if pv.DestroyTime != 0 {
				version.DestroyTime = timestamppb.New(time.Unix(0, pv.DestroyTime))
			}

			var payload []byte
			if pv.Payload != "" {
				payload, err = base64.StdEncoding.DecodeString(pv.Payload)
				if err != nil {
					continue
				}
			}

			sd.Versions = append(sd.Versions, &versionData{
				Version: version,
				Payload: payload,
			})
		}

		s.secrets[ps.Name] = sd
	}
}
