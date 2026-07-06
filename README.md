# BadgerClaw

BadgerClaw is a command-line and terminal interface for
[RootBadger](https://rootbadger.com), a structured discussion platform built
around `rb.*` groups, threaded replies, charters, public group history, search,
RSS, standing, moderation tools, and old-internet style topic organization.

RootBadger is inspired by classic threaded discussion systems, but it is its
own platform. Groups are arranged in a hierarchy so related discussions stay
near each other instead of disappearing into a flat feed.

BadgerClaw lets you use RootBadger from a terminal. It includes a full-screen
TUI for reading subscribed groups, opening threads, replying, posting, searching,
viewing headers, checking notifications, and using admin tools when your account
has permission.

BadgerClaw was created by Yodabytz.

## Features

- Full-screen terminal interface.
- Login and signup support.
- Subscribed group list with unread counts.
- Group browsing.
- Threaded article view with nested replies.
- Root article loading from the thread list.
- Reply and followup support using your preferred editor.
- Confirmation before sending posts and replies.
- Search by all supported RootBadger search modes.
- Message and notification viewing.
- Profile viewing and profile updates.
- Useful marks and voting commands.
- Group proposal support.
- Subscribe and unsubscribe commands.
- Admin section commands for users with admin permissions.
- Header display toggle.
- RootBadger CLI user agent.

## Requirements

- Go 1.24 or newer.
- A terminal that supports ANSI escape sequences.
- A RootBadger account for authenticated actions.

Linux, BSD, and macOS terminals should work. Windows terminals may work if they
support ANSI escape sequences and Go builds the required terminal packages.

## Install From Source

Clone the repository:

```sh
git clone git@github.com:yodabytz/BadgerClaw.git
cd BadgerClaw
```

Build:

```sh
go build -o badgerclaw .
```

Install for the current user:

```sh
mkdir -p ~/.local/bin
cp badgerclaw ~/.local/bin/
```

Make sure `~/.local/bin` is in your `PATH`.

Install system-wide:

```sh
sudo install -m 0755 badgerclaw /usr/bin/badgerclaw
```

Verify:

```sh
badgerclaw --version
```

## Configuration

BadgerClaw stores configuration in:

```text
~/.config/badgerclaw/config.json
```

The config file stores the RootBadger base URL, login token, and username. It is
written with private file permissions.

To use a different RootBadger server:

```sh
ROOTBADGER_URL=https://example.com badgerclaw
```

The default server is:

```text
https://rootbadger.com
```

## Terminal Interface

Run:

```sh
badgerclaw
```

or:

```sh
badgerclaw tui
```

The default screen shows subscribed groups with unread counts. Groups with no
new articles are hidden by default. Press `L` to toggle between unread groups
and all subscribed groups.

Common TUI keys:

```text
Up/Down      Move selection
Enter        Open group, expand thread, or open selected item
O            Open/read selected article directly
Left         Go back
Q            Back, or quit from the subscribed-groups screen
Y / Enter    Confirm yes when prompted
N            Confirm no when prompted
G            Refresh current view
L            Toggle unread-only/all subscribed groups
A            Show all groups
S            Search
P            Post to the selected/current group
C            Mark selected/current group read
F            Follow up while reading an article
T            Toggle full headers
```

Inside a group, root articles are listed newest first. Replies stay nested under
their thread so the hierarchy remains readable.

When you create a post or reply, BadgerClaw opens your preferred editor and asks
before sending.

Editor selection order:

```text
BADGERCLAW_EDITOR
VISUAL
EDITOR
nvim
vim
vi
nano
```

## Login

Interactive login:

```sh
badgerclaw login
```

Login with a username or email:

```sh
badgerclaw login --login USER_OR_EMAIL
```

Login and stay in command mode instead of entering the TUI:

```sh
badgerclaw login --login USER_OR_EMAIL --no-tui
```

Logout:

```sh
badgerclaw logout
```

## Signup

Create an account:

```sh
badgerclaw signup --username USER --email USER@example.com
```

Use an invite token:

```sh
badgerclaw signup --username USER --email USER@example.com --invite TOKEN
```

## Account Commands

Show your account:

```sh
badgerclaw me
```

View a profile:

```sh
badgerclaw profile USERNAME
```

Update profile fields:

```sh
badgerclaw profile-update --display-name "Display Name"
badgerclaw profile-update --bio-file bio.txt
badgerclaw profile-update --signature-file signature.txt
```

## Groups

List groups:

```sh
badgerclaw groups
```

Search group names:

```sh
badgerclaw groups --q linux
```

Show subscribed groups:

```sh
badgerclaw subscriptions
badgerclaw subs
```

Open a group:

```sh
badgerclaw group rb.comp.lang.python
```

Subscribe:

```sh
badgerclaw subscribe rb.comp.lang.python
```

Unsubscribe:

```sh
badgerclaw unsubscribe rb.comp.lang.python
```

## Reading Threads

Open a post or thread:

```sh
badgerclaw thread 123
badgerclaw post 123
```

Show headers:

```sh
badgerclaw headers 123
```

## Posting

Create a new post:

```sh
badgerclaw new rb.comp.lang.python --subject "Question about decorators"
```

Use a body file:

```sh
badgerclaw new rb.comp.lang.python --subject "Release notes" --body-file post.md
```

Crosspost:

```sh
badgerclaw new rb.comp.lang.python \
  --subject "Python tooling question" \
  --crosspost rb.comp.programs,rb.comp.rootbadger
```

Reply:

```sh
badgerclaw reply 123
```

Reply from a file:

```sh
badgerclaw reply 123 --body-file reply.md
```

BadgerClaw asks `Send? Y/n` before posting. Press Enter or `Y` to send, or `N`
to cancel.

## Search

General search:

```sh
badgerclaw search rootbadger
```

Search by type:

```sh
badgerclaw search --type all rootbadger
badgerclaw search --type posts linux
badgerclaw search --type groups rb.comp
badgerclaw search --type users yoda
badgerclaw search --type hashtags programming
badgerclaw search --type article 123
badgerclaw search --type message_id '<message-id@rootbadger.com>'
```

Supported search types:

```text
all
posts
groups
users
hashtags
article
message_id
```

Wildcard searches are passed to RootBadger where supported by the server-side
search API.

## Useful Marks And Voting

Mark a post useful:

```sh
badgerclaw useful 123
```

Remove a useful mark:

```sh
badgerclaw unuseful 123
```

Vote:

```sh
badgerclaw vote 123 --value 1
badgerclaw vote 123 --value -1
```

## Messages And Notifications

List messages:

```sh
badgerclaw messages
```

Send a private message:

```sh
badgerclaw send USERNAME
```

Send a private message from a file:

```sh
badgerclaw send USERNAME --body-file message.txt
```

Show notifications:

```sh
badgerclaw notifications
badgerclaw notices
```

## Proposing Groups

Propose a new group:

```sh
badgerclaw propose \
  --parent rb.comp \
  --slug example \
  --name "Example Group" \
  --charter-file charter.txt \
  --rationale-file rationale.txt \
  --moderation-file moderation.txt
```

RootBadger group proposals should include a strong charter, a clear rationale,
and a moderation plan when appropriate.

## Admin Commands

Admin commands use RootBadger API endpoints and require an account with admin
permissions.

Show admin overview:

```sh
badgerclaw admin
```

Open an admin section:

```sh
badgerclaw admin-section users
badgerclaw admin-section statistics
badgerclaw admin-section proposals
badgerclaw admin-section reports
badgerclaw admin-section images
badgerclaw admin-section private_groups
badgerclaw admin-section bans
badgerclaw admin-section logs
badgerclaw admin-section newsletters
badgerclaw admin-section webhooks
```

Open an admin detail record:

```sh
badgerclaw admin-detail proposals 12
badgerclaw admin-detail users 42
badgerclaw admin-detail reports 9
```

Run an admin action:

```sh
badgerclaw admin-action proposals 12 --action approve
badgerclaw admin-action proposals 12 --action reject --reason "Needs a clearer charter."
badgerclaw admin-action users 42 --action add-standing --points 10 --event-type moderator_review_bonus --reason "Helpful report."
```

Available actions depend on the server-side RootBadger admin API and the
permissions of the logged-in account.

## Doctor

Run a basic connectivity/configuration check:

```sh
badgerclaw doctor
```

## User Agent

BadgerClaw identifies itself as:

```text
RootBadger CLI
```

This lets RootBadger statistics separate CLI traffic from web and Android app
traffic.

## Development

Format:

```sh
gofmt -w main.go
```

Test:

```sh
go test ./...
```

Build:

```sh
go build -o badgerclaw .
```

Clean build artifacts:

```sh
rm -f badgerclaw
rm -rf dist
```

## License

Copyright Yodabytz. All rights reserved unless a separate license file is added.
