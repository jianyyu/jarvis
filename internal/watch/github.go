package watch

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"jarvis/internal/config"
)

// ReviewVerdict represents the outcome of a PR review.
type ReviewVerdict string

const (
	VerdictApproved         ReviewVerdict = "APPROVED"
	VerdictChangesRequested ReviewVerdict = "CHANGES_REQUESTED"
	VerdictCommented        ReviewVerdict = "COMMENTED"
	VerdictNone             ReviewVerdict = ""
)

// PRComment represents a single comment on a PR.
type PRComment struct {
	User      string
	Body      string
	Path      string // file path (empty for general comments)
	Line      int    // line number (0 for general comments)
	CreatedAt time.Time
}

// GitHubEvent represents an actionable GitHub PR notification.
type GitHubEvent struct {
	Owner     string
	Repo      string
	PRNumber  int
	PRTitle   string
	PRURL     string        // html_url
	PRAuthor  string
	HeadRef   string        // branch name (e.g. "stack/fix-...")
	Reason    string        // "review_requested" or "author"
	Verdict   ReviewVerdict // most recent review verdict
	Comments  []PRComment   // new human comments
	NotifID   string        // notification thread ID (for marking read)
	Timestamp time.Time
}

// ContextKey returns a unique key for deduplication in the context registry.
func (e GitHubEvent) ContextKey() string {
	return fmt.Sprintf("github:%s/%s#%d", e.Owner, e.Repo, e.PRNumber)
}

// SessionName returns a human-readable session name for the dashboard.
func (e GitHubEvent) SessionName() string {
	if e.Reason == "review_requested" {
		return fmt.Sprintf("gh: Review PR #%d — %s", e.PRNumber, e.PRTitle)
	}
	return fmt.Sprintf("gh: PR #%d — %s", e.PRNumber, e.PRTitle)
}

// InitialPrompt returns the user message that kicks off the Claude session.
func (e GitHubEvent) InitialPrompt() string {
	if e.Reason == "review_requested" {
		return fmt.Sprintf("Please review this GitHub PR: %s\n\n"+
			"Run `gh pr diff %d` to read the full diff. Provide a thorough code\n"+
			"review covering correctness, edge cases, and potential issues. Draft review\n"+
			"comments that I can post.\n\n"+
			"Do NOT post any comments, approve, or take any external-facing actions.",
			e.PRURL, e.PRNumber)
	}

	// Author event — include comments and verdict
	var sb strings.Builder
	fmt.Fprintf(&sb, "Someone left comments on my PR: %s\n\n", e.PRURL)

	if e.Verdict != VerdictNone {
		fmt.Fprintf(&sb, "Review verdict: %s\n\n", e.Verdict)
	}

	if len(e.Comments) > 0 {
		sb.WriteString("New comments since last check:\n")
		for _, c := range e.Comments {
			if c.Path != "" {
				fmt.Fprintf(&sb, "- %s on %s:%d: %q\n", c.User, c.Path, c.Line, truncate(c.Body, 200))
			} else {
				fmt.Fprintf(&sb, "- %s (general comment): %q\n", c.User, truncate(c.Body, 200))
			}
		}
		sb.WriteString("\n")
	}

	fmt.Fprintf(&sb, "Read the PR diff with `gh pr diff %d` for context. Understand the review\n", e.PRNumber)
	sb.WriteString("feedback and draft responses to each comment. If code changes are suggested,\n")
	sb.WriteString("explain what changes would address the feedback.\n\n")
	fmt.Fprintf(&sb, "IMPORTANT: Verify you are on the correct branch (%s) before making\n", e.HeadRef)
	sb.WriteString("any code changes. Run `git branch --show-current` to confirm.\n\n")
	sb.WriteString("Do NOT post any comments or take any external-facing actions.")
	return sb.String()
}

// FollowUpPrompt returns the message injected into an existing session for new activity.
func (e GitHubEvent) FollowUpPrompt() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "[New comments on PR #%d at %s]:\n\n", e.PRNumber, time.Now().Format(time.RFC3339))

	if e.Verdict != VerdictNone {
		fmt.Fprintf(&sb, "Review verdict: %s\n\n", e.Verdict)
	}

	for _, c := range e.Comments {
		if c.Path != "" {
			fmt.Fprintf(&sb, "- %s on %s:%d: %q\n", c.User, c.Path, c.Line, truncate(c.Body, 200))
		} else {
			fmt.Fprintf(&sb, "- %s (general comment): %q\n", c.User, truncate(c.Body, 200))
		}
	}

	fmt.Fprintf(&sb, "\nRun `gh pr diff %d` if you need to refresh context on the changes.\n", e.PRNumber)
	sb.WriteString("Please read the new comments and draft responses.")
	return sb.String()
}

// --- Poller ---

// GitHubPoller polls GitHub notifications and enriches them with PR details.
type GitHubPoller struct {
	owner       string
	repo        string
	username    string
	reasons     map[string]bool
	ignoreUsers map[string]bool
	lastPollTS  string // RFC3339
}

// NewGitHubPoller creates a poller from config.
func NewGitHubPoller(cfg config.GitHubWatcherConfig, lastPollTS string) *GitHubPoller {
	if lastPollTS == "" {
		lastPollTS = time.Now().UTC().Format(time.RFC3339)
	}
	reasons := make(map[string]bool)
	for _, r := range cfg.Reasons {
		reasons[r] = true
	}
	ignoreUsers := make(map[string]bool)
	for _, u := range cfg.IgnoreUsers {
		ignoreUsers[strings.ToLower(u)] = true
	}
	return &GitHubPoller{
		owner:       cfg.Owner,
		repo:        cfg.Repo,
		username:    cfg.Username,
		reasons:     reasons,
		ignoreUsers: ignoreUsers,
		lastPollTS:  lastPollTS,
	}
}

// LastPollTS returns the timestamp of the last poll.
func (p *GitHubPoller) LastPollTS() string {
	return p.lastPollTS
}

// Poll fetches new notifications and enriches them with PR details.
func (p *GitHubPoller) Poll(ctx context.Context, getCommentTS func(string) string) ([]GitHubEvent, error) {
	notifs, err := p.fetchNotifications()
	if err != nil {
		return nil, fmt.Errorf("fetch notifications: %w", err)
	}

	fullName := p.owner + "/" + p.repo
	seen := make(map[int]bool)
	var events []GitHubEvent

	for _, n := range notifs {
		if n.Subject.Type != "PullRequest" {
			continue
		}
		if n.Repository.FullName != fullName {
			continue
		}
		if !p.reasons[n.Reason] {
			continue
		}

		prNum := parsePRNumberFromURL(n.Subject.URL)
		if prNum == 0 {
			continue
		}
		if seen[prNum] {
			continue
		}
		seen[prNum] = true

		pr, err := p.fetchPR(prNum)
		if err != nil {
			log.Printf("github: fetch PR #%d: %v", prNum, err)
			continue
		}
		if pr.State == "closed" {
			log.Printf("github: skipping closed PR #%d", prNum)
			p.markNotificationRead(n.ID)
			continue
		}

		contextKey := fmt.Sprintf("github:%s#%d", fullName, prNum)
		commentSince := getCommentTS(contextKey)
		if commentSince == "" {
			commentSince = p.lastPollTS
		}

		var comments []PRComment
		var verdict ReviewVerdict

		if n.Reason == "author" {
			comments, err = p.fetchNewComments(prNum, commentSince)
			if err != nil {
				log.Printf("github: fetch comments PR #%d: %v", prNum, err)
			}
			verdict = p.fetchLatestVerdict(prNum)

			if len(comments) == 0 {
				// No new human comments — mark read but don't create event
				p.markNotificationRead(n.ID)
				continue
			}
		}

		events = append(events, GitHubEvent{
			Owner:     p.owner,
			Repo:      p.repo,
			PRNumber:  prNum,
			PRTitle:   pr.Title,
			PRURL:     pr.HTMLURL,
			PRAuthor:  pr.User.Login,
			HeadRef:   pr.Head.Ref,
			Reason:    n.Reason,
			Verdict:   verdict,
			Comments:  comments,
			NotifID:   n.ID,
			Timestamp: time.Now(),
		})
	}

	// Advance global poll timestamp
	p.lastPollTS = time.Now().UTC().Format(time.RFC3339)

	return events, nil
}

// markNotificationRead marks a notification as read so it doesn't reappear.
func (p *GitHubPoller) markNotificationRead(threadID string) {
	_, err := ghAPI("PATCH", fmt.Sprintf("/notifications/threads/%s", threadID), nil)
	if err != nil {
		log.Printf("github: mark read %s: %v", threadID, err)
	}
}

// shouldSkipUser returns true if the comment author should be filtered out.
func (p *GitHubPoller) shouldSkipUser(login, userType string) bool {
	if strings.EqualFold(login, p.username) {
		return true
	}
	if userType == "Bot" {
		return true
	}
	if p.ignoreUsers[strings.ToLower(login)] {
		return true
	}
	return false
}

// --- GitHub API types ---

type ghNotification struct {
	ID      string `json:"id"`
	Reason  string `json:"reason"`
	Subject struct {
		Title string `json:"title"`
		URL   string `json:"url"`
		Type  string `json:"type"`
	} `json:"subject"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
	UpdatedAt string `json:"updated_at"`
}

type ghPR struct {
	Number  int    `json:"number"`
	Title   string `json:"title"`
	State   string `json:"state"`
	HTMLURL string `json:"html_url"`
	User    struct {
		Login string `json:"login"`
	} `json:"user"`
	Head struct {
		Ref string `json:"ref"`
	} `json:"head"`
}

type ghReviewComment struct {
	User struct {
		Login string `json:"login"`
		Type  string `json:"type"`
	} `json:"user"`
	Body      string `json:"body"`
	Path      string `json:"path"`
	Line      int    `json:"line"`
	CreatedAt string `json:"created_at"`
}

type ghIssueComment struct {
	User struct {
		Login string `json:"login"`
		Type  string `json:"type"`
	} `json:"user"`
	Body      string `json:"body"`
	CreatedAt string `json:"created_at"`
}

type ghReview struct {
	User struct {
		Login string `json:"login"`
	} `json:"user"`
	State       string `json:"state"`
	SubmittedAt string `json:"submitted_at"`
}

// --- gh CLI helpers ---

func (p *GitHubPoller) fetchNotifications() ([]ghNotification, error) {
	out, err := ghAPI("GET", fmt.Sprintf("/notifications?since=%s&all=false&per_page=50", p.lastPollTS), nil)
	if err != nil {
		return nil, err
	}
	var notifs []ghNotification
	if err := json.Unmarshal([]byte(out), &notifs); err != nil {
		return nil, fmt.Errorf("parse notifications: %w", err)
	}
	return notifs, nil
}

func (p *GitHubPoller) fetchPR(number int) (*ghPR, error) {
	out, err := ghAPI("GET", fmt.Sprintf("/repos/%s/%s/pulls/%d", p.owner, p.repo, number), nil)
	if err != nil {
		return nil, err
	}
	var pr ghPR
	if err := json.Unmarshal([]byte(out), &pr); err != nil {
		return nil, fmt.Errorf("parse PR: %w", err)
	}
	return &pr, nil
}

func (p *GitHubPoller) fetchNewComments(number int, since string) ([]PRComment, error) {
	var comments []PRComment

	// Inline review comments
	out, err := ghAPI("GET", fmt.Sprintf("/repos/%s/%s/pulls/%d/comments?since=%s&per_page=100",
		p.owner, p.repo, number, since), nil)
	if err != nil {
		return nil, fmt.Errorf("review comments: %w", err)
	}
	var reviewComments []ghReviewComment
	if err := json.Unmarshal([]byte(out), &reviewComments); err != nil {
		return nil, fmt.Errorf("parse review comments: %w", err)
	}
	for _, c := range reviewComments {
		if p.shouldSkipUser(c.User.Login, c.User.Type) {
			continue
		}
		comments = append(comments, PRComment{
			User:      c.User.Login,
			Body:      c.Body,
			Path:      c.Path,
			Line:      c.Line,
			CreatedAt: parseRFC3339(c.CreatedAt),
		})
	}

	// General issue comments
	out, err = ghAPI("GET", fmt.Sprintf("/repos/%s/%s/issues/%d/comments?since=%s&per_page=100",
		p.owner, p.repo, number, since), nil)
	if err != nil {
		return nil, fmt.Errorf("issue comments: %w", err)
	}
	var issueComments []ghIssueComment
	if err := json.Unmarshal([]byte(out), &issueComments); err != nil {
		return nil, fmt.Errorf("parse issue comments: %w", err)
	}
	for _, c := range issueComments {
		if p.shouldSkipUser(c.User.Login, c.User.Type) {
			continue
		}
		comments = append(comments, PRComment{
			User:      c.User.Login,
			Body:      c.Body,
			CreatedAt: parseRFC3339(c.CreatedAt),
		})
	}

	return comments, nil
}

func (p *GitHubPoller) fetchLatestVerdict(number int) ReviewVerdict {
	out, err := ghAPI("GET", fmt.Sprintf("/repos/%s/%s/pulls/%d/reviews?per_page=20",
		p.owner, p.repo, number), nil)
	if err != nil {
		return VerdictNone
	}
	var reviews []ghReview
	if err := json.Unmarshal([]byte(out), &reviews); err != nil {
		return VerdictNone
	}
	for i := len(reviews) - 1; i >= 0; i-- {
		switch ReviewVerdict(reviews[i].State) {
		case VerdictApproved, VerdictChangesRequested:
			return ReviewVerdict(reviews[i].State)
		}
	}
	return VerdictNone
}

// --- PR Discovery ---

// DiscoverPRs returns a map of branch name → PR number for the user's open PRs.
// Primary: gh pr list. Fallback: git stack ls.
func DiscoverPRs(owner, repo, repoDir string) map[string]int {
	m, err := discoverPRsViaGH(owner, repo)
	if err != nil {
		log.Printf("github: gh pr list failed (%v), trying git stack ls", err)
		return parseStackLS(discoverPRsViaStackLS(repoDir))
	}
	return m
}

func discoverPRsViaGH(owner, repo string) (map[string]int, error) {
	out, err := exec.Command("gh", "pr", "list",
		"--author", "@me",
		"--repo", owner+"/"+repo,
		"--state", "open",
		"--json", "number,headRefName",
	).Output()
	if err != nil {
		return nil, err
	}
	return parsePRListJSON(string(out))
}

func discoverPRsViaStackLS(repoDir string) string {
	cmd := exec.Command("git", "stack", "ls")
	cmd.Dir = repoDir
	out, err := cmd.Output()
	if err != nil {
		log.Printf("github: git stack ls failed: %v", err)
		return ""
	}
	return string(out)
}

// FindWorktreeForBranch finds the worktree path for a given branch name.
// Returns empty string if not found. Picks the first match if multiple exist.
func FindWorktreeForBranch(repoDir, branch string) string {
	cmd := exec.Command("git", "worktree", "list", "--porcelain")
	cmd.Dir = repoDir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	lines := strings.Split(string(out), "\n")
	var currentPath string
	for _, line := range lines {
		if strings.HasPrefix(line, "worktree ") {
			currentPath = strings.TrimPrefix(line, "worktree ")
		}
		if strings.HasPrefix(line, "branch refs/heads/") {
			b := strings.TrimPrefix(line, "branch refs/heads/")
			if b == branch {
				return currentPath
			}
		}
	}
	return ""
}

// --- Parsing helpers ---

var prURLRegex = regexp.MustCompile(`/pulls/(\d+)$`)

func parsePRNumberFromURL(url string) int {
	m := prURLRegex.FindStringSubmatch(url)
	if len(m) < 2 {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

func parsePRListJSON(data string) (map[string]int, error) {
	var items []struct {
		Number      int    `json:"number"`
		HeadRefName string `json:"headRefName"`
	}
	if err := json.Unmarshal([]byte(data), &items); err != nil {
		return nil, err
	}
	m := make(map[string]int)
	for _, item := range items {
		m[item.HeadRefName] = item.Number
	}
	return m, nil
}

var stackLSBranchRegex = regexp.MustCompile(`[·●✓⦿]\s+(\S+)`)
var stackLSPRURLRegex = regexp.MustCompile(`\[https://github\.com/[^/]+/[^/]+/pull/(\d+)\]`)

func parseStackLS(output string) map[string]int {
	m := make(map[string]int)
	lines := strings.Split(output, "\n")

	var lastBranch string
	for _, line := range lines {
		if bm := stackLSBranchRegex.FindStringSubmatch(line); len(bm) >= 2 {
			lastBranch = bm[1]
		}
		if pm := stackLSPRURLRegex.FindStringSubmatch(line); len(pm) >= 2 && lastBranch != "" {
			n, _ := strconv.Atoi(pm[1])
			if n > 0 {
				m[lastBranch] = n
			}
			lastBranch = ""
		}
	}
	return m
}

// ghAPI calls the GitHub API via the gh CLI.
func ghAPI(method, path string, body interface{}) (string, error) {
	args := []string{"api", "--method", method, path}
	out, err := exec.Command("gh", args...).Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("gh api %s %s: %s", method, path, string(exitErr.Stderr))
		}
		return "", err
	}
	return string(out), nil
}

func parseRFC3339(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return t
}
