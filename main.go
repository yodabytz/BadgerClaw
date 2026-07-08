package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const (
	appName       = "RootBadger CLI"
	userAgentName = "RootBadger CLI"
	version       = "0.1.0"
	defaultURL    = "https://rootbadger.com"
	tuiBg         = "\033[48;5;233m"
	tuiStatusBg   = "\033[48;5;235m"
	tuiText       = "\033[38;5;230m"
	tuiOrange     = "\033[38;5;208m"
	tuiLink       = "\033[38;5;214m"
	tuiMuted      = "\033[38;5;245m"
)

type Config struct {
	BaseURL string `json:"base_url"`
	Token   string `json:"token,omitempty"`
	User    string `json:"user,omitempty"`
}

type APIClient struct {
	cfg    Config
	client *http.Client
}

type APIError struct {
	Status int
	Body   string
}

func (e APIError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("HTTP %d", e.Status)
	}
	return fmt.Sprintf("HTTP %d: %s", e.Status, e.Body)
}

func main() {
	cfg, _ := loadConfig()
	if cfg.BaseURL == "" {
		cfg.BaseURL = envOrDefault("ROOTBADGER_URL", defaultURL)
	}
	api := APIClient{
		cfg:    cfg,
		client: &http.Client{Timeout: 30 * time.Second},
	}

	if len(os.Args) < 2 {
		if err := runTUI(api); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		return
	}

	cmd := os.Args[1]
	if cmd == "--version" || cmd == "version" {
		fmt.Printf("%s %s\n", appName, version)
		return
	}

	var err error
	switch cmd {
	case "signup", "register":
		err = cmdSignup(api, os.Args[2:])
	case "login":
		err = cmdLogin(api, os.Args[2:])
	case "logout":
		err = cmdLogout(api)
	case "me":
		err = cmdMe(api)
	case "groups":
		err = cmdGroups(api, os.Args[2:])
	case "subscriptions", "subs":
		err = cmdSubscriptions(api, os.Args[2:])
	case "group":
		err = cmdGroup(api, os.Args[2:])
	case "thread", "post":
		err = cmdThread(api, os.Args[2:])
	case "search":
		err = cmdSearch(api, os.Args[2:])
	case "new":
		err = cmdNewPost(api, os.Args[2:])
	case "reply":
		err = cmdReply(api, os.Args[2:])
	case "useful":
		err = cmdUseful(api, os.Args[2:], true)
	case "unuseful":
		err = cmdUseful(api, os.Args[2:], false)
	case "vote":
		err = cmdVote(api, os.Args[2:])
	case "messages":
		err = cmdMessages(api, os.Args[2:])
	case "send":
		err = cmdSendMessage(api, os.Args[2:])
	case "notifications", "notices":
		err = cmdNotifications(api, os.Args[2:])
	case "profile":
		err = cmdProfile(api, os.Args[2:])
	case "profile-update":
		err = cmdProfileUpdate(api, os.Args[2:])
	case "admin":
		err = cmdAdmin(api)
	case "admin-section":
		err = cmdAdminSection(api, os.Args[2:])
	case "admin-detail":
		err = cmdAdminDetail(api, os.Args[2:])
	case "admin-action":
		err = cmdAdminAction(api, os.Args[2:])
	case "subscribe":
		err = cmdSubscribe(api, os.Args[2:], true)
	case "unsubscribe":
		err = cmdSubscribe(api, os.Args[2:], false)
	case "propose":
		err = cmdPropose(api, os.Args[2:])
	case "headers":
		err = cmdHeaders(api, os.Args[2:])
	case "tui":
		err = runTUI(api)
	case "doctor":
		err = cmdDoctor(api)
	case "help", "-h", "--help":
		printUsage()
	default:
		err = fmt.Errorf("unknown command: %s", cmd)
	}

	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Printf(`%s %s

Usage:
  badgerclaw signup --username USER --email EMAIL
  badgerclaw login [--login USER_OR_EMAIL] [--no-tui]
  badgerclaw tui
  badgerclaw me
  badgerclaw groups [--q text]
  badgerclaw subscriptions
  badgerclaw group rb.comp.lang.python
  badgerclaw thread 123
  badgerclaw headers 123
  badgerclaw search --type all|posts|groups|users|hashtags|article|message_id QUERY
  badgerclaw new rb.group.path --subject "Subject" [--body-file file] [--crosspost rb.a,rb.b]
  badgerclaw reply POST_ID [--body-file file]
  badgerclaw useful POST_ID
  badgerclaw vote POST_ID --value 1|-1
  badgerclaw messages
  badgerclaw send USERNAME [--body-file file]
  badgerclaw notifications
  badgerclaw profile [USERNAME]
  badgerclaw profile-update [--display-name NAME] [--bio-file file] [--signature-file file]
  badgerclaw admin
  badgerclaw admin-section users|statistics|proposals|reports|images|private_groups|bans|logs|newsletters|webhooks
  badgerclaw admin-detail SECTION ID
  badgerclaw admin-action SECTION ID --action ACTION [--reason TEXT] [--role ROLE] [--points N] [--event-type TYPE]
  badgerclaw subscribe rb.group.path
  badgerclaw unsubscribe rb.group.path
  badgerclaw propose --parent rb.comp --slug example --name "Example" --charter-file charter.txt --rationale-file rationale.txt --moderation-file moderation.txt
  badgerclaw doctor

Config:
  ROOTBADGER_URL may override the default %s.
  Config is stored at %s.

`, appName, version, defaultURL, configPath())
}

func cmdSignup(api APIClient, args []string) error {
	fs := flag.NewFlagSet("signup", flag.ExitOnError)
	username := fs.String("username", "", "username")
	email := fs.String("email", "", "email")
	invite := fs.String("invite", "", "invite token")
	_ = fs.Parse(args)

	if *username == "" {
		*username = promptLine("Username: ")
	}
	if *email == "" {
		*email = promptLine("Email: ")
	}
	password, err := promptPasswordTwice()
	if err != nil {
		return err
	}

	var out map[string]any
	err = api.postPublic("/api/v1/auth/register", map[string]any{
		"username":              *username,
		"email":                 *email,
		"password":              password,
		"password_confirmation": password,
		"invite_token":          *invite,
	}, &out)
	if err != nil {
		return err
	}
	fmt.Println(stringify(out["message"]))
	return nil
}

func cmdLogin(api APIClient, args []string) error {
	fs := flag.NewFlagSet("login", flag.ExitOnError)
	login := fs.String("login", "", "username or email")
	noTUI := fs.Bool("no-tui", false, "do not open the TUI after login")
	_ = fs.Parse(args)

	if *login == "" {
		*login = promptLine("Username/email: ")
	}
	password, err := readSecret("Password: ")
	if err != nil {
		return err
	}

	var out map[string]any
	err = api.postPublic("/api/v1/auth/login", map[string]any{
		"login":       *login,
		"password":    password,
		"device_name": userAgentName,
	}, &out)
	if err != nil {
		return err
	}
	data := asMap(out["data"])
	token := stringify(data["access_token"])
	user := asMap(data["user"])
	if token == "" {
		return errors.New("login response did not include access token")
	}
	api.cfg.Token = token
	api.cfg.User = firstNonEmpty(stringify(user["display_name"]), stringify(user["username"]), *login)
	if err := saveConfig(api.cfg); err != nil {
		return err
	}
	fmt.Printf("Logged in as %s.\n", api.cfg.User)
	if !*noTUI && isInteractiveTerminal() {
		return runTUI(api)
	}
	return nil
}

func cmdLogout(api APIClient) error {
	var out map[string]any
	_ = api.post("/api/v1/auth/logout", nil, &out)
	api.cfg.Token = ""
	api.cfg.User = ""
	if err := saveConfig(api.cfg); err != nil {
		return err
	}
	fmt.Println("Logged out.")
	return nil
}

func cmdMe(api APIClient) error {
	var out map[string]any
	if err := api.get("/api/v1/auth/me", nil, &out); err != nil {
		return err
	}
	printJSON(out["data"])
	return nil
}

func cmdGroups(api APIClient, args []string) error {
	fs := flag.NewFlagSet("groups", flag.ExitOnError)
	q := fs.String("q", "", "search text")
	page := fs.Int("page", 1, "page")
	_ = fs.Parse(args)
	path := "/api/v1/groups"
	params := url.Values{"page": {strconv.Itoa(*page)}, "per_page": {"50"}}
	if *q != "" {
		path = "/api/v1/search"
		params.Set("q", *q)
		params.Set("type", "groups")
	}
	var out map[string]any
	if err := api.getPublic(path, params, &out); err != nil {
		return err
	}
	if *q != "" {
		groups := asMap(asMap(out["data"])["groups"])
		printGroupList(asSlice(groups["data"]))
		printMeta(groups["meta"])
		return nil
	}
	printGroupList(asSlice(out["data"]))
	printMeta(out["meta"])
	return nil
}

func cmdSubscriptions(api APIClient, args []string) error {
	fs := flag.NewFlagSet("subscriptions", flag.ExitOnError)
	page := fs.Int("page", 1, "page")
	_ = fs.Parse(args)
	var out map[string]any
	if err := api.get("/api/v1/app/subscriptions", url.Values{"page": {strconv.Itoa(*page)}, "per_page": {"50"}}, &out); err != nil {
		return err
	}
	printGroupList(asSlice(out["data"]))
	printMeta(out["meta"])
	return nil
}

func cmdGroup(api APIClient, args []string) error {
	if len(args) < 1 {
		return errors.New("group path required")
	}
	groupPath := args[0]
	var detail map[string]any
	if err := api.get("/api/v1/app/groups/"+url.PathEscape(groupPath), nil, &detail); err != nil {
		return err
	}
	printGroupDetail(asMap(detail["data"]))
	var threads map[string]any
	if err := api.get("/api/v1/app/groups/"+url.PathEscape(groupPath)+"/threads", url.Values{"per_page": {"25"}}, &threads); err != nil {
		return err
	}
	fmt.Println("\nLatest articles and replies:")
	printThreadList(asSlice(threads["data"]))
	printMeta(threads["meta"])
	return nil
}

func cmdThread(api APIClient, args []string) error {
	if len(args) < 1 {
		return errors.New("post id required")
	}
	var out map[string]any
	if err := api.get("/api/v1/app/threads/"+args[0], nil, &out); err != nil {
		return err
	}
	printPostTree(asMap(out["data"]), 0, false)
	return nil
}

func cmdHeaders(api APIClient, args []string) error {
	if len(args) < 1 {
		return errors.New("post id required")
	}
	var out map[string]any
	if err := api.get("/api/v1/app/threads/"+args[0], nil, &out); err != nil {
		return err
	}
	printPostTree(asMap(out["data"]), 0, true)
	return nil
}

func cmdSearch(api APIClient, args []string) error {
	fs := flag.NewFlagSet("search", flag.ExitOnError)
	typ := fs.String("type", "all", "all, posts, groups, users, hashtags, article, message_id")
	page := fs.Int("page", 1, "page")
	_ = fs.Parse(args)
	query := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if query == "" {
		return errors.New("search query required")
	}
	var out map[string]any
	err := api.get("/api/v1/app/search", url.Values{
		"q":        {query},
		"type":     {*typ},
		"page":     {strconv.Itoa(*page)},
		"per_page": {"25"},
	}, &out)
	if err != nil {
		return err
	}
	data := asMap(out["data"])
	if *typ == "all" || *typ == "groups" {
		fmt.Println("Groups:")
		printGroupList(asSlice(asMap(data["groups"])["data"]))
	}
	if *typ == "all" || *typ == "posts" || *typ == "article" || *typ == "message_id" {
		fmt.Println("Posts:")
		printThreadList(asSlice(asMap(data["threads"])["data"]))
	}
	if *typ == "all" || *typ == "users" {
		fmt.Println("Users:")
		for _, item := range asSlice(asMap(data["users"])["data"]) {
			u := asMap(item)
			fmt.Printf("  @%s  %s  posts:%v standing:%v %s\n", stringify(u["username"]), stringify(u["display_name"]), u["post_count"], u["standing_points"], stringify(u["standing_level"]))
		}
	}
	if *typ == "all" || *typ == "hashtags" {
		fmt.Println("Hashtags:")
		for _, item := range asSlice(asMap(data["tags"])["data"]) {
			t := asMap(item)
			fmt.Printf("  %-24s posts:%v\n", stringify(t["label"]), t["post_count"])
		}
	}
	return nil
}

func cmdNewPost(api APIClient, args []string) error {
	fs := flag.NewFlagSet("new", flag.ExitOnError)
	subject := fs.String("subject", "", "subject")
	bodyFile := fs.String("body-file", "", "body file")
	crosspost := fs.String("crosspost", "", "comma separated crosspost groups")
	_ = fs.Parse(args)
	if fs.NArg() < 1 {
		return errors.New("group path required")
	}
	if *subject == "" {
		*subject = promptLine("Subject: ")
	}
	body, err := bodyFromFlagOrEditor(*bodyFile, "Write your post. Save and close to submit.\n")
	if err != nil {
		return err
	}
	var out map[string]any
	err = api.post("/api/v1/app/groups/"+url.PathEscape(fs.Arg(0))+"/posts", map[string]any{
		"subject":          *subject,
		"body":             body,
		"crosspost_groups": *crosspost,
	}, &out)
	if err != nil {
		return err
	}
	fmt.Println(stringify(out["message"]))
	printJSON(out["data"])
	return nil
}

func cmdReply(api APIClient, args []string) error {
	fs := flag.NewFlagSet("reply", flag.ExitOnError)
	bodyFile := fs.String("body-file", "", "body file")
	_ = fs.Parse(args)
	if fs.NArg() < 1 {
		return errors.New("post id required")
	}
	initial := ""
	var err error
	if *bodyFile == "" && confirmDefaultYes("Quote original? Y/n: ") {
		initial, err = fetchReplyQuote(api, fs.Arg(0))
		if err != nil {
			return err
		}
	}
	body, err := bodyFromFlagOrEditorWithInitial(*bodyFile, "Write your reply. Save and close to review.\n", initial)
	if err != nil {
		return err
	}
	if !confirmDefaultYes("Send? Y/n: ") {
		fmt.Println("Reply cancelled.")
		return nil
	}
	var out map[string]any
	err = api.post("/api/v1/app/posts/"+fs.Arg(0)+"/replies", map[string]any{"body": body}, &out)
	if err != nil {
		return err
	}
	fmt.Println(stringify(out["message"]))
	printJSON(out["data"])
	return nil
}

func cmdUseful(api APIClient, args []string, add bool) error {
	if len(args) < 1 {
		return errors.New("post id required")
	}
	var out map[string]any
	path := "/api/v1/app/posts/" + args[0] + "/useful"
	var err error
	if add {
		err = api.post(path, nil, &out)
	} else {
		err = api.delete(path, &out)
	}
	if err != nil {
		return err
	}
	printJSON(out)
	return nil
}

func cmdVote(api APIClient, args []string) error {
	fs := flag.NewFlagSet("vote", flag.ExitOnError)
	value := fs.Int("value", 0, "1 or -1")
	_ = fs.Parse(args)
	if fs.NArg() < 1 {
		return errors.New("post id required")
	}
	if *value != 1 && *value != -1 {
		return errors.New("--value must be 1 or -1")
	}
	var out map[string]any
	if err := api.post("/api/v1/app/posts/"+fs.Arg(0)+"/vote", map[string]any{"value": *value}, &out); err != nil {
		return err
	}
	printJSON(out)
	return nil
}

func cmdMessages(api APIClient, args []string) error {
	var out map[string]any
	if err := api.get("/api/v1/app/conversations", url.Values{"per_page": {"25"}}, &out); err != nil {
		return err
	}
	for _, item := range asSlice(out["data"]) {
		c := asMap(item)
		u := asMap(c["other_user"])
		fmt.Printf("[%v] %-24s unread:%v last:%s\n", c["id"], stringify(u["display_name"]), c["unread_count"], stringify(c["last_message_at"]))
	}
	return nil
}

func cmdSendMessage(api APIClient, args []string) error {
	fs := flag.NewFlagSet("send", flag.ExitOnError)
	bodyFile := fs.String("body-file", "", "body file")
	_ = fs.Parse(args)
	if fs.NArg() < 1 {
		return errors.New("username required")
	}
	body, err := bodyFromFlagOrEditor(*bodyFile, "Write your message. Save and close to send.\n")
	if err != nil {
		return err
	}
	var out map[string]any
	if err := api.post("/api/v1/app/messages/"+url.PathEscape(fs.Arg(0)), map[string]any{"body": body}, &out); err != nil {
		return err
	}
	fmt.Println(stringify(out["message"]))
	return nil
}

func cmdNotifications(api APIClient, args []string) error {
	fs := flag.NewFlagSet("notifications", flag.ExitOnError)
	markRead := fs.Bool("mark-read", false, "mark returned notifications read")
	_ = fs.Parse(args)
	var out map[string]any
	params := url.Values{"per_page": {"50"}}
	if *markRead {
		params.Set("mark_read", "1")
	}
	if err := api.get("/api/v1/app/notifications", params, &out); err != nil {
		return err
	}
	data := asMap(out["data"])
	fmt.Printf("Unread notices: %v  Unread messages: %v\n", data["unread_count"], data["unread_message_count"])
	for _, item := range asSlice(data["items"]) {
		n := asMap(item)
		fmt.Printf("[%s] %-18s %s\n  %s\n", stringify(n["id"]), stringify(n["type"]), stringify(n["title"]), stringify(n["body"]))
	}
	return nil
}

func cmdProfile(api APIClient, args []string) error {
	var out map[string]any
	if len(args) > 0 {
		if err := api.get("/api/v1/app/users/"+url.PathEscape(args[0]), nil, &out); err != nil {
			return err
		}
	} else {
		if err := api.get("/api/v1/app/profile", nil, &out); err != nil {
			return err
		}
	}
	printJSON(out["data"])
	return nil
}

func cmdProfileUpdate(api APIClient, args []string) error {
	fs := flag.NewFlagSet("profile-update", flag.ExitOnError)
	displayName := fs.String("display-name", "", "display name")
	bioFile := fs.String("bio-file", "", "bio file")
	signatureFile := fs.String("signature-file", "", "signature file")
	organization := fs.String("organization", "", "Organization header")
	xinfo := fs.String("x-info", "", "X-Info header")
	tagline := fs.String("tagline", "", "tagline")
	showHeaders := fs.Bool("show-headers", false, "show posting headers")
	newsletter := fs.Bool("newsletter", true, "receive newsletter emails")
	_ = fs.Parse(args)

	payload := map[string]any{
		"show_headers":          *showHeaders,
		"newsletter_emails":     *newsletter,
		"notify_direct_replies": true,
	}
	if *displayName != "" {
		payload["display_name"] = *displayName
	}
	if *organization != "" {
		payload["hdr_organization"] = *organization
	}
	if *xinfo != "" {
		payload["hdr_x_info"] = *xinfo
	}
	if *tagline != "" {
		payload["hdr_tagline"] = *tagline
	}
	if *bioFile != "" {
		b, err := os.ReadFile(*bioFile)
		if err != nil {
			return err
		}
		payload["bio"] = string(b)
	}
	if *signatureFile != "" {
		b, err := os.ReadFile(*signatureFile)
		if err != nil {
			return err
		}
		payload["hdr_x_signature"] = string(b)
	}

	var out map[string]any
	if err := api.post("/api/v1/app/profile", payload, &out); err != nil {
		return err
	}
	fmt.Println(stringify(out["message"]))
	printJSON(asMap(out["data"])["user"])
	return nil
}

func cmdAdmin(api APIClient) error {
	var out map[string]any
	if err := api.get("/api/v1/app/admin", nil, &out); err != nil {
		return err
	}
	printJSON(out["data"])
	return nil
}

func cmdAdminSection(api APIClient, args []string) error {
	if len(args) < 1 {
		return errors.New("admin section required")
	}
	var out map[string]any
	if err := api.get("/api/v1/app/admin/"+url.PathEscape(args[0]), nil, &out); err != nil {
		return err
	}
	data := asMap(out["data"])
	fmt.Printf("%s\n", stringify(data["section"]))
	for _, item := range asSlice(data["items"]) {
		row := asMap(item)
		fmt.Printf("[%v] %-32s %-16s %s\n", row["id"], stringify(row["title"]), stringify(row["status"]), stringify(row["subtitle"]))
		if detail := stringify(row["detail"]); detail != "" {
			fmt.Println("    " + detail)
		}
	}
	return nil
}

func cmdAdminDetail(api APIClient, args []string) error {
	if len(args) < 2 {
		return errors.New("section and id required")
	}
	var out map[string]any
	if err := api.get("/api/v1/app/admin/"+url.PathEscape(args[0])+"/"+url.PathEscape(args[1]), nil, &out); err != nil {
		return err
	}
	d := asMap(out["data"])
	fmt.Printf("[%v] %s\n%s\n\n", d["id"], stringify(d["title"]), stringify(d["subtitle"]))
	for _, field := range asSlice(d["fields"]) {
		f := asMap(field)
		fmt.Printf("%-22s %s\n", stringify(f["label"])+":", stringify(f["value"]))
	}
	if body := stringify(d["body"]); body != "" {
		fmt.Println("\n" + wrap(body, 88))
	}
	if actions := asSlice(d["actions"]); len(actions) > 0 {
		fmt.Println("\nActions:")
		for _, action := range actions {
			a := asMap(action)
			fmt.Printf("  %-24s %s\n", stringify(a["key"]), stringify(a["label"]))
		}
	}
	return nil
}

func cmdAdminAction(api APIClient, args []string) error {
	fs := flag.NewFlagSet("admin-action", flag.ExitOnError)
	action := fs.String("action", "", "action key")
	reason := fs.String("reason", "", "reason/note")
	role := fs.String("role", "", "role for set_role")
	points := fs.Int("points", 0, "standing points change")
	eventType := fs.String("event-type", "", "standing event type")
	awardGoodCharter := fs.Bool("award-good-charter", false, "award good charter bonus")
	discussionDays := fs.Int("discussion-days", 0, "discussion days")
	_ = fs.Parse(args)
	if fs.NArg() < 2 {
		return errors.New("section and id required")
	}
	if *action == "" {
		return errors.New("--action is required")
	}
	payload := map[string]any{"action": *action}
	if *reason != "" {
		payload["reason"] = *reason
	}
	if *role != "" {
		payload["role"] = *role
	}
	if *points != 0 {
		payload["points_change"] = *points
	}
	if *eventType != "" {
		payload["event_type"] = *eventType
	}
	if *awardGoodCharter {
		payload["award_good_charter"] = true
	}
	if *discussionDays != 0 {
		payload["discussion_days"] = *discussionDays
	}
	var out map[string]any
	err := api.post("/api/v1/app/admin/"+url.PathEscape(fs.Arg(0))+"/"+url.PathEscape(fs.Arg(1))+"/action", payload, &out)
	if err != nil {
		return err
	}
	fmt.Println(stringify(out["message"]))
	return nil
}

func cmdSubscribe(api APIClient, args []string, add bool) error {
	if len(args) < 1 {
		return errors.New("group path required")
	}
	path := "/api/v1/app/groups/" + url.PathEscape(args[0]) + "/subscribe"
	var out map[string]any
	var err error
	if add {
		err = api.post(path, nil, &out)
	} else {
		err = api.delete(path, &out)
	}
	if err != nil {
		return err
	}
	fmt.Println(stringify(out["message"]))
	return nil
}

func cmdPropose(api APIClient, args []string) error {
	fs := flag.NewFlagSet("propose", flag.ExitOnError)
	parent := fs.String("parent", "", "parent group path")
	slug := fs.String("slug", "", "new slug")
	name := fs.String("name", "", "display name")
	slogan := fs.String("slogan", "", "slogan")
	charterFile := fs.String("charter-file", "", "charter file")
	rationaleFile := fs.String("rationale-file", "", "rationale file")
	moderationFile := fs.String("moderation-file", "", "moderation file")
	moderated := fs.Bool("moderated", false, "moderated group")
	private := fs.Bool("private", false, "secret/private group")
	_ = fs.Parse(args)
	if *parent == "" || *slug == "" || *name == "" {
		return errors.New("--parent, --slug, and --name are required")
	}
	charter, err := readRequiredText(*charterFile, "Charter")
	if err != nil {
		return err
	}
	rationale, err := readRequiredText(*rationaleFile, "Rationale")
	if err != nil {
		return err
	}
	moderation, err := readRequiredText(*moderationFile, "Moderation plan")
	if err != nil {
		return err
	}
	var out map[string]any
	err = api.post("/api/v1/app/proposals", map[string]any{
		"parent_path":     *parent,
		"proposed_slug":   *slug,
		"proposed_name":   *name,
		"slogan":          *slogan,
		"charter":         charter,
		"rationale":       rationale,
		"moderation_plan": moderation,
		"is_moderated":    *moderated,
		"is_private":      *private,
	}, &out)
	if err != nil {
		return err
	}
	fmt.Println(stringify(out["message"]))
	printJSON(out["data"])
	return nil
}

func cmdDoctor(api APIClient) error {
	fmt.Println(appName, version)
	fmt.Println("Config:", configPath())
	fmt.Println("Base URL:", api.cfg.BaseURL)
	fmt.Println("Token:", yesNo(api.cfg.Token != ""))
	var health map[string]any
	if err := api.getPublic("/api/v1/health", nil, &health); err != nil {
		return err
	}
	fmt.Println("API health: OK")
	return nil
}

func (api APIClient) get(path string, params url.Values, out any) error {
	return api.do("GET", path, params, nil, true, out)
}

func (api APIClient) getPublic(path string, params url.Values, out any) error {
	return api.do("GET", path, params, nil, false, out)
}

func (api APIClient) post(path string, body any, out any) error {
	return api.do("POST", path, nil, body, true, out)
}

func (api APIClient) postPublic(path string, body any, out any) error {
	return api.do("POST", path, nil, body, false, out)
}

func (api APIClient) delete(path string, out any) error {
	return api.do("DELETE", path, nil, nil, true, out)
}

func (api APIClient) do(method, path string, params url.Values, body any, auth bool, out any) error {
	base := strings.TrimRight(api.cfg.BaseURL, "/")
	u, err := url.Parse(base + path)
	if err != nil {
		return err
	}
	if params != nil {
		u.RawQuery = params.Encode()
	}
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, u.String(), reader)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgentName+"/"+version+" badgerclaw")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if auth {
		if api.cfg.Token == "" {
			return errors.New("not logged in; run badgerclaw login first")
		}
		req.Header.Set("Authorization", "Bearer "+api.cfg.Token)
	}
	resp, err := api.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return APIError{Status: resp.StatusCode, Body: compactAPIError(b)}
	}
	if out != nil && len(bytes.TrimSpace(b)) > 0 {
		if err := json.Unmarshal(b, out); err != nil {
			return fmt.Errorf("invalid JSON from API: %w", err)
		}
	}
	return nil
}

func compactAPIError(b []byte) string {
	var m map[string]any
	if json.Unmarshal(b, &m) == nil {
		if msg := stringify(m["message"]); msg != "" {
			if errs := asMap(m["errors"]); len(errs) > 0 {
				parts := []string{msg}
				for k, v := range errs {
					parts = append(parts, k+": "+strings.Join(stringSlice(asSlice(v)), "; "))
				}
				return strings.Join(parts, " | ")
			}
			return msg
		}
	}
	s := strings.TrimSpace(string(b))
	if len(s) > 500 {
		s = s[:500] + "..."
	}
	return s
}

func printGroupList(items []any) {
	width := terminalWidth()
	printTableHeader("Groups", width)
	pathW := clamp(width/3, 22, 42)
	nameW := clamp(width-pathW-34, 18, 44)
	fmt.Printf("  %s  %s  %s\n",
		muted(fit("GROUP", pathW)),
		muted(fit("NAME", nameW)),
		muted("COUNTS"),
	)
	fmt.Println(muted(strings.Repeat("─", width)))
	for _, item := range items {
		g := asMap(item)
		unread := " "
		if n, ok := g["unread_count"]; ok && fmt.Sprint(n) != "0" {
			unread = fmt.Sprintf(" unread:%v", n)
		}
		fmt.Printf("  %s  %s  %s\n",
			cyan(fit(cleanInline(stringify(g["path"])), pathW)),
			fit(cleanInline(stringify(g["name"])), nameW),
			muted(fmt.Sprintf("posts:%v subs:%v%s", g["post_count"], g["subscriber_count"], unread)),
		)
		if desc := cleanInline(stringify(g["description"])); desc != "" {
			for _, line := range wrapLines(desc, width-6) {
				fmt.Println("      " + muted(line))
			}
		}
	}
}

func printGroupDetail(g map[string]any) {
	width := terminalWidth()
	printTableHeader(firstNonEmpty(cleanInline(stringify(g["name"])), cleanInline(stringify(g["path"]))), width)
	fmt.Println(cyan(cleanInline(stringify(g["path"]))))
	if slogan := stringify(g["slogan"]); slogan != "" {
		fmt.Println(amber(cleanInline(slogan)))
	}
	fmt.Println(muted(fmt.Sprintf("posts:%v  subscribers:%v  unread:%v  can_post:%v", g["post_count"], g["subscriber_count"], g["unread_count"], g["can_post"])))
	if creator := stringify(g["creator_name"]); creator != "" {
		fmt.Println(muted(fmt.Sprintf("creator:%s  established:%s  discussions:%v  replies:%v", creator, stringify(g["established_label"]), g["discussion_count"], g["reply_count"])))
	}
	if charter := stringify(g["charter"]); charter != "" {
		fmt.Println("\n" + amber("Charter"))
		fmt.Println(wrap(cleanBlock(charter), min(width-4, 96)))
	}
}

func printThreadList(items []any) {
	width := terminalWidth()
	printTableHeader("Articles and replies", width)
	for _, item := range items {
		p := asMap(item)
		author := asMap(p["author"])
		group := asMap(p["group"])
		marker := " "
		if truthy(p["is_unread"]) {
			marker = cyan("*")
		}
		id := fmt.Sprintf("[%v]", p["id"])
		subjectW := max(24, width-18)
		fmt.Printf("%s %s %s\n", marker, muted(fit(id, 8)), fit(cleanInline(stringify(p["subject"])), subjectW))
		byline := fmt.Sprintf("%s <%s> in %s  replies:%v score:%v useful:%v",
			firstNonEmpty(cleanInline(stringify(author["display_name"])), cleanInline(stringify(author["username"]))),
			cleanInline(stringify(author["public_email"])),
			cleanInline(stringify(group["path"])),
			p["reply_count"], p["vote_score"], p["useful_count"])
		for _, line := range wrapLines(byline, width-6) {
			fmt.Println("      " + muted(line))
		}
	}
}

func printPostTree(post map[string]any, depth int, headers bool) {
	indent := strings.Repeat("  ", depth)
	fmt.Printf("%s%s %s\n", indent, muted(fmt.Sprintf("[%v]", post["id"])), fit(cleanInline(stringify(post["subject"])), max(30, terminalWidth()-len(indent)-12)))
	for _, line := range postHeaderLines(post, headers) {
		fmt.Println(indent + muted(line))
	}
	fmt.Println()
	for _, line := range displayBlockLines(postDisplayText(post), max(40, min(96, terminalWidth()-len(indent)-2))) {
		fmt.Println(indent + line)
	}
	for _, reply := range asSlice(post["replies"]) {
		fmt.Println()
		printPostTree(asMap(reply), depth+1, headers)
	}
}

func printMeta(v any) {
	m := asMap(v)
	if len(m) == 0 {
		return
	}
	fmt.Println(muted(fmt.Sprintf("page %v/%v  total:%v  more:%s", m["current_page"], m["last_page"], m["total"], yesNo(truthy(m["has_more_pages"])))))
}

func printJSON(v any) {
	b, _ := json.MarshalIndent(v, "", "  ")
	fmt.Println(string(b))
}

func configPath() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "badgerclaw", "config.json")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "badgerclaw", "config.json")
}

func loadConfig() (Config, error) {
	path := configPath()
	b, err := os.ReadFile(path)
	if err != nil {
		if legacy, legacyErr := loadLegacyConfig(); legacyErr == nil {
			_ = saveConfig(legacy)
			return legacy, nil
		}
		return Config{BaseURL: envOrDefault("ROOTBADGER_URL", defaultURL)}, err
	}
	var cfg Config
	if err := json.Unmarshal(b, &cfg); err != nil {
		return cfg, err
	}
	if env := os.Getenv("ROOTBADGER_URL"); env != "" {
		cfg.BaseURL = env
	}
	return cfg, nil
}

func loadLegacyConfig() (Config, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Config{}, err
	}
	path := filepath.Join(home, ".config", "root"+"oager", "config.json")
	b, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var cfg Config
	if err := json.Unmarshal(b, &cfg); err != nil {
		return cfg, err
	}
	if env := os.Getenv("ROOTBADGER_URL"); env != "" {
		cfg.BaseURL = env
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultURL
	}
	return cfg, nil
}

func saveConfig(cfg Config) error {
	path := configPath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0600)
}

func promptLine(label string) string {
	line, _ := readEditableLine(label)
	return strings.TrimSpace(line)
}

func readEditableLine(label string) (string, error) {
	fd := int(os.Stdin.Fd())
	oldState, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		fmt.Print(label)
		line, readErr := bufio.NewReader(os.Stdin).ReadString('\n')
		return strings.TrimRight(line, "\r\n"), readErr
	}

	if err := rawTerminal(fd); err != nil {
		return "", err
	}
	defer restoreTerminalState(fd, oldState)

	reader := bufio.NewReader(os.Stdin)
	buffer := []rune{}
	cursor := 0
	redraw := func() {
		fmt.Print("\r\033[2K")
		fmt.Print(label + string(buffer))
		if move := len(buffer) - cursor; move > 0 {
			fmt.Printf("\033[%dD", move)
		}
	}

	fmt.Print(label)
	for {
		r, _, err := reader.ReadRune()
		if err != nil {
			return string(buffer), err
		}

		switch r {
		case '\r', '\n':
			fmt.Print("\r\n")
			return string(buffer), nil
		case 3:
			fmt.Print("\r\n")
			return "", errors.New("cancelled")
		case 1:
			cursor = 0
			redraw()
		case 5:
			cursor = len(buffer)
			redraw()
		case 4:
			if cursor < len(buffer) {
				buffer = append(buffer[:cursor], buffer[cursor+1:]...)
				redraw()
			}
		case 8, 127:
			if cursor > 0 {
				buffer = append(buffer[:cursor-1], buffer[cursor:]...)
				cursor--
				redraw()
			}
		case 27:
			key := readEditableEscape(reader)
			switch key {
			case "left":
				if cursor > 0 {
					cursor--
					redraw()
				}
			case "right":
				if cursor < len(buffer) {
					cursor++
					redraw()
				}
			case "home":
				cursor = 0
				redraw()
			case "end":
				cursor = len(buffer)
				redraw()
			case "delete":
				if cursor < len(buffer) {
					buffer = append(buffer[:cursor], buffer[cursor+1:]...)
					redraw()
				}
			}
		default:
			if r < 32 {
				continue
			}
			buffer = append(buffer[:cursor], append([]rune{r}, buffer[cursor:]...)...)
			cursor++
			redraw()
		}
	}
}

func readEditableEscape(reader *bufio.Reader) string {
	r, _, err := reader.ReadRune()
	if err != nil {
		return ""
	}
	if r != '[' && r != 'O' {
		return ""
	}
	r, _, err = reader.ReadRune()
	if err != nil {
		return ""
	}
	switch r {
	case 'A':
		return "up"
	case 'B':
		return "down"
	case 'C':
		return "right"
	case 'D':
		return "left"
	case 'H':
		return "home"
	case 'F':
		return "end"
	}
	if r < '0' || r > '9' {
		return ""
	}
	var seq []rune
	seq = append(seq, r)
	for {
		r, _, err = reader.ReadRune()
		if err != nil {
			return ""
		}
		if r == '~' {
			switch string(seq) {
			case "1", "7":
				return "home"
			case "3":
				return "delete"
			case "4", "8":
				return "end"
			default:
				return ""
			}
		}
		if r == 'A' || r == 'B' || r == 'C' || r == 'D' || r == 'H' || r == 'F' {
			switch r {
			case 'A':
				return "up"
			case 'B':
				return "down"
			case 'C':
				return "right"
			case 'D':
				return "left"
			case 'H':
				return "home"
			case 'F':
				return "end"
			}
		}
		if (r >= '0' && r <= '9') || r == ';' {
			seq = append(seq, r)
			continue
		}
		return ""
	}
}

func promptPasswordTwice() (string, error) {
	a, err := readSecret("Password: ")
	if err != nil {
		return "", err
	}
	b, err := readSecret("Confirm password: ")
	if err != nil {
		return "", err
	}
	if a != b {
		return "", errors.New("passwords did not match")
	}
	return a, nil
}

func readSecret(label string) (string, error) {
	fmt.Print(label)
	cmd := exec.Command("stty", "-echo")
	cmd.Stdin = os.Stdin
	_ = cmd.Run()
	defer func() {
		restore := exec.Command("stty", "echo")
		restore.Stdin = os.Stdin
		_ = restore.Run()
		fmt.Println()
	}()
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	return strings.TrimSpace(line), err
}

func bodyFromFlagOrEditor(path, intro string) (string, error) {
	return bodyFromFlagOrEditorWithInitial(path, intro, "")
}

func bodyFromFlagOrEditorWithInitial(path, intro, initial string) (string, error) {
	if path != "" {
		b, err := os.ReadFile(path)
		return string(b), err
	}
	tmp, err := os.CreateTemp("", "badgerclaw-*.md")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString("<!-- " + intro + "Lines inside this comment are ignored. -->\n\n"); err != nil {
		return "", err
	}
	if strings.TrimSpace(initial) != "" {
		if _, err := tmp.WriteString(strings.TrimRight(initial, "\n") + "\n\n"); err != nil {
			return "", err
		}
	}
	_ = tmp.Close()
	editor := preferredEditor()
	c := editorCommand(editor, tmp.Name())
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	if err := c.Run(); err != nil {
		return "", err
	}
	b, err := os.ReadFile(tmp.Name())
	if err != nil {
		return "", err
	}
	body := stripHTMLComments(string(b))
	if strings.TrimSpace(body) == "" {
		return "", errors.New("empty body")
	}
	return body, nil
}

func fetchReplyQuote(api APIClient, postID string) (string, error) {
	var out map[string]any
	if err := api.get("/api/v1/app/posts/"+url.PathEscape(postID)+"/quote", nil, &out); err != nil {
		return "", err
	}
	quote := normalizeBlock(stringify(asMap(out["data"])["quote"]))
	if strings.TrimSpace(quote) == "" {
		return "", errors.New("server returned an empty quote")
	}
	return quote, nil
}

func preferredEditor() string {
	for _, candidate := range []string{
		os.Getenv("BADGERCLAW_EDITOR"),
		os.Getenv("VISUAL"),
		os.Getenv("EDITOR"),
		"nvim",
		"vim",
		"vi",
		"nano",
	} {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		if strings.ContainsAny(candidate, " \t") {
			return candidate
		}
		if _, err := exec.LookPath(candidate); err == nil {
			return candidate
		}
	}
	return "nano"
}

func editorCommand(editor, path string) *exec.Cmd {
	if strings.ContainsAny(editor, " \t") {
		return exec.Command("sh", "-c", editor+" "+shellQuote(path))
	}
	return exec.Command(editor, path)
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}

func readRequiredText(path, label string) (string, error) {
	if path != "" {
		b, err := os.ReadFile(path)
		return string(b), err
	}
	fmt.Printf("%s: enter text, then Ctrl-D:\n", label)
	b, err := io.ReadAll(os.Stdin)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func stripHTMLComments(s string) string {
	for {
		start := strings.Index(s, "<!--")
		if start < 0 {
			return strings.TrimSpace(s)
		}
		end := strings.Index(s[start:], "-->")
		if end < 0 {
			return strings.TrimSpace(s[:start])
		}
		s = s[:start] + s[start+end+3:]
	}
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func asMap(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return map[string]any{}
}

func asSlice(v any) []any {
	if s, ok := v.([]any); ok {
		return s
	}
	return nil
}

func stringify(v any) string {
	if v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	case fmt.Stringer:
		return t.String()
	default:
		return fmt.Sprint(v)
	}
}

func stringSlice(items []any) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, stringify(item))
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func truthy(v any) bool {
	b, ok := v.(bool)
	return ok && b
}

func yesNo(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

func wrap(s string, width int) string {
	var out []string
	for _, para := range strings.Split(s, "\n") {
		words := strings.Fields(para)
		if len(words) == 0 {
			out = append(out, "")
			continue
		}
		line := ""
		for _, word := range words {
			if len(line)+len(word)+1 > width {
				out = append(out, line)
				line = word
			} else if line == "" {
				line = word
			} else {
				line += " " + word
			}
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

func wrapLines(s string, width int) []string {
	wrapped := wrap(s, max(16, width))
	if wrapped == "" {
		return nil
	}
	return strings.Split(wrapped, "\n")
}

func cleanInline(s string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", " "), "\r", " ")), " ")
}

func cleanBlock(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return strings.TrimSpace(s)
}

func fit(s string, width int) string {
	if width <= 1 {
		return ""
	}
	r := []rune(s)
	if len(r) <= width {
		return s + strings.Repeat(" ", width-len(r))
	}
	if width <= 4 {
		return string(r[:width])
	}
	return string(r[:width-1]) + "…"
}

func printTableHeader(title string, width int) {
	title = cleanInline(title)
	if title == "" {
		title = "RootBadger"
	}
	fmt.Println(amber(title))
	fmt.Println(muted(strings.Repeat("─", max(20, width))))
}

func terminalWidth() int {
	if width, _, ok := terminalSize(); ok && width >= 20 {
		return width
	}
	return 100
}

func terminalHeight() int {
	if _, height, ok := terminalSize(); ok && height >= 10 {
		return height
	}
	return 32
}

func terminalSize() (int, int, bool) {
	for _, fd := range []int{int(os.Stdout.Fd()), int(os.Stdin.Fd()), int(os.Stderr.Fd())} {
		ws, err := unix.IoctlGetWinsize(fd, unix.TIOCGWINSZ)
		if err == nil && ws.Col > 0 && ws.Row > 0 {
			return int(ws.Col), int(ws.Row), true
		}
	}
	return 0, 0, false
}

func isInteractiveTerminal() bool {
	return exec.Command("stty", "-g").Run() == nil
}

func amber(s string) string {
	return tuiOrange + s + "\033[39m"
}

func cyan(s string) string {
	return tuiLink + s + "\033[39m"
}

func muted(s string) string {
	return tuiMuted + s + "\033[39m"
}

func statusBar(text string, width int) string {
	text = cleanInline(text)
	if text == "" {
		text = "↑/↓ select  Enter open  Q quit"
	}
	return tuiStatusBg + tuiText + colorizeStatusCommands(fit(text, width)) + tuiBg + "\033[39m"
}

func colorizeStatusCommands(text string) string {
	replacer := strings.NewReplacer(
		" P ", " "+amber("P")+" ",
		" C ", " "+amber("C")+" ",
		" g ", " "+amber("g")+" ",
		" G ", " "+amber("G")+" ",
		" S ", " "+amber("S")+" ",
		" T ", " "+amber("T")+" ",
		" L ", " "+amber("L")+" ",
		" A ", " "+amber("A")+" ",
		" U ", " "+amber("U")+" ",
		" O ", " "+amber("O")+" ",
		" Q ", " "+amber("Q")+" ",
		" R ", " "+amber("R")+" ",
		" F:", " "+amber("F")+":",
		" N:", " "+amber("N")+":",
		" B:", " "+amber("B")+":",
		" U:", " "+amber("U")+":",
		" Q:", " "+amber("Q")+":",
		" Y/n", " "+amber("Y")+"/"+amber("n"),
		"+ subscribe", amber("+")+" subscribe",
		"- unsubscribe", amber("-")+" unsubscribe",
		"SPC:", amber("SPC")+":",
	)
	return replacer.Replace(text)
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func clamp(v, low, high int) int {
	return min(max(v, low), high)
}

func runTUI(api APIClient) error {
	state := &TUIState{api: api, screen: "subs", title: "Subscribed Groups", selected: 0}
	if api.cfg.Token == "" {
		state.status = "not logged in; run badgerclaw login first"
	} else if err := state.loadSubscriptions(); err != nil {
		state.status = err.Error()
	}
	return withCbreakTerminal(func() error {
		for {
			state.draw()
			key, err := readKey()
			if err != nil {
				if errors.Is(err, io.EOF) {
					clearScreen()
					return nil
				}
				return err
			}
			if state.confirmQuit {
				switch key {
				case "open", "y", "Y":
					clearScreen()
					return nil
				case "n", "N", "q":
					state.confirmQuit = false
					state.status = "quit cancelled"
					continue
				default:
					continue
				}
			}
			if key == "left" || key == "back" || key == "q" || key == "quit" || key == "exit" {
				if key == "left" || key == "back" || state.screen != "subs" {
					if err := state.goBack(); err != nil {
						state.status = err.Error()
					}
					continue
				}
				state.confirmQuit = true
				state.status = ""
				continue
			}
			if state.statusBarOnly() {
				state.status = ""
			}
			if err := state.handle(key); err != nil {
				state.status = err.Error()
			}
		}
	})
}

type TUIState struct {
	api         APIClient
	screen      string
	status      string
	items       []map[string]any
	title       string
	current     string
	selected    int
	listScroll  int
	showHeaders bool
	showAllSubs bool
	confirmQuit bool

	previousScreen     string
	previousTitle      string
	previousItems      []map[string]any
	previousSelected   int
	previousListScroll int
	articleRootID      string
	articleRoot        map[string]any
	articleNodes       []ArticleNode
	articleSelected    int
	articleBodyScroll  int
	articlePageSize    int
	expandedThreads    map[string]bool
	expandedGroups     map[string]bool
}

type ArticleNode struct {
	Post  map[string]any
	Depth int
	Last  bool
	Trace []bool
}

func (s *TUIState) draw() {
	width := terminalWidth()
	height := terminalHeight()
	clearScreen()
	fmt.Print(tuiBg + tuiText)
	defer fmt.Print("\033[0m")
	if s.screen == "article" {
		s.drawArticle(width, height)
		return
	}
	user := firstNonEmpty(s.api.cfg.User, "not logged in")
	fmt.Printf("%s  %s  %s\n", amber(appName), muted(s.api.cfg.BaseURL), cyan(user))
	usedRows := 1
	location := "home"
	if s.current != "" {
		location = s.current
	}
	fmt.Println(muted(strings.Repeat("─", width)))
	usedRows++
	if s.status != "" && !s.statusBarOnly() {
		fmt.Println(cyan(s.status))
		usedRows++
	}
	if s.title != "" {
		fmt.Println("\033[1m" + cleanInline(s.title) + "\033[22m")
		usedRows++
	}
	if len(s.items) == 0 {
		fmt.Println(muted("No items to show. Press g to refresh new subscribed articles, G for the group tree, or s to search."))
		usedRows++
	}
	listHeight := max(1, height-usedRows-2)
	s.ensureListSelectionVisible(listHeight)
	end := min(len(s.items), s.listScroll+listHeight)
	for i := s.listScroll; i < end; i++ {
		item := s.items[i]
		prefix := "  "
		if i == s.selected {
			prefix = cyan("> ")
		}
		switch s.screen {
		case "groups", "subs":
			connector := ""
			expandMarker := " "
			if s.screen == "groups" {
				connector = threadItemConnector(item)
				if len(childItems(item)) > 0 {
					if truthy(item["_expanded"]) {
						expandMarker = "-"
					} else {
						expandMarker = "+"
					}
				}
			}
			pathW := clamp(width/3-len([]rune(connector)), 18, 42)
			nameW := clamp(width-pathW-len([]rune(connector))-38, 14, 44)
			unread := "0"
			if n := fmt.Sprint(item["unread_count"]); n != "" && n != "0" && n != "<nil>" {
				unread = n
			}
			fmt.Printf("%s%s %s  %s%s  %s  %s\n",
				prefix,
				cyan(fit(unread, 4)),
				expandMarker,
				muted(connector),
				cyan(fit(cleanInline(groupPath(item)), pathW)),
				fit(cleanInline(stringify(item["name"])), nameW),
				muted(fmt.Sprintf("posts:%v", item["post_count"])),
			)
		case "search-users":
			fmt.Printf("%s%3d  %s  %s\n",
				prefix,
				i+1,
				cyan(fit(cleanInline(firstNonEmpty(stringify(item["display_name"]), stringify(item["username"]))), 32)),
				muted(fit("@"+cleanInline(stringify(item["username"])), max(16, width-42))),
			)
		case "search-tags":
			fmt.Printf("%s%3d  %s  %s\n",
				prefix,
				i+1,
				cyan(fit("#"+strings.TrimPrefix(cleanInline(stringify(item["name"])), "#"), 36)),
				muted(fit(fmt.Sprintf("posts:%v", item["post_count"]), max(12, width-44))),
			)
		default:
			author := asMap(item["author"])
			group := asMap(item["group"])
			unread := " "
			if truthy(item["is_unread"]) {
				unread = cyan("*")
			}
			connector := ""
			if s.screen == "threads" {
				connector = threadItemConnector(item)
			}
			replyCount := stringify(item["reply_count"])
			if replyCount == "" || replyCount == "<nil>" {
				replyCount = "0"
			}
			expandMarker := " "
			if s.screen == "threads" && len(childItems(item)) > 0 {
				if truthy(item["_expanded"]) {
					expandMarker = "-"
				} else {
					expandMarker = "+"
				}
			}
			name := firstNonEmpty(cleanInline(stringify(author["display_name"])), cleanInline(stringify(author["username"])), "unknown")
			groupPath := cleanInline(stringify(group["path"]))
			if s.screen == "threads" {
				groupPath = ""
			}
			meta := name
			if groupPath != "" {
				meta += " " + groupPath
			}
			meta += fmt.Sprintf(" u:%v", item["useful_count"])
			metaW := clamp(width/4, 12, 34)
			subjectW := max(18, width-18-len([]rune(connector))-metaW)
			fmt.Printf("%s%s%s %s  %s%s %s %s\n",
				prefix,
				unread,
				fit(replyCount, 3),
				expandMarker,
				muted(connector),
				muted(fmt.Sprintf("[%v]", item["id"])),
				amber(fit(cleanInline(stringify(item["subject"])), subjectW)),
				muted(fit(meta, metaW)),
			)
		}
	}
	fmt.Println(muted(strings.Repeat("─", width)))
	fmt.Printf("\033[%d;1H%s", height, statusBar(s.statusText(location), width))
}

func (s *TUIState) handle(key string) error {
	if s.screen == "article" {
		return s.handleArticleKey(key)
	}

	switch key {
	case "h":
		return s.loadHome()
	case "g":
		s.showAllSubs = false
		return s.loadSubscriptions()
	case "G":
		return s.loadGroups()
	case "L", "l":
		s.showAllSubs = !s.showAllSubs
		return s.loadSubscriptions()
	case "a":
		return s.loadGroups()
	case "u":
		return s.loadSubscriptions()
	case "+":
		return s.subscribeSelected(true)
	case "-":
		return s.subscribeSelected(false)
	case "s", "/":
		return s.searchFlow()
	case "t", "x":
		s.toggleHeaders()
	case "p", "n":
		return s.postFlow()
	case "c":
		return s.markCurrentGroupRead()
	case "r":
		id := s.promptInline("reply to post id: ")
		clearScreen()
		var body string
		err := withNormalTerminal(func() error {
			var bodyErr error
			body, bodyErr = bodyFromFlagOrEditor("", "Write your reply. Save and close to review.\n")
			return bodyErr
		})
		if err != nil {
			return err
		}
		if strings.TrimSpace(body) == "" {
			return errors.New("reply body was empty")
		}
		if !confirmDefaultYes("Send? Y/n: ") {
			s.status = "reply cancelled"
			return nil
		}
		var out map[string]any
		if err := s.api.post("/api/v1/app/posts/"+id+"/replies", map[string]any{"body": body}, &out); err != nil {
			return err
		}
		s.status = stringify(out["message"])
	case "m":
		var out map[string]any
		if err := s.api.get("/api/v1/app/conversations", url.Values{"per_page": {"25"}}, &out); err != nil {
			return err
		}
		clearScreen()
		printTableHeader("Messages", terminalWidth())
		for _, item := range asSlice(out["data"]) {
			c := asMap(item)
			u := asMap(c["other_user"])
			fmt.Printf("%s  %s  %s\n", muted(fmt.Sprintf("[%v]", c["id"])), fit(cleanInline(stringify(u["display_name"])), 28), muted(fmt.Sprintf("unread:%v last:%s", c["unread_count"], stringify(c["last_message_at"]))))
		}
		pause()
	case "!":
		var out map[string]any
		if err := s.api.get("/api/v1/app/notifications", url.Values{"per_page": {"50"}}, &out); err != nil {
			return err
		}
		clearScreen()
		data := asMap(out["data"])
		printTableHeader(fmt.Sprintf("Notifications  notices:%v messages:%v", data["unread_count"], data["unread_message_count"]), terminalWidth())
		for _, item := range asSlice(data["items"]) {
			n := asMap(item)
			fmt.Printf("%s %s\n%s\n\n", muted("["+stringify(n["type"])+"]"), cleanInline(stringify(n["title"])), muted(wrap(cleanInline(stringify(n["body"])), terminalWidth()-4)))
		}
		pause()
	case "up", "k":
		if s.selected > 0 {
			s.selected--
		}
	case "down", "j":
		if s.selected < len(s.items)-1 {
			s.selected++
		}
	case "enter", "open":
		if s.screen == "groups" {
			return s.toggleSelectedGroup()
		}
		if s.screen == "threads" {
			return s.toggleSelectedThread()
		}
		return s.openSelected()
	case "o":
		return s.openSelected()
	case "?":
		showTUIHelp()
	default:
		if n, err := strconv.Atoi(key); err == nil && n >= 1 && n <= len(s.items) {
			s.selected = n - 1
			return s.openSelected()
		}
	}
	return nil
}

func (s *TUIState) toggleHeaders() {
	s.showHeaders = !s.showHeaders
	if s.showHeaders {
		s.status = "headers expanded; all opened posts show full headers"
	} else {
		s.status = "headers collapsed"
	}
}

func (s *TUIState) goBack() error {
	if s.screen == "article" {
		return s.closeArticle()
	}
	if s.screen != "subs" {
		return s.loadSubscriptions()
	}
	s.status = "already at subscribed groups"
	return nil
}

func (s *TUIState) statusText(location string) string {
	if s.confirmQuit {
		return "Quit RootBadger CLI? Y/n"
	}
	if s.statusBarOnly() {
		return s.status
	}
	switch s.screen {
	case "subs":
		if s.showAllSubs {
			return "↑/↓ select  Enter open group  + subscribe  - unsubscribe  P post  C mark read  g new groups  G group tree  L unread only  S search  Q quit"
		}
		return "↑/↓ select  Enter open group  + subscribe  - unsubscribe  P post  C mark read  g refresh  G group tree  L show all  S search  Q quit"
	case "groups":
		return "↑/↓ select  Enter expand/open  O open group  + subscribe  - unsubscribe  P post  g new groups  G group tree  S search  U subscriptions  Q quit"
	case "threads", "home", "search":
		return "↑/↓ select  Enter expand/collapse  O open/read  T headers  + subscribe  - unsubscribe  P post  R reply by id  g new groups  G group tree  S search  Q quit  current:" + location
	case "article":
		return "↑/↓ select  T headers  SPC:PgDn  B:PgUp  + subscribe  - unsubscribe  F:Followup  g new groups  G group tree  Q:Quit"
	case "search-users", "search-tags":
		return "↑/↓ select  Enter open where possible  g new groups  G group tree  S search again  U subscriptions  Q quit"
	default:
		return "↑/↓ select  Enter open  S search  P post  C mark read  Q quit"
	}
}

func (s *TUIState) statusBarOnly() bool {
	return s.status == "quit cancelled"
}

func (s *TUIState) ensureListSelectionVisible(visibleRows int) {
	if visibleRows < 1 {
		visibleRows = 1
	}
	if len(s.items) == 0 {
		s.selected = 0
		s.listScroll = 0
		return
	}
	s.selected = clamp(s.selected, 0, len(s.items)-1)
	maxScroll := max(0, len(s.items)-visibleRows)
	if s.listScroll > maxScroll {
		s.listScroll = maxScroll
	}
	if s.listScroll < 0 {
		s.listScroll = 0
	}
	if s.selected < s.listScroll {
		s.listScroll = s.selected
	}
	if s.selected >= s.listScroll+visibleRows {
		s.listScroll = s.selected - visibleRows + 1
	}
}

func (s *TUIState) loadHome() error {
	var out map[string]any
	if err := s.api.get("/api/v1/app/home", url.Values{"per_page": {"20"}}, &out); err != nil {
		return err
	}
	data := asMap(out["data"])
	feed := asMap(data["feed"])
	s.screen = "home"
	s.title = "Unread feed"
	s.items = mapsFromSlice(asSlice(feed["data"]))
	s.status = "home loaded"
	s.selected = 0
	s.listScroll = 0
	return nil
}

func (s *TUIState) loadSubscriptions() error {
	var out map[string]any
	if err := s.api.get("/api/v1/app/subscriptions", url.Values{"per_page": {"100"}}, &out); err != nil {
		return err
	}
	items := mapsFromSlice(asSlice(out["data"]))
	if !s.showAllSubs {
		filtered := make([]map[string]any, 0, len(items))
		for _, item := range items {
			if unreadCount(item) > 0 {
				filtered = append(filtered, item)
			}
		}
		items = filtered
	}
	s.screen = "subs"
	if s.showAllSubs {
		s.title = "Subscribed Groups"
	} else {
		s.title = "Subscribed Groups With New Articles"
	}
	s.current = ""
	s.items = items
	if s.showAllSubs {
		s.status = "showing all subscribed groups"
	} else {
		s.status = "showing only subscribed groups with new articles; press L to show all"
	}
	s.selected = 0
	s.listScroll = 0
	return nil
}

func (s *TUIState) loadGroups() error {
	var out map[string]any
	if err := s.api.getPublic("/api/v1/groups/hierarchy", nil, &out); err != nil {
		return err
	}
	all := mapsFromSlice(asSlice(out["data"]))
	s.items = buildGroupTreeRows(all)
	s.screen = "groups"
	s.title = "Group Tree"
	s.current = ""
	s.status = "group tree loaded; Enter expands branches, O opens a group"
	s.selected = 0
	s.listScroll = 0
	s.expandedGroups = make(map[string]bool)
	return nil
}

func (s *TUIState) refreshCurrent() error {
	switch s.screen {
	case "subs":
		return s.loadSubscriptions()
	case "groups":
		return s.loadGroups()
	case "threads":
		if s.current != "" {
			return s.openGroup(s.current)
		}
	case "home":
		return s.loadHome()
	case "article":
		if s.articleRootID != "" {
			return s.loadArticle(s.articleRootID)
		}
	}
	return s.loadSubscriptions()
}

func (s *TUIState) searchFlow() error {
	types := []string{"all", "users", "groups", "hashtags", "article", "message_id"}
	labels := []string{"All", "User", "Group", "Hashtag", "Article num", "Message-ID"}
	var selected int

	err := withNormalTerminal(func() error {
		clearScreen()
		fmt.Print(tuiBg + tuiText)
		defer fmt.Print("\033[0m")
		printTableHeader("Search", terminalWidth())
		for i, label := range labels {
			fmt.Printf("  %d  %s\n", i+1, label)
		}
		fmt.Print("\nChoose search type: ")
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		n, convErr := strconv.Atoi(strings.TrimSpace(line))
		if convErr != nil || n < 1 || n > len(types) {
			return errors.New("search cancelled")
		}
		selected = n - 1
		return nil
	})
	if err != nil {
		return err
	}

	s.status = "search type: " + labels[selected]
	q := s.promptInline("search " + labels[selected] + ": ")
	if q == "" {
		return errors.New("search cancelled")
	}

	var out map[string]any
	if err := s.api.get("/api/v1/app/search", url.Values{"q": {q}, "type": {types[selected]}, "per_page": {"50"}}, &out); err != nil {
		return err
	}
	data := asMap(out["data"])
	s.title = "Search: " + labels[selected] + " / " + q
	s.current = ""
	s.selected = 0
	s.listScroll = 0
	s.status = "search results loaded; wildcards are allowed"

	switch types[selected] {
	case "groups":
		s.screen = "groups"
		s.items = mapsFromSlice(asSlice(asMap(data["groups"])["data"]))
	case "users":
		s.screen = "search-users"
		s.items = mapsFromSlice(asSlice(asMap(data["users"])["data"]))
	case "hashtags":
		s.screen = "search-tags"
		s.items = mapsFromSlice(asSlice(asMap(data["tags"])["data"]))
	default:
		s.screen = "search"
		s.items = mapsFromSlice(asSlice(asMap(data["threads"])["data"]))
	}
	return nil
}

func (s *TUIState) postFlow() error {
	group := s.current
	if group == "" {
		if s.selected >= 0 && s.selected < len(s.items) && (s.screen == "groups" || s.screen == "subs") {
			group = stringify(s.items[s.selected]["path"])
		}
	}
	if group == "" {
		group = s.promptInline("group path: ")
	}
	if group == "" {
		return errors.New("group path required")
	}
	subject := s.promptInline("subject: ")
	if subject == "" {
		return errors.New("subject required")
	}
	crosspost := s.promptInline("crosspost groups, comma separated (optional): ")
	clearScreen()
	var body string
	err := withNormalTerminal(func() error {
		var bodyErr error
		body, bodyErr = bodyFromFlagOrEditor("", "Write your post. Save and close to review.\n")
		return bodyErr
	})
	if err != nil {
		return err
	}
	if strings.TrimSpace(body) == "" {
		return errors.New("post body was empty")
	}
	if !confirmDefaultYes("Send? Y/n: ") {
		s.status = "post cancelled"
		return nil
	}
	var out map[string]any
	payload := map[string]any{"subject": subject, "body": body}
	if crosspost != "" {
		payload["crosspost_groups"] = crosspost
	}
	if err := s.api.post("/api/v1/app/groups/"+url.PathEscape(group)+"/posts", payload, &out); err != nil {
		return err
	}
	s.status = stringify(out["message"])
	return s.openGroup(group)
}

func (s *TUIState) markCurrentGroupRead() error {
	group := s.current
	if group == "" && s.selected >= 0 && s.selected < len(s.items) && (s.screen == "groups" || s.screen == "subs") {
		group = stringify(s.items[s.selected]["path"])
	}
	if group == "" {
		return errors.New("select or open a group first")
	}

	var out map[string]any
	if err := s.api.get("/api/v1/app/groups/"+url.PathEscape(group)+"/threads", url.Values{"per_page": {"1"}}, &out); err != nil {
		return err
	}
	items := mapsFromSlice(asSlice(out["data"]))
	if len(items) > 0 {
		if id := stringify(items[0]["id"]); id != "" {
			_ = s.api.post("/api/v1/app/posts/"+id+"/read", nil, &map[string]any{})
		}
	}
	s.status = "marked " + group + " read"
	if s.screen == "subs" {
		return s.loadSubscriptions()
	}
	return s.openGroup(group)
}

func (s *TUIState) openSelected() error {
	if s.selected < 0 || s.selected >= len(s.items) {
		return nil
	}
	item := s.items[s.selected]
	if s.screen == "groups" || s.screen == "subs" {
		return s.openGroup(groupPath(item))
	}
	return s.openArticle(fmt.Sprint(item["id"]))
}

func (s *TUIState) toggleSelectedGroup() error {
	if s.selected < 0 || s.selected >= len(s.items) {
		return nil
	}
	item := s.items[s.selected]
	children := childItems(item)
	if len(children) == 0 {
		return s.openGroup(groupPath(item))
	}

	path := groupPath(item)
	if path == "" {
		return nil
	}

	if truthy(item["_expanded"]) {
		s.items = collapseGroupItems(s.items, path)
		item["_expanded"] = false
		if s.expandedGroups != nil {
			delete(s.expandedGroups, path)
		}
		s.status = "branch collapsed"
		return nil
	}

	insert := make([]map[string]any, 0, len(children))
	for _, child := range children {
		child["_root_item_id"] = path
		insert = append(insert, child)
	}
	before := append([]map[string]any(nil), s.items[:s.selected+1]...)
	after := append([]map[string]any(nil), s.items[s.selected+1:]...)
	s.items = append(append(before, insert...), after...)
	item["_expanded"] = true
	if s.expandedGroups == nil {
		s.expandedGroups = make(map[string]bool)
	}
	s.expandedGroups[path] = true
	s.status = "branch expanded"
	return nil
}

func (s *TUIState) toggleSelectedThread() error {
	if s.selected < 0 || s.selected >= len(s.items) {
		return nil
	}
	item := s.items[s.selected]
	children := childItems(item)
	if len(children) == 0 {
		return s.openSelected()
	}

	rootID := stringify(item["id"])
	if rootID == "" {
		return s.openSelected()
	}

	if truthy(item["_expanded"]) {
		s.items = collapseThreadItems(s.items, rootID)
		item["_expanded"] = false
		if s.expandedThreads != nil {
			delete(s.expandedThreads, rootID)
		}
		s.status = "thread collapsed"
		return nil
	}

	insert := make([]map[string]any, 0, len(children))
	for _, child := range children {
		child["_root_item_id"] = rootID
		insert = append(insert, child)
	}
	before := append([]map[string]any(nil), s.items[:s.selected+1]...)
	after := append([]map[string]any(nil), s.items[s.selected+1:]...)
	s.items = append(append(before, insert...), after...)
	item["_expanded"] = true
	if s.expandedThreads == nil {
		s.expandedThreads = make(map[string]bool)
	}
	s.expandedThreads[rootID] = true
	s.status = "thread expanded"
	return s.openArticle(rootID)
}

func (s *TUIState) subscribeSelected(add bool) error {
	group := ""
	if s.screen == "threads" && s.current != "" {
		group = s.current
	} else if s.selected >= 0 && s.selected < len(s.items) {
		group = groupPath(s.items[s.selected])
		if group == "" {
			group = cleanInline(stringify(asMap(s.items[s.selected]["group"])["path"]))
		}
	}
	if group == "" {
		return errors.New("no group selected")
	}

	path := "/api/v1/app/groups/" + url.PathEscape(group) + "/subscribe"
	var out map[string]any
	var err error
	if add {
		err = s.api.post(path, nil, &out)
	} else {
		err = s.api.delete(path, &out)
	}
	if err != nil {
		return err
	}
	s.status = firstNonEmpty(cleanInline(stringify(out["message"])), "subscription updated")
	return nil
}

func (s *TUIState) openGroup(path string) error {
	var out map[string]any
	if err := s.api.get("/api/v1/app/groups/"+url.PathEscape(path)+"/threads", url.Values{"per_page": {"30"}, "sort": {"newest_threads"}}, &out); err != nil {
		return err
	}
	roots := mapsFromSlice(asSlice(out["data"]))
	items := make([]map[string]any, 0, len(roots))
	s.expandedThreads = make(map[string]bool)
	for _, root := range roots {
		var threadOut map[string]any
		rootID := stringify(root["id"])
		if rootID == "" {
			continue
		}
		if err := s.api.get("/api/v1/app/threads/"+rootID, nil, &threadOut); err != nil {
			root["_depth"] = 0
			root["_last"] = true
			items = append(items, root)
			continue
		}
		threadRoot := asMap(threadOut["data"])
		nodes := flattenArticleNodes(threadRoot)
		if len(nodes) == 0 {
			continue
		}
		rootPost := nodes[0].Post
		rootPost["_depth"] = 0
		rootPost["_last"] = true
		rootPost["_expanded"] = false
		children := make([]map[string]any, 0, max(0, len(nodes)-1))
		for _, node := range nodes[1:] {
			post := cloneMap(node.Post)
			post["_depth"] = node.Depth
			post["_last"] = node.Last
			post["_trace"] = node.Trace
			post["_root_item_id"] = rootID
			children = append(children, post)
		}
		rootPost["_children"] = children
		items = append(items, rootPost)
	}
	s.screen = "threads"
	s.title = path + " threads"
	s.current = path
	s.items = items
	s.status = "inside " + path + "; replies are nested under their root article"
	s.selected = 0
	s.listScroll = 0
	return nil
}

func (s *TUIState) openArticle(id string) error {
	s.previousScreen = s.screen
	s.previousTitle = s.title
	s.previousItems = append([]map[string]any(nil), s.items...)
	s.previousSelected = s.selected
	s.previousListScroll = s.listScroll
	return s.loadArticle(id)
}

func (s *TUIState) loadArticle(id string) error {
	var out map[string]any
	if err := s.api.get("/api/v1/app/threads/"+id, nil, &out); err != nil {
		return err
	}
	root := asMap(out["data"])
	nodes := flattenArticleNodes(root)
	selected := 0
	for i, node := range nodes {
		if stringify(node.Post["id"]) == id {
			selected = i
			break
		}
	}

	s.screen = "article"
	s.articleRootID = stringify(root["id"])
	s.articleRoot = root
	s.articleNodes = nodes
	s.articleSelected = selected
	s.articleBodyScroll = 0
	s.articlePageSize = 8
	s.status = "article loaded"
	if id != "" {
		_ = s.api.post("/api/v1/app/posts/"+id+"/read", nil, &map[string]any{})
	}
	return nil
}

func (s *TUIState) closeArticle() error {
	s.screen = firstNonEmpty(s.previousScreen, "subs")
	s.title = s.previousTitle
	s.items = append([]map[string]any(nil), s.previousItems...)
	s.selected = clamp(s.previousSelected, 0, max(0, len(s.items)-1))
	s.listScroll = clamp(s.previousListScroll, 0, max(0, len(s.items)-1))
	s.articleRoot = nil
	s.articleNodes = nil
	s.articleRootID = ""
	s.articleBodyScroll = 0
	s.status = "returned to group list"
	return nil
}

func (s *TUIState) handleArticleKey(key string) error {
	switch key {
	case "up", "k", "p":
		if s.articleSelected > 0 {
			s.articleSelected--
			s.articleBodyScroll = 0
		}
	case "down", "j", "n":
		if s.articleSelected < len(s.articleNodes)-1 {
			s.articleSelected++
			s.articleBodyScroll = 0
		}
	case " ", "space":
		s.articleBodyScroll += max(1, s.articlePageSize)
	case "b":
		s.articleBodyScroll = max(0, s.articleBodyScroll-max(1, s.articlePageSize))
	case "u":
		s.status = "un-mark-as-read needs API support"
	case "f", "r":
		return s.followupSelectedArticle()
	case "+":
		return s.subscribeSelected(true)
	case "-":
		return s.subscribeSelected(false)
	case "g":
		s.showAllSubs = false
		return s.loadSubscriptions()
	case "G":
		return s.loadGroups()
	case "q":
		return s.closeArticle()
	case "t", "x":
		s.toggleHeaders()
	default:
		if n, err := strconv.Atoi(key); err == nil && n >= 1 && n <= len(s.articleNodes) {
			s.articleSelected = n - 1
			s.articleBodyScroll = 0
		}
	}
	return nil
}

func (s *TUIState) followupSelectedArticle() error {
	if s.articleSelected < 0 || s.articleSelected >= len(s.articleNodes) {
		return errors.New("no article selected")
	}
	post := s.articleNodes[s.articleSelected].Post
	id := stringify(post["id"])
	clearScreen()
	initial := ""
	if confirmDefaultYes("Quote original? Y/n: ") {
		quote, err := fetchReplyQuote(s.api, id)
		if err != nil {
			return err
		}
		initial = quote
	}
	var body string
	err := withNormalTerminal(func() error {
		var bodyErr error
		body, bodyErr = bodyFromFlagOrEditorWithInitial("", "Write your followup. Save and close to review.\n", initial)
		return bodyErr
	})
	if err != nil {
		return err
	}
	if strings.TrimSpace(body) == "" {
		return errors.New("followup body was empty")
	}
	if !confirmDefaultYes("Send? Y/n: ") {
		s.status = "followup cancelled"
		return nil
	}
	var out map[string]any
	if err := s.api.post("/api/v1/app/posts/"+id+"/replies", map[string]any{"body": body}, &out); err != nil {
		return err
	}
	s.status = stringify(out["message"])
	return s.loadArticle(s.articleRootID)
}

func (s *TUIState) drawArticle(width, height int) {
	selected := map[string]any{}
	if s.articleSelected >= 0 && s.articleSelected < len(s.articleNodes) {
		selected = s.articleNodes[s.articleSelected].Post
	}
	title := cleanInline(firstNonEmpty(stringify(s.articleRoot["subject"]), stringify(selected["subject"]), "Article"))
	fmt.Printf("%s  %s\n", amber(appName), cyan(fit(title, max(20, width-len(appName)-4))))
	fmt.Println(muted(strings.Repeat("─", width)))

	topHeight := clamp(height/3, 7, 14)
	threadLines := s.threadPaneLines(width)
	start := 0
	if len(threadLines) > topHeight {
		start = clamp(s.articleSelected-(topHeight/2), 0, max(0, len(threadLines)-topHeight))
	}
	for i := 0; i < topHeight; i++ {
		idx := start + i
		if idx < len(threadLines) {
			fmt.Println(threadLines[idx])
		} else {
			fmt.Println()
		}
	}

	fmt.Println(muted(strings.Repeat("─", width)))
	bodyHeight := max(6, height-topHeight-6)
	s.articlePageSize = max(3, bodyHeight-2)
	bodyLines := s.articleBodyLines(selected, width)
	maxScroll := max(0, len(bodyLines)-bodyHeight)
	if s.articleBodyScroll > maxScroll {
		s.articleBodyScroll = maxScroll
	}
	for i := 0; i < bodyHeight; i++ {
		idx := s.articleBodyScroll + i
		if idx < len(bodyLines) {
			fmt.Println(bodyLines[idx])
		} else {
			fmt.Println()
		}
	}
	fmt.Printf("\033[%d;1H%s", height, statusBar(s.statusText("article"), width))
}

func (s *TUIState) threadPaneLines(width int) []string {
	lines := make([]string, 0, len(s.articleNodes))
	for i, node := range s.articleNodes {
		post := node.Post
		connector := threadConnector(node)
		prefix := "  "
		if i == s.articleSelected {
			prefix = cyan("> ")
		}
		author := asMap(post["author"])
		name := firstNonEmpty(cleanInline(stringify(author["display_name"])), cleanInline(stringify(author["username"])), "unknown")
		subject := cleanInline(stringify(post["subject"]))
		byline := "by " + name
		bylineW := clamp(width/4, 14, 30)
		subjectW := max(10, width-len([]rune(connector))-bylineW-18)
		line := fmt.Sprintf("%s%s%s %s  %s",
			prefix,
			muted(connector),
			cyan(fit(fmt.Sprintf("[%v]", post["id"]), 8)),
			amber(fit(subject, subjectW)),
			muted(fit(byline, bylineW)),
		)
		lines = append(lines, line)
	}
	return lines
}

func (s *TUIState) articleBodyLines(post map[string]any, width int) []string {
	if len(post) == 0 {
		return []string{muted("No article selected.")}
	}
	lines := make([]string, 0, 16)
	for _, line := range postHeaderLines(post, s.showHeaders) {
		lines = append(lines, muted(line))
	}
	lines = append(lines, "")
	for _, line := range displayBlockLines(postDisplayText(post), max(40, width-2)) {
		lines = append(lines, line)
	}
	return lines
}

func postDisplayText(post map[string]any) string {
	body := normalizeBlock(stringify(post["body"]))
	signature := normalizeBlock(stringify(post["display_signature"]))
	if strings.TrimSpace(signature) == "" {
		return body
	}
	if strings.TrimSpace(body) == "" {
		return signature
	}
	return strings.TrimRight(body, "\n") + "\n\n" + signature
}

func postHeaderLines(post map[string]any, expanded bool) []string {
	headers := asMap(post["headers"])
	lines := []string{
		"From: " + postFromLine(post),
		fmt.Sprintf("Date: %s   Score: %v   Useful: %v", cleanInline(stringify(post["created_at"])), post["vote_score"], post["useful_count"]),
	}
	if !expanded {
		return lines
	}

	ordered := []struct {
		label string
		value string
	}{
		{"Newsgroups", firstNonEmpty(headerValue(headers, "Newsgroups"), formatGroupPaths(post["group_paths"]))},
		{"Subject", cleanInline(stringify(post["subject"]))},
		{"Organization", headerValue(headers, "Organization")},
		{"Message-ID", headerValue(headers, "Message-ID")},
		{"X-Info", headerValue(headers, "X-Info")},
		{"User-Agent", firstNonEmpty(headerValue(headers, "User-Agent"), "RootBadger CLI")},
		{"Lines", firstNonEmpty(headerValue(headers, "Lines"), strconv.Itoa(countDisplayLines(stringify(post["body"]))))},
		{"X-System", firstNonEmpty(headerValue(headers, "X-System"), "RootBadger/1.0 (privacy-protected)")},
	}

	for _, item := range ordered {
		if strings.TrimSpace(item.value) == "" {
			continue
		}
		lines = append(lines, item.label+": "+item.value)
	}
	return lines
}

func postFromLine(post map[string]any) string {
	if from := headerValue(asMap(post["headers"]), "From"); from != "" {
		return from
	}
	author := asMap(post["author"])
	name := firstNonEmpty(cleanInline(stringify(author["display_name"])), cleanInline(stringify(author["username"])), "unknown")
	email := cleanInline(stringify(author["public_email"]))
	if email == "" {
		return name
	}
	return fmt.Sprintf("%s <%s>", name, email)
}

func headerValue(headers map[string]any, key string) string {
	for k, v := range headers {
		if strings.EqualFold(k, key) {
			return cleanInline(stringify(v))
		}
	}
	return ""
}

func formatGroupPaths(v any) string {
	switch value := v.(type) {
	case []any:
		return strings.Join(stringSlice(value), ", ")
	case []string:
		return strings.Join(value, ", ")
	default:
		s := strings.TrimSpace(stringify(value))
		s = strings.TrimPrefix(s, "[")
		s = strings.TrimSuffix(s, "]")
		return strings.Join(strings.Fields(s), ", ")
	}
}

func countDisplayLines(s string) int {
	s = normalizeBlock(s)
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}

func normalizeBlock(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n")
}

func displayBlockLines(s string, width int) []string {
	s = normalizeBlock(s)
	if s == "" {
		return nil
	}
	width = max(16, width)
	var out []string
	for _, line := range strings.Split(s, "\n") {
		runes := []rune(line)
		if len(runes) == 0 {
			out = append(out, "")
			continue
		}
		for len(runes) > width {
			out = append(out, string(runes[:width]))
			runes = runes[width:]
		}
		out = append(out, string(runes))
	}
	return out
}

func flattenArticleNodes(root map[string]any) []ArticleNode {
	var nodes []ArticleNode
	var walk func(map[string]any, int, bool, []bool)
	walk = func(post map[string]any, depth int, last bool, trace []bool) {
		nodes = append(nodes, ArticleNode{Post: post, Depth: depth, Last: last, Trace: append([]bool(nil), trace...)})
		replies := asSlice(post["replies"])
		for i, reply := range replies {
			walk(asMap(reply), depth+1, i == len(replies)-1, append(trace, !last))
		}
	}
	walk(root, 0, true, nil)
	return nodes
}

func threadConnector(node ArticleNode) string {
	if node.Depth == 0 {
		return ""
	}
	var b strings.Builder
	for _, keep := range node.Trace[:max(0, len(node.Trace)-1)] {
		if keep {
			b.WriteString("│  ")
		} else {
			b.WriteString("   ")
		}
	}
	if node.Last {
		b.WriteString("└─ ")
	} else {
		b.WriteString("├─ ")
	}
	return b.String()
}

func showTUIHelp() {
	clearScreen()
	fmt.Print(tuiBg + tuiText)
	defer fmt.Print("\033[0m")
	width := terminalWidth()
	printTableHeader("RootBadger CLI Help", width)
	fmt.Println("  u              subscribed groups")
	fmt.Println("  h              unread home feed")
	fmt.Println("  g              refresh subscribed groups with new articles")
	fmt.Println("  G              show full collapsed group hierarchy")
	fmt.Println("  s              search with type choices")
	fmt.Println("  j / k          move selection down/up")
	fmt.Println("  enter, o       open selected row")
	fmt.Println("  number         open that numbered row")
	fmt.Println("  p              create a post; inside a group it uses that group automatically")
	fmt.Println("  r, reply       reply by post id")
	fmt.Println("  c              mark selected/current group read")
	fmt.Println("  m, messages    private messages")
	fmt.Println("  !, notices     notifications")
	fmt.Println("  t              toggle full headers for all opened posts")
	fmt.Println("  q              quit")
	fmt.Println()
	fmt.Println(muted("Posting uses $VISUAL first, then $EDITOR, then nano."))
	pause()
}

func openThreadPager(api APIClient, id string, headers bool) error {
	var out map[string]any
	if err := api.get("/api/v1/app/threads/"+id, nil, &out); err != nil {
		return err
	}
	clearScreen()
	printPostTree(asMap(out["data"]), 0, headers)
	_ = api.post("/api/v1/app/posts/"+id+"/read", nil, &map[string]any{})
	pause()
	return nil
}

func mapsFromSlice(items []any) []map[string]any {
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		out = append(out, asMap(item))
	}
	return out
}

func (s *TUIState) promptInline(label string) string {
	var out string
	_ = withNormalTerminal(func() error {
		fmt.Printf("\033[%d;1H%s", terminalHeight(), strings.Repeat(" ", terminalWidth()))
		fmt.Printf("\033[%d;1H", terminalHeight())
		line, _ := readEditableLine(label)
		out = strings.TrimSpace(strings.TrimRight(line, "\r\n"))
		return nil
	})
	return out
}

func promptInline(label string) string {
	line, _ := readEditableLine("\n" + label)
	return strings.TrimSpace(strings.TrimRight(line, "\r\n"))
}

func pause() {
	_ = withNormalTerminal(func() error {
		fmt.Print("\nPress Enter...")
		_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
		return nil
	})
}

func confirmYesNo(label string) bool {
	var ok bool
	_ = withNormalTerminal(func() error {
		fmt.Print("\n" + label)
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		answer := strings.ToLower(strings.TrimSpace(strings.TrimRight(line, "\r\n")))
		ok = answer == "y" || answer == "yes"
		return nil
	})
	return ok
}

func confirmDefaultYes(label string) bool {
	var ok bool
	_ = withNormalTerminal(func() error {
		fmt.Print("\n" + label)
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		answer := strings.ToLower(strings.TrimSpace(strings.TrimRight(line, "\r\n")))
		ok = answer == "" || answer == "y" || answer == "yes"
		return nil
	})
	return ok
}

func clearScreen() {
	width := terminalWidth()
	height := terminalHeight()
	if width < 1 || height < 1 {
		fmt.Print("\033[2J\033[H")
		return
	}
	blank := strings.Repeat(" ", width)
	fmt.Print("\033[2J\033[H" + tuiBg)
	for row := 0; row < height; row++ {
		fmt.Print(blank)
		if row < height-1 {
			fmt.Print("\n")
		}
	}
	fmt.Print("\033[H")
}

func withCbreakTerminal(fn func() error) error {
	fd := int(os.Stdin.Fd())
	oldState, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		return fn()
	}
	terminalRestoreState = oldState
	if err := rawTerminal(fd); err != nil {
		return err
	}
	defer restoreTerminalState(fd, terminalRestoreState)
	return fn()
}

func withNormalTerminal(fn func() error) error {
	if terminalRestoreState == nil {
		return fn()
	}
	fd := int(os.Stdin.Fd())
	restoreTerminalState(fd, terminalRestoreState)
	defer func() {
		_ = rawTerminal(fd)
	}()
	return fn()
}

func rawTerminal(fd int) error {
	state, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		return err
	}
	raw := *state
	raw.Iflag &^= unix.BRKINT | unix.ICRNL | unix.INPCK | unix.ISTRIP | unix.IXON
	raw.Cflag |= unix.CS8
	raw.Lflag &^= unix.ECHO | unix.ICANON | unix.IEXTEN | unix.ISIG
	raw.Cc[unix.VMIN] = 1
	raw.Cc[unix.VTIME] = 0
	return unix.IoctlSetTermios(fd, unix.TCSETS, &raw)
}

func restoreTerminalState(fd int, state *unix.Termios) {
	if state == nil {
		return
	}
	_ = unix.IoctlSetTermios(fd, unix.TCSETS, state)
}

func readKey() (string, error) {
	var b [1]byte
	_, err := os.Stdin.Read(b[:])
	if err != nil {
		return "", err
	}
	switch b[0] {
	case '\r', '\n':
		return "open", nil
	case 27:
		return readEscapeKey()
	case 3:
		return "q", nil
	case 127, 8:
		return "backspace", nil
	}
	if b[0] >= 'A' && b[0] <= 'Z' {
		return string(b[0]), nil
	}
	return strings.ToLower(string(b[0])), nil
}

func readEscapeKey() (string, error) {
	var seq [1]byte
	if _, err := os.Stdin.Read(seq[:]); err != nil {
		return "", err
	}
	if seq[0] != '[' && seq[0] != 'O' {
		return "", nil
	}

	if _, err := os.Stdin.Read(seq[:]); err != nil {
		return "", err
	}
	switch seq[0] {
	case 'A':
		return "k", nil
	case 'B':
		return "j", nil
	case 'C':
		return "right", nil
	case 'D':
		return "left", nil
	case 'H':
		return "home-key", nil
	case 'F':
		return "end-key", nil
	}

	if seq[0] >= '0' && seq[0] <= '9' {
		// Consume CSI modifier terminator such as ESC [ 1 ; 5 B without echoing it.
		for {
			if _, err := os.Stdin.Read(seq[:]); err != nil {
				return "", err
			}
			switch seq[0] {
			case 'A':
				return "k", nil
			case 'B':
				return "j", nil
			case 'C':
				return "right", nil
			case 'D':
				return "left", nil
			case '~':
				return "", nil
			}
			if (seq[0] >= 'A' && seq[0] <= 'Z') || (seq[0] >= 'a' && seq[0] <= 'z') {
				return "", nil
			}
		}
	}

	return "", nil
}

func threadItemConnector(item map[string]any) string {
	depth, _ := strconv.Atoi(stringify(item["_depth"]))
	if depth <= 0 {
		return ""
	}
	trace := boolSlice(item["_trace"])
	last := truthy(item["_last"])
	node := ArticleNode{Depth: depth, Last: last, Trace: trace}
	return threadConnector(node)
}

func childItems(item map[string]any) []map[string]any {
	switch children := item["_children"].(type) {
	case []map[string]any:
		return children
	case []any:
		out := make([]map[string]any, 0, len(children))
		for _, child := range children {
			out = append(out, asMap(child))
		}
		return out
	default:
		return nil
	}
}

func unreadCount(item map[string]any) int {
	raw := stringify(item["unread_count"])
	if raw == "" || raw == "<nil>" {
		return 0
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0
	}
	return n
}

func collapseThreadItems(items []map[string]any, rootID string) []map[string]any {
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if stringify(item["_root_item_id"]) == rootID {
			continue
		}
		out = append(out, item)
	}
	return out
}

func collapseGroupItems(items []map[string]any, rootPath string) []map[string]any {
	out := make([]map[string]any, 0, len(items))
	prefix := rootPath + "."
	for _, item := range items {
		path := groupPath(item)
		if path != "" && strings.HasPrefix(path, prefix) {
			continue
		}
		out = append(out, item)
	}
	return out
}

func buildGroupTreeRows(groups []map[string]any) []map[string]any {
	byParent := make(map[string][]map[string]any)
	byID := make(map[string]map[string]any)
	rootID := ""

	for _, raw := range groups {
		item := cloneMap(raw)
		path := cleanInline(firstNonEmpty(stringify(item["path"]), stringify(item["full_name"])))
		if path == "" {
			continue
		}
		item["path"] = path
		item["name"] = firstNonEmpty(cleanInline(stringify(item["name"])), cleanInline(stringify(item["display_name"])), path)
		item["post_count"] = item["post_count"]
		item["_depth"] = 0
		item["_last"] = true
		item["_expanded"] = false

		id := stringify(item["id"])
		if id != "" {
			byID[id] = item
		}
		if path == "rb" {
			rootID = id
		}
	}

	for _, item := range byID {
		parentID := stringify(item["parent_id"])
		byParent[parentID] = append(byParent[parentID], item)
	}

	for parentID := range byParent {
		items := byParent[parentID]
		sortGroupRows(items)
		byParent[parentID] = items
	}

	var roots []map[string]any
	if rootID != "" {
		roots = byParent[rootID]
	} else {
		roots = byParent[""]
	}

	var attach func(map[string]any, int, bool, []bool) map[string]any
	attach = func(item map[string]any, depth int, last bool, trace []bool) map[string]any {
		row := cloneMap(item)
		row["_depth"] = depth
		row["_last"] = last
		row["_trace"] = append([]bool(nil), trace...)
		children := byParent[stringify(row["id"])]
		if len(children) > 0 {
			treeChildren := make([]map[string]any, 0, len(children))
			for i, child := range children {
				treeChildren = append(treeChildren, attach(child, depth+1, i == len(children)-1, append(trace, !last)))
			}
			row["_children"] = treeChildren
		}
		return row
	}

	rows := make([]map[string]any, 0, len(roots))
	for i, root := range roots {
		rows = append(rows, attach(root, 0, i == len(roots)-1, nil))
	}
	return rows
}

func sortGroupRows(items []map[string]any) {
	for i := 0; i < len(items)-1; i++ {
		for j := i + 1; j < len(items); j++ {
			if groupPath(items[j]) < groupPath(items[i]) {
				items[i], items[j] = items[j], items[i]
			}
		}
	}
}

func groupPath(item map[string]any) string {
	return cleanInline(firstNonEmpty(stringify(item["path"]), stringify(item["full_name"])))
}

func cloneMap(item map[string]any) map[string]any {
	out := make(map[string]any, len(item))
	for key, value := range item {
		out[key] = value
	}
	return out
}

func connectorIndent(item map[string]any) string {
	connector := threadItemConnector(item)
	if connector == "" {
		return ""
	}
	return strings.Repeat(" ", len([]rune(connector)))
}

func boolSlice(v any) []bool {
	switch value := v.(type) {
	case []bool:
		return append([]bool(nil), value...)
	case []any:
		out := make([]bool, 0, len(value))
		for _, item := range value {
			out = append(out, truthy(item))
		}
		return out
	default:
		return nil
	}
}

var terminalRestoreState *unix.Termios

func init() {
	_ = syscall.SIGTERM
}
