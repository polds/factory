package improve

import (
	"math/rand"
	"strings"

	"github.com/polds/factory/internal/definition"
)

// DefaultBlastLimit is the docs/08 blast-radius cap. More changed definition
// files than this label the PR needs-human-design and assign the admins pool.
const DefaultBlastLimit = 3

const needsHumanDesign = "needs-human-design"

// Pools are GitHub logins from factoryd config. T63 fills them. LoginByEmail
// maps selfImprovement.reviewerEmails to those logins.
type Pools struct {
	Admins       []string
	Team         []string
	LoginByEmail map[string]string
}

// Routing is who to request and which labels to add. An empty Reviewer leaves
// the PR unassigned. Pool names the source list.
type Routing struct {
	Reviewer string
	Pool     string
	Labels   []string
}

// Route picks one reviewer. admins and team are random members of those
// pools. custom maps reviewerEmails through Pools.LoginByEmail. none leaves
// the PR unassigned. changedDefinitionFiles greater than blastLimit forces
// the admins pool and the needs-human-design label. rng is the caller's
// seeded source. blastLimit <= 0 uses DefaultBlastLimit. A nil rng uses a
// fixed seed so the function still returns.
func Route(cfg definition.SelfImprovement, pools Pools, changedDefinitionFiles, blastLimit int, rng *rand.Rand) Routing {
	if blastLimit <= 0 {
		blastLimit = DefaultBlastLimit
	}
	if rng == nil {
		rng = rand.New(rand.NewSource(0))
	}
	rt := strings.TrimSpace(cfg.ReviewerType)
	if rt == "" {
		rt = definition.ReviewerAdmins
	}
	if changedDefinitionFiles > blastLimit {
		return Routing{
			Reviewer: pick(pools.Admins, rng),
			Pool:     definition.ReviewerAdmins,
			Labels:   []string{needsHumanDesign},
		}
	}
	switch rt {
	case definition.ReviewerAdmins:
		return Routing{Reviewer: pick(pools.Admins, rng), Pool: definition.ReviewerAdmins}
	case definition.ReviewerTeam:
		return Routing{Reviewer: pick(pools.Team, rng), Pool: definition.ReviewerTeam}
	case definition.ReviewerCustom:
		return Routing{Reviewer: pick(loginsFor(cfg.ReviewerEmails, pools.LoginByEmail), rng), Pool: definition.ReviewerCustom}
	case definition.ReviewerNone:
		return Routing{Pool: definition.ReviewerNone}
	default:
		return Routing{Pool: rt}
	}
}

func pick(pool []string, rng *rand.Rand) string {
	if len(pool) == 0 {
		return ""
	}
	return pool[rng.Intn(len(pool))]
}

func loginsFor(emails []string, byEmail map[string]string) []string {
	var out []string
	seen := map[string]bool{}
	for _, email := range emails {
		login := ""
		if byEmail != nil {
			login = strings.TrimSpace(byEmail[strings.TrimSpace(email)])
		}
		if login == "" || seen[login] {
			continue
		}
		seen[login] = true
		out = append(out, login)
	}
	return out
}
