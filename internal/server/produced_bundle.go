package server

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/polds/factory/internal/artifacts"
	"github.com/polds/factory/internal/definition"
	"github.com/polds/factory/internal/harness"
	"github.com/polds/factory/internal/store"
)

const bundleCap = 32 << 20

const headHeader = "X-Factory-Head"

// objectClient is the artifact write used for one bundle. Production uses S3.
// Tests replace it. Put is not wrapped with a size cap.
type objectClient interface {
	Put(ctx context.Context, key string, body io.Reader, size int64, contentType string) error
	Get(ctx context.Context, key string) (io.ReadCloser, error)
	Delete(ctx context.Context, key string) error
}

var bundleClients sync.Map

func (s *Server) mountProducedBundle(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/agent/runs/{runId}/head.bundle", s.acceptProducedBundle)
	mux.HandleFunc("GET /api/v1/agent/runs/{runId}/head.bundle", s.getProducedBundle)
	mux.HandleFunc("DELETE /api/v1/agent/runs/{runId}/head.bundle", s.deleteProducedBundle)
}

func (s *Server) acceptProducedBundle(w http.ResponseWriter, r *http.Request) {
	if err := s.takeProducedBundle(r); err != nil {
		code := http.StatusBadRequest
		if errors.Is(err, errBundleUnauthorized) {
			code = http.StatusUnauthorized
		}
		http.Error(w, err.Error(), code)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) getProducedBundle(w http.ResponseWriter, r *http.Request) {
	body, err := s.readProducedBundle(r)
	if err != nil {
		http.Error(w, "produced bundle missing", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	_, _ = w.Write(body)
}

func (s *Server) deleteProducedBundle(w http.ResponseWriter, r *http.Request) {
	if err := s.removeProducedBundle(r); err != nil {
		http.Error(w, "produced bundle missing", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

var errBundleUnauthorized = errors.New("produced bundle: unauthorized")

func (s *Server) takeProducedBundle(r *http.Request) error {
	runID := r.PathValue("runId")
	token, ok := bearer(r)
	if !ok {
		return errBundleUnauthorized
	}
	execID, ok, err := s.matchToken(r.Context(), runID, tokenHash(token))
	if err != nil || !ok {
		return errBundleUnauthorized
	}
	want := strings.TrimSpace(r.Header.Get(headHeader))
	if !commitHex40(want) {
		return errors.New("produced bundle: head")
	}
	body, err := readBundle(r.Body)
	if err != nil {
		return err
	}
	client := s.objects()
	if client == nil {
		return errors.New("produced bundle: artifact client unset")
	}
	key := headKey(runID, execID)
	sum := sha256.Sum256(body)
	hash := hex.EncodeToString(sum[:])
	if err := client.Put(r.Context(), key, bytes.NewReader(body), int64(len(body)), "application/octet-stream"); err != nil {
		return errors.New("produced bundle: put")
	}
	path, err := os.CreateTemp("", "head-bundle-")
	if err != nil {
		_ = client.Delete(r.Context(), key)
		return err
	}
	tmp := path.Name()
	_, werr := path.Write(body)
	cerr := path.Close()
	if werr != nil || cerr != nil {
		_ = os.Remove(tmp)
		_ = client.Delete(r.Context(), key)
		return errors.New("produced bundle: not a bundle")
	}
	defer func() { _ = os.Remove(tmp) }()
	if err := harness.VerifyBundleTip(r.Context(), tmp, want); err != nil {
		_ = client.Delete(r.Context(), key)
		return errors.New("produced bundle: " + verifyReason(err))
	}
	_, taskID, _, err := s.store.RunSeat(r.Context(), runID)
	if err != nil {
		_ = client.Delete(r.Context(), key)
		return errors.New("produced bundle: run")
	}
	if err := s.store.PutProducedHead(r.Context(), store.ProducedHead{
		TaskID: taskID, RunID: runID, ExecutionID: execID,
		HeadSHA: want, BundleSHA256: hash, State: store.ProducedReady,
	}); err != nil {
		_ = client.Delete(r.Context(), key)
		return errors.New("produced bundle: row")
	}
	return nil
}

func verifyReason(err error) string {
	if err == nil {
		return "tip mismatch"
	}
	msg := err.Error()
	if strings.Contains(msg, "tip") {
		return "tip mismatch"
	}
	return "not a bundle"
}

func (s *Server) readProducedBundle(r *http.Request) ([]byte, error) {
	runID := r.PathValue("runId")
	token, ok := bearer(r)
	if !ok {
		return nil, errBundleUnauthorized
	}
	if _, ok, err := s.matchToken(r.Context(), runID, tokenHash(token)); err != nil || !ok {
		return nil, errBundleUnauthorized
	}
	_, taskID, _, err := s.store.RunSeat(r.Context(), runID)
	if err != nil {
		return nil, err
	}
	row, err := s.store.GetProducedHead(r.Context(), taskID)
	if err != nil || row.State != store.ProducedReady {
		return nil, errors.New("produced bundle missing")
	}
	client := s.objects()
	if client == nil {
		return nil, errors.New("produced bundle missing")
	}
	rc, err := client.Get(r.Context(), headKey(row.RunID, row.ExecutionID))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rc.Close() }()
	return io.ReadAll(io.LimitReader(rc, bundleCap+1))
}

func (s *Server) removeProducedBundle(r *http.Request) error {
	runID := r.PathValue("runId")
	token, ok := bearer(r)
	if !ok {
		return errBundleUnauthorized
	}
	if _, ok, err := s.matchToken(r.Context(), runID, tokenHash(token)); err != nil || !ok {
		return errBundleUnauthorized
	}
	_, taskID, invocationID, err := s.store.RunSeat(r.Context(), runID)
	if err != nil {
		return err
	}
	row, err := s.store.GetProducedHead(r.Context(), taskID)
	if err != nil {
		return err
	}
	retain, err := s.retainProducedBundle(r.Context(), invocationID)
	if err != nil {
		return err
	}
	if retain {
		return nil
	}
	client := s.objects()
	if client == nil {
		return errors.New("produced bundle missing")
	}
	return client.Delete(r.Context(), headKey(row.RunID, row.ExecutionID))
}

func readBundle(body io.Reader) ([]byte, error) {
	if body == nil {
		return nil, errors.New("produced bundle: empty")
	}
	buf, err := io.ReadAll(io.LimitReader(body, bundleCap+1))
	if err != nil {
		return nil, err
	}
	if len(buf) > bundleCap {
		return nil, errors.New("produced bundle: larger than 32 MiB")
	}
	if len(buf) == 0 {
		return nil, errors.New("produced bundle: empty")
	}
	return buf, nil
}

func headKey(runID, execID string) string {
	return "runs/" + runID + "/" + execID + "/head.bundle"
}

func (s *Server) objects() objectClient {
	if v, ok := bundleClients.Load(s); ok {
		client, _ := v.(objectClient)
		return client
	}
	if strings.TrimSpace(s.cfg.S3.Endpoint) == "" {
		return nil
	}
	return &s3Objects{S3: s.cfg.S3}
}

func bearer(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if !strings.HasPrefix(h, prefix) {
		return "", false
	}
	token := strings.TrimSpace(strings.TrimPrefix(h, prefix))
	return token, token != ""
}

func commitHex40(s string) bool {
	if len(s) != 40 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

type s3Objects struct {
	artifacts.S3
}

func (o s3Objects) Delete(ctx context.Context, key string) error {
	u, err := objectURL(o.S3, key)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, u.String(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Amz-Content-Sha256", sha256HexEmpty())
	if err := signEmptyDelete(req, o.AccessKey, o.SecretKey, o.Region, time.Now()); err != nil {
		return err
	}
	client := o.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = res.Body.Close() }()
	_, _ = io.Copy(io.Discard, res.Body)
	if res.StatusCode/100 != 2 && res.StatusCode != http.StatusNotFound {
		return errors.New("produced bundle: delete")
	}
	return nil
}

func objectURL(s artifacts.S3, key string) (*url.URL, error) {
	endpoint := strings.TrimSpace(s.Endpoint)
	if endpoint == "" {
		return nil, errors.New("s3: endpoint is empty")
	}
	if !strings.Contains(endpoint, "://") {
		endpoint = "http://" + endpoint
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, err
	}
	key = strings.TrimPrefix(key, "/")
	if s.PathStyle {
		u.Path = "/" + s.Bucket + "/" + key
	} else {
		u.Host = s.Bucket + "." + u.Host
		u.Path = "/" + key
	}
	return u, nil
}

func sha256HexEmpty() string {
	sum := sha256.Sum256(nil)
	return hex.EncodeToString(sum[:])
}

func (s *Server) reviewerHead(ctx context.Context, invocationID, assignee string) (store.ProducedHead, error) {
	policy, err := s.store.GetNativePolicy(ctx, invocationID)
	if errors.Is(err, store.ErrNotFound) {
		return store.ProducedHead{}, nil
	}
	if err != nil {
		return store.ProducedHead{}, err
	}
	impl, rev, ok := seatNames(policy)
	if !ok || assignee != rev {
		return store.ProducedHead{}, nil
	}
	implID, err := s.store.TaskByAssignee(ctx, invocationID, impl)
	if err != nil {
		return store.ProducedHead{}, errors.New("produced head: implementer: missing")
	}
	row, err := s.store.GetProducedHead(ctx, implID)
	if err != nil || row.State != store.ProducedReady || !commitHex40(row.HeadSHA) {
		return store.ProducedHead{}, errors.New("produced head: implementer: not ready")
	}
	return row, nil
}

func (s *Server) requireReadyImplementer(ctx context.Context, parent store.NativeTask) error {
	policy, err := s.store.GetNativePolicy(ctx, parent.InvocationID)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	impl, _, ok := seatNames(policy)
	if !ok {
		return nil
	}
	kids, err := s.store.RequiredChildren(ctx, parent.ID)
	if err != nil {
		return err
	}
	for _, kid := range kids {
		if kid.Assignee != impl || kid.Status != store.NativeCompleted {
			continue
		}
		row, err := s.store.GetProducedHead(ctx, kid.ID)
		if err != nil || row.State != store.ProducedReady {
			return errors.New("produced head: implementer: missing")
		}
	}
	return nil
}

type seatClass struct {
	kind   string
	taskID string
}

func (s *Server) classifySeat(ctx context.Context, run store.Run) (seatClass, error) {
	if s.store == nil || run.WorkItemID == "" || run.ID == "" {
		return seatClass{}, nil
	}
	item, err := s.store.GetWorkItem(ctx, run.WorkItemID)
	if errors.Is(err, store.ErrNotFound) || item.InvocationID == "" {
		return seatClass{}, nil
	}
	if err != nil {
		return seatClass{}, err
	}
	policy, err := s.store.GetNativePolicy(ctx, item.InvocationID)
	if errors.Is(err, store.ErrNotFound) {
		return seatClass{}, nil
	}
	if err != nil {
		return seatClass{}, err
	}
	_, rev, ok := seatNames(policy)
	if !ok {
		return seatClass{}, nil
	}
	assignee, taskID, _, err := s.store.RunSeat(ctx, run.ID)
	if errors.Is(err, store.ErrNotFound) {
		return seatClass{kind: "other"}, nil
	}
	if err != nil {
		return seatClass{}, err
	}
	if assignee == rev {
		return seatClass{kind: "reviewer", taskID: taskID}, nil
	}
	return seatClass{kind: "other"}, nil
}

func seatNames(policy store.NativePolicy) (implementer, reviewer string, ok bool) {
	if policy.Implementer == "" || policy.Reviewer == "" {
		return "", "", false
	}
	seen := map[string]struct{}{}
	for _, persona := range policy.Personas {
		seen[persona.Name] = struct{}{}
	}
	if _, ok := seen[policy.Implementer]; !ok {
		return "", "", false
	}
	if _, ok := seen[policy.Reviewer]; !ok {
		return "", "", false
	}
	return policy.Implementer, policy.Reviewer, true
}

// bindDeclaration copies the applied declaration onto the tree that will be
// frozen. An omitted native block, or an empty slug, leaves that seat empty.
// Depth does not choose the implementer or the reviewer.
func (s *Server) bindDeclaration(tree definition.NativeTree) definition.NativeTree {
	if layout := s.roleLayout(); layout.Declared() {
		tree.RosterPath = layout.RosterPath
		tree.PathTemplate = layout.PathTemplate
		tree.SeatFiles = append([]string{}, layout.Files...)
	}
	if native := s.nativeDeclaration(); native != nil {
		tree.Implementer = native.Implementer
		tree.Reviewer = native.Reviewer
	}
	return tree
}

func (s *Server) nativeDeclaration() *definition.NativeDeclaration {
	snap := s.snapshot()
	if snap.tree == nil {
		return nil
	}
	return snap.tree.Factory.Native
}

func (s *Server) roleLayout() definition.RoleLayout {
	snap := s.snapshot()
	if snap.tree == nil || snap.tree.Factory.RoleSource == nil {
		return definition.RoleLayout{}
	}
	src := snap.tree.Factory.RoleSource
	layout := definition.RoleLayout{
		RosterPath:   src.Roster.Path,
		PathTemplate: src.Seats.PathTemplate,
		Files:        append([]string{}, src.Seats.Files...),
	}
	if snap.tree.Factory.Native != nil {
		n := snap.tree.Factory.Native
		layout.MaxDepth = n.Limits.MaxDepth
		layout.AbsoluteMaxDepth = n.Limits.AbsoluteMaxDepth
		layout.Root = n.Root
		layout.Implementer = n.Implementer
		layout.Reviewer = n.Reviewer
	}
	return layout
}

func (s *Server) declaredDepth() (maxDepth, absolute int) {
	maxDepth = definition.DefaultNativeMaxDepth
	absolute = definition.DefaultNativeAbsoluteMaxDepth
	if n := s.nativeDeclaration(); n != nil {
		if n.Limits.MaxDepth > 0 {
			maxDepth = n.Limits.MaxDepth
		}
		if n.Limits.AbsoluteMaxDepth > 0 {
			absolute = n.Limits.AbsoluteMaxDepth
		}
	}
	return maxDepth, absolute
}

func (s *Server) checkOpenDepth(tree definition.NativeTree) error {
	maxDepth, absolute := s.declaredDepth()
	if tree.AbsoluteMaxDepth > definition.NativeAbsoluteMaxDepth || tree.AbsoluteMaxDepth > absolute {
		return fmt.Errorf("%w: absoluteMaxDepth", definition.ErrNativeTree)
	}
	if tree.MaxDepth > maxDepth {
		return fmt.Errorf("%w: maxDepth", definition.ErrNativeTree)
	}
	return nil
}

// attestIfCommit rejects a role_files open whose sourceCommit is not 40 hex
// before the policy insert. A frozen policy is returned as-is. A legacy tree
// with no pins and no seat files is not a git lookup. The checkout is only
// the per-factory cache for (factory_id, source_commit).
func (s *Server) attestIfCommit(ctx context.Context, factoryID, invocationID string, tree definition.NativeTree) error {
	if invocationID != "" && s.store != nil {
		_, err := s.store.GetNativePolicy(ctx, invocationID)
		if err == nil {
			return nil
		}
		if !errors.Is(err, store.ErrNotFound) {
			return err
		}
	}
	if err := s.checkOpenDepth(tree); err != nil {
		return err
	}
	if !roleFilesNeedsAttest(tree) {
		return nil
	}
	if !commitHex40(tree.SourceCommit) {
		return errors.New("attest: sourceCommit: not a commit")
	}
	layout := s.roleLayout()
	if !layout.Declared() {
		return errors.New("attest: roleSource: absent object")
	}
	dir := roleCacheDir(factoryID, tree.SourceCommit)
	if dir == "" {
		return errors.New("attest: absent object")
	}
	return definition.AttestRoleSource(ctx, dir, tree.SourceCommit, tree, layout)
}

var (
	roleCacheMu sync.Mutex
	roleCache   = map[string]string{}
)

// PutRoleCache records the local checkout for one factory and source commit.
// The attest compare reads that directory and does not fetch.
func (s *Server) PutRoleCache(factoryID, commit, dir string) {
	if factoryID == "" || dir == "" || !commitHex40(commit) {
		return
	}
	roleCacheMu.Lock()
	roleCache[factoryID+"\x00"+strings.ToLower(commit)] = dir
	roleCacheMu.Unlock()
}

func roleCacheDir(factoryID, commit string) string {
	roleCacheMu.Lock()
	defer roleCacheMu.Unlock()
	return roleCache[factoryID+"\x00"+strings.ToLower(commit)]
}

func roleFilesNeedsAttest(tree definition.NativeTree) bool {
	if tree.BundleMode == definition.BundleModeRoleFiles || tree.SourceCommit != "" || tree.RosterSHA256 != "" || tree.RosterGitBlob != "" {
		return true
	}
	for _, persona := range tree.Personas {
		if len(persona.Files) > 0 {
			return true
		}
	}
	return false
}

func signEmptyDelete(req *http.Request, accessKey, secretKey, region string, now time.Time) error {
	if accessKey == "" || secretKey == "" || region == "" {
		return errors.New("s3: incomplete signing parameters")
	}
	amzDate := now.UTC().Format("20060102T150405Z")
	req.Header.Set("X-Amz-Date", amzDate)
	sum := sha256.Sum256(nil)
	payloadHash := hex.EncodeToString(sum[:])
	canon, signedHeaders := canonicalDelete(req, payloadHash)
	scope := amzDate[:8] + "/" + region + "/s3/aws4_request"
	stringToSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + sha256HexBytes([]byte(canon))
	sig := hex.EncodeToString(hmacSHA256(deleteSigningKey(secretKey, amzDate[:8], region), []byte(stringToSign)))
	req.Header.Set("Authorization", fmt.Sprintf(
		"AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		accessKey, scope, signedHeaders, sig,
	))
	return nil
}

func canonicalDelete(req *http.Request, payloadHash string) (string, string) {
	host := req.Host
	if host == "" && req.URL != nil {
		host = req.URL.Host
	}
	type header struct {
		name  string
		value string
	}
	headers := []header{{name: "host", value: strings.Join(strings.Fields(host), " ")}}
	for name, values := range req.Header {
		lower := strings.ToLower(name)
		if lower == "host" || lower == "authorization" {
			continue
		}
		cleaned := make([]string, len(values))
		for i, value := range values {
			cleaned[i] = strings.Join(strings.Fields(value), " ")
		}
		headers = append(headers, header{name: lower, value: strings.Join(cleaned, ",")})
	}
	sort.Slice(headers, func(i, j int) bool { return headers[i].name < headers[j].name })
	var canonHeaders strings.Builder
	names := make([]string, len(headers))
	for i, h := range headers {
		canonHeaders.WriteString(h.name)
		canonHeaders.WriteByte(':')
		canonHeaders.WriteString(h.value)
		canonHeaders.WriteByte('\n')
		names[i] = h.name
	}
	signed := strings.Join(names, ";")
	uri := "/"
	if req.URL != nil {
		if escaped := req.URL.EscapedPath(); escaped != "" {
			uri = escaped
		}
	}
	canon := strings.Join([]string{req.Method, uri, "", canonHeaders.String(), signed, payloadHash}, "\n")
	return canon, signed
}

func deleteSigningKey(secret, dateStamp, region string) []byte {
	kDate := hmacSHA256([]byte("AWS4"+secret), []byte(dateStamp))
	kRegion := hmacSHA256(kDate, []byte(region))
	kService := hmacSHA256(kRegion, []byte("s3"))
	return hmacSHA256(kService, []byte("aws4_request"))
}

func hmacSHA256(key, data []byte) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(data)
	return mac.Sum(nil)
}

func sha256HexBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
