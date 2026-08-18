package handoff

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const maxCollaborationBytes = 1 << 20

type handoffComment struct {
	ID        string    `json:"id"`
	AuthorID  string    `json:"author_id"`
	Author    string    `json:"author"`
	Message   string    `json:"message"`
	CreatedAt time.Time `json:"created_at"`
}

type auditEvent struct {
	Action    string    `json:"action"`
	ActorID   string    `json:"actor_id"`
	Actor     string    `json:"actor"`
	Detail    string    `json:"detail,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

type viewerState struct {
	Read     bool `json:"read,omitempty"`
	Archived bool `json:"archived,omitempty"`
}

type handoffViewerStates map[string]viewerState

func (s *service) commentsPath(id string) string {
	return filepath.Join(s.dataDir, id+".comments.json")
}
func (s *service) auditPath(id string) string  { return filepath.Join(s.dataDir, id+".audit.json") }
func (s *service) statesPath(id string) string { return filepath.Join(s.dataDir, id+".states.json") }

func readOptionalJSON(path string, destination any) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(data) > maxCollaborationBytes {
		return errors.New("collaboration metadata exceeds its size limit")
	}
	return json.Unmarshal(data, destination)
}

func actorName(user authenticatedUser) string {
	if strings.TrimSpace(user.Name) != "" {
		return user.Name
	}
	return user.ID
}

func (s *service) appendAuditLocked(id string, event auditEvent) error {
	var events []auditEvent
	if err := readOptionalJSON(s.auditPath(id), &events); err != nil {
		return err
	}
	if len(events) >= 2000 {
		events = append([]auditEvent(nil), events[len(events)-1999:]...)
	}
	events = append(events, event)
	return writeJSONAtomic(s.auditPath(id), events, 0o600)
}

func (s *service) loadVisibleMetadata(w http.ResponseWriter, r *http.Request) (handoffMetadata, bool) {
	id := r.PathValue("id")
	if !idPattern.MatchString(id) {
		http.NotFound(w, r)
		return handoffMetadata{}, false
	}
	record, err := s.loadMetadata(id)
	if err != nil || !canView(record, requestUser(r)) {
		http.NotFound(w, r)
		return handoffMetadata{}, false
	}
	return record, true
}

func (s *service) handleComments(w http.ResponseWriter, r *http.Request) {
	record, ok := s.loadVisibleMetadata(w, r)
	if !ok {
		return
	}
	s.quotaMu.Lock()
	defer s.quotaMu.Unlock()
	record, err := s.loadMetadata(record.ID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	var comments []handoffComment
	if err := readOptionalJSON(s.commentsPath(record.ID), &comments); err != nil {
		http.Error(w, "cannot read comments", http.StatusInternalServerError)
		return
	}
	if r.Method == http.MethodGet {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(struct {
			Comments []handoffComment `json:"comments"`
		}{Comments: nonNilComments(comments)})
		return
	}
	var request struct {
		Message string `json:"message"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 8<<10)).Decode(&request); err != nil {
		http.Error(w, "invalid comment", http.StatusBadRequest)
		return
	}
	request.Message = strings.TrimSpace(request.Message)
	if request.Message == "" || len(request.Message) > 2000 {
		http.Error(w, "comment must contain 1 to 2000 characters", http.StatusBadRequest)
		return
	}
	user := requestUser(r)
	comment := handoffComment{ID: fmt.Sprintf("%d", time.Now().UTC().UnixNano()), AuthorID: user.ID, Author: actorName(user), Message: request.Message, CreatedAt: time.Now().UTC()}
	comments = append(comments, comment)
	if err := writeJSONAtomic(s.commentsPath(record.ID), comments, 0o600); err != nil {
		http.Error(w, "cannot store comment", http.StatusInternalServerError)
		return
	}
	record.CommentsCount = len(comments)
	if err := writeJSONAtomic(s.metadataPath(record.ID), record, 0o600); err != nil {
		http.Error(w, "cannot update handoff", http.StatusInternalServerError)
		return
	}
	_ = s.appendAuditLocked(record.ID, auditEvent{Action: "commented", ActorID: user.ID, Actor: actorName(user), CreatedAt: time.Now().UTC()})
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(comment)
}

func nonNilComments(values []handoffComment) []handoffComment {
	if values == nil {
		return []handoffComment{}
	}
	return values
}

func appendIdentity(values []string, identity string) []string {
	for _, value := range values {
		if value == identity {
			return values
		}
	}
	return append(values, identity)
}

func (s *service) handleEvent(w http.ResponseWriter, r *http.Request) {
	record, ok := s.loadVisibleMetadata(w, r)
	if !ok {
		return
	}
	var request struct {
		Action    string `json:"action"`
		Target    string `json:"target,omitempty"`
		ExpiresAt string `json:"expires_at,omitempty"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 16<<10)).Decode(&request); err != nil {
		http.Error(w, "invalid handoff event", http.StatusBadRequest)
		return
	}
	request.Action = strings.ToLower(strings.TrimSpace(request.Action))
	user := requestUser(r)
	s.quotaMu.Lock()
	defer s.quotaMu.Unlock()
	record, err := s.loadMetadata(record.ID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	var states handoffViewerStates
	if err := readOptionalJSON(s.statesPath(record.ID), &states); err != nil {
		http.Error(w, "cannot read handoff state", http.StatusInternalServerError)
		return
	}
	if states == nil {
		states = make(handoffViewerStates)
	}
	state := states[user.ID]
	detail := ""
	switch request.Action {
	case "read":
		state.Read = true
	case "unread":
		state.Read = false
	case "archive":
		state.Archived = true
	case "unarchive":
		state.Archived = false
	case "acknowledged":
		record.AcknowledgedBy = appendIdentity(record.AcknowledgedBy, user.ID)
		record.Lifecycle = "acknowledged"
	case "applied":
		record.AppliedBy = appendIdentity(record.AppliedBy, user.ID)
		record.Lifecycle = "applied"
	case "revoke":
		if !user.admin() && record.OwnerID != user.ID {
			http.Error(w, "only the owner or an administrator can revoke a handoff", http.StatusForbidden)
			return
		}
		record.Lifecycle = "revoked"
		record.RevokedAt = time.Now().UTC()
	case "assign":
		if !user.admin() && record.OwnerID != user.ID {
			http.Error(w, "only the owner or an administrator can assign a handoff", http.StatusForbidden)
			return
		}
		record.AssignedTo = strings.ToLower(strings.TrimSpace(request.Target))
		if len(record.AssignedTo) > 254 {
			http.Error(w, "assignment target is too long", http.StatusBadRequest)
			return
		}
		detail = record.AssignedTo
	case "expire":
		if !user.admin() && record.OwnerID != user.ID {
			http.Error(w, "only the owner or an administrator can change expiry", http.StatusForbidden)
			return
		}
		expires, err := time.Parse(time.RFC3339, request.ExpiresAt)
		if err != nil || !expires.After(time.Now()) || expires.After(time.Now().Add(s.retention)) {
			http.Error(w, "expiry must be in the future and within server retention", http.StatusBadRequest)
			return
		}
		record.ExpiresAt = expires.UTC()
		detail = record.ExpiresAt.Format(time.RFC3339)
	default:
		http.Error(w, "unsupported handoff event", http.StatusBadRequest)
		return
	}
	states[user.ID] = state
	if err := writeJSONAtomic(s.statesPath(record.ID), states, 0o600); err != nil {
		http.Error(w, "cannot store viewer state", http.StatusInternalServerError)
		return
	}
	if err := writeJSONAtomic(s.metadataPath(record.ID), record, 0o600); err != nil {
		http.Error(w, "cannot update handoff", http.StatusInternalServerError)
		return
	}
	if err := s.appendAuditLocked(record.ID, auditEvent{Action: request.Action, ActorID: user.ID, Actor: actorName(user), Detail: detail, CreatedAt: time.Now().UTC()}); err != nil {
		http.Error(w, "cannot append audit history", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(record)
}

func (s *service) handleAudit(w http.ResponseWriter, r *http.Request) {
	record, ok := s.loadVisibleMetadata(w, r)
	if !ok {
		return
	}
	var events []auditEvent
	if err := readOptionalJSON(s.auditPath(record.ID), &events); err != nil {
		http.Error(w, "cannot read audit history", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(struct {
		Events []auditEvent `json:"events"`
	}{Events: nonNilAudit(events)})
}

func nonNilAudit(values []auditEvent) []auditEvent {
	if values == nil {
		return []auditEvent{}
	}
	return values
}
