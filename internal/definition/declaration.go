package definition

import (
	"fmt"
	"math"
	"math/big"
	"path/filepath"
	"strings"
)

const (
	// DefaultNativeMaxDepth is the maxDepth used when native is set and the
	// field is omitted. The global maximum is NativeAbsoluteMaxDepth.
	DefaultNativeMaxDepth = NativeAbsoluteMaxDepth
	// DefaultNativeAbsoluteMaxDepth is the absoluteMaxDepth used when native
	// is set and the field is omitted.
	DefaultNativeAbsoluteMaxDepth = NativeAbsoluteMaxDepth
	// DefaultEvidenceSentinel is the reviewed-blob marker.
	DefaultEvidenceSentinel = "factoryEvidencePass"
	// DefaultEvidenceBreak is the marker the harness writes over the sentinel.
	DefaultEvidenceBreak = "factoryEvidenceFail"

	maxSeatFiles = 16
	maxPersonas  = 256
)

func applyDeclarationDefaults(f *Factory, raw map[string]any) {
	if f.Native != nil {
		limits := childMap(childMap(raw, "native"), "limits")
		if _, ok := limits["maxDepth"]; !ok {
			f.Native.Limits.MaxDepth = DefaultNativeMaxDepth
		}
		if _, ok := limits["absoluteMaxDepth"]; !ok {
			f.Native.Limits.AbsoluteMaxDepth = DefaultNativeAbsoluteMaxDepth
		}
	}
	if f.Evidence != nil {
		ev := childMap(raw, "evidence")
		if _, ok := ev["sentinel"]; !ok {
			f.Evidence.Sentinel = DefaultEvidenceSentinel
		}
		if _, ok := ev["break"]; !ok {
			f.Evidence.Break = DefaultEvidenceBreak
		}
	}
}

func validateDeclaration(f *Factory, raw map[string]any, lines map[string]int, diags *[]Diagnostic) {
	const file = "factory.yaml"
	validateRepoCeilings(file, raw, lines, diags)
	validateRoleSource(file, f, raw, lines, diags)
	validateNative(file, f, lines, diags)
	validateEvidence(file, f, lines, diags)
	validateBudget(file, f, raw, lines, diags)
	validatePublish(file, f, lines, diags)
	validateRates(file, f, raw, lines, diags)
}

func validateRepoCeilings(file string, raw map[string]any, lines map[string]int, diags *[]Diagnostic) {
	items, _ := raw["repositories"].([]any)
	for _, item := range items {
		obj, ok := item.(map[string]any)
		if !ok {
			continue
		}
		v, set := obj["ceilingCents"]
		if !set {
			continue
		}
		n, ok := wholeInt(v)
		if !ok || n < 1 {
			addDiag(diags, file, lineAt(lines, "repositories.ceilingCents"), SeverityError, "repositories.ceilingCents must be a positive integer")
		}
	}
}

func validateRoleSource(file string, f *Factory, raw map[string]any, lines map[string]int, diags *[]Diagnostic) {
	if f.RoleSource == nil {
		return
	}
	if !repoListed(f.Repositories, f.RoleSource.Repo) {
		addDiag(diags, file, lineAt(lines, "roleSource.repo"), SeverityError, "roleSource.repo is not in repositories")
	}
	if !stableRelPath(f.RoleSource.Roster.Path) {
		addDiag(diags, file, lineAt(lines, "roleSource.roster.path"), SeverityError, "roster.path must be a relative path with no ..")
	}
	if !pathTemplateOK(f.RoleSource.Seats.PathTemplate) {
		addDiag(diags, file, lineAt(lines, "roleSource.seats.pathTemplate"), SeverityError, "seats.pathTemplate must contain {slug} once and {file} once, and no other braces")
	}
	if msg := seatFilesOK(f.RoleSource.Seats.Files); msg != "" {
		addDiag(diags, file, lineAt(lines, "roleSource.seats.files"), SeverityError, msg)
	}
	if !ceilingSet(raw) {
		addDiag(diags, file, lineAt(lines, "budget.ceilingCents"), SeverityError, "budget.ceilingCents is required when roleSource is set")
	}
}

func validateNative(file string, f *Factory, lines map[string]int, diags *[]Diagnostic) {
	if f.Native == nil {
		return
	}
	checkDepthField(file, "native.limits.maxDepth", "maxDepth", f.Native.Limits.MaxDepth, lines, diags)
	checkDepthField(file, "native.limits.absoluteMaxDepth", "absoluteMaxDepth", f.Native.Limits.AbsoluteMaxDepth, lines, diags)
	if len(f.Native.Seats) > maxPersonas {
		addDiag(diags, file, lineAt(lines, "native.seats"), SeverityError, "native.seats must contain at most 256 slugs")
	}
	seen := map[string]struct{}{}
	for _, slug := range f.Native.Seats {
		if strings.TrimSpace(slug) == "" || strings.ContainsAny(slug, `/\`) {
			addDiag(diags, file, lineAt(lines, "native.seats"), SeverityError, "native.seats entries must be single slugs")
			continue
		}
		if _, ok := seen[slug]; ok {
			addDiag(diags, file, lineAt(lines, "native.seats"), SeverityError, "native.seats must be unique")
			continue
		}
		seen[slug] = struct{}{}
	}
	if len(seen) == 0 {
		return
	}
	requireSeat(file, "native.root", f.Native.Root, seen, lines, diags)
	requireSeat(file, "native.implementer", f.Native.Implementer, seen, lines, diags)
	requireSeat(file, "native.reviewer", f.Native.Reviewer, seen, lines, diags)
}

func validateEvidence(file string, f *Factory, lines map[string]int, diags *[]Diagnostic) {
	if f.Evidence == nil {
		return
	}
	ev := f.Evidence
	if ev.Package != "" && !evidencePackageOK(ev.Package) {
		addDiag(diags, file, lineAt(lines, "evidence.package"), SeverityError, "evidence.package must be a relative path")
	}
	if ev.Mutation != "" && !evidenceMutationOK(ev.Mutation) {
		addDiag(diags, file, lineAt(lines, "evidence.mutation"), SeverityError, "evidence.mutation must be a relative path")
	}
	if !markerOK(ev.Sentinel) {
		addDiag(diags, file, lineAt(lines, "evidence.sentinel"), SeverityError, "evidence.sentinel must be 1 to 64 bytes with no newline")
	}
	if !markerOK(ev.Break) {
		addDiag(diags, file, lineAt(lines, "evidence.break"), SeverityError, "evidence.break must be 1 to 64 bytes with no newline")
	}
	if ev.Sentinel == ev.Break {
		addDiag(diags, file, lineAt(lines, "evidence.break"), SeverityError, "evidence.break must differ from evidence.sentinel")
	}
	requireActors(file, "evidence is set", f, lines, diags)
}

func validateBudget(file string, f *Factory, raw map[string]any, lines map[string]int, diags *[]Diagnostic) {
	if !ceilingSet(raw) {
		return
	}
	if f.Budget == nil || f.Budget.CeilingCents < 1 {
		addDiag(diags, file, lineAt(lines, "budget.ceilingCents"), SeverityError, "budget.ceilingCents must be a positive integer")
	}
}

func validatePublish(file string, f *Factory, lines map[string]int, diags *[]Diagnostic) {
	if f.Publish == nil {
		return
	}
	if f.Publish.Repo != nil && !repoListed(f.Repositories, *f.Publish.Repo) {
		addDiag(diags, file, lineAt(lines, "publish.repo"), SeverityError, "publish.repo is not in repositories")
	}
	if !f.Publish.Enabled {
		return
	}
	if f.Publish.Repo == nil {
		addDiag(diags, file, lineAt(lines, "publish.repo"), SeverityError, "publish.repo is not in repositories")
	}
	if f.Budget == nil || f.Budget.CeilingCents < 1 {
		addDiag(diags, file, lineAt(lines, "budget.ceilingCents"), SeverityError, "budget.ceilingCents is required when publish.enabled is true")
	}
	pkg, mut := "", ""
	if f.Evidence != nil {
		pkg, mut = f.Evidence.Package, f.Evidence.Mutation
	}
	if strings.TrimSpace(pkg) == "" || !evidencePackageOK(pkg) {
		addDiag(diags, file, lineAt(lines, "evidence.package"), SeverityError, "evidence.package is required when publish.enabled is true")
	}
	if strings.TrimSpace(mut) == "" || !evidenceMutationOK(mut) {
		addDiag(diags, file, lineAt(lines, "evidence.mutation"), SeverityError, "evidence.mutation is required when publish.enabled is true")
	}
	requireActors(file, "publish.enabled is true", f, lines, diags)
}

func requireActors(file, why string, f *Factory, lines map[string]int, diags *[]Diagnostic) {
	impl, rev := "", ""
	if f.Native != nil {
		impl, rev = f.Native.Implementer, f.Native.Reviewer
	}
	if strings.TrimSpace(impl) == "" {
		addDiag(diags, file, lineAt(lines, "native.implementer"), SeverityError, "native.implementer is required when "+why)
	}
	if strings.TrimSpace(rev) == "" {
		addDiag(diags, file, lineAt(lines, "native.reviewer"), SeverityError, "native.reviewer is required when "+why)
	}
}

func validateRates(file string, f *Factory, raw map[string]any, lines map[string]int, diags *[]Diagnostic) {
	ratesRaw, ok := childMap(raw, "inference")["rates"]
	if !ok {
		return
	}
	rates, ok := ratesRaw.(map[string]any)
	if !ok || f.Inference == nil || f.Inference.Rates == nil {
		addDiag(diags, file, lineAt(lines, "inference.rates"), SeverityError, "inference.rates must be a spend price document")
		return
	}
	doc := f.Inference.Rates
	if _, set := rates["prompt_byte_cap"]; !set || doc.PromptByteCap < 0 {
		addDiag(diags, file, lineAt(lines, "inference.rates.prompt_byte_cap"), SeverityError, "inference.rates.prompt_byte_cap must be a non-negative integer")
	}
	if _, set := rates["max_tokens"]; !set || doc.MaxTokens < 1 {
		addDiag(diags, file, lineAt(lines, "inference.rates.max_tokens"), SeverityError, "inference.rates.max_tokens must be a positive integer")
	}
	models, _ := rates["models"].([]any)
	if len(doc.Models) == 0 || len(models) == 0 {
		addDiag(diags, file, lineAt(lines, "inference.rates.models"), SeverityError, "inference.rates.models must name at least one model")
		return
	}
	for i, model := range doc.Models {
		var obj map[string]any
		if i < len(models) {
			obj, _ = models[i].(map[string]any)
		}
		path := "inference.rates.models"
		if strings.TrimSpace(model.Name) == "" {
			addDiag(diags, file, lineAt(lines, path), SeverityError, "inference.rates.models.name is required")
		}
		if !decimalRateOK(model.InputCostPerToken) {
			addDiag(diags, file, lineAt(lines, path+".input_cost_per_token"), SeverityError, "inference.rates.models.input_cost_per_token must be a decimal rate")
		}
		if !decimalRateOK(model.OutputCostPerToken) {
			addDiag(diags, file, lineAt(lines, path+".output_cost_per_token"), SeverityError, "inference.rates.models.output_cost_per_token must be a decimal rate")
		}
		if _, set := obj["cache_creation_input_token_cost"]; set && !decimalRateOK(model.CacheCreationInputTokenCost) {
			addDiag(diags, file, lineAt(lines, path+".cache_creation_input_token_cost"), SeverityError, "inference.rates.models.cache_creation_input_token_cost must be a decimal rate")
		}
		checkExtraRates(file, path+".input_above", model.InputAbove, lines, diags)
		checkExtraRates(file, path+".output_above", model.OutputAbove, lines, diags)
	}
}

func checkExtraRates(file, path string, rates []string, lines map[string]int, diags *[]Diagnostic) {
	for _, rate := range rates {
		if !decimalRateOK(rate) {
			addDiag(diags, file, lineAt(lines, path), SeverityError, path+" must be decimal rates")
			return
		}
	}
}

func checkDepthField(file, path, name string, n int, lines map[string]int, diags *[]Diagnostic) {
	if n < 1 || n > NativeAbsoluteMaxDepth {
		addDiag(diags, file, lineAt(lines, path), SeverityError, fmt.Sprintf("%s must be from 1 to %d", name, NativeAbsoluteMaxDepth))
	}
}

func requireSeat(file, path, slug string, seats map[string]struct{}, lines map[string]int, diags *[]Diagnostic) {
	if slug == "" {
		return
	}
	if _, ok := seats[slug]; !ok {
		addDiag(diags, file, lineAt(lines, path), SeverityError, path+" must be one of native.seats")
	}
}

func ceilingSet(raw map[string]any) bool {
	budget := childMap(raw, "budget")
	v, ok := budget["ceilingCents"]
	if !ok {
		return false
	}
	n, ok := wholeInt(v)
	return ok && n >= 1
}

func repoListed(repos []Repository, repo Repository) bool {
	if strings.TrimSpace(repo.Owner) == "" || strings.TrimSpace(repo.Name) == "" {
		return false
	}
	for _, item := range repos {
		if item.Owner == repo.Owner && item.Name == repo.Name && item.CodeForge == repo.CodeForge {
			return true
		}
	}
	return false
}

func stableRelPath(p string) bool {
	if p == "" || strings.HasPrefix(p, "/") || strings.Contains(p, `\`) || strings.Contains(p, "\x00") {
		return false
	}
	if filepath.IsAbs(p) || filepath.Clean(p) != p {
		return false
	}
	for _, part := range strings.Split(p, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

func pathTemplateOK(tmpl string) bool {
	if strings.Count(tmpl, "{slug}") != 1 || strings.Count(tmpl, "{file}") != 1 {
		return false
	}
	rest := strings.ReplaceAll(strings.ReplaceAll(tmpl, "{slug}", ""), "{file}", "")
	return !strings.ContainsAny(rest, "{}")
}

func seatFilesOK(files []string) string {
	if len(files) < 1 || len(files) > maxSeatFiles {
		return "seats.files must be 1 to 16 unique path segments"
	}
	seen := map[string]struct{}{}
	for _, name := range files {
		if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) || strings.Contains(name, "\x00") {
			return "seats.files must be 1 to 16 unique path segments"
		}
		if _, ok := seen[name]; ok {
			return "seats.files must be 1 to 16 unique path segments"
		}
		seen[name] = struct{}{}
	}
	return ""
}

func evidencePackageOK(pkg string) bool {
	if pkg == "" || pkg == "." {
		return true
	}
	return lexicalRel(strings.TrimPrefix(pkg, "./"))
}

func evidenceMutationOK(rel string) bool {
	if rel == "" {
		return true
	}
	return lexicalRel(rel)
}

func lexicalRel(rel string) bool {
	if rel == "" || strings.HasPrefix(rel, "/") || strings.Contains(rel, `\`) || strings.Contains(rel, "\x00") {
		return false
	}
	for _, part := range strings.Split(rel, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

func markerOK(s string) bool {
	if strings.Contains(s, "\n") || strings.Contains(s, "\r") {
		return false
	}
	n := len([]byte(s))
	return n >= 1 && n <= 64
}

func decimalRateOK(raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return false
	}
	r := new(big.Rat)
	if _, ok := r.SetString(raw); !ok || r.Sign() < 0 {
		return false
	}
	return true
}

func wholeInt(v any) (int64, bool) {
	switch n := v.(type) {
	case int:
		return int64(n), true
	case int64:
		return n, true
	case uint64:
		if n > math.MaxInt64 {
			return 0, false
		}
		return int64(n), true
	case float64:
		if math.IsNaN(n) || math.IsInf(n, 0) || n != math.Trunc(n) {
			return 0, false
		}
		if n > math.MaxInt64 || n < math.MinInt64 {
			return 0, false
		}
		return int64(n), true
	default:
		return 0, false
	}
}
