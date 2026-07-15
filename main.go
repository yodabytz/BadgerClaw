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
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const (
	appName       = "RootBadger CLI"
	userAgentName = "RootBadger CLI"
	version       = "0.1.0"
	defaultURL    = "https://rootbadger.com"
)

// The active theme's escape codes. Everything the TUI prints goes through
// these six values (plus the amber/cyan/muted wrappers), so switching theme is
// just reassigning them. applyTheme sets them from the table below.
var (
	tuiBg       = "\033[48;5;233m"
	tuiStatusBg = "\033[48;5;235m"
	tuiText     = "\033[38;5;230m"
	tuiOrange   = "\033[38;5;208m"
	tuiLink     = "\033[38;5;214m"
	tuiMuted    = "\033[38;5;245m"
)

// Theme holds one color scheme as ANSI escape strings.
type Theme struct {
	Bg, StatusBg, Text, Accent, Link, Muted string
}

// useTrueColor decides at startup how theme colors are emitted: exact 24-bit
// codes when the terminal advertises support, otherwise each color is
// quantized to the nearest xterm-256 index so every theme works everywhere.
// BADGERCLAW_COLOR=truecolor or =256 overrides the detection.
var useTrueColor = trueColorTerminal()

func trueColorTerminal() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("BADGERCLAW_COLOR"))) {
	case "truecolor", "24bit":
		return true
	case "256":
		return false
	}
	ct := strings.ToLower(os.Getenv("COLORTERM"))
	return strings.Contains(ct, "truecolor") || strings.Contains(ct, "24bit")
}

func bg(hex string) string {
	r, g, b := parseHex(hex)
	if useTrueColor {
		return fmt.Sprintf("\033[48;2;%d;%d;%dm", r, g, b)
	}
	return fmt.Sprintf("\033[48;5;%dm", nearest256(r, g, b))
}

func fg(hex string) string {
	r, g, b := parseHex(hex)
	if useTrueColor {
		return fmt.Sprintf("\033[38;2;%d;%d;%dm", r, g, b)
	}
	return fmt.Sprintf("\033[38;5;%dm", nearest256(r, g, b))
}

func parseHex(h string) (int, int, int) {
	var r, g, b int
	fmt.Sscanf(h, "%02x%02x%02x", &r, &g, &b)
	return r, g, b
}

func hexRGB(h string) string {
	r, g, b := parseHex(h)
	return fmt.Sprintf("%d;%d;%d", r, g, b)
}

// nearest256 maps an RGB color to the closest entry in the xterm-256 palette:
// the 6x6x6 color cube (16-231) or the grayscale ramp (232-255), whichever is
// nearer.
func nearest256(r, g, b int) int {
	levels := []int{0, 95, 135, 175, 215, 255}
	nearestLevel := func(v int) int {
		best, bi := 1<<30, 0
		for i, l := range levels {
			if d := (v - l) * (v - l); d < best {
				best, bi = d, i
			}
		}
		return bi
	}
	dist := func(r2, g2, b2 int) int {
		return (r-r2)*(r-r2) + (g-g2)*(g-g2) + (b-b2)*(b-b2)
	}

	ri, gi, bi := nearestLevel(r), nearestLevel(g), nearestLevel(b)
	cubeIdx := 16 + 36*ri + 6*gi + bi
	cubeDist := dist(levels[ri], levels[gi], levels[bi])

	// Grayscale ramp: 24 entries at brightness 8, 18, ... 238.
	avg := (r + g + b) / 3
	gi2 := (avg - 8 + 5) / 10
	if gi2 < 0 {
		gi2 = 0
	}
	if gi2 > 23 {
		gi2 = 23
	}
	grayLevel := 8 + 10*gi2
	grayIdx := 232 + gi2
	grayDist := dist(grayLevel, grayLevel, grayLevel)

	if grayDist < cubeDist {
		return grayIdx
	}
	return cubeIdx
}

// themeOrder keeps the picker and `badgerclaw theme` listing stable.
var themeOrder = []string{
	"default", "gruvbox-dark", "gruvbox-light", "tokyonight-storm",
	"tokyonight-moon", "catppuccin-mocha", "nord", "one-dark", "dracula",
	"nightfox", "everforest",
}

var themes = map[string]Theme{
	// The original RootBadger scheme, in 256-color codes.
	"default": {
		Bg: "\033[48;5;233m", StatusBg: "\033[48;5;235m", Text: "\033[38;5;230m",
		Accent: "\033[38;5;208m", Link: "\033[38;5;214m", Muted: "\033[38;5;245m",
	},
	"gruvbox-dark": {
		Bg: bg("282828"), StatusBg: bg("3c3836"), Text: fg("ebdbb2"),
		Accent: fg("fe8019"), Link: fg("8ec07c"), Muted: fg("928374"),
	},
	"gruvbox-light": {
		Bg: bg("fbf1c7"), StatusBg: bg("ebdbb2"), Text: fg("3c3836"),
		Accent: fg("af3a03"), Link: fg("427b58"), Muted: fg("928374"),
	},
	"tokyonight-storm": {
		Bg: bg("24283b"), StatusBg: bg("1f2335"), Text: fg("c0caf5"),
		Accent: fg("7aa2f7"), Link: fg("7dcfff"), Muted: fg("565f89"),
	},
	"tokyonight-moon": {
		Bg: bg("222436"), StatusBg: bg("1e2030"), Text: fg("c8d3f5"),
		Accent: fg("82aaff"), Link: fg("86e1fc"), Muted: fg("636da6"),
	},
	"catppuccin-mocha": {
		Bg: bg("1e1e2e"), StatusBg: bg("181825"), Text: fg("cdd6f4"),
		Accent: fg("cba6f7"), Link: fg("89dceb"), Muted: fg("6c7086"),
	},
	"nord": {
		Bg: bg("2e3440"), StatusBg: bg("3b4252"), Text: fg("d8dee9"),
		Accent: fg("88c0d0"), Link: fg("81a1c1"), Muted: fg("616e88"),
	},
	"one-dark": {
		Bg: bg("282c34"), StatusBg: bg("21252b"), Text: fg("abb2bf"),
		Accent: fg("c678dd"), Link: fg("56b6c2"), Muted: fg("5c6370"),
	},
	"dracula": {
		Bg: bg("282a36"), StatusBg: bg("44475a"), Text: fg("f8f8f2"),
		Accent: fg("ff79c6"), Link: fg("8be9fd"), Muted: fg("6272a4"),
	},
	"nightfox": {
		Bg: bg("192330"), StatusBg: bg("212e3f"), Text: fg("cdcecf"),
		Accent: fg("719cd6"), Link: fg("63cdcf"), Muted: fg("71839b"),
	},
	"everforest": {
		Bg: bg("2d353b"), StatusBg: bg("343f44"), Text: fg("d3c6aa"),
		Accent: fg("a7c080"), Link: fg("7fbbb3"), Muted: fg("859289"),
	},
}

// applyTheme switches the active colors. Unknown names keep the default and
// report false.
var activeThemeName = "default"

func applyTheme(name string) bool {
	if name == "" {
		name = "default"
	}
	t, ok := themes[name]
	if !ok {
		t = themes["default"]
		name = "default"
	}
	activeThemeName = name
	tuiBg, tuiStatusBg, tuiText = t.Bg, t.StatusBg, t.Text
	tuiOrange, tuiLink, tuiMuted = t.Accent, t.Link, t.Muted
	return ok
}

type Config struct {
	BaseURL     string `json:"base_url"`
	Token       string `json:"token,omitempty"`
	User        string `json:"user,omitempty"`
	WrapColumns *int   `json:"wrap_columns,omitempty"`
	Theme       string `json:"theme,omitempty"`
}

type APIClient struct {
	cfg    Config
	client *http.Client
	// onRequest, when set, is called just before each HTTP request so an
	// interactive client can show live activity (fetching, updating, ...).
	onRequest func(method, path string)
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
	applyTheme(cfg.Theme)
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
	case "profile-edit":
		err = cmdProfileEdit(api)
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
	case "theme":
		err = cmdTheme(os.Args[2:])
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
  badgerclaw profile-edit
  badgerclaw theme [NAME]
  badgerclaw profile-update [--display-name NAME] [--bio TEXT] [--bio-file file]
                            [--website URL] [--interests "a, b"] [--signature TEXT]
                            [--signature-file file] [--organization TEXT] [--x-info TEXT]
                            [--tagline TEXT] [--quote-header TPL] [--show-headers]
                            [--notify-replies] [--newsletter] [--private]
                            [--image-attachments click|never]
                            [--email-display hidden|masked|real|custom] [--email-public ADDR]
                            [--custom-headers "Name: Value; Name2: Value2"]
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
	wrap := fs.Int("wrap", -1, "hard-wrap column; 0 disables wrapping")
	_ = fs.Parse(args)
	if fs.NArg() < 1 {
		return errors.New("group path required")
	}
	if *subject == "" {
		*subject = promptLine("Subject: ")
	}
	body, err := composeBody(*bodyFile, "Write your post. Save and close to submit.\n", wrapOverride(api, *wrap))
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
	wrap := fs.Int("wrap", -1, "hard-wrap column; 0 disables wrapping")
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
	body, err := composeBodyWithInitial(*bodyFile, "Write your reply. Save and close to review.\n", initial, wrapOverride(api, *wrap))
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
	wrap := fs.Int("wrap", -1, "hard-wrap column; 0 disables wrapping")
	_ = fs.Parse(args)
	if fs.NArg() < 1 {
		return errors.New("username required")
	}
	body, err := composeBody(*bodyFile, "Write your message. Save and close to send.\n", wrapOverride(api, *wrap))
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

// cmdTheme lists the color schemes or switches to one. The choice is saved in
// the local config, so it sticks across sessions and does not touch the
// server profile.
func cmdTheme(args []string) error {
	if len(args) == 0 {
		for _, name := range themeOrder {
			t := themes[name]
			marker := "  "
			if name == activeThemeName {
				marker = "* "
			}
			fmt.Printf("%s%s%s%s %-18s sample text \033[0m\n", marker, t.Bg, t.Accent, "\u2588\u2588", name)
		}
		fmt.Println("\nbadgerclaw theme NAME  switches; the setting is saved locally.")
		return nil
	}
	name := strings.ToLower(strings.TrimSpace(args[0]))
	if _, ok := themes[name]; !ok {
		return fmt.Errorf("unknown theme %q; run `badgerclaw theme` for the list", args[0])
	}
	applyTheme(name)
	cfg, _ := loadConfig()
	cfg.Theme = name
	if err := saveConfig(cfg); err != nil {
		return err
	}
	fmt.Printf("Theme set to %s.\n", name)
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

// profileForm holds every profile field the app API lets a user change.
// The API replaces the whole profile on write: any editable field left out of
// the request is reset to its default. So every write starts from the current
// server state, applies just the requested changes, and sends the lot back.
type profileForm struct {
	displayName   string
	bio           string
	website       string
	interests     string
	organization  string
	xInfo         string
	signature     string
	tagline       string
	quoteHeader   string
	showHeaders   bool
	notifyReplies bool
	newsletter    bool
	private       bool
	imagePref     string
	emailDisplay  string
	emailPublic   string
	custom        string // one "Name: Value" per line, max 5
}

func fetchProfileForm(api APIClient) (profileForm, error) {
	var out map[string]any
	if err := api.get("/api/v1/app/profile", nil, &out); err != nil {
		return profileForm{}, err
	}

	data := asMap(out["data"])
	user := asMap(data["user"])
	prefs := asMap(data["preferences"])
	headers := asMap(data["headers"])

	interests := []string{}
	for _, item := range asSlice(data["interests"]) {
		if v := strings.TrimSpace(stringify(item)); v != "" {
			interests = append(interests, v)
		}
	}

	customLines := []string{}
	for _, item := range asSlice(headers["custom"]) {
		h := asMap(item)
		name := cleanInline(stringify(h["name"]))
		value := cleanInline(stringify(h["value"]))
		if name != "" && value != "" {
			customLines = append(customLines, name+": "+value)
		}
	}

	form := profileForm{
		displayName:   stringify(user["display_name"]),
		bio:           stringify(user["bio"]),
		website:       stringify(user["website"]),
		interests:     strings.Join(interests, ", "),
		organization:  stringify(headers["organization"]),
		xInfo:         stringify(headers["x_info"]),
		signature:     stringify(headers["signature"]),
		tagline:       stringify(headers["tagline"]),
		quoteHeader:   stringify(headers["quote_header_template"]),
		showHeaders:   asBool(prefs["show_headers"]),
		notifyReplies: asBool(prefs["notify_direct_replies"]),
		newsletter:    asBool(prefs["newsletter_emails"]),
		private:       asBool(prefs["is_profile_private"]),
		imagePref:     stringify(prefs["image_attachment_preference"]),
		emailDisplay:  stringify(prefs["email_display"]),
		emailPublic:   stringify(prefs["email_public"]),
		custom:        strings.Join(customLines, "\n"),
	}

	if form.imagePref != "never" {
		form.imagePref = "click"
	}
	if !validEmailDisplay(form.emailDisplay) {
		form.emailDisplay = "hidden"
	}
	return form, nil
}

// payload renders the complete profile write body. Empty optional strings are
// sent as null so the server clears them instead of failing validation on "".
func (f profileForm) payload() map[string]any {
	orNull := func(s string) any {
		if strings.TrimSpace(s) == "" {
			return nil
		}
		return s
	}

	body := map[string]any{
		"display_name":                orNull(f.displayName),
		"bio":                         orNull(f.bio),
		"website":                     orNull(f.website),
		"interests_text":              f.interests,
		"hdr_organization":            orNull(f.organization),
		"hdr_x_info":                  orNull(f.xInfo),
		"hdr_x_signature":             orNull(f.signature),
		"hdr_tagline":                 orNull(f.tagline),
		"quote_header_template":       orNull(f.quoteHeader),
		"show_headers":                f.showHeaders,
		"notify_direct_replies":       f.notifyReplies,
		"newsletter_emails":           f.newsletter,
		"is_profile_private":          f.private,
		"image_attachment_preference": f.imagePref,
		"email_display":               f.emailDisplay,
		"email_public":                nil,
	}

	if f.emailDisplay == "custom" {
		body["email_public"] = orNull(f.emailPublic)
	}

	custom, _ := parseCustomHeaders(f.custom)
	if len(custom) == 0 {
		body["custom_headers"] = nil
	} else {
		body["custom_headers"] = custom
	}
	return body
}

// parseCustomHeaders turns "Name: Value" lines into the API's header list,
// enforcing the five-header limit and the header-name shape locally so the
// user gets a clear message instead of a 422.
func parseCustomHeaders(text string) ([]map[string]string, error) {
	out := []map[string]string{}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		name, value, found := strings.Cut(line, ":")
		name = strings.TrimSpace(name)
		value = strings.TrimSpace(value)
		if !found || name == "" || value == "" {
			return nil, fmt.Errorf("bad header line %q; use Name: Value", line)
		}
		if !validHeaderName(name) {
			return nil, fmt.Errorf("bad header name %q; letters, digits and dashes only", name)
		}
		out = append(out, map[string]string{"name": name, "value": value})
		if len(out) > 5 {
			return nil, errors.New("at most 5 custom headers")
		}
	}
	return out, nil
}

func validHeaderName(name string) bool {
	if name == "" || (name[0] < 'A' || name[0] > 'Z') && (name[0] < 'a' || name[0] > 'z') {
		return false
	}
	for _, r := range name {
		if !(r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			return false
		}
	}
	return true
}

func saveProfileForm(api APIClient, form profileForm) (map[string]any, error) {
	var out map[string]any
	if err := api.post("/api/v1/app/profile", form.payload(), &out); err != nil {
		return nil, err
	}
	return out, nil
}

func validEmailDisplay(v string) bool {
	switch v {
	case "hidden", "masked", "real", "custom":
		return true
	}
	return false
}

func orPlaceholder(s, placeholder string) string {
	if strings.TrimSpace(s) == "" {
		return muted(placeholder)
	}
	return cleanInline(s)
}

func cmdProfileUpdate(api APIClient, args []string) error {
	fs := flag.NewFlagSet("profile-update", flag.ExitOnError)
	displayName := fs.String("display-name", "", "display name")
	bio := fs.String("bio", "", "bio text")
	bioFile := fs.String("bio-file", "", "read bio from a file")
	website := fs.String("website", "", "website URL")
	interests := fs.String("interests", "", "comma separated interests")
	organization := fs.String("organization", "", "Organization header")
	xinfo := fs.String("x-info", "", "X-Info header")
	signature := fs.String("signature", "", "signature text")
	signatureFile := fs.String("signature-file", "", "read signature from a file")
	tagline := fs.String("tagline", "", "tagline")
	quoteHeader := fs.String("quote-header", "", "quote header template; must contain {date} and {author}")
	showHeaders := fs.Bool("show-headers", false, "show posting headers")
	notifyReplies := fs.Bool("notify-replies", true, "notify on direct replies")
	newsletter := fs.Bool("newsletter", true, "receive newsletter emails")
	private := fs.Bool("private", false, "keep the profile private")
	imagePref := fs.String("image-attachments", "click", "image attachments: click or never")
	emailDisplay := fs.String("email-display", "hidden", "email display: hidden, masked, real, or custom")
	emailPublic := fs.String("email-public", "", "custom public email; used with --email-display custom")
	customHeaders := fs.String("custom-headers", "", "up to 5 as \"Name: Value; Name2: Value2\"; empty string clears")
	_ = fs.Parse(args)

	changed := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { changed[f.Name] = true })

	if len(changed) == 0 {
		return errors.New("no fields given; run `badgerclaw profile-edit` for the interactive editor, or pass flags such as --display-name")
	}

	// Start from the current profile so untouched fields survive the write.
	form, err := fetchProfileForm(api)
	if err != nil {
		return err
	}

	if changed["display-name"] {
		form.displayName = *displayName
	}
	if changed["bio"] {
		form.bio = *bio
	}
	if changed["bio-file"] {
		b, readErr := os.ReadFile(*bioFile)
		if readErr != nil {
			return readErr
		}
		form.bio = string(b)
	}
	if changed["website"] {
		form.website = *website
	}
	if changed["interests"] {
		form.interests = *interests
	}
	if changed["organization"] {
		form.organization = *organization
	}
	if changed["x-info"] {
		form.xInfo = *xinfo
	}
	if changed["signature"] {
		form.signature = *signature
	}
	if changed["signature-file"] {
		b, readErr := os.ReadFile(*signatureFile)
		if readErr != nil {
			return readErr
		}
		form.signature = string(b)
	}
	if changed["tagline"] {
		form.tagline = *tagline
	}
	if changed["quote-header"] {
		form.quoteHeader = *quoteHeader
	}
	if changed["show-headers"] {
		form.showHeaders = *showHeaders
	}
	if changed["notify-replies"] {
		form.notifyReplies = *notifyReplies
	}
	if changed["newsletter"] {
		form.newsletter = *newsletter
	}
	if changed["private"] {
		form.private = *private
	}
	if changed["image-attachments"] {
		if *imagePref != "click" && *imagePref != "never" {
			return errors.New("--image-attachments must be click or never")
		}
		form.imagePref = *imagePref
	}
	if changed["email-display"] {
		if !validEmailDisplay(*emailDisplay) {
			return errors.New("--email-display must be hidden, masked, real, or custom")
		}
		form.emailDisplay = *emailDisplay
	}
	if changed["email-public"] {
		form.emailPublic = *emailPublic
	}
	if changed["custom-headers"] {
		text := strings.ReplaceAll(*customHeaders, ";", "\n")
		if _, err := parseCustomHeaders(text); err != nil {
			return err
		}
		form.custom = strings.TrimSpace(text)
	}

	out, err := saveProfileForm(api, form)
	if err != nil {
		return err
	}

	fmt.Println(stringify(out["message"]))
	printJSON(asMap(out["data"])["user"])
	return nil
}

type profileField struct {
	label   string
	display func(f profileForm) string
	edit    func(f *profileForm) error
}

func profileFields() []profileField {
	return []profileField{
		{
			label:   "Display name",
			display: func(f profileForm) string { return orPlaceholder(f.displayName, "(none)") },
			edit: func(f *profileForm) error {
				value, err := profilePromptInitial("display name: ", f.displayName)
				if err != nil {
					return err
				}
				f.displayName = value
				return nil
			},
		},
		{
			label:   "Bio",
			display: func(f profileForm) string { return orPlaceholder(firstLine(f.bio), "(none)") },
			edit: func(f *profileForm) error {
				body, err := bodyFromFlagOrEditorWithInitial("", "Bio, up to 500 characters. ", f.bio)
				if err != nil {
					return err
				}
				f.bio = strings.TrimSpace(body)
				return nil
			},
		},
		{
			label:   "Website",
			display: func(f profileForm) string { return orPlaceholder(f.website, "(none)") },
			edit: func(f *profileForm) error {
				value, err := profilePromptInitial("website URL: ", f.website)
				if err != nil {
					return err
				}
				f.website = value
				return nil
			},
		},
		{
			label:   "Interests",
			display: func(f profileForm) string { return orPlaceholder(f.interests, "(none)") },
			edit: func(f *profileForm) error {
				value, err := profilePromptInitial("interests, comma separated: ", f.interests)
				if err != nil {
					return err
				}
				f.interests = value
				return nil
			},
		},
		{
			label:   "Organization",
			display: func(f profileForm) string { return orPlaceholder(f.organization, "(none)") },
			edit: func(f *profileForm) error {
				value, err := profilePromptInitial("organization: ", f.organization)
				if err != nil {
					return err
				}
				f.organization = value
				return nil
			},
		},
		{
			label:   "X-Info",
			display: func(f profileForm) string { return orPlaceholder(f.xInfo, "(none)") },
			edit: func(f *profileForm) error {
				value, err := profilePromptInitial("x-info: ", f.xInfo)
				if err != nil {
					return err
				}
				f.xInfo = value
				return nil
			},
		},
		{
			label:   "Tagline",
			display: func(f profileForm) string { return orPlaceholder(f.tagline, "(none)") },
			edit: func(f *profileForm) error {
				value, err := profilePromptInitial("tagline: ", f.tagline)
				if err != nil {
					return err
				}
				f.tagline = value
				return nil
			},
		},
		{
			label:   "Signature",
			display: func(f profileForm) string { return orPlaceholder(firstLine(f.signature), "(none)") },
			edit: func(f *profileForm) error {
				body, err := bodyFromFlagOrEditorWithInitial("", "Signature shown under your posts. ", f.signature)
				if err != nil {
					return err
				}
				f.signature = strings.TrimSpace(body)
				return nil
			},
		},
		{
			label:   "Quote header",
			display: func(f profileForm) string { return orPlaceholder(f.quoteHeader, "(default)") },
			edit: func(f *profileForm) error {
				value, err := profilePromptInitial("quote header, needs {date} and {author}, empty for default: ", f.quoteHeader)
				if err != nil {
					return err
				}
				if value != "" && (!strings.Contains(value, "{date}") || !strings.Contains(value, "{author}")) {
					return errors.New("quote header must contain {date} and {author}, or be empty for the default")
				}
				f.quoteHeader = value
				return nil
			},
		},
		{
			label:   "Show posting headers",
			display: func(f profileForm) string { return yesNo(f.showHeaders) },
			edit: func(f *profileForm) error {
				f.showHeaders = !f.showHeaders
				return nil
			},
		},
		{
			label:   "Notify on direct replies",
			display: func(f profileForm) string { return yesNo(f.notifyReplies) },
			edit: func(f *profileForm) error {
				f.notifyReplies = !f.notifyReplies
				return nil
			},
		},
		{
			label:   "Newsletter emails",
			display: func(f profileForm) string { return yesNo(f.newsletter) },
			edit: func(f *profileForm) error {
				f.newsletter = !f.newsletter
				return nil
			},
		},
		{
			label:   "Private profile",
			display: func(f profileForm) string { return yesNo(f.private) },
			edit: func(f *profileForm) error {
				f.private = !f.private
				return nil
			},
		},
		{
			label:   "Image attachments",
			display: func(f profileForm) string { return f.imagePref },
			edit: func(f *profileForm) error {
				if f.imagePref == "never" {
					f.imagePref = "click"
				} else {
					f.imagePref = "never"
				}
				return nil
			},
		},
		{
			label: "Custom headers",
			display: func(f profileForm) string {
				n := 0
				for _, line := range strings.Split(f.custom, "\n") {
					if strings.TrimSpace(line) != "" {
						n++
					}
				}
				if n == 0 {
					return muted("(none, up to 5)")
				}
				return fmt.Sprintf("%d set  %s", n, muted(firstLine(f.custom)))
			},
			edit: func(f *profileForm) error {
				body, err := bodyFromFlagOrEditorWithInitial("", "Custom post headers, one per line as Name: Value, up to 5. Names use letters, digits and dashes. ", f.custom)
				if err != nil {
					if strings.Contains(err.Error(), "empty body") {
						f.custom = ""
						return nil
					}
					return err
				}
				text := strings.TrimSpace(body)
				if _, err := parseCustomHeaders(text); err != nil {
					return err
				}
				f.custom = text
				return nil
			},
		},
		{
			label: "Theme (this device)",
			display: func(f profileForm) string {
				return activeThemeName + muted("  local setting, saved immediately")
			},
			edit: func(f *profileForm) error {
				next := 0
				for i, name := range themeOrder {
					if name == activeThemeName {
						next = (i + 1) % len(themeOrder)
						break
					}
				}
				applyTheme(themeOrder[next])
				cfg, _ := loadConfig()
				cfg.Theme = themeOrder[next]
				return saveConfig(cfg)
			},
		},
		{
			label: "Email display",
			display: func(f profileForm) string {
				if f.emailDisplay == "custom" {
					return "custom: " + orPlaceholder(f.emailPublic, "(not set)")
				}
				return f.emailDisplay
			},
			edit: func(f *profileForm) error {
				order := []string{"hidden", "masked", "real", "custom"}
				next := 0
				for i, v := range order {
					if v == f.emailDisplay {
						next = (i + 1) % len(order)
						break
					}
				}
				f.emailDisplay = order[next]
				if f.emailDisplay == "custom" {
					value, err := profilePromptInitial("public email address: ", f.emailPublic)
					if err != nil {
						return err
					}
					f.emailPublic = value
				}
				return nil
			},
		},
	}
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if idx := strings.IndexAny(s, "\r\n"); idx >= 0 {
		return strings.TrimSpace(s[:idx]) + " ..."
	}
	return s
}

// The editor draws with absolute cursor moves and never prints a trailing
// newline. clearScreen paints every row of the terminal, so a stray newline at
// the bottom scrolls the whole screen and leaves an unpainted row behind.
func renderProfileEditor(form profileForm, fields []profileField, dirty bool) {
	clearScreen()
	width := terminalWidth()
	height := terminalHeight()

	lines := []string{
		amber("Edit Profile"),
		muted(strings.Repeat("─", max(20, width))),
		"",
	}
	for i, field := range fields {
		lines = append(lines, fmt.Sprintf("  %2d  %-24s %s", i+1, field.label, field.display(form)))
	}
	lines = append(lines, "")
	if dirty {
		lines = append(lines, "  unsaved changes")
	} else {
		lines = append(lines, muted("  no unsaved changes"))
	}
	lines = append(lines, muted("  number = edit that field, s = save, q = cancel"))

	for i, line := range lines {
		row := i + 1
		if row >= height {
			break
		}
		fmt.Printf("\033[%d;1H%s%s", row, tuiBg+tuiText, line)
	}
}

// profilePrompt reads a line on the bottom row, in place. A read error means
// stdin closed (Ctrl-D); callers must stop rather than re-prompt, or the editor
// spins forever on EOF.
func profilePrompt(label string) (string, error) {
	return profilePromptInitial(label, "")
}

// profilePromptInitial pre-fills the line with the field's current value, so
// editing does not silently wipe it.
func profilePromptInitial(label, initial string) (string, error) {
	width := terminalWidth()
	height := terminalHeight()
	fmt.Printf("\033[%d;1H%s%s", height, tuiBg+tuiText, strings.Repeat(" ", width))
	fmt.Printf("\033[%d;1H", height)
	line, err := readEditableLineInitial(label, initial)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(strings.TrimRight(line, "\r\n")), nil
}

// profileNotice shows a message on the bottom row and waits for Enter.
func profileNotice(message string) {
	width := terminalWidth()
	height := terminalHeight()
	fmt.Printf("\033[%d;1H%s%s", height, tuiBg+tuiText, strings.Repeat(" ", width))
	fmt.Printf("\033[%d;1H%s%s", height, tuiBg+tuiText, cleanInline(message)+muted("  [Enter]"))
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
}

// cmdProfileEdit is the interactive profile editor. It reads the current
// profile, lets the user change fields one at a time, and only writes when the
// user saves.
func cmdProfileEdit(api APIClient) error {
	form, err := fetchProfileForm(api)
	if err != nil {
		return err
	}

	original := form
	fields := profileFields()
	defer func() {
		fmt.Printf("\033[%d;1H\033[0m\n", terminalHeight())
	}()

	for {
		renderProfileEditor(form, fields, form != original)

		raw, promptErr := profilePrompt("choice: ")
		if promptErr != nil {
			// stdin closed; leave the profile untouched rather than loop.
			return nil
		}

		switch choice := strings.ToLower(raw); choice {
		case "q", "quit":
			if form != original {
				answer, err := profilePrompt("discard unsaved changes? [y/N]: ")
				if err != nil {
					return nil
				}
				if a := strings.ToLower(answer); a != "y" && a != "yes" {
					continue
				}
			}
			return nil
		case "s", "save":
			if form == original {
				profileNotice("No changes to save.")
				continue
			}
			out, saveErr := saveProfileForm(api, form)
			if saveErr != nil {
				profileNotice(saveErr.Error())
				continue
			}
			original = form
			profileNotice(stringify(out["message"]))
			return nil
		case "":
			continue
		default:
			n, convErr := strconv.Atoi(choice)
			if convErr != nil || n < 1 || n > len(fields) {
				continue
			}
			if editErr := fields[n-1].edit(&form); editErr != nil {
				if errors.Is(editErr, io.EOF) {
					return nil
				}
				profileNotice(editErr.Error())
			}
		}
	}
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
	if api.onRequest != nil {
		api.onRequest(method, path)
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

func looksLikeHTML(s string) bool {
	head := strings.ToLower(strings.TrimSpace(s))
	if len(head) > 512 {
		head = head[:512]
	}
	return strings.HasPrefix(head, "<!doctype html") || strings.HasPrefix(head, "<html") || strings.Contains(head, "<head>")
}

func htmlTitle(s string) string {
	lower := strings.ToLower(s)
	start := strings.Index(lower, "<title>")
	if start < 0 {
		return ""
	}
	start += len("<title>")
	end := strings.Index(lower[start:], "</title>")
	if end < 0 {
		return ""
	}
	return strings.TrimSpace(cleanInline(s[start : start+end]))
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

	// Some 403s never reach the app: an nginx-level block (firewall, WAF, rate
	// limit) answers with an HTML page. Dumping that markup into the terminal
	// is useless, so report the page's title instead.
	if looksLikeHTML(s) {
		if title := htmlTitle(s); title != "" {
			return title
		}
		return "blocked before reaching RootBadger (HTML response)"
	}

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
	return readEditableLineInitial(label, "")
}

// readEditableLineInitial is readEditableLine with the buffer pre-filled, so
// editing an existing value starts from that value instead of an empty line.
func readEditableLineInitial(label, initial string) (string, error) {
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

	fmt.Print("\x1b[?25h")
	reader := bufio.NewReader(os.Stdin)
	buffer := []rune(initial)
	cursor := len(buffer)
	redraw := func() {
		fmt.Print("\r\033[2K")
		fmt.Print(label + string(buffer))
		if move := len(buffer) - cursor; move > 0 {
			fmt.Printf("\033[%dD", move)
		}
	}

	redraw()
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
	return editorBody(path, intro, "", 0)
}

func bodyFromFlagOrEditorWithInitial(path, intro, initial string) (string, error) {
	return editorBody(path, intro, initial, 0)
}

// composeAndSend runs the edit-confirm-send cycle for the TUI so a failed send
// never loses the draft. On send failure it shows the error and reopens the
// editor pre-filled with what the user wrote, letting them fix and retry or
// cancel. send returns the success status text.
func composeAndSend(s *TUIState, intro, initial string, send func(body string) (string, error)) error {
	draft := initial
	for {
		var body string
		editErr := withNormalTerminal(func() error {
			var e error
			body, e = composeBodyWithInitial("", intro, draft, s.api.wrapColumns())
			return e
		})
		if editErr != nil {
			// Empty body cancels; anything else is a real editor failure.
			if strings.Contains(editErr.Error(), "empty body") {
				s.status = "cancelled; nothing sent"
				return nil
			}
			return editErr
		}
		draft = body
		if strings.TrimSpace(body) == "" {
			s.status = "cancelled; nothing sent"
			return nil
		}
		if !confirmDefaultYes("Send? Y/n: ") {
			s.status = "held as draft; not sent"
			return nil
		}

		status, sendErr := send(body)
		if sendErr == nil {
			s.status = status
			return nil
		}

		// Show why it failed on a clean screen and keep the draft, so the user
		// reads the reason and can fix it instead of losing what they wrote.
		retry := false
		_ = withNormalTerminal(func() error {
			clearScreen()
			fmt.Print(tuiBg + tuiText)
			printTableHeader("Could not send", terminalWidth())
			fmt.Println()
			for _, line := range wrapLines(sendErr.Error(), terminalWidth()-2) {
				fmt.Println("  " + line)
			}
			fmt.Println()
			fmt.Println(muted("  Your draft is kept. Edit it and try again, or cancel to keep it aside."))
			fmt.Print("\033[0m")
			retry = confirmDefaultYes("\nEdit and retry? Y/n: ")
			return nil
		})
		if !retry {
			s.status = "not sent: " + cleanInline(sendErr.Error())
			return nil
		}
	}
}

// composeBody is the entry point for posts, replies and messages: the same
// editor flow, but hard-wrapped at wrapCols.
func composeBody(path, intro string, wrapCols int) (string, error) {
	return editorBody(path, intro, "", wrapCols)
}

func composeBodyWithInitial(path, intro, initial string, wrapCols int) (string, error) {
	return editorBody(path, intro, initial, wrapCols)
}

func editorBody(path, intro, initial string, wrapCols int) (string, error) {
	if path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		return wrapBody(string(b), wrapCols), nil
	}
	tmp, err := os.CreateTemp("", "badgerclaw-*.md")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())

	header := "<!-- " + intro + "Lines inside this comment are ignored. -->\n"
	if wrapCols > 0 {
		// Vim and Neovim read this modeline and wrap while you type. Other
		// editors ignore it; wrapBody still wraps their text on save.
		header += fmt.Sprintf("<!-- vim: set textwidth=%d formatoptions+=t: -->\n", wrapCols)
	}
	if _, err := tmp.WriteString(header + "\n"); err != nil {
		return "", err
	}
	if strings.TrimSpace(initial) != "" {
		if _, err := tmp.WriteString(strings.TrimRight(initial, "\n") + "\n\n"); err != nil {
			return "", err
		}
	}
	_ = tmp.Close()

	editor := preferredEditor()
	c := editorCommand(editor, editorWrapArgs(editor, wrapCols), tmp.Name())
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
	return wrapBody(body, wrapCols), nil
}

// Composed text is hard-wrapped at this column unless the config or
// BADGERCLAW_WRAP says otherwise. Zero or less turns wrapping off.
const defaultWrapColumns = 80

func resolveWrapColumns(cfg Config) int {
	if env := strings.TrimSpace(os.Getenv("BADGERCLAW_WRAP")); env != "" {
		if n, err := strconv.Atoi(env); err == nil {
			return n
		}
	}
	if cfg.WrapColumns != nil {
		return *cfg.WrapColumns
	}
	return defaultWrapColumns
}

// wrapOverride lets --wrap N beat the configured width. -1 means "not given".
func wrapOverride(api APIClient, flagValue int) int {
	if flagValue >= 0 {
		return flagValue
	}
	return api.wrapColumns()
}

func (api APIClient) wrapColumns() int {
	return resolveWrapColumns(api.cfg)
}

var (
	nanoWrapOnce sync.Once
	nanoWrapOK   bool
)

// Old nano builds reject --breaklonglines, and an unknown flag would stop the
// editor from opening at all, so ask nano what it supports before using it.
func nanoSupportsHardWrap() bool {
	nanoWrapOnce.Do(func() {
		out, err := exec.Command("nano", "--help").CombinedOutput()
		if err != nil {
			return
		}
		help := string(out)
		nanoWrapOK = strings.Contains(help, "--breaklonglines") && strings.Contains(help, "--fill")
	})
	return nanoWrapOK
}

// editorWrapArgs asks the editor to hard-wrap while the user types. Editors we
// do not recognise get nothing here; their text is still wrapped on save by
// wrapBody, so the posted result is the same either way.
func editorWrapArgs(editor string, cols int) []string {
	if cols <= 0 {
		return nil
	}
	fields := strings.Fields(editor)
	if len(fields) == 0 {
		return nil
	}

	switch filepath.Base(fields[0]) {
	case "vim", "nvim", "vi", "vimx", "gvim", "mvim":
		return []string{"-c", fmt.Sprintf("setlocal textwidth=%d formatoptions+=t", cols)}
	case "emacs", "emacsclient":
		return []string{"--eval", fmt.Sprintf("(progn (setq-default fill-column %d) (add-hook 'find-file-hook 'turn-on-auto-fill))", cols)}
	case "nano":
		if nanoSupportsHardWrap() {
			return []string{fmt.Sprintf("--fill=%d", cols), "--breaklonglines"}
		}
	}
	return nil
}

func listMarker(s string) string {
	trimmed := strings.TrimLeft(s, " \t")
	if trimmed == "" {
		return ""
	}

	// "- ", "* ", "+ "
	if len(trimmed) > 1 && strings.ContainsRune("-*+", rune(trimmed[0])) && (trimmed[1] == ' ' || trimmed[1] == '\t') {
		return trimmed[:2]
	}

	// "1. ", "2) "
	i := 0
	for i < len(trimmed) && trimmed[i] >= '0' && trimmed[i] <= '9' {
		i++
	}
	if i > 0 && i+1 < len(trimmed) && (trimmed[i] == '.' || trimmed[i] == ')') && trimmed[i+1] == ' ' {
		return trimmed[:i+2]
	}
	return ""
}

// wrapLine hard-wraps one line, keeping any quote prefix ("> ") on every
// continuation line and indenting continuations of a list item under its text.
// Words longer than the limit (URLs, mostly) are never broken.
func wrapLine(line string, cols int) []string {
	indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
	rest := line[len(indent):]

	quote := ""
	for strings.HasPrefix(rest, ">") {
		quote += ">"
		rest = rest[1:]
		for strings.HasPrefix(rest, " ") {
			quote += " "
			rest = rest[1:]
		}
	}

	prefix := indent + quote
	continuation := prefix
	if marker := listMarker(rest); marker != "" {
		continuation = prefix + strings.Repeat(" ", len([]rune(marker)))
	}

	words := strings.Fields(rest)
	if len(words) == 0 {
		return []string{line}
	}

	var (
		out        []string
		current    = prefix
		hasContent bool
	)
	for _, word := range words {
		if !hasContent {
			current += word
			hasContent = true
			continue
		}
		if len([]rune(current))+1+len([]rune(word)) <= cols {
			current += " " + word
			continue
		}
		out = append(out, current)
		current = continuation + word
	}
	return append(out, current)
}

// wrapBody hard-wraps prose at cols. Code fences, indented code, tables and
// headings are copied through untouched so wrapping cannot corrupt them.
func wrapBody(text string, cols int) string {
	if cols <= 0 || strings.TrimSpace(text) == "" {
		return text
	}

	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	out := make([]string, 0, len(lines))
	inFence := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
			out = append(out, line)
			continue
		}

		switch {
		case inFence,
			trimmed == "",
			strings.HasPrefix(line, "    "),
			strings.HasPrefix(line, "\t"),
			strings.HasPrefix(trimmed, "|"),
			strings.HasPrefix(trimmed, "#"),
			len([]rune(line)) <= cols:
			out = append(out, line)
		default:
			out = append(out, wrapLine(line, cols)...)
		}
	}
	return strings.Join(out, "\n")
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
	return wrapNewsreaderText(quote, 80), nil
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

func editorCommand(editor string, args []string, path string) *exec.Cmd {
	if strings.ContainsAny(editor, " \t") {
		parts := []string{editor}
		for _, arg := range args {
			parts = append(parts, shellQuote(arg))
		}
		parts = append(parts, shellQuote(path))
		return exec.Command("sh", "-c", strings.Join(parts, " "))
	}
	return exec.Command(editor, append(append([]string{}, args...), path)...)
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

func asBool(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return t == "1" || strings.EqualFold(t, "true") || strings.EqualFold(t, "yes")
	case float64:
		return t != 0
	}
	return false
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

// formatTimestamp renders an ISO-8601 timestamp as a short local time, with a
// relative form for recent moments. Unparseable input is returned as-is.
func formatTimestamp(iso string) string {
	iso = strings.TrimSpace(iso)
	if iso == "" || iso == "<nil>" {
		return ""
	}
	t, err := time.Parse(time.RFC3339, iso)
	if err != nil {
		return iso
	}
	t = t.Local()
	age := time.Since(t)
	switch {
	case age < time.Minute:
		return "just now"
	case age < time.Hour:
		return fmt.Sprintf("%dm ago", int(age.Minutes()))
	case age < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(age.Hours()))
	case age < 7*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(age.Hours()/24))
	}
	return t.Format("2006-01-02 15:04")
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
	return tuiOrange + s + tuiText
}

func cyan(s string) string {
	return tuiLink + s + tuiText
}

func muted(s string) string {
	return tuiMuted + s + tuiText
}

func statusBar(text string, width int) string {
	text = cleanInline(text)
	if text == "" {
		text = "↑/↓ select  Enter open  Q quit"
	}
	return tuiStatusBg + tuiText + colorizeStatusCommands(fit(text, width)) + tuiBg + tuiText
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

// progress paints the status bar row immediately, so an action shows its
// activity during the blocking request that follows, before the next redraw.
func (s *TUIState) progress(msg string) {
	if msg == "" {
		return
	}
	s.status = msg
	fmt.Printf("\033[%d;1H%s", terminalHeight(), statusBar(cleanInline(msg), terminalWidth()))
}

// requestLabel turns a request into a short status message.
func requestLabel(method, path string) string {
	host := "rootbadger"
	switch {
	case strings.Contains(path, "/subscribe"):
		if method == "DELETE" {
			return "Unsubscribing on " + host + "\u2026"
		}
		return "Subscribing on " + host + "\u2026"
	case strings.Contains(path, "/subscriptions"):
		return "Fetching subscriptions\u2026"
	case strings.Contains(path, "/threads/"):
		return "Fetching thread\u2026"
	case strings.Contains(path, "/threads"):
		return "Fetching articles\u2026"
	case strings.Contains(path, "/read"):
		return "Marking read\u2026"
	case strings.Contains(path, "/posts") && method == "POST":
		return "Posting\u2026"
	case strings.Contains(path, "/replies") && method == "POST":
		return "Sending reply\u2026"
	case strings.Contains(path, "/conversations"):
		return "Fetching messages\u2026"
	case strings.Contains(path, "/messages"):
		if method == "POST" {
			return "Sending message\u2026"
		}
		return "Fetching messages\u2026"
	case strings.Contains(path, "/notifications"):
		return "Fetching notifications\u2026"
	case strings.Contains(path, "/search"):
		return "Searching\u2026"
	case strings.Contains(path, "/profile"):
		if method == "GET" {
			return "Fetching profile\u2026"
		}
		return "Saving profile\u2026"
	case strings.Contains(path, "/groups"):
		return "Fetching groups\u2026"
	case strings.Contains(path, "/home"):
		return "Fetching home feed\u2026"
	}
	switch method {
	case "GET":
		return "Loading\u2026"
	default:
		return "Working\u2026"
	}
}

func runTUI(api APIClient) error {
	state := &TUIState{api: api, screen: "subs", title: "Subscribed Groups", selected: 0}
	state.api.onRequest = func(method, path string) {
		state.progress(requestLabel(method, path))
	}
	if api.cfg.Token == "" {
		state.status = "not logged in; run badgerclaw login first"
	} else if err := state.loadSubscriptions(); err != nil {
		state.status = err.Error()
	}
	return withCbreakTerminal(func() error {
		// Redraw on terminal resize. The mutex is held everywhere except while
		// blocked waiting for a key, which is the only time the resize
		// goroutine may paint: state is quiescent there.
		var mu sync.Mutex
		winch := make(chan os.Signal, 1)
		signal.Notify(winch, syscall.SIGWINCH)
		defer signal.Stop(winch)
		go func() {
			for range winch {
				mu.Lock()
				tuiNeedsFullClear = true
				state.draw()
				mu.Unlock()
			}
		}()

		mu.Lock()
		defer mu.Unlock()
		for {
			state.draw()
			mu.Unlock()
			key, err := readKey()
			mu.Lock()
			if err != nil {
				if errors.Is(err, io.EOF) {
					clearScreen()
					fmt.Print("\x1b[?25h\x1b[0m")
					return nil
				}
				return err
			}
			if state.confirmQuit {
				switch key {
				case "open", "y", "Y":
					clearScreen()
					fmt.Print("\x1b[?25h\x1b[0m")
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
	api              APIClient
	screen           string
	status           string
	items            []map[string]any
	title            string
	current          string
	selected         int
	listScroll       int
	lastListHeight   int
	conversationID   string
	conversationUser string
	showHeaders      bool
	showAllSubs      bool
	confirmQuit      bool

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

// tuiNeedsFullClear requests one real screen clear on the next frame: set on
// entry, after an external program (editor, pager) has drawn, and on resize.
// Steady-state frames never clear the screen; they overwrite it in place.
var tuiNeedsFullClear = true

// draw renders one full frame into a buffer and writes it with a single
// syscall: cursor home (no clear), every line ends with clear-to-EOL, unused
// rows are blanked, and the frame is wrapped in synchronized-output markers
// with the cursor hidden. This is what stops the terminal from flickering.
func (s *TUIState) draw() {
	width := terminalWidth()
	height := terminalHeight()

	var w strings.Builder
	w.Grow(32 << 10)
	location := s.renderBody(&w, width, height)

	// Clear to end-of-line at every line break so leftovers from the previous
	// frame never show through.
	content := strings.ReplaceAll(w.String(), "\n", "\x1b[K\n")
	used := strings.Count(content, "\n")

	var f strings.Builder
	f.Grow(len(content) + 1024)
	f.WriteString("\x1b[?2026h\x1b[?25l\x1b[H")
	if tuiNeedsFullClear {
		f.WriteString("\x1b[2J")
		tuiNeedsFullClear = false
	}
	f.WriteString(tuiBg + tuiText)
	f.WriteString(content)
	for row := used; row < height-1; row++ {
		f.WriteString("\x1b[K\n")
	}
	f.WriteString(fmt.Sprintf("\x1b[%d;1H%s\x1b[0m\x1b[?2026l", height, statusBar(s.statusText(location), width)))
	os.Stdout.WriteString(f.String())
}

// renderBody writes the frame's content lines (everything above the status
// bar) and reports the location shown in it.
func (s *TUIState) renderBody(w *strings.Builder, width, height int) string {
	if s.screen == "article" {
		s.drawArticle(w, width, height)
		return "article"
	}
	user := firstNonEmpty(s.api.cfg.User, "not logged in")
	fmt.Fprintf(w, "%s  %s  %s\n", amber(appName), muted(s.api.cfg.BaseURL), cyan(user))
	usedRows := 1
	location := "home"
	if s.current != "" {
		location = s.current
	}
	fmt.Fprintln(w, muted(strings.Repeat("─", width)))
	usedRows++
	if s.status != "" && !s.statusBarOnly() {
		fmt.Fprintln(w, cyan(s.status))
		usedRows++
	}
	if s.title != "" {
		fmt.Fprintln(w, "\033[1m"+cleanInline(s.title)+"\033[22m")
		usedRows++
	}
	if len(s.items) == 0 {
		fmt.Fprintln(w, muted("No items to show. Press g to refresh new subscribed articles, G for the group tree, or s to search."))
		usedRows++
	}
	listHeight := max(1, height-usedRows-2)
	s.lastListHeight = listHeight
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
			fmt.Fprintf(w, "%s%s %s  %s%s  %s  %s\n",
				prefix,
				cyan(fit(unread, 4)),
				expandMarker,
				muted(connector),
				cyan(fit(cleanInline(groupPath(item)), pathW)),
				fit(cleanInline(stringify(item["name"])), nameW),
				muted(fmt.Sprintf("posts:%v", item["post_count"])),
			)
		case "messages":
			other := asMap(item["other_user"])
			name := firstNonEmpty(cleanInline(stringify(other["display_name"])), cleanInline(stringify(other["username"])), "unknown")
			marker := " "
			if unreadCount(item) > 0 {
				marker = cyan("*")
			}
			last := formatTimestamp(stringify(item["last_message_at"]))
			fmt.Fprintf(w, "%s%s %s  %s  %s\n",
				prefix,
				marker,
				cyan(fit(name, 28)),
				muted(fit(fmt.Sprintf("unread:%v", item["unread_count"]), 12)),
				muted(fit(last, max(12, width-50))),
			)
		case "message-thread":
			sender := asMap(item["sender"])
			name := firstNonEmpty(cleanInline(stringify(sender["display_name"])), cleanInline(stringify(sender["username"])), "unknown")
			when := formatTimestamp(stringify(item["created_at"]))
			body := cleanInline(stringify(item["body"]))
			nameCol := cyan(fit(name, 18))
			if truthy(item["is_mine"]) {
				nameCol = muted(fit("me", 18))
			}
			fmt.Fprintf(w, "%s%s  %s  %s\n",
				prefix,
				muted(fit(when, 16)),
				nameCol,
				fit(body, max(20, width-42)),
			)
		case "notifications":
			marker := cyan("*")
			if truthy(item["is_read"]) {
				marker = " "
			}
			typeW := 14
			title := cleanInline(stringify(item["title"]))
			body := cleanInline(stringify(item["body"]))
			line := title
			if body != "" {
				line += muted("  " + body)
			}
			fmt.Fprintf(w, "%s%s %s  %s\n",
				prefix,
				marker,
				muted(fit("["+stringify(item["type"])+"]", typeW)),
				fit(line, max(20, width-typeW-6)),
			)
		case "search-users":
			fmt.Fprintf(w, "%s%3d  %s  %s\n",
				prefix,
				i+1,
				cyan(fit(cleanInline(firstNonEmpty(stringify(item["display_name"]), stringify(item["username"]))), 32)),
				muted(fit("@"+cleanInline(stringify(item["username"])), max(16, width-42))),
			)
		case "search-tags":
			fmt.Fprintf(w, "%s%3d  %s  %s\n",
				prefix,
				i+1,
				cyan(fit("#"+strings.TrimPrefix(cleanInline(stringify(item["name"])), "#"), 36)),
				muted(fit(fmt.Sprintf("posts:%v", item["post_count"]), max(12, width-44))),
			)
		default:
			author := asMap(item["author"])
			group := asMap(item["group"])
			unread := " "
			if truthy(item["is_unread"]) || hasUnreadChild(item) {
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
			// The id column is padded to a fixed width; otherwise short and
			// long ids shift the subject start and the author column with it.
			idW := 8
			subjectW := max(18, width-12-idW-len([]rune(connector))-metaW)
			fmt.Fprintf(w, "%s%s%s %s  %s%s %s %s\n",
				prefix,
				unread,
				fit(replyCount, 3),
				expandMarker,
				muted(connector),
				muted(fit(fmt.Sprintf("[%v]", item["id"]), idW)),
				amber(fit(cleanInline(stringify(item["subject"])), subjectW)),
				muted(fit(meta, metaW)),
			)
		}
	}
	fmt.Fprintln(w, muted(strings.Repeat("─", width)))
	return location
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
		if s.screen == "notifications" {
			return s.clearNotifications()
		}
		return s.markCurrentGroupRead()
	case "d":
		if s.screen == "notifications" {
			return s.dismissSelectedNotification()
		}
	case "r":
		if s.screen == "messages" || s.screen == "message-thread" {
			return s.replyToConversation()
		}
		id := s.promptInline("reply to post id: ")
		clearScreen()
		return composeAndSend(s, "Write your reply. Save and close to review.\n", "", func(body string) (string, error) {
			var out map[string]any
			if err := s.api.post("/api/v1/app/posts/"+id+"/replies", map[string]any{"body": body}, &out); err != nil {
				return "", err
			}
			return stringify(out["message"]), nil
		})
	case "P":
		return s.profileEditFlow()
	case "m":
		return s.loadConversations()
	case "!":
		return s.loadNotifications()
	case "up", "k":
		if s.selected > 0 {
			s.selected--
		}
	case "down", "j":
		if s.selected < len(s.items)-1 {
			s.selected++
		}
	case "pgup":
		s.selected = max(0, s.selected-max(1, s.lastListHeight))
	case "pgdn":
		s.selected = min(max(0, len(s.items)-1), s.selected+max(1, s.lastListHeight))
	case "home-key":
		s.selected = 0
	case "end-key":
		s.selected = max(0, len(s.items)-1)
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
	if s.screen == "message-thread" {
		return s.loadConversations()
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
	case "messages":
		return "\u2191/\u2193 select  Enter open  r reply  \u2190 back  q quit"
	case "message-thread":
		return "\u2191/\u2193 select  Enter full message  r reply  \u2190 back"
	case "notifications":
		return "\u2191/\u2193 select  Enter open post  d dismiss  c clear all  \u2190 back"
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

func (s *TUIState) profileEditFlow() error {
	if err := withNormalTerminal(func() error { return cmdProfileEdit(s.api) }); err != nil {
		return err
	}
	s.status = "profile editor closed"
	return nil
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
	if err := composeAndSend(s, "Write your post. Save and close to review.\n", "", func(body string) (string, error) {
		var out map[string]any
		payload := map[string]any{"subject": subject, "body": body}
		if crosspost != "" {
			payload["crosspost_groups"] = crosspost
		}
		if err := s.api.post("/api/v1/app/groups/"+url.PathEscape(group)+"/posts", payload, &out); err != nil {
			return "", err
		}
		return stringify(out["message"]), nil
	}); err != nil {
		return err
	}
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
	if s.screen == "messages" {
		return s.openConversation(item)
	}
	if s.screen == "message-thread" {
		s.showMessagePager(item)
		return nil
	}
	if s.screen == "notifications" {
		postID := stringify(item["post_id"])
		if postID == "" || postID == "<nil>" {
			s.status = "this notification has no linked post"
			return nil
		}
		// Opening the post also marks this notification read on the server.
		_ = s.api.post("/api/v1/app/notifications/delete", map[string]any{"id": stringify(item["id"])}, nil)
		return s.openArticle(postID)
	}
	return s.openArticle(fmt.Sprint(item["id"]))
}

func (s *TUIState) loadConversations() error {
	var out map[string]any
	if err := s.api.get("/api/v1/app/conversations", url.Values{"per_page": {"50"}}, &out); err != nil {
		return err
	}
	s.screen = "messages"
	s.current = ""
	s.conversationID = ""
	s.conversationUser = ""
	s.title = "Messages"
	s.items = mapsFromSlice(asSlice(out["data"]))
	s.selected = 0
	s.listScroll = 0
	if len(s.items) == 0 {
		s.status = "no conversations; use `badgerclaw send USER` to start one"
	} else {
		s.status = "Enter open conversation  r reply"
	}
	return nil
}

func (s *TUIState) openConversation(item map[string]any) error {
	id := stringify(item["id"])
	if id == "" || id == "<nil>" {
		return errors.New("no conversation selected")
	}
	var out map[string]any
	if err := s.api.get("/api/v1/app/conversations/"+url.PathEscape(id), nil, &out); err != nil {
		return err
	}
	data := asMap(out["data"])
	other := asMap(data["other_user"])
	name := firstNonEmpty(cleanInline(stringify(other["display_name"])), cleanInline(stringify(other["username"])), "conversation")
	s.screen = "message-thread"
	s.conversationID = id
	s.conversationUser = cleanInline(stringify(other["username"]))
	s.title = "Messages with " + name
	s.items = mapsFromSlice(asSlice(data["messages"]))
	s.selected = max(0, len(s.items)-1)
	s.listScroll = 0
	s.status = "Enter read full message  r reply  \u2190 back"
	return nil
}

// showMessagePager prints one full message and waits, for bodies that do not
// fit on a list row.
func (s *TUIState) showMessagePager(item map[string]any) {
	_ = withNormalTerminal(func() error {
		clearScreen()
		fmt.Print(tuiBg + tuiText)
		defer fmt.Print("\033[0m")
		width := terminalWidth()
		sender := asMap(item["sender"])
		name := firstNonEmpty(cleanInline(stringify(sender["display_name"])), cleanInline(stringify(sender["username"])), "unknown")
		printTableHeader("Message from "+name+"  "+formatTimestamp(stringify(item["created_at"])), width)
		fmt.Println()
		for _, line := range displayBlockLines(stringify(item["body"]), max(40, width-2)) {
			fmt.Println(line)
		}
		pause()
		return nil
	})
}

// replyToConversation composes a message to the other participant.
func (s *TUIState) replyToConversation() error {
	user := s.conversationUser
	if user == "" && s.screen == "messages" && s.selected >= 0 && s.selected < len(s.items) {
		user = cleanInline(stringify(asMap(s.items[s.selected]["other_user"])["username"]))
	}
	if user == "" {
		return errors.New("no conversation selected")
	}
	clearScreen()
	var body string
	err := withNormalTerminal(func() error {
		var bodyErr error
		body, bodyErr = composeBody("", "Write your message to "+user+". Save and close to send.\n", s.api.wrapColumns())
		return bodyErr
	})
	if err != nil {
		return err
	}
	if strings.TrimSpace(body) == "" {
		return errors.New("message body was empty")
	}
	if !confirmDefaultYes("Send to " + user + "? Y/n: ") {
		s.status = "message cancelled"
		return nil
	}
	var out map[string]any
	if err := s.api.post("/api/v1/app/messages/"+url.PathEscape(user), map[string]any{"body": body}, &out); err != nil {
		return err
	}
	s.status = firstNonEmpty(cleanInline(stringify(out["message"])), "message sent")
	if s.conversationID != "" {
		return s.openConversation(map[string]any{"id": s.conversationID})
	}
	return s.loadConversations()
}

func (s *TUIState) loadNotifications() error {
	var out map[string]any
	if err := s.api.get("/api/v1/app/notifications", url.Values{"per_page": {"50"}}, &out); err != nil {
		return err
	}
	data := asMap(out["data"])
	s.screen = "notifications"
	s.current = ""
	s.title = fmt.Sprintf("Notifications  notices:%v messages:%v", data["unread_count"], data["unread_message_count"])
	s.items = mapsFromSlice(asSlice(data["items"]))
	s.selected = 0
	s.listScroll = 0
	if len(s.items) == 0 {
		s.status = "no notifications"
	} else {
		s.status = "Enter open  d dismiss  c clear all"
	}
	return nil
}

// clearNotifications marks every notification read on the server.
func (s *TUIState) clearNotifications() error {
	if err := s.api.post("/api/v1/app/notifications/clear", nil, nil); err != nil {
		return err
	}
	s.status = "all notifications cleared"
	return s.loadNotifications()
}

// dismissSelectedNotification marks the selected notification read and drops it.
func (s *TUIState) dismissSelectedNotification() error {
	if s.selected < 0 || s.selected >= len(s.items) {
		return nil
	}
	id := stringify(s.items[s.selected]["id"])
	if id == "" || id == "<nil>" {
		return nil
	}
	if err := s.api.post("/api/v1/app/notifications/delete", map[string]any{"id": id}, nil); err != nil {
		return err
	}
	s.items = append(s.items[:s.selected], s.items[s.selected+1:]...)
	if s.selected >= len(s.items) {
		s.selected = max(0, len(s.items)-1)
	}
	s.status = "notification dismissed"
	return nil
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

	// On unsubscribe, drop the group from the list right away so it does not
	// linger as a stale row until the next refresh.
	if !add && (s.screen == "subs" || s.screen == "groups") {
		s.items = removeGroupItem(s.items, group)
		if s.selected >= len(s.items) {
			s.selected = max(0, len(s.items)-1)
		}
		s.status = firstNonEmpty(cleanInline(stringify(out["message"])), "unsubscribed from "+group)
		return nil
	}

	s.status = firstNonEmpty(cleanInline(stringify(out["message"])), "subscription updated")
	return nil
}

// removeGroupItem drops the row for path, plus any of its expanded children.
func removeGroupItem(items []map[string]any, path string) []map[string]any {
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if groupPath(item) == path {
			continue
		}
		if stringify(item["_root_item_id"]) == path {
			continue
		}
		out = append(out, item)
	}
	return out
}

func (s *TUIState) openGroup(path string) error {
	var out map[string]any
	if err := s.api.get("/api/v1/app/groups/"+url.PathEscape(path)+"/threads", url.Values{"per_page": {"30"}, "sort": {"newest_threads"}}, &out); err != nil {
		return err
	}
	roots := mapsFromSlice(asSlice(out["data"]))
	items := make([]map[string]any, 0, len(roots))
	s.expandedThreads = make(map[string]bool)

	// Fetch every thread concurrently instead of one at a time: a busy group
	// has dozens of threads and serial round-trips made opening it take
	// seconds. Workers use a hook-free client copy so progress painting is not
	// interleaved from goroutines; order is preserved by index.
	s.progress(fmt.Sprintf("Fetching %d threads\u2026", len(roots)))
	quiet := s.api
	quiet.onRequest = nil
	threadOuts := make([]map[string]any, len(roots))
	sem := make(chan struct{}, 6)
	var wg sync.WaitGroup
	for i, root := range roots {
		rootID := stringify(root["id"])
		if rootID == "" {
			continue
		}
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			var threadOut map[string]any
			if err := quiet.get("/api/v1/app/threads/"+id, nil, &threadOut); err == nil {
				threadOuts[i] = threadOut
			}
		}(i, rootID)
	}
	wg.Wait()

	for i, root := range roots {
		rootID := stringify(root["id"])
		if rootID == "" {
			continue
		}
		threadOut := threadOuts[i]
		if threadOut == nil {
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

		// If a reply in this thread is unread, expand the thread inline so the
		// new post (marked with * in the list) is visible without a keypress.
		hasUnread := false
		for _, child := range children {
			if truthy(child["is_unread"]) {
				hasUnread = true
				break
			}
		}
		items = append(items, rootPost)
		if hasUnread {
			rootPost["_expanded"] = true
			for _, child := range children {
				child["_root_item_id"] = rootID
				items = append(items, child)
			}
			if s.expandedThreads == nil {
				s.expandedThreads = make(map[string]bool)
			}
			s.expandedThreads[rootID] = true
		}
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
		return s.markSelectedUseful()
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

// markSelectedUseful marks the selected post in the article view as useful.
func (s *TUIState) markSelectedUseful() error {
	if s.articleSelected < 0 || s.articleSelected >= len(s.articleNodes) {
		return errors.New("no post selected")
	}
	post := s.articleNodes[s.articleSelected].Post
	id := stringify(post["id"])
	if id == "" || id == "<nil>" {
		return errors.New("no post selected")
	}
	var out map[string]any
	if err := s.api.post("/api/v1/app/posts/"+url.PathEscape(id)+"/useful", nil, &out); err != nil {
		return err
	}
	if n, ok := asMap(out["data"])["useful_count"]; ok {
		post["useful_count"] = n
	}
	s.status = firstNonEmpty(cleanInline(stringify(out["message"])), "marked useful")
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
	if err := composeAndSend(s, "Write your followup. Save and close to review.\n", initial, func(body string) (string, error) {
		var out map[string]any
		if err := s.api.post("/api/v1/app/posts/"+id+"/replies", map[string]any{"body": body}, &out); err != nil {
			return "", err
		}
		return stringify(out["message"]), nil
	}); err != nil {
		return err
	}
	return s.loadArticle(s.articleRootID)
}

func (s *TUIState) drawArticle(w *strings.Builder, width, height int) {
	selected := map[string]any{}
	if s.articleSelected >= 0 && s.articleSelected < len(s.articleNodes) {
		selected = s.articleNodes[s.articleSelected].Post
	}
	title := cleanInline(firstNonEmpty(stringify(s.articleRoot["subject"]), stringify(selected["subject"]), "Article"))
	fmt.Fprintf(w, "%s  %s\n", amber(appName), cyan(fit(title, max(20, width-len(appName)-4))))
	fmt.Fprintln(w, muted(strings.Repeat("─", width)))

	topHeight := clamp(height/3, 7, 14)
	threadLines := s.threadPaneLines(width)
	start := 0
	if len(threadLines) > topHeight {
		start = clamp(s.articleSelected-(topHeight/2), 0, max(0, len(threadLines)-topHeight))
	}
	for i := 0; i < topHeight; i++ {
		idx := start + i
		if idx < len(threadLines) {
			fmt.Fprintln(w, threadLines[idx])
		} else {
			fmt.Fprintln(w)
		}
	}

	fmt.Fprintln(w, muted(strings.Repeat("─", width)))
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
			fmt.Fprintln(w, bodyLines[idx])
		} else {
			fmt.Fprintln(w)
		}
	}
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
		unread := " "
		if truthy(post["is_unread"]) {
			unread = cyan("*")
		}
		author := asMap(post["author"])
		name := firstNonEmpty(cleanInline(stringify(author["display_name"])), cleanInline(stringify(author["username"])), "unknown")
		subject := cleanInline(stringify(post["subject"]))
		byline := "by " + name
		bylineW := clamp(width/4, 14, 30)
		subjectW := max(10, width-len([]rune(connector))-bylineW-18)
		line := fmt.Sprintf("%s%s%s%s %s  %s",
			prefix,
			unread,
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
	for _, line := range displayBlockLines(postDisplayText(post), min(80, max(40, width-2))) {
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

	known := map[string]bool{
		"from": true, "date": true, "newsgroups": true, "subject": true,
		"organization": true, "message-id": true, "x-info": true,
		"user-agent": true, "lines": true, "x-system": true,
		"references": true, "followup-to": true,
	}
	for _, item := range ordered {
		if strings.TrimSpace(item.value) == "" {
			continue
		}
		lines = append(lines, item.label+": "+item.value)
	}
	// The author's custom profile headers arrive as extra keys; print them in
	// a stable order after the built-ins.
	extras := make([]string, 0, len(headers))
	for k := range headers {
		if !known[strings.ToLower(k)] {
			extras = append(extras, k)
		}
	}
	sort.Strings(extras)
	for _, k := range extras {
		if v := cleanInline(stringify(headers[k])); v != "" {
			lines = append(lines, k+": "+v)
		}
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
	width = clamp(width, 16, 80)
	var out []string
	for _, line := range strings.Split(s, "\n") {
		out = append(out, wrapNewsreaderLine(line, width)...)
	}
	return out
}

func wrapNewsreaderText(s string, width int) string {
	s = normalizeBlock(s)
	if s == "" {
		return ""
	}
	width = clamp(width, 16, 80)
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		out = append(out, wrapNewsreaderLine(line, width)...)
	}
	return strings.Join(out, "\n")
}

func wrapNewsreaderLine(line string, width int) []string {
	width = clamp(width, 16, 80)
	if line == "" {
		return []string{""}
	}

	prefix, content := quotePrefix(line)
	available := width - runeLen(prefix)
	if available < 12 {
		available = max(12, width)
		prefix = ""
		content = strings.TrimSpace(line)
	}

	wrapped := wrapWords(content, available)
	if len(wrapped) == 0 {
		return []string{strings.TrimRight(prefix, " ")}
	}

	out := make([]string, 0, len(wrapped))
	for _, part := range wrapped {
		out = append(out, prefix+part)
	}
	return out
}

func quotePrefix(line string) (string, string) {
	trimmedLeft := strings.TrimLeft(line, " \t")
	if !strings.HasPrefix(trimmedLeft, ">") {
		return "", strings.TrimRight(line, " \t")
	}

	depth := 0
	i := 0
	for i < len(trimmedLeft) {
		if trimmedLeft[i] != '>' {
			break
		}
		depth++
		i++
		for i < len(trimmedLeft) && (trimmedLeft[i] == ' ' || trimmedLeft[i] == '\t') {
			i++
		}
	}
	if depth <= 0 {
		return "", strings.TrimRight(line, " \t")
	}
	if depth > 5 {
		depth = 5
	}
	return strings.Repeat(">", depth) + " ", strings.TrimSpace(trimmedLeft[i:])
}

func wrapWords(s string, width int) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	width = max(1, width)

	words := strings.Fields(s)
	lines := make([]string, 0, max(1, len(s)/width))
	current := ""
	for _, word := range words {
		if current == "" {
			for runeLen(word) > width {
				lines = append(lines, takeRunes(word, width))
				word = dropRunes(word, width)
			}
			current = word
			continue
		}

		if runeLen(current)+1+runeLen(word) <= width {
			current += " " + word
			continue
		}

		lines = append(lines, current)
		current = ""
		for runeLen(word) > width {
			lines = append(lines, takeRunes(word, width))
			word = dropRunes(word, width)
		}
		current = word
	}
	if current != "" {
		lines = append(lines, current)
	}
	return lines
}

func runeLen(s string) int {
	return len([]rune(s))
}

func takeRunes(s string, n int) string {
	runes := []rune(s)
	if n >= len(runes) {
		return s
	}
	return string(runes[:n])
}

func dropRunes(s string, n int) string {
	runes := []rune(s)
	if n >= len(runes) {
		return ""
	}
	return string(runes[n:])
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
	fmt.Println("  P              edit your profile")
	fmt.Println("  m, messages    private messages (Enter open, r reply)")
	fmt.Println("  !, notices     notifications (Enter open, d dismiss, c clear all)")
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
	var b strings.Builder
	b.Grow((width + 1) * height)
	b.WriteString("\033[2J\033[H" + tuiBg)
	for row := 0; row < height; row++ {
		b.WriteString(blank)
		if row < height-1 {
			b.WriteByte('\n')
		}
	}
	b.WriteString("\033[H")
	fmt.Print(b.String())
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
	fmt.Print("\x1b[?25h")
	defer func() {
		// Whatever ran (editor, pager, prompt) drew over the frame; the next
		// draw must start from a clean screen.
		tuiNeedsFullClear = true
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
		// CSI sequences such as ESC [ 5 ~ (PgUp) or ESC [ 1 ; 5 B (modified
		// arrow). Remember the leading number so the ~-terminated keys resolve.
		digits := string(seq[0])
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
				switch digits {
				case "5":
					return "pgup", nil
				case "6":
					return "pgdn", nil
				case "1", "7":
					return "home-key", nil
				case "4", "8":
					return "end-key", nil
				}
				return "", nil
			}
			if seq[0] >= '0' && seq[0] <= '9' {
				digits += string(seq[0])
				continue
			}
			if seq[0] == ';' {
				// Modifier follows; the leading number no longer identifies the key.
				digits = ""
				continue
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

func hasUnreadChild(item map[string]any) bool {
	for _, child := range childItems(item) {
		if truthy(child["is_unread"]) {
			return true
		}
	}
	return false
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
