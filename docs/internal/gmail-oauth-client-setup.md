# Registering Mailfire's Google OAuth client

This guide is for Mailfire maintainers. It describes how to register and verify the Google OAuth client that the
Gmail provider uses, so users can run `mailfire auth login --provider gmail` without creating their own Google
Cloud project.

**Status (October 18, 2026):** the client exists (project `mailfire`, client `Mailfire CLI`) and is built in; the
app is in **Testing**, so only listed test users can sign in until Phase 4 is complete.

## Where the client ID and secret live

- The client ID is public and in the source: `gmail.BuiltinClientID` in `internal/gmail/auth.go`.
- The client secret is never committed. Builds set `gmail.builtinClientSecret` through `-ldflags -X`:
  - `make build` takes it from `GMAIL_CLIENT_SECRET`, or else from the client file downloaded from the Cloud console
    into `tmp/client_secret_*.json` (ignored by git).
  - Releases take it from the `GMAIL_CLIENT_SECRET` repository secret (Settings → Secrets and variables → Actions);
    the release workflow fails without it.
- A build without the secret refuses to sign in with the built-in client and says so;
  `MAILFIRE_GMAIL_CLIENT_SECRET` supplies it at run time.
- To rotate the secret: Clients → Mailfire CLI → Add secret, update the repository secret
  (`gh secret set GMAIL_CLIENT_SECRET`) and `tmp/`, release, then disable the old secret.

Mailfire requests `gmail.send` and `openid`. Google classifies `gmail.send` as a **sensitive** scope, not a
restricted one: the app needs Google's verification review, but not the paid third-party security assessment
(CASA) that mailbox-reading scopes require.

Checked against Google's documentation in October 2026:

- [OAuth 2.0 for Desktop Apps](https://developers.google.com/identity/protocols/oauth2/native-app)
- [Sensitive scope verification](https://developers.google.com/identity/protocols/oauth2/production-readiness/sensitive-scope-verification)
- [Choose Gmail API scopes](https://developers.google.com/workspace/gmail/api/auth/scopes)

## Phase 1: Prerequisites

These steps take the longest, so start with them.

1. **Get a domain you own** for Mailfire's home page and privacy policy. Google does not accept
   `github.com/thorstenkramm/mailfire`, because you must prove ownership of the domain. A domain you already own
   works, or GitHub Pages with a custom domain. A plain `thorstenkramm.github.io` address might be accepted, but
   that is unconfirmed, so a real domain is safer.
2. **Verify the domain** in [Google Search Console](https://search.google.com/search-console), using the same
   Google account that will own the Cloud project.
3. **Publish two public pages on that domain.** Done: the home page <https://mailfire.system42.io/> (section "How
   Mailfire uses your Gmail account") and the privacy policy <https://mailfire.system42.io/privacy/>
   (`docs/content/privacy.md`, linked from the home page and every page footer). Requirements:
   - **Home page:** what Mailfire is, what it does with Gmail, and a link to the privacy policy.
   - **Privacy policy.** It must state:
     - Mailfire only sends mail the user tells it to send.
     - Tokens are stored only on the user's own machine.
     - No data goes to the developer or anyone else.
     - The standard disclosure: *"Mailfire's use of information received from Google APIs will adhere to the
       [Google API Services User Data Policy](https://developers.google.com/terms/api-services-user-data-policy),
       including the Limited Use requirements."*

## Phase 2: Create the project and client

This takes about 15 minutes.

1. In the [Google Cloud console](https://console.cloud.google.com/), create a project, for example `mailfire`.
   Keep it for production only; Google recommends a separate project for testing.
2. In the [API Library](https://console.cloud.google.com/apis/library), find the **Gmail API** and click
   **Enable**.
3. **Google Auth Platform → Branding:**
   - App name `Mailfire`, support email, and logo.
   - Home page `https://mailfire.system42.io/` and privacy policy `https://mailfire.system42.io/privacy/`.
   - `system42.io` under authorized domains.
   - Developer contact email.
4. **Audience:** choose **External**. The app starts in **Testing**; add your own Gmail address as a test user.
5. **Data Access → Add or remove scopes:** add `https://www.googleapis.com/auth/gmail.send` (listed as
   sensitive) and `openid` (listed as non-sensitive). Add nothing else.
6. **Clients → Create client:**
   - Application type **Desktop app**, name `Mailfire CLI`.
   - There is no redirect or callback URL setting for Desktop clients, and none is needed: Google accepts any
     loopback callback `http://127.0.0.1:<port>/...` for them, which is what Mailfire uses
     (`/oauth2/callback` on a random port, or the one given with `--port`). A redirect URI field only appears for
     the "Web application" type, which is the wrong type for Mailfire.
   - Leave "Use this client for an AI-powered agent" unchecked.
   - New clients and settings can take from 5 minutes to a few hours to take effect; an "invalid client" error
     right after creation usually just means waiting.
   - Note the **client ID** and **client secret**. For desktop apps neither is treated as confidential, because
     anyone can extract them from the binary. The users' refresh tokens are what must be protected.

## Phase 3: Test with the current build

Test the new client before submitting it for review:

```sh
export MAILFIRE_GMAIL_CLIENT_ID="<id>.apps.googleusercontent.com"
export MAILFIRE_GMAIL_CLIENT_SECRET="<secret>"   # optional per Google's docs; try without it first
mailfire auth login --provider gmail
mailfire validate
mailfire --to you@example.com --subject "Gmail test" --message "hello"
mailfire auth refresh --provider gmail
```

This is also the real-account onboarding run that task 30 requires before release. Include the headless path:
`mailfire auth login --provider gmail --no-browser --port 8765` over an SSH tunnel (see [Gmail provider](../content/guides/gmail.md)).

While the app is in Testing, refresh tokens expire after 7 days. That is expected and does not happen in
production.

## Phase 4: Publish and verify

Google's review typically takes 3–5 business days.

1. **Audience → Publish app** (switch to production).
2. **Branding → Verify Branding.** An automated check runs within minutes; then click **Publish branding**. The
   result must be published within 7 days, or the branding has to be verified again.
3. **Verification Center → data access request.** Google asks for:
   - **A justification for each scope.** Suggested text:
     - **`gmail.send`:** "Mailfire is a command-line tool that sends notification and report emails the user
       composes, through the user's own Gmail account. It only calls `users.messages.send`; it never reads,
       lists, or modifies mail. No narrower scope can send email."
     - **`openid`:** "Used only to obtain the opaque account ID, so messages queued for retry are never sent from
       a different account after the user signs in again."
   - **A demo video**, uploaded to YouTube as **Unlisted**, in English. It must show:
     - the login (`mailfire auth login --provider gmail`)
     - the consent screen showing the app name
     - the browser address bar on the consent screen, with the client ID visible
     - a send actually happening: the command and the email arriving
4. Watch the developer and support email addresses; Google's reviewers follow up there.

Until verification passes, users see an "unverified app" warning and at most 100 accounts can sign in.

## Phase 5: Code changes after approval

1. Done: the client ID is in `gmail.BuiltinClientID`, and the secret is injected at build time (see
   [Where the client ID and secret live](#where-the-client-id-and-secret-live)).
2. Done: the `GMAIL_CLIENT_SECRET` repository secret is set.
3. Remove the "Availability" note from [docs/content/guides/gmail.md](../content/guides/gmail.md).
4. Set task 30 to `done` in `tmp/tasks/30-gmail-provider.md` and `tmp/tasks/README.md`.
