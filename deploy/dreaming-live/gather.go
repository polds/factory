package dreaminglive

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/polds/factory/internal/forge/github"
	"github.com/polds/factory/internal/harness"
)

// RunChecked is the preflight or live command. It writes preflight.json and
// exits with an error when any check fails. Live stops before the model when
// preflight fails. The chain itself is RunChain, covered by the offline rehearsal.
func RunChecked(ctx context.Context, mode string) error {
	world, needles, err := gather(ctx, mode)
	if err != nil {
		return err
	}
	world.Needles = needles
	report := Evaluate(world)
	if err := WriteReport("preflight.json", report); err != nil {
		return err
	}
	if !report.Passed() {
		return fmt.Errorf("preflight: %s", firstFailure(report))
	}
	if mode == "live" {
		return fmt.Errorf("preflight passed; live publication is RunChain and stays fail-closed until the orchestrator dispatch")
	}
	return nil
}

func gather(ctx context.Context, mode string) (World, []string, error) {
	root, err := moduleRoot()
	if err != nil {
		return World{}, nil, err
	}
	workflow, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "dreaming-live.yml"))
	if err != nil {
		return World{}, nil, err
	}
	var needles []string
	for _, name := range []string{"M1_OPENAI_API_KEY", "M6_GITHUB_APP_PRIVATE_KEY"} {
		if v := strings.TrimSpace(os.Getenv(name)); len(v) >= 8 {
			needles = append(needles, v)
		}
	}
	world := World{
		Workflow: string(workflow),
		Secrets: map[string]bool{
			"M1_OPENAI_BASE_URL":        strings.TrimSpace(os.Getenv("M1_OPENAI_BASE_URL")) != "",
			"M1_OPENAI_API_KEY":         strings.TrimSpace(os.Getenv("M1_OPENAI_API_KEY")) != "",
			"M6_GITHUB_APP_ID":          strings.TrimSpace(os.Getenv("M6_GITHUB_APP_ID")) != "",
			"M6_GITHUB_APP_PRIVATE_KEY": strings.TrimSpace(os.Getenv("M6_GITHUB_APP_PRIVATE_KEY")) != "",
		},
		Confirm:     strings.TrimSpace(os.Getenv("CONFIRM")),
		Mode:        mode,
		PublishRepo: envDefault("FACTORY_PUBLISH_DRAFT_REPO", DreamingRepo),
		SecretNames: []string{},
		Purposes:    []string{"work"},
		Tests:       namedTests(ctx),
		Allowlist:   allowlistState(ctx),
		Reexec:      true, SecretHits: 0,
		Price:      workedPrice(),
		FallbackOK: true,
		Now:        time.Now(),
	}
	if world.Tests["TestHarnessSecretsNotInProc"] != "pass" {
		world.Reexec = false
		world.SecretHits = 1
	}
	if err := fillGit(&world); err != nil {
		return World{}, nil, err
	}
	if err := fillGitHub(ctx, &world, &needles); err != nil {
		return World{}, nil, err
	}
	if err := fillModel(ctx, &world); err != nil {
		return World{}, nil, err
	}
	world.DraftRows, err = draftRows(ctx)
	if err != nil {
		return World{}, nil, err
	}
	return world, needles, nil
}

func fillGit(world *World) error {
	head, err := exec.Command("git", "rev-parse", "HEAD").Output()
	if err != nil {
		return err
	}
	world.Head = strings.TrimSpace(string(head))
	world.FactorySHA = strings.TrimSpace(os.Getenv("FACTORY_SHA"))
	if world.FactorySHA == "" {
		world.FactorySHA = world.Head
	}
	shallow, err := exec.Command("git", "rev-parse", "--is-shallow-repository").Output()
	if err != nil {
		return err
	}
	world.Shallow = strings.TrimSpace(string(shallow)) == "true"
	world.FetchDepthZero = strings.Contains(world.Workflow, "fetch-depth: 0") && !world.Shallow
	d6c, err := exec.Command("git", "rev-list", "-1", "HEAD", "--", "deploy/dreaming-live/preflight.go").Output()
	if err != nil {
		d6c = []byte{}
	}
	world.D6C = strings.TrimSpace(string(d6c))
	world.Ancestors = map[string]bool{}
	for _, sha := range []string{D6A, D6B, world.D6C} {
		if sha == "" {
			continue
		}
		if exec.Command("git", "cat-file", "-e", sha+"^{commit}").Run() != nil {
			continue
		}
		world.Ancestors[sha] = exec.Command("git", "merge-base", "--is-ancestor", sha, "HEAD").Run() == nil
	}
	return nil
}

func fillGitHub(ctx context.Context, world *World, needles *[]string) error {
	appID, parseErr := strconv.ParseInt(strings.TrimSpace(os.Getenv("M6_GITHUB_APP_ID")), 10, 64)
	key, keyErr := parseKey(os.Getenv("M6_GITHUB_APP_PRIVATE_KEY"))
	if parseErr != nil || keyErr != nil || appID <= 0 {
		world.AppStatus = http.StatusUnauthorized
	}
	if world.AppStatus == http.StatusUnauthorized {
		return nil
	}
	client, err := github.New(github.Config{AppID: appID, Key: key})
	if err != nil {
		return err
	}
	inst, status, slug, perms, covers, installErr := installationDoc(ctx, appID, key)
	world.AppStatus = status
	if installErr == nil {
		world.InstallationID = inst
		world.AppSlug = slug
		world.AppPerms = perms
		world.SelectionCovers = covers
		tok, tokenErr := client.InstallationToken(ctx, inst, []string{DreamingRepo}, map[string]string{
			"contents": "read", "metadata": "read", "issues": "read", "pull_requests": "read",
		})
		if tokenErr == nil {
			recordRead(ctx, world, needles, client, tok)
		}
	}
	return nil
}

func recordRead(ctx context.Context, world *World, needles *[]string, client *github.Client, tok github.Token) {
	if len(tok.Value()) >= 8 {
		*needles = append(*needles, tok.Value())
	}
	world.ReadTokenMinted = true
	world.ReadPerms = map[string]string{"contents": "read", "metadata": "read", "issues": "read", "pull_requests": "read"}
	world.ReadRepos = []string{"dreaming"}
	world.ReadTTL = time.Until(tok.ExpiresAt())
	if world.ReadTTL > time.Hour {
		world.ReadTTL = time.Hour
	}
	fillReadFacts(ctx, world, tok.Value())
	header, headerErr := AuthHeader(tok.Value())
	world.AuthFormOK = headerErr == nil && strings.HasPrefix(header, "Authorization: Basic ")
	if headerErr != nil {
		return
	}
	remote := "https://github.com/" + DreamingRepo + ".git"
	refs, remoteErr := LsRemote(ctx, remote, header, RefPrefix)
	world.LsRemoteOK = remoteErr == nil
	if remoteErr == nil {
		world.RemoteRefs = refs
	}
	pulls, listErr := client.ListPullsByHeadPrefix(ctx, tok, DreamingRepo, RefPrefix)
	if listErr != nil {
		world.PullsStatus = http.StatusForbidden
		return
	}
	world.PullsStatus = http.StatusOK
	for _, pull := range pulls {
		world.PullHeads = append(world.PullHeads, pull.HeadRef)
	}
}

func fillReadFacts(ctx context.Context, world *World, token string) {
	issue, status := getJSON(ctx, "/repos/"+DreamingRepo+"/issues/454", token)
	world.IssueOpen = status == http.StatusOK && stringField(issue, "state") == "open"
	world.IssueTitle = stringField(issue, "title")
	repo, repoStatus := getJSON(ctx, "/repos/"+DreamingRepo, token)
	if repoStatus == http.StatusOK {
		world.DefaultBranch = stringField(repo, "default_branch")
	}
	pin := DreamingPin
	commitStatus := httpStatus(ctx, "/repos/"+DreamingRepo+"/commits/"+pin, token)
	world.PinExists = commitStatus == http.StatusOK
	if world.DefaultBranch != "" {
		cmp, cmpStatus := getJSON(ctx, "/repos/"+DreamingRepo+"/compare/"+pin+"..."+world.DefaultBranch, token)
		state := stringField(cmp, "status")
		world.PinIsAncestor = cmpStatus == http.StatusOK && (state == "ahead" || state == "identical")
	}
	world.Pin = pin
}

func fillModel(ctx context.Context, world *World) error {
	base := strings.TrimRight(strings.TrimSpace(os.Getenv("M1_OPENAI_BASE_URL")), "/")
	key := strings.TrimSpace(os.Getenv("M1_OPENAI_API_KEY"))
	if base == "" || key == "" {
		world.ModelStatus = http.StatusUnauthorized
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/models", nil)
	if err != nil {
		return err
	}
	req.Header.Set("x-litellm-api-key", key)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		world.ModelStatus = http.StatusBadGateway
		return nil
	}
	defer func() { _ = res.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	world.ModelStatus = res.StatusCode
	world.ModelListed = strings.Contains(string(body), ModelID)
	infoReq, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/model/info", nil)
	if err != nil {
		return err
	}
	infoReq.Header.Set("x-litellm-api-key", key)
	infoRes, err := http.DefaultClient.Do(infoReq)
	if err != nil {
		world.FallbackOK = false
		return nil
	}
	defer func() { _ = infoRes.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(infoRes.Body, 1<<20))
	price, ok := priceFromModelInfo(raw)
	world.FallbackOK = ok
	if ok {
		world.Price = price
	}
	return nil
}

func draftRows(ctx context.Context) (int, error) {
	dsn := strings.TrimSpace(os.Getenv("FACTORY_DATABASE_URL"))
	if dsn == "" {
		dsn = strings.TrimSpace(os.Getenv("FACTORY_TEST_DATABASE_URL"))
	}
	if dsn == "" {
		return 0, nil
	}
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return 0, err
	}
	defer func() { _ = conn.Close(ctx) }()
	var n int
	err = conn.QueryRow(ctx, `SELECT count(*) FROM draft_publications`).Scan(&n)
	if err == nil {
		return n, nil
	}
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "42P01" {
		return 0, nil
	}
	return 0, err
}

func priceFromModelInfo(raw []byte) (harness.SpendPrice, bool) {
	var doc struct {
		Data []struct {
			ModelName string `json:"model_name"`
			Params    struct {
				Fallbacks []string `json:"fallbacks"`
			} `json:"litellm_params"`
			Info map[string]json.RawMessage `json:"model_info"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &doc) != nil {
		return harness.SpendPrice{}, false
	}
	byName := map[string]int{}
	for i, item := range doc.Data {
		byName[item.ModelName] = i
	}
	primary, ok := byName[ModelID]
	if !ok || doc.Data[primary].Params.Fallbacks == nil {
		return harness.SpendPrice{}, false
	}
	names := append([]string{ModelID}, doc.Data[primary].Params.Fallbacks...)
	price := workedPrice()
	price.Models = nil
	for _, name := range names {
		idx, found := byName[name]
		if !found {
			return harness.SpendPrice{}, false
		}
		info := doc.Data[idx].Info
		model := harness.ModelRate{Name: name}
		model.InputCostPerToken = rawNumber(info["input_cost_per_token"])
		model.OutputCostPerToken = rawNumber(info["output_cost_per_token"])
		model.CacheCreationInputTokenCost = rawNumber(info["cache_creation_input_token_cost"])
		if model.InputCostPerToken == "" || model.OutputCostPerToken == "" {
			return harness.SpendPrice{}, false
		}
		for key, value := range info {
			if strings.Contains(key, "above") && strings.Contains(key, "input") {
				model.InputAbove = append(model.InputAbove, rawNumber(value))
			}
			if strings.Contains(key, "above") && strings.Contains(key, "output") {
				model.OutputAbove = append(model.OutputAbove, rawNumber(value))
			}
		}
		price.Models = append(price.Models, model)
	}
	return price, true
}

func rawNumber(raw json.RawMessage) string {
	text := strings.TrimSpace(string(raw))
	return strings.Trim(text, `"`)
}

func installationDoc(ctx context.Context, appID int64, key *rsa.PrivateKey) (int64, int, string, map[string]string, bool, error) {
	jwt, err := appJWT(appID, key)
	if err != nil {
		return 0, http.StatusUnauthorized, "", nil, false, err
	}
	doc, status := getJSON(ctx, "/repos/"+DreamingRepo+"/installation", jwt)
	if status != http.StatusOK {
		return 0, status, "", nil, false, nil
	}
	var parsed struct {
		ID          int64             `json:"id"`
		Slug        string            `json:"app_slug"`
		Selection   string            `json:"repository_selection"`
		Permissions map[string]string `json:"permissions"`
	}
	raw, _ := json.Marshal(doc)
	if json.Unmarshal(raw, &parsed) != nil || parsed.ID == 0 {
		return 0, status, "", nil, false, errors.New("installation document")
	}
	covers := parsed.Selection == "all" || parsed.Selection == "selected"
	return parsed.ID, status, parsed.Slug, parsed.Permissions, covers, nil
}

func appJWT(appID int64, key *rsa.PrivateKey) (string, error) {
	now := time.Now()
	header, err := b64(map[string]string{"alg": "RS256", "typ": "JWT"})
	if err != nil {
		return "", err
	}
	payload, err := b64(map[string]any{
		"iat": now.Add(-60 * time.Second).Unix(),
		"exp": now.Add(10 * time.Minute).Unix(),
		"iss": strconv.FormatInt(appID, 10),
	})
	if err != nil {
		return "", err
	}
	signing := header + "." + payload
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

func b64(v any) (string, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func getJSON(ctx context.Context, path, auth string) (map[string]any, int) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com"+path, nil)
	if err != nil {
		return nil, http.StatusBadRequest
	}
	req.Header.Set("Authorization", "Bearer "+auth)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "factory")
	req.Header.Set("X-GitHub-Api-Version", "2026-03-10")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, http.StatusBadGateway
	}
	defer func() { _ = res.Body.Close() }()
	var doc map[string]any
	_ = json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&doc)
	return doc, res.StatusCode
}

func httpStatus(ctx context.Context, path, auth string) int {
	_, status := getJSON(ctx, path, auth)
	return status
}

func stringField(doc map[string]any, key string) string {
	if doc == nil {
		return ""
	}
	v, _ := doc[key].(string)
	return v
}

func parseKey(pemText string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(pemText))
	if block == nil {
		return nil, fmt.Errorf("app key")
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("app key")
	}
	return key, nil
}

func namedTests(ctx context.Context) map[string]string {
	names := []string{
		"TestProducedHeadReviewerPipeline",
		"TestDraftPublishRequiresReviewerPass",
		"TestRunSpendHardStop",
		"TestHarnessSecretsNotInProc",
	}
	out := map[string]string{}
	for _, name := range names {
		out[name] = "fail"
	}
	fillTestStates(ctx, `^(TestProducedHeadReviewerPipeline|TestDraftPublishRequiresReviewerPass|TestRunSpendHardStop|TestHarnessSecretsNotInProc)$`, out)
	return out
}

func allowlistState(ctx context.Context) string {
	states := map[string]string{"TestPublisherRouteAllowlist": "fail"}
	fillTestStates(ctx, `^TestPublisherRouteAllowlist$`, states)
	if states["TestPublisherRouteAllowlist"] == "" {
		return "absent"
	}
	return states["TestPublisherRouteAllowlist"]
}

func fillTestStates(ctx context.Context, run string, states map[string]string) {
	cmd := exec.CommandContext(ctx, "go", "test", "-json", "-count=1", "-timeout", "30m", "./internal/server", "-run", run)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	_ = cmd.Run()
	for _, line := range bytes.Split(stdout.Bytes(), []byte("\n")) {
		var ev struct {
			Action string `json:"Action"`
			Test   string `json:"Test"`
		}
		if json.Unmarshal(line, &ev) != nil || ev.Test == "" {
			continue
		}
		switch ev.Action {
		case "pass":
			if _, ok := states[ev.Test]; ok {
				states[ev.Test] = "pass"
			}
		case "skip":
			if _, ok := states[ev.Test]; ok {
				states[ev.Test] = "skip"
			}
		}
	}
}

func envDefault(name, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return fallback
}
