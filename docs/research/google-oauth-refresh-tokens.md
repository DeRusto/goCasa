# Google OAuth: keeping a refresh token alive for a self-hosted app

Research for [issue #3](https://github.com/DeRusto/goCasa/issues/3). Checked 2026-10-17 against Google's own docs. Spec references: §7.2 (Google sync) and §14 risk #3.

## Short answer

- **Testing status does force a weekly reconnect.** If the user type is External and the publishing status is Testing, refresh tokens expire 7 days after consent. The only exception is a sign-in that asks for nothing but `openid` / `email` / `profile`. Calendar scopes are not covered by that exception. So the spec's assumption that "test-user mode is sufficient" (§14 #3) is **wrong** for an unattended dashboard.
- **Viable route: External + "In production" + unverified, used as a personal-use app.** Google exempts personal-use apps (fewer than 100 users) from verification. Users click through an "unverified app" warning once at consent, and the project has a lifetime cap of 100 new users. Google documents the 7-day expiry only for Testing status. A production refresh token follows the normal rules: it stays valid until it is revoked, goes unused for 6 months, or hits one of the other invalidation triggers listed below.
- **Alternative: Internal user type.** This needs a Google Workspace or Cloud Identity organization, and only members of that org can authorize. It has no unverified screen, no 100-user cap and no verification. It does not work for ordinary `@gmail.com` household accounts.
- **Calendar scopes are not restricted.** They do not appear on Google's restricted-scope list (Gmail, Drive, Fit, Chat, Data Portability, Photos Ambient, Health), so no annual security assessment applies. They do count as "user data" scopes that trigger the unverified-app screen when unverified, which means they are sensitive. *Inference:* Google's docs do not print a per-scope label for Calendar. The Cloud Console's Data Access page shows the classification, so check it there when setting up.

## Findings with sources

### 1. Testing status means a 7-day refresh token

- A project whose consent screen is set to the External user type and the Testing publishing status gets refresh tokens that expire in 7 days. The exception is when the only scopes requested are a subset of name, email address and user profile. Source: https://developers.google.com/identity/protocols/oauth2#expiration
- Testing is limited to 100 test users listed on the consent screen. Their authorizations "will expire seven days from the time of consent", and that includes refresh tokens issued to offline-access clients. Source: https://support.google.com/cloud/answer/15549945

### 2. Other reasons a refresh token stops working (any publishing status)

From https://developers.google.com/identity/protocols/oauth2#expiration:
- the user revoked access
- the token was not used for **six months**
- the user changed their password and the token has Gmail scopes (does not apply to GoCasa)
- the account went over the limit of **100 live refresh tokens per OAuth client ID**, in which case the oldest is silently invalidated. Repeated "Connect" clicks can cause this.
- the user granted time-based access and it expired
- a Workspace admin restricted the service (`admin_policy_enforced`) or a session-control length was exceeded

Implication: a dashboard that syncs every few minutes never reaches the 6-month idle limit. GoCasa still has to treat `invalid_grant` as "reconnect needed" and show it in Settings (§7.2 `last_error`).

### 3. Production without verification, under the personal-use exemption

- Personal-use apps with fewer than 100 users are exempt from verification. Users are allowed to click through the "unverified app" warning during sign-in. Apps in development, testing or staging are also exempt but have the same unverified screen and 100-user cap. Source: https://support.google.com/cloud/answer/13464323
- An unverified app is one that requests a sensitive or restricted scope without completing Google's verification. It shows the unverified-app screen before consent and is limited to 100 new users until it is verified. Source: https://support.google.com/cloud/answer/7454865
- The cap counts 100 new users in total over the project's lifetime and cannot be reset. Source: https://support.google.com/cloud/answer/15549945
- Only the Testing status carries the 7-day expiry (section 1), so production tokens get the normal lifetime.

Each self-hoster creates their own Google Cloud project and OAuth client. Each project therefore serves one household, well under 100 users.

### 4. Internal user type

- Internal is for apps used only by people in your Google Workspace or Cloud Identity organization. An internal-only app is not subject to the unverified-app screen or the 100-user cap. Source: https://support.google.com/cloud/answer/13464323
- Internal requires a Cloud Organization, and accounts outside it get `org_internal`. Source: https://support.google.com/cloud/answer/15549945
- For internal-only apps, sensitive and restricted scopes need no further Google review. Source: https://developers.google.com/workspace/guides/configure-oauth-consent

### 5. Scope classification

- Restricted-scope API groups: Gmail, Drive, Fit, Chat, Data Portability, Photos Ambient, Health. No Calendar scope is listed. Source: https://support.google.com/cloud/answer/13464325
- Sensitive scopes require review by Google and carry a *sensitive* indicator in the console. Source: https://developers.google.com/identity/protocols/oauth2/scopes
- Calendar scope list and the advice to request the narrowest scope: https://developers.google.com/workspace/calendar/api/auth. Phase 2a (read-only sync) needs only `calendar.readonly` or `calendar.events.readonly`. Phase 2b write-back needs `calendar.events`.

## Implications for GoCasa

1. Replace §14 risk #3's handling with: "External user type, publishing status **In production**, unverified (personal-use exemption). Click through the unverified-app warning once. Do **not** leave it in Testing (7-day tokens)." Internal is the alternative for Workspace households.
2. The README setup steps must include "Publish app" (move from Testing to In production) and say that the warning screen is expected.
3. Sync must handle `invalid_grant`: mark the integration as needing reconnect, show it in Settings, and do not retry in a loop.
4. Reconnect should reuse or overwrite the stored token for that Google account rather than piling up new ones, because of the per-client cap of 100 tokens.

## Not verified

- No live test was run, so it is not confirmed that a production-unverified token survives past 7 days. The conclusion rests on Google documenting the 7-day limit only for Testing.
- The per-scope "sensitive" label for Calendar was not found printed in the docs. Confirm it on the console's Data Access page.
- Whether switching from Testing to Production invalidates tokens already issued in Testing. Plan to reconnect once after publishing.
